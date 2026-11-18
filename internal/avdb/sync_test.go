package avdb

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"testing"
)

type trackingImporter struct {
	store *ResourceStore
	calls int
	names []string
}

func (importer *trackingImporter) ImportZip(ctx context.Context, name string, body io.Reader) (ImportResult, error) {
	importer.calls++
	importer.names = append(importer.names, name)
	return importer.store.ImportZip(ctx, name, body)
}

type syncPackageClient struct {
	assets []ReleaseAsset
	data   []byte
}

func (client *syncPackageClient) ResolveLatest(context.Context, string, string) ([]ReleaseAsset, error) {
	return client.assets, nil
}

func (client *syncPackageClient) DownloadAsset(_ context.Context, _ ReleaseAsset, file *os.File, progress func(int64)) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(client.data); err != nil {
		return err
	}
	if progress != nil {
		progress(int64(len(client.data)))
	}
	return nil
}

func TestSyncDownloadsImportsAndPersistsState(t *testing.T) {
	store, err := OpenResourceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	importer := &trackingImporter{store: store}
	client := &syncPackageClient{
		assets: []ReleaseAsset{{
			Name:        "All_X1080X_2_2026-11-18-04-00-00.zip",
			Size:        1,
			DownloadURL: "https://download.test/package.zip",
		}},
		data: testZip(t, "tid,number,title\n1,CODE-001,First\n2,CODE-002,Second\n"),
	}
	service := NewSyncService(SyncDependencies{
		Importer: importer,
		State:    store,
		Releases: client,
	}, SyncOptions{})

	result, err := service.Sync(t.Context(), SyncRequest{
		Sources: []string{SourceX1080X},
		Mode:    ModeFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	if importer.calls != 1 || importer.names[0] != "All_X1080X_2_2026-11-18-04-00-00.zip" {
		t.Fatalf("imports=%d names=%#v", importer.calls, importer.names)
	}
	if result.Imported != 2 || result.Total != 2 {
		t.Fatalf("result = %#v", result)
	}

	states, err := store.ListSyncStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Source != SourceX1080X ||
		states[0].Package != "All_X1080X_2_2026-11-18-04-00-00.zip" ||
		states[0].Imported != 2 || states[0].Total != 2 || states[0].SyncedAt == "" {
		t.Fatalf("states = %#v", states)
	}

	status, err := service.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Result == nil || status.Result.JobID != result.JobID ||
		status.Result.Imported != 2 || len(status.Last) != 1 {
		t.Fatalf("status = %#v", status)
	}
}

func testZip(t *testing.T, csv string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	file, err := archive.Create("library.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, csv); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

package cd2

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/pan"
)

// Live smoke test against the developer's own CloudDrive2 instance.
func TestLiveSmoke(t *testing.T) {
	endpoint := os.Getenv("CD2_LIVE_ENDPOINT")
	token := os.Getenv("CD2_LIVE_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip("CD2_LIVE_ENDPOINT/CD2_LIVE_TOKEN not set")
	}
	drive, err := New(Config{Endpoint: endpoint, Token: token, Account: os.Getenv("CD2_LIVE_ACCOUNT")})
	if err != nil {
		t.Fatal(err)
	}
	defer drive.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	account, err := drive.Account(ctx, "")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	t.Logf("account=%+v", account)

	root, err := drive.List(ctx, "", RootID, 0, 100)
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	t.Logf("root total=%d hasMore=%t path=%+v", root.Total, root.HasMore, root.Path)
	for _, f := range root.Files {
		t.Logf("  %s dir=%t id=%s pick=%s", f.Name, f.IsDirectory, f.ID, f.PickCode)
	}
	if len(root.Files) == 0 {
		t.Fatal("empty root")
	}
	first := root.Files[0]
	page, err := drive.List(ctx, "", first.ID, 0, 3)
	if err != nil {
		t.Fatalf("List %s: %v", first.Name, err)
	}
	t.Logf("%s total=%d page=%d path=%+v", first.Name, page.Total, len(page.Files), page.Path)

	var file pan.File
	for _, f := range page.Files {
		if !f.IsDirectory {
			file = f
			break
		}
	}
	if file.ID == "" && len(page.Files) > 0 {
		sub, err := drive.List(ctx, "", page.Files[0].ID, 0, 100)
		if err != nil {
			t.Fatalf("List nested: %v", err)
		}
		for _, f := range sub.Files {
			if !f.IsDirectory {
				file = f
				break
			}
		}
	}
	if file.ID == "" {
		t.Skip("no file found in the first two levels")
	}
	info, err := drive.Info(ctx, "", file.ID)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	t.Logf("info=%+v name=%s parent=%s size=%d sha1=%s path=%+v", info.ID == file.ID, info.Name, info.ParentID, info.Size, info.SHA1, info.Path)

	url, err := drive.DownloadURL(ctx, "", file.PickCode, pan.MediaUserAgent)
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}
	t.Logf("download=%s", url)
	resp, err := drive.OpenMedia(ctx, "HEAD", url, nil)
	if err != nil {
		t.Fatalf("OpenMedia: %v", err)
	}
	defer resp.Body.Close()
	t.Logf("HEAD status=%d length=%d type=%s ranges=%s", resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Header.Get("Accept-Ranges"))
}

// TestLiveMissingDirectory checks the stream error path against a real server.
func TestLiveMissingDirectory(t *testing.T) {
	endpoint, token := os.Getenv("CD2_LIVE_ENDPOINT"), os.Getenv("CD2_LIVE_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip("CD2_LIVE_ENDPOINT/CD2_LIVE_TOKEN not set")
	}
	drive, err := New(Config{Endpoint: endpoint, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	defer drive.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = drive.List(ctx, "", EncodePath("/definitely-missing-miyabi-probe"), 0, 10)
	t.Logf("missing directory error = %v", err)
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("List error = %v, want ErrNotFound", err)
	}
}

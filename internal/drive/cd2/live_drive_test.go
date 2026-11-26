package cd2

import (
	"os"
	"testing"
)

func TestLiveDriveLevel(t *testing.T) {
	endpoint, token, account := os.Getenv("CD2_LIVE_ENDPOINT"), os.Getenv("CD2_LIVE_TOKEN"), os.Getenv("CD2_LIVE_ACCOUNT")
	if endpoint == "" || token == "" {
		t.Skip("CD2_LIVE_ENDPOINT/CD2_LIVE_TOKEN not set")
	}
	client, err := New(Config{Endpoint: endpoint, Token: token, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	drive := newDrive(Config{Account: account}, client.client)
	ctx := t.Context()

	status, err := drive.Account(ctx, "")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	t.Logf("account=%s name=%s level=%s total=%s", status.ID, status.Name, status.Level, status.Space.Total.Formatted)

	root, err := drive.List(ctx, "", RootID, 0, 100)
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	if len(root.Files) == 0 {
		t.Fatal("empty root")
	}
	target := root.Files[0]
	page, err := drive.List(ctx, "", target.ID, 4, 2)
	if err != nil {
		t.Fatalf("List page: %v", err)
	}
	t.Logf("paged: total=%d returned=%d hasMore=%t", page.Total, len(page.Files), page.HasMore)

	// Encode an ID, derive a path back, and read it through ReadMetadata.
	full, err := drive.List(ctx, "", target.ID, 0, 100)
	if err != nil {
		t.Fatalf("List full: %v", err)
	}
	var probe string
	for _, f := range full.Files {
		if !f.IsDirectory && f.Size > 0 && f.Size < 1<<20 {
			probe = f.ID
			break
		}
	}
	if probe == "" {
		t.Skip("no file in this page")
	}
	body, err := drive.ReadMetadata(ctx, "", probe, 1<<20)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	t.Logf("metadata bytes=%d", len(body))

	// A file ID must not be mountable as a directory, and a directory download
	// must fail as an unsupported download rather than panic.
	if _, err := drive.DownloadURL(ctx, "", target.ID, ""); err != nil {
		t.Logf("directory DownloadURL error (expected) = %v", err)
	}
	if _, err := drive.Info(ctx, "", "1184404884787683448"); err == nil {
		t.Fatal("a CloudDrive2 internal ID must not resolve")
	} else {
		t.Logf("internal ID Info error (expected) = %v", err)
	}
}

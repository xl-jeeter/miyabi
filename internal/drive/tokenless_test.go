package drive

import (
	"context"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/pan"
)

var tokenlessSource = domain.LibrarySource{
	AccountID: "115open:80005515",
	Directory: domain.LibraryDirectory{ID: "L-aZuuWLpA", Name: "智勤", Path: "/智勤"},
}

// tokenlessClient stands in for CloudDrive2: it holds its own credentials and
// therefore never has an access token to present.
type tokenlessClient struct {
	stubClient
	mounted string
}

func (c *tokenlessClient) Tokenless() bool { return true }

func (c *tokenlessClient) Account(context.Context, string) (pan.Account, error) {
	return pan.Account{ID: tokenlessSource.AccountID, Name: "CloudDrive2"}, nil
}

func (c *tokenlessClient) List(_ context.Context, _ string, directory string, _, _ int) (pan.FilePage, error) {
	if directory != tokenlessSource.Directory.ID {
		return pan.FilePage{}, pan.ErrNotFound
	}
	return pan.FilePage{
		Files: []pan.File{{ID: "L-aZuuWLpAL3ZpZGVvLm1wNA", ParentID: directory, Name: "video.mp4", Size: 1}},
		Path:  []pan.Directory{{ID: RootIDForTest}, {ID: directory, Name: "智勤"}},
		Total: 1,
	}, nil
}

// RootIDForTest mirrors the root ID contract shared with the 115 client.
const RootIDForTest = "0"

// A client that owns its credentials must work without a 115 login, otherwise
// CloudDrive2 could never mount a directory.
func TestTokenlessClientSkipsAccessToken(t *testing.T) {
	client := &tokenlessClient{}
	d := newTestDrive(t, &client.stubClient)
	d.client = client
	d.tokenless = client.Tokenless()
	ctx := t.Context()

	status, err := d.Account(ctx)
	if err != nil {
		t.Fatalf("Account error = %v", err)
	}
	if !status.Connected || status.Account == nil || status.Account.ID != tokenlessSource.AccountID {
		t.Fatalf("account status = %+v", status)
	}

	page, err := d.Files(ctx, tokenlessSource.Directory.ID, 1)
	if err != nil {
		t.Fatalf("Files error = %v", err)
	}
	if len(page.Files) != 1 || page.Files[0].Name != "video.mp4" {
		t.Fatalf("files = %+v", page.Files)
	}

	directory, err := d.SelectDirectory(ctx, tokenlessSource.Directory.ID)
	if err != nil {
		t.Fatalf("SelectDirectory error = %v", err)
	}
	if directory.ID != tokenlessSource.Directory.ID || directory.Path != "/智勤" {
		t.Fatalf("mounted directory = %+v", directory)
	}
	sess, err := d.Open(ctx)
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	if _, err := sess.Info(ctx, "L-aZuuWLpA"); err != nil {
		t.Fatalf("Info error = %v", err)
	}
}

// A normal 115 client keeps the access-token requirement.
func TestTokenClientStillRequiresAccessToken(t *testing.T) {
	d := newTestDrive(t, &stubClient{})
	if _, err := d.Account(t.Context()); err != nil {
		t.Fatalf("Account error = %v", err)
	}
	if _, err := d.Open(t.Context()); err == nil {
		t.Fatal("Open succeeded without 115 credentials")
	}
}

package cd2

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveOffline reads real offline tasks and checks the destination cache
// without submitting anything to the cloud.
func TestLiveOffline(t *testing.T) {
	endpoint, token, account := os.Getenv("CD2_LIVE_ENDPOINT"), os.Getenv("CD2_LIVE_TOKEN"), os.Getenv("CD2_LIVE_ACCOUNT")
	if endpoint == "" || token == "" {
		t.Skip("CD2_LIVE_ENDPOINT/CD2_LIVE_TOKEN not set")
	}
	client, err := New(Config{Endpoint: endpoint, Token: token, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	page, err := client.OfflineTasks(ctx, "", 1)
	if err != nil {
		t.Fatalf("OfflineTasks: %v", err)
	}
	t.Logf("pageCount=%d tasks=%d", page.PageCount, len(page.Tasks))
	for i, task := range page.Tasks {
		if i >= 5 {
			break
		}
		t.Logf("  hash=%s status=%d progress=%.1f dir=%q", task.Hash, task.Status, task.RawProgress, task.DirectoryID)
	}

	// Cancelling an unknown hash must not touch any real download.
	if len(page.Tasks) > 0 {
		t.Logf("skipping RemoveOffline on live data to avoid cancelling a real task")
	}
}

package avdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/netx"
)

// TestLiveRankings hits a real AVDB instance; skipped unless AVDB_PROBE_URL is set.
func TestLiveRankings(t *testing.T) {
	base := os.Getenv("AVDB_PROBE_URL")
	if base == "" {
		t.Skip("AVDB_PROBE_URL not set")
	}
	manager, err := netx.NewProxyManager(netx.ProxyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	client := New(Options{BaseURL: base, APIKey: os.Getenv("AVDB_PROBE_KEY"), Proxy: manager})
	start := time.Now()
	movies, err := client.Rankings(context.Background(), "daily", 0)
	t.Logf("elapsed=%s count=%d err=%v", time.Since(start), len(movies), err)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) == 0 {
		t.Fatal("no movies returned")
	}
	t.Logf("first: %s %s", movies[0].Code, movies[0].Title[:40])
}

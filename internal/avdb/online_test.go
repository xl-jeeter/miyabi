package avdb

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/domain"
)

func TestSelectPackagesByModeSourceAndTimestamp(t *testing.T) {
	assets := []ReleaseAsset{
		{Name: "All_X1080X_100_2026-11-17-04-00-00.zip", Size: 1, DownloadURL: "old-x"},
		{Name: "All_X1080X_121843_2026-11-18-04-00-00.zip", Size: 2, DownloadURL: "new-x"},
		{Name: "（All_sehuatang_333651_2026-11-18-04-00-00.zip", Size: 3, DownloadURL: "sehuatang"},
		{Name: "All_LTX_2289_2026-11-18-03-00-00.zip", Size: 4, DownloadURL: "ltx"},
		{Name: "All_FTAV_1165_2026-11-18-02-00-00.zip", Size: 5, DownloadURL: "ftav"},
		{Name: "All_X1080X_not-a-date.zip", Size: 6, DownloadURL: "invalid"},
	}
	packages, err := SelectPackages(assets, SelectOptions{
		Sources: []string{SourceX1080X, SourceSehuatang, SourceLTX, SourceFTAV},
		Mode:    ModeFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 4 {
		t.Fatalf("packages = %#v", packages)
	}
	if packages[0].Name != "All_X1080X_121843_2026-11-18-04-00-00.zip" ||
		packages[0].DownloadURL != "new-x" || packages[0].Rows != 121843 {
		t.Fatalf("x1080x package = %#v", packages[0])
	}
	if packages[1].Source != SourceSehuatang || packages[2].Source != SourceLTX ||
		packages[3].Source != SourceFTAV {
		t.Fatalf("source order = %#v", packages)
	}

	assets = append(assets,
		ReleaseAsset{Name: "30D_X1080X_2026-11-17-01-00-00.zip", DownloadURL: "recent-old"},
		ReleaseAsset{Name: "30D_X1080X_2026-11-18-01-03-04.zip", DownloadURL: "recent-new"},
		ReleaseAsset{Name: "30D_LTX_2026-11-18-01-03-04.zip", DownloadURL: "recent-ltx"},
		ReleaseAsset{Name: "30D_2026-11-18-01-03-04.zip", DownloadURL: "unscoped"},
	)
	packages, err = SelectPackages(assets, SelectOptions{
		Sources: []string{SourceX1080X, SourceLTX},
		Mode:    ModeRecent30D,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 || packages[0].DownloadURL != "recent-new" ||
		packages[1].DownloadURL != "recent-ltx" || packages[0].Rows != 0 {
		t.Fatalf("recent packages = %#v", packages)
	}
}

func TestSelectPackagesRejectsUnknownSourceAndMode(t *testing.T) {
	if _, err := SelectPackages(nil, SelectOptions{Mode: "nightly"}); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("mode error = %v", err)
	}
	if _, err := SelectPackages(nil, SelectOptions{Sources: []string{"other"}}); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("source error = %v", err)
	}
}

func TestResolveLatestParsesGitHubAssets(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases/latest" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"assets":[
			{"name":"All_X1080X_2_2026-11-18-04-00-00.zip","size":42,"browser_download_url":"https://download.test/all.zip"},
			{"name":"README.txt","size":1,"download_url":"https://download.test/readme"}
		]}`)
	})
	client := NewReleaseClient(ReleaseClientOptions{
		GitHubToken: "token",
		HTTPClient:  &http.Client{Transport: handlerTransport(handler)},
	})
	client.githubAPI = "https://github.test"

	assets, err := client.ResolveLatest(t.Context(), "owner/repo", "mirror/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 || assets[0].DownloadURL != "https://download.test/all.zip" {
		t.Fatalf("assets = %#v", assets)
	}
}

func TestResolveLatestFallsBackToGitee(t *testing.T) {
	var githubCalls, giteeCalls int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "github.test" {
			githubCalls++
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		giteeCalls++
		if r.URL.Path != "/repos/mirror/repo/releases/latest" {
			t.Fatalf("gitee path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"assets":[{"name":"All_FTAV_2_2026-11-18.zip","size":3,"browser_download_url":"gitee"}]}`)
	})
	client := NewReleaseClient(ReleaseClientOptions{HTTPClient: &http.Client{Transport: handlerTransport(handler)}})
	client.githubAPI = "https://github.test"
	client.giteeAPI = "https://gitee.test"

	assets, err := client.ResolveLatest(t.Context(), "owner/repo", "mirror/repo")
	if err != nil {
		t.Fatal(err)
	}
	if githubCalls != 1 || giteeCalls != 1 || len(assets) != 1 || assets[0].DownloadURL != "gitee" {
		t.Fatalf("calls=%d/%d assets=%#v", githubCalls, giteeCalls, assets)
	}
}

func TestResolveLatestReportsBothSourcesUnavailable(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	client := NewReleaseClient(ReleaseClientOptions{HTTPClient: &http.Client{Transport: handlerTransport(handler)}})
	client.githubAPI = "https://github.test"
	client.giteeAPI = "https://gitee.test"

	_, err := client.ResolveLatest(t.Context(), "owner/repo", "mirror/repo")
	if !domain.IsKind(err, domain.KindUpstream) || !strings.Contains(err.Error(), "在线源不可达") {
		t.Fatalf("error = %v", err)
	}
}

func TestDownloadAssetRetriesAndReportsAbsoluteProgress(t *testing.T) {
	attempts := 0
	lastProgress := int64(0)
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		attempts++
		if request.Header.Get("Range") == "" {
			t.Fatal("single fallback must be used only after chunk downloads")
		}
		start, end, err := parseRangeHeader(request.Header.Get("Range"))
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Length", "4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/4", start, end))
		w.WriteHeader(http.StatusPartialContent)
		if attempts == 1 {
			_, _ = io.WriteString(w, "ab")
			return
		}
		_, _ = io.WriteString(w, "data")
	})
	client := NewReleaseClient(ReleaseClientOptions{
		HTTPClient: &http.Client{Transport: handlerTransport(handler)},
		RetryDelay: func(int) time.Duration { return 0 },
	})
	file, err := os.Create(filepath.Join(t.TempDir(), "package.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	err = client.DownloadAsset(t.Context(), ReleaseAsset{
		Name:        "package.zip",
		Size:        4,
		DownloadURL: "https://download.test/package.zip",
	}, file, func(bytes int64) { lastProgress = bytes })
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || lastProgress != 4 {
		t.Fatalf("attempts=%d progress=%d", attempts, lastProgress)
	}
}

type releaseHandler func(*http.Request) (*http.Response, error)

func (handler releaseHandler) RoundTrip(request *http.Request) (*http.Response, error) {
	return handler(request)
}

func handlerTransport(handler http.Handler) releaseHandler {
	return func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	}
}

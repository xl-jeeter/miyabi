package avdb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSplitRangesCoversWholeFile(t *testing.T) {
	cases := []int64{1, 100, 4 << 20, 294 << 20, 308_706_826, (294 << 20) + 12345}
	for _, size := range cases {
		parts := splitRanges(size, defaultDownloadChunkSize)
		if len(parts) == 0 {
			t.Fatalf("size %d produced no parts", size)
		}
		expected := int64(0)
		for index, part := range parts {
			if part.start != expected {
				t.Fatalf("size %d part %d starts at %d, want %d", size, index, part.start, expected)
			}
			if part.end < part.start || part.total != size {
				t.Fatalf("size %d part %d = %#v", size, index, part)
			}
			expected = part.end + 1
		}
		if expected != size {
			t.Fatalf("size %d: ranges cover %d bytes", size, expected)
		}
	}
}

func TestConfiguredDownloadOptions(t *testing.T) {
	t.Setenv(downloadChunkEnv, "")
	t.Setenv(downloadPartsEnv, "")
	t.Setenv(downloadPerSourceEnv, "")
	if chunk := configuredDownloadChunkSize(); chunk != defaultDownloadChunkSize {
		t.Fatalf("default chunk = %d", chunk)
	}
	if parts := configuredDownloadParts(); parts != defaultDownloadParts {
		t.Fatalf("default parts = %d", parts)
	}
	if perSource := configuredDownloadPerSource(); perSource != defaultDownloadPerSource {
		t.Fatalf("default per-source parts = %d", perSource)
	}

	t.Setenv(downloadChunkEnv, "1024")
	t.Setenv(downloadPartsEnv, "13")
	t.Setenv(downloadPerSourceEnv, "2")
	if chunk := configuredDownloadChunkSize(); chunk != 1024 {
		t.Fatalf("configured chunk = %d", chunk)
	}
	if parts := configuredDownloadParts(); parts != 13 {
		t.Fatalf("configured parts = %d", parts)
	}
	if perSource := configuredDownloadPerSource(); perSource != 2 {
		t.Fatalf("configured per-source parts = %d", perSource)
	}

	t.Setenv(downloadChunkEnv, "invalid")
	t.Setenv(downloadPartsEnv, "invalid")
	t.Setenv(downloadPerSourceEnv, "invalid")
	if chunk := configuredDownloadChunkSize(); chunk != defaultDownloadChunkSize {
		t.Fatalf("invalid chunk = %d", chunk)
	}
	if parts := configuredDownloadParts(); parts != defaultDownloadParts {
		t.Fatalf("invalid parts = %d", parts)
	}
	if perSource := configuredDownloadPerSource(); perSource != defaultDownloadPerSource {
		t.Fatalf("invalid per-source parts = %d", perSource)
	}
}

func TestDownloadMirrorsDefaultsAndParses(t *testing.T) {
	t.Setenv("MIYABI_AVDB_DOWNLOAD_MIRRORS", "")
	if got := strings.Join(downloadMirrors(), ","); got != defaultDownloadMirrors {
		t.Fatalf("default mirrors = %q", got)
	}
	t.Setenv("MIYABI_AVDB_DOWNLOAD_MIRRORS", " https://one.test/ ,,https://two.test,https://one.test")
	if got := strings.Join(downloadMirrors(), ","); got != "https://one.test,https://two.test" {
		t.Fatalf("configured mirrors = %q", got)
	}
}

func TestParseContentRange(t *testing.T) {
	start, end, total, ok := parseContentRangeBounds("bytes 0-12/294400000")
	if !ok || start != 0 || end != 12 || total != 294400000 {
		t.Fatalf("unexpected parse: %d %d %d %v", start, end, total, ok)
	}
	if _, _, ok := parseContentRange("bytes */1234"); ok {
		t.Fatal("unsatisfied range must not parse")
	}
	if _, _, ok := parseContentRange(""); ok {
		t.Fatal("empty header must not parse")
	}
}

func TestParallelProgressAggregatesCompletedAndActiveParts(t *testing.T) {
	var values []int64
	progress := newParallelProgress(3, func(value int64) {
		values = append(values, value)
	})

	progress.completePart(0, 10)
	progress.setPart(1, 4)
	progress.setPart(1, 20)
	progress.completePart(1, 20)
	progress.setPart(2, 5)

	expected := []int64{0, 10, 14, 30, 30, 35}
	if fmt.Sprint(values) != fmt.Sprint(expected) {
		t.Fatalf("progress = %v, want %v", values, expected)
	}
	assertMonotonic(t, values)
}

func TestChunkRetrySwitchesToNextSource(t *testing.T) {
	t.Setenv(downloadChunkEnv, "8")
	t.Setenv(downloadPartsEnv, "1")
	payload := []byte("abcdefgh")
	events := &eventLog{}
	handler := releaseHandler(func(request *http.Request) (*http.Response, error) {
		events.add(request.URL.Host)
		if request.Header.Get("Range") != "bytes=0-7" {
			t.Fatalf("Range = %q", request.Header.Get("Range"))
		}
		if !request.Close {
			t.Fatal("chunk request must not reuse its connection")
		}
		if request.URL.Host == "download.test" {
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return chunkResponse(payload, 0, 7, int64(len(payload))), nil
	})
	client := NewReleaseClient(ReleaseClientOptions{
		HTTPClient: &http.Client{Transport: handler},
		RetryDelay: func(int) time.Duration { return 0 },
	})
	client.mirrors = []string{"https://mirror.test"}

	logs := captureLogs(t)
	file := createDownloadFile(t)
	var values []int64
	if err := client.downloadRanged(t.Context(), "https://download.test/package.zip", "package.zip", file, int64(len(payload)), func(value int64) {
		values = append(values, value)
	}); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(events.snapshot(), ","); got != "download.test,mirror.test" {
		t.Fatalf("source order = %q", got)
	}
	assertDownloaded(t, file, payload)
	assertMonotonic(t, values)
	if !strings.Contains(logs.String(), "chunk failed") ||
		!strings.Contains(logs.String(), "source=proxy") ||
		!strings.Contains(logs.String(), "duration=") {
		t.Fatalf("chunk failure log missing details:\n%s", logs.String())
	}
}

func TestDownloadRangedAggregatesProgressAcrossChunkRetry(t *testing.T) {
	t.Setenv(downloadChunkEnv, "4")
	t.Setenv(downloadPartsEnv, "2")
	payload := []byte("abcdefgh")
	var proxyAttempts atomic.Int64
	handler := releaseHandler(func(request *http.Request) (*http.Response, error) {
		start, end, err := parseRangeHeader(request.Header.Get("Range"))
		if err != nil {
			return nil, err
		}
		if request.URL.Host == "download.test" && start == 0 && proxyAttempts.Add(1) == 1 {
			response := chunkResponse(payload[:2], start, end, int64(len(payload)))
			return response, nil
		}
		return chunkResponse(payload[start:end+1], start, end, int64(len(payload))), nil
	})
	client := NewReleaseClient(ReleaseClientOptions{
		HTTPClient: &http.Client{Transport: handler},
		RetryDelay: func(int) time.Duration { return 0 },
	})
	client.mirrors = []string{"https://mirror.test"}

	file := createDownloadFile(t)
	var values []int64
	if err := client.downloadRanged(t.Context(), "https://download.test/package.zip", "package.zip", file, int64(len(payload)), func(value int64) {
		values = append(values, value)
	}); err != nil {
		t.Fatal(err)
	}

	assertDownloaded(t, file, payload)
	assertMonotonic(t, values)
	if len(values) == 0 || values[len(values)-1] != int64(len(payload)) {
		t.Fatalf("progress = %v", values)
	}
}

func TestIdleTimeoutRetriesNextSource(t *testing.T) {
	t.Setenv(downloadChunkEnv, "4")
	t.Setenv(downloadPartsEnv, "1")
	payload := []byte("data")
	var failed atomic.Bool
	handler := releaseHandler(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "download.test" {
			failed.Store(true)
			return stalledRangeResponse(request, payload[:1]), nil
		}
		if !failed.Load() {
			t.Fatal("mirror used before the proxy idle timeout")
		}
		return chunkResponse(payload, 0, 3, int64(len(payload))), nil
	})
	client := NewReleaseClient(ReleaseClientOptions{
		HTTPClient: &http.Client{Transport: handler},
		RetryDelay: func(int) time.Duration { return 0 },
	})
	client.direct = client.http
	client.mirrors = []string{"https://mirror.test"}
	client.downloadIdleTimeout = 25 * time.Millisecond

	logs := captureLogs(t)
	file := createDownloadFile(t)
	var values []int64
	started := time.Now()
	if err := client.downloadRanged(t.Context(), "https://download.test/package.zip", "package.zip", file, int64(len(payload)), func(value int64) {
		values = append(values, value)
	}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("idle timeout took %s", elapsed)
	}

	assertDownloaded(t, file, payload)
	assertMonotonic(t, values)
	if !strings.Contains(logs.String(), "chunk failed") ||
		!strings.Contains(logs.String(), "下载读取空闲超时") {
		t.Fatalf("idle timeout log missing:\n%s", logs.String())
	}
}

func TestChunkPoolHonorsPerSourceConcurrency(t *testing.T) {
	t.Setenv(downloadChunkEnv, "1")
	t.Setenv(downloadPartsEnv, "6")
	t.Setenv(downloadPerSourceEnv, "3")
	payload := []byte("abcdefgh")
	var activeTotal, maxTotal, activeProxy, activeMirror, maxProxy, maxMirror atomic.Int64
	handler := releaseHandler(func(request *http.Request) (*http.Response, error) {
		total := activeTotal.Add(1)
		for {
			previous := maxTotal.Load()
			if total <= previous || maxTotal.CompareAndSwap(previous, total) {
				break
			}
		}
		var active, maximum *atomic.Int64
		if request.URL.Host == "download.test" {
			active, maximum = &activeProxy, &maxProxy
		} else {
			active, maximum = &activeMirror, &maxMirror
		}
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		activeTotal.Add(-1)
		start, end, err := parseRangeHeader(request.Header.Get("Range"))
		if err != nil {
			return nil, err
		}
		return chunkResponse(payload[start:end+1], start, end, int64(len(payload))), nil
	})
	client := NewReleaseClient(ReleaseClientOptions{
		HTTPClient: &http.Client{Transport: handler},
		RetryDelay: func(int) time.Duration { return 0 },
	})
	client.mirrors = []string{"https://mirror.test"}

	file := createDownloadFile(t)
	if err := client.downloadRanged(t.Context(), "https://download.test/package.zip", "package.zip", file, int64(len(payload)), func(int64) {}); err != nil {
		t.Fatal(err)
	}
	if maxTotal.Load() > 6 || maxProxy.Load() > 3 || maxMirror.Load() > 3 {
		t.Fatalf("concurrency = total:%d proxy:%d mirror:%d", maxTotal.Load(), maxProxy.Load(), maxMirror.Load())
	}
	assertDownloaded(t, file, payload)
}

func TestDownloadRangedRetriesWholeDownloadOnSizeMismatch(t *testing.T) {
	t.Setenv(downloadChunkEnv, "4")
	t.Setenv(downloadPartsEnv, "2")
	payload := []byte("abcdefgh")
	var requests atomic.Int64
	handler := releaseHandler(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		start, end, err := parseRangeHeader(request.Header.Get("Range"))
		if err != nil {
			return nil, err
		}
		return chunkResponse(payload[start:end+1], start, end, int64(len(payload))), nil
	})
	client := NewReleaseClient(ReleaseClientOptions{
		HTTPClient: &http.Client{Transport: handler},
		RetryDelay: func(int) time.Duration { return 0 },
	})
	client.mirrors = nil

	file := createDownloadFile(t)
	if err := file.Truncate(int64(len(payload)) + 1); err != nil {
		t.Fatal(err)
	}
	var values []int64
	if err := client.downloadRanged(t.Context(), "https://download.test/package.zip", "package.zip", file, int64(len(payload)), func(value int64) {
		values = append(values, value)
	}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 {
		t.Fatalf("requests = %d, want 4", requests.Load())
	}
	assertDownloaded(t, file, payload)
	assertMonotonic(t, values)
}

func TestDownloadAssetFallsBackFromMultiSourceToSingle(t *testing.T) {
	t.Setenv(downloadChunkEnv, "32")
	t.Setenv(downloadPartsEnv, "1")
	payload := bytes.Repeat([]byte("miyabi"), 6)[:32]
	events := &eventLog{}
	var ranges []string
	handler := releaseHandler(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Range") != "" {
			ranges = append(ranges, request.Header.Get("Range"))
			events.add(request.URL.Host)
			if !request.Close {
				t.Fatal("chunk request must close its connection")
			}
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		events.add("single")
		return fullResponse(payload), nil
	})
	client := NewReleaseClient(ReleaseClientOptions{
		HTTPClient: &http.Client{Transport: handler},
		RetryDelay: func(int) time.Duration { return 0 },
	})
	client.direct = client.http
	client.mirrors = []string{"https://mirror.test"}

	logs := captureLogs(t)
	file := createDownloadFile(t)
	var values []int64
	err := client.DownloadAsset(t.Context(), ReleaseAsset{
		Name:        "All_LTX_2289_2026-11-18-04-00-00.zip",
		Size:        int64(len(payload)),
		DownloadURL: "https://download.test/package.zip",
	}, file, func(value int64) {
		values = append(values, value)
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(events.snapshot(), ","); got != "download.test,mirror.test,download.test,mirror.test,single" {
		t.Fatalf("download order = %q", got)
	}
	if len(ranges) != chunkRetryAttempts {
		t.Fatalf("range requests = %v", ranges)
	}
	for _, value := range ranges {
		if value != "bytes=0-31" {
			t.Fatalf("unexpected Range %q; probe requests are forbidden", value)
		}
	}
	assertDownloaded(t, file, payload)
	assertMonotonic(t, values)

	logText := logs.String()
	for _, expected := range []string{
		"chunk failed",
		"avdb asset download failed",
		"path=multi_source",
		"path=proxy_single",
		"avdb sync throughput",
		"total_bytes=32",
		"average_kbps=",
	} {
		if !strings.Contains(logText, expected) {
			t.Fatalf("logs missing %q:\n%s", expected, logText)
		}
	}
}

func TestLogDownloadThroughputWarnsBelowThreshold(t *testing.T) {
	logs := captureLogs(t)
	logDownloadThroughput("All_LTX_2289_2026-11-18-04-00-00.zip", downloadPathMultiSource, 2048, time.Second)

	logText := logs.String()
	for _, expected := range []string{
		"level=WARN",
		"msg=\"avdb sync throughput\"",
		`average_kbps="2.0 KB/s"`,
		"total_bytes=2048",
		"duration=1s",
		"path=multi_source",
		"package=All_LTX_2289_2026-11-18-04-00-00.zip",
	} {
		if !strings.Contains(logText, expected) {
			t.Fatalf("logs missing %q:\n%s", expected, logText)
		}
	}
}

type stalledBody struct {
	ctx  context.Context
	data []byte
	read bool
}

func (body *stalledBody) Read(buffer []byte) (int, error) {
	if !body.read {
		body.read = true
		return copy(buffer, body.data), nil
	}
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (body *stalledBody) Close() error {
	return nil
}

func stalledRangeResponse(request *http.Request, data []byte) *http.Response {
	return &http.Response{
		Status:     "206 Partial Content",
		StatusCode: http.StatusPartialContent,
		Header: http.Header{
			"Content-Range":  []string{fmt.Sprintf("bytes %s/4", strings.TrimPrefix(request.Header.Get("Range"), "bytes="))},
			"Content-Length": []string{"4"},
		},
		Body: &stalledBody{ctx: request.Context(), data: data},
	}
}

func chunkResponse(payload []byte, start, end, total int64) *http.Response {
	response := httptest.NewRecorder()
	response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	response.Header().Set("Content-Length", fmt.Sprint(len(payload)))
	response.WriteHeader(http.StatusPartialContent)
	_, _ = response.Write(payload)
	return response.Result()
}

func fullResponse(payload []byte) *http.Response {
	response := httptest.NewRecorder()
	response.Header().Set("Content-Length", fmt.Sprint(len(payload)))
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(payload)
	return response.Result()
}

func createDownloadFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "package.zip"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func assertDownloaded(t *testing.T, file *os.File, expected []byte) {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	downloaded, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloaded, expected) {
		t.Fatalf("downloaded %q, want %q", downloaded, expected)
	}
}

func assertMonotonic(t *testing.T, values []int64) {
	t.Helper()
	if len(values) == 0 {
		t.Fatal("progress values are empty")
	}
	for index, value := range values {
		if value < 0 {
			t.Fatalf("negative progress: %v", values)
		}
		if index > 0 && value < values[index-1] {
			t.Fatalf("progress decreased at %d: %v", index, values)
		}
	}
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *eventLog) add(event string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = append(log.events, event)
}

func (log *eventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.events...)
}

func parseRangeHeader(value string) (int64, int64, error) {
	var start, end int64
	if _, err := fmt.Sscanf(value, "bytes=%d-%d", &start, &end); err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

package avdb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultDownloadChunkSize   = 4 << 20
	defaultDownloadParts       = 6
	defaultDownloadPerSource   = 3
	defaultDownloadIdleTimeout = 25 * time.Second

	downloadChunkEnv     = "MIYABI_AVDB_DOWNLOAD_CHUNK"
	downloadPartsEnv     = "MIYABI_AVDB_DOWNLOAD_PARTS"
	downloadPerSourceEnv = "MIYABI_AVDB_DOWNLOAD_PER_SOURCE"
	chunkRetryAttempts   = 4
)

func configuredDownloadChunkSize() int64 {
	chunk, err := strconv.ParseInt(os.Getenv(downloadChunkEnv), 10, 64)
	if err != nil || chunk <= 0 {
		return defaultDownloadChunkSize
	}
	return chunk
}

func configuredDownloadParts() int {
	parts, err := strconv.Atoi(os.Getenv(downloadPartsEnv))
	if err != nil || parts <= 0 {
		return defaultDownloadParts
	}
	return parts
}

func configuredDownloadPerSource() int {
	parts, err := strconv.Atoi(os.Getenv(downloadPerSourceEnv))
	if err != nil || parts <= 0 {
		return defaultDownloadPerSource
	}
	return parts
}

func parseContentRange(value string) (start, total int64, ok bool) {
	start, _, total, ok = parseContentRangeBounds(value)
	return start, total, ok
}

func parseContentRangeBounds(value string) (start, end, total int64, ok bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, false
	}
	rest := strings.TrimPrefix(value, "bytes ")
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return 0, 0, 0, false
	}
	rangePart, totalPart := rest[:slash], rest[slash+1:]
	if totalPart == "*" {
		return 0, 0, 0, false
	}
	parsedTotal, err := strconv.ParseInt(totalPart, 10, 64)
	if err != nil || parsedTotal <= 0 {
		return 0, 0, 0, false
	}
	dash := strings.Index(rangePart, "-")
	if dash < 0 {
		return 0, 0, 0, false
	}
	parsedStart, err := strconv.ParseInt(rangePart[:dash], 10, 64)
	if err != nil || parsedStart < 0 {
		return 0, 0, 0, false
	}
	parsedEnd, err := strconv.ParseInt(rangePart[dash+1:], 10, 64)
	if err != nil || parsedEnd < parsedStart {
		return 0, 0, 0, false
	}
	return parsedStart, parsedEnd, parsedTotal, true
}

type rangePart struct {
	start int64
	end   int64
	total int64
	index int
}

func splitRanges(size, chunkSize int64) []rangePart {
	if size <= 0 {
		return nil
	}
	if chunkSize <= 0 {
		chunkSize = 1
	}
	count := int((size + chunkSize - 1) / chunkSize)
	out := make([]rangePart, 0, count)
	for start, index := int64(0), 0; start < size; start, index = start+chunkSize, index+1 {
		end := start + chunkSize - 1
		if end >= size {
			end = size - 1
		}
		out = append(out, rangePart{start: start, end: end, total: size, index: index})
	}
	return out
}

type parallelProgress struct {
	mu        sync.Mutex
	completed int64
	active    []int64
	report    func(int64)
}

func newParallelProgress(parts int, report func(int64)) *parallelProgress {
	progress := &parallelProgress{active: make([]int64, parts), report: report}
	progress.reportValue(0)
	return progress
}

func (progress *parallelProgress) setPart(index int, written int64) {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if written > progress.active[index] {
		progress.active[index] = written
	}
	progress.reportValue(progress.totalLocked())
}

func (progress *parallelProgress) completePart(index int, size int64) {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.completed += size
	progress.active[index] = 0
	progress.reportValue(progress.totalLocked())
}

func (progress *parallelProgress) totalLocked() int64 {
	total := progress.completed
	for _, written := range progress.active {
		total += written
	}
	return total
}

func (progress *parallelProgress) reportValue(value int64) {
	if progress.report != nil {
		progress.report(value)
	}
}

type downloadSource struct {
	name   string
	url    string
	client *http.Client
}

func (c *ReleaseClient) downloadSources(rawURL string) []downloadSource {
	sources := []downloadSource{{name: "proxy", url: rawURL, client: c.http}}
	client := c.direct
	if client == nil {
		client = c.http
	}
	for _, mirror := range c.mirrors {
		sources = append(sources, downloadSource{
			name:   "mirror:" + mirror,
			url:    mirrorURL(mirror, rawURL),
			client: client,
		})
	}
	return sources
}

func (c *ReleaseClient) downloadRanged(ctx context.Context, rawURL, packageName string, file *os.File, size int64, progress func(int64)) error {
	if size <= 0 {
		return fmt.Errorf("数据包大小无效: %d", size)
	}
	parts := splitRanges(size, c.downloadChunkSize)
	sources := c.downloadSources(rawURL)
	var lastProgress int64
	monotonicProgress := func(value int64) {
		if value <= lastProgress {
			return
		}
		lastProgress = value
		if progress != nil {
			progress(value)
		}
	}
	var lastSizeErr error
	for pass := 1; pass <= 2; pass++ {
		partProgress := newParallelProgress(len(parts), monotonicProgress)
		if err := c.downloadChunkPool(ctx, packageName, file, parts, sources, partProgress); err != nil {
			return fmt.Errorf("分块下载失败: %w", err)
		}
		lastSizeErr = validateDownloadFileSize(file, size)
		if lastSizeErr == nil {
			return nil
		}
		if err := resetDownloadFile(file); err != nil {
			return err
		}
	}
	return lastSizeErr
}

func validateDownloadFileSize(file *os.File, expected int64) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != expected {
		return fmt.Errorf("下载文件大小 %d，应为 %d", info.Size(), expected)
	}
	return nil
}

func (c *ReleaseClient) downloadChunkPool(ctx context.Context, packageName string, file *os.File, parts []rangePart, sources []downloadSource, progress *parallelProgress) error {
	workers := min(c.downloadParts, len(parts))
	if workers <= 0 {
		workers = 1
	}
	semaphores := make([]chan struct{}, len(sources))
	perSource := max(1, c.downloadPerSource)
	for index := range semaphores {
		semaphores[index] = make(chan struct{}, perSource)
	}

	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan rangePart)
	var pool sync.WaitGroup
	var failureMu sync.Mutex
	var firstFailure error
	for range workers {
		pool.Add(1)
		go func() {
			defer pool.Done()
			for part := range jobs {
				err := c.downloadChunk(poolCtx, packageName, file, part, sources, semaphores, progress)
				if err == nil {
					continue
				}
				failureMu.Lock()
				if firstFailure == nil {
					firstFailure = err
				}
				failureMu.Unlock()
				cancel()
				return
			}
		}()
	}
	for _, part := range parts {
		select {
		case jobs <- part:
		case <-poolCtx.Done():
		}
	}
	close(jobs)
	pool.Wait()
	return firstFailure
}

func (c *ReleaseClient) downloadChunk(ctx context.Context, packageName string, file *os.File, part rangePart, sources []downloadSource, semaphores []chan struct{}, progress *parallelProgress) error {
	var lastErr error
	for attempt := 1; attempt <= chunkRetryAttempts; attempt++ {
		sourceIndex := (part.index + attempt - 1) % len(sources)
		source := sources[sourceIndex]
		release, err := acquireSource(ctx, semaphores[sourceIndex])
		if err != nil {
			return err
		}
		started := time.Now()
		written, err := c.downloadChunkAttempt(ctx, source, file, part, func(value int64) {
			progress.setPart(part.index, value)
		})
		release()
		if err == nil {
			progress.completePart(part.index, part.end-part.start+1)
			return nil
		}
		lastErr = err
		slog.Warn("chunk failed",
			"chunk", part.index,
			"source", source.name,
			"reason", err,
			"duration", time.Since(started),
			"downloaded_bytes", written,
			"attempt", attempt,
			"package", packageName,
		)
		if attempt == chunkRetryAttempts {
			break
		}
	}
	return lastErr
}

func acquireSource(ctx context.Context, semaphore chan struct{}) (func(), error) {
	select {
	case semaphore <- struct{}{}:
		return func() { <-semaphore }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *ReleaseClient) downloadChunkAttempt(ctx context.Context, source downloadSource, file *os.File, part rangePart, report func(int64)) (int64, error) {
	requestCtx, cancel, idleTimer := newDownloadRequestContext(ctx, c.downloadIdleTimeout)
	defer cancel(nil)
	defer idleTimer.Stop()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, source.url, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", part.start, part.end))
	request.Close = true
	response, err := source.client.Do(request)
	if err != nil {
		return 0, downloadRequestError(requestCtx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusPartialContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return 0, fmt.Errorf("分片 %d 返回 %d", part.index, response.StatusCode)
	}
	start, end, total, ok := parseContentRangeBounds(response.Header.Get("Content-Range"))
	if !ok || start != part.start || end != part.end || total != part.total {
		return 0, fmt.Errorf("分片 %d Content-Range 无效: %q", part.index, response.Header.Get("Content-Range"))
	}

	expected := part.end - part.start + 1
	body := io.LimitReader(response.Body, expected)
	buffer := make([]byte, 256<<10)
	var written int64
	for written < expected {
		n, readErr := body.Read(buffer)
		if n > 0 {
			idleTimer.Reset(c.downloadIdleTimeout)
			if _, err := file.WriteAt(buffer[:n], part.start+written); err != nil {
				return written, err
			}
			written += int64(n)
			report(written)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return written, downloadRequestError(requestCtx, readErr)
		}
	}
	if written != expected {
		return written, fmt.Errorf("分片 %d 下载 %d 字节，应为 %d", part.index, written, expected)
	}
	return written, nil
}

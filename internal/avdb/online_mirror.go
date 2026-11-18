package avdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const defaultDownloadMirrors = "https://ghfast.top,https://ghfile.geekertao.top,https://ghproxy.vip"

func downloadMirrors() []string {
	raw := os.Getenv("MIYABI_AVDB_DOWNLOAD_MIRRORS")
	if strings.TrimSpace(raw) == "" {
		raw = defaultDownloadMirrors
	}
	out := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		item = strings.TrimRight(item, "/")
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func mirrorURL(prefix, rawURL string) string {
	return prefix + "/" + rawURL
}

func (c *ReleaseClient) streamTo(ctx context.Context, client *http.Client, rawURL string, asset ReleaseAsset, file *os.File, progress func(int64)) (int64, error) {
	requestCtx, cancel, idleTimer := newDownloadRequestContext(ctx, c.downloadIdleTimeout)
	defer cancel(nil)
	defer idleTimer.Stop()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	request.Close = true
	response, err := client.Do(request)
	if err != nil {
		return 0, downloadRequestError(requestCtx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return 0, fmt.Errorf("下载返回 %d", response.StatusCode)
	}

	var written int64
	counter := &progressWriter{report: progress}
	writer := io.Writer(file)
	if progress != nil {
		writer = io.MultiWriter(file, counter)
	}
	buffer := make([]byte, 256<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			idleTimer.Reset(c.downloadIdleTimeout)
			n, err = writer.Write(buffer[:n])
			written += int64(n)
			if err != nil {
				return written, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return written, downloadRequestError(requestCtx, readErr)
		}
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return written, fmt.Errorf("下载 %d 字节，Content-Length 为 %d", written, response.ContentLength)
	}
	if asset.Size > 0 && written != asset.Size {
		return written, fmt.Errorf("下载 %d 字节，数据包大小为 %d", written, asset.Size)
	}
	return written, nil
}

type progressWriter struct {
	report  func(int64)
	written int64
}

func (writer *progressWriter) Write(data []byte) (int, error) {
	writer.written += int64(len(data))
	if writer.report != nil {
		writer.report(writer.written)
	}
	return len(data), nil
}

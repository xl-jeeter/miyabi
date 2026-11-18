package avdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/netx"
)

const (
	DefaultReleaseRepo   = "li-peifeng/AVdb-Only"
	DefaultReleaseMirror = "avdb/avdb-only"

	SourceX1080X    = "x1080x"
	SourceSehuatang = "sehuatang"
	SourceLTX       = "LTX"
	SourceFTAV      = "FTAV"

	ModeFull      = "full"
	ModeRecent30D = "recent30d"

	defaultGitHubAPI = "https://api.github.com"
	defaultGiteeAPI  = "https://gitee.com/api/v5"
	releaseTimeout   = 30 * time.Second
	downloadAttempts = 3

	downloadPathMultiSource downloadPath = "multi_source"
	downloadPathProxySingle downloadPath = "proxy_single"
)

var (
	fullPackagePattern = regexp.MustCompile(
		`^[（(]?All_(?i:(色花堂|sehuatang|X1080X|LTX|FTAV))[-_](\d+)[-_](\d{4}-\d{2}-\d{2}(?:-\d{2}-\d{2}-\d{2})?)\.zip$`)
	recentPackagePattern = regexp.MustCompile(
		`^[（(]?30D(?:[-_](?i:(色花堂|sehuatang|X1080X|LTX|FTAV)))?[-_](\d{4}-\d{2}-\d{2}(?:-\d{2}-\d{2}-\d{2})?)\.zip$`)
)

// ReleaseAsset is one downloadable file attached to a release.
type ReleaseAsset struct {
	Name                string `json:"name"`
	Size                int64  `json:"size"`
	DownloadURL         string `json:"browser_download_url"`
	AlternativeDownload string `json:"download_url"`
}

// Package is a recognized AVDB data package selected for synchronization.
type Package struct {
	Source      string `json:"source"`
	Mode        string `json:"mode"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
	Timestamp   string `json:"timestamp"`
	Rows        int64  `json:"rows,omitempty"`
}

// SelectOptions filters release assets by data source and update mode.
type SelectOptions struct {
	Sources []string `json:"sources"`
	Mode    string   `json:"mode"`
}

// ReleaseClientOptions configures release discovery and package downloads.
type ReleaseClientOptions struct {
	Proxy       *netx.ProxyManager
	GitHubToken string
	HTTPClient  *http.Client
	RetryDelay  func(attempt int) time.Duration
}

// ReleaseClient reads release metadata and streams package assets through the
// same proxy-aware transport used by other upstream integrations.
type ReleaseClient struct {
	http                *http.Client
	direct              *http.Client
	mirrors             []string
	downloadChunkSize   int64
	downloadParts       int
	downloadPerSource   int
	downloadIdleTimeout time.Duration
	githubAPI           string
	giteeAPI            string
	githubToken         string
	retryDelay          func(attempt int) time.Duration
}

// NewReleaseClient creates a proxy-aware GitHub/Gitee release client.
func NewReleaseClient(options ReleaseClientOptions) *ReleaseClient {
	client := options.HTTPClient
	if client == nil {
		transport := netx.NewTransport(options.Proxy)
		transport.ResponseHeaderTimeout = releaseTimeout
		client = &http.Client{Transport: transport}
	}
	retryDelay := options.RetryDelay
	if retryDelay == nil {
		retryDelay = func(attempt int) time.Duration {
			return time.Second << (attempt - 1)
		}
	}
	direct := options.HTTPClient
	if direct == nil {
		transport := netx.NewTransport(nil)
		transport.ResponseHeaderTimeout = releaseTimeout
		direct = &http.Client{Transport: transport}
	}
	return &ReleaseClient{
		http:                client,
		direct:              direct,
		mirrors:             downloadMirrors(),
		downloadChunkSize:   configuredDownloadChunkSize(),
		downloadParts:       configuredDownloadParts(),
		downloadPerSource:   configuredDownloadPerSource(),
		downloadIdleTimeout: defaultDownloadIdleTimeout,
		githubAPI:           defaultGitHubAPI,
		giteeAPI:            defaultGiteeAPI,
		githubToken:         strings.TrimSpace(options.GitHubToken),
		retryDelay:          retryDelay,
	}
}

// ResolveLatest resolves the configured GitHub release and falls back to Gitee.
func ResolveLatest(ctx context.Context, repo, mirror string) ([]ReleaseAsset, error) {
	return NewReleaseClient(ReleaseClientOptions{GitHubToken: os.Getenv("GITHUB_TOKEN")}).
		ResolveLatest(ctx, repo, mirror)
}

// ResolveLatest reads the latest release from GitHub, then Gitee if GitHub is
// unavailable or returns an unusable response.
func (c *ReleaseClient) ResolveLatest(ctx context.Context, repo, mirror string) ([]ReleaseAsset, error) {
	githubAssets, githubErr := c.latestAssets(ctx, c.githubAPI, repo, "GitHub", c.githubToken != "")
	if githubErr == nil {
		return githubAssets, nil
	}
	giteeAssets, giteeErr := c.latestAssets(ctx, c.giteeAPI, mirror, "Gitee", false)
	if giteeErr == nil {
		return giteeAssets, nil
	}
	return nil, domain.E(domain.KindUpstream, "在线源不可达", errors.Join(githubErr, giteeErr))
}

func (c *ReleaseClient) latestAssets(ctx context.Context, base, repo, provider string, authenticated bool) ([]ReleaseAsset, error) {
	repo = strings.Trim(repo, "/")
	if len(strings.Split(repo, "/")) != 2 {
		return nil, fmt.Errorf("%s 仓库地址无效", provider)
	}
	requestCtx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet,
		strings.TrimRight(base, "/")+"/repos/"+url.PathEscape(strings.Split(repo, "/")[0])+"/"+
			url.PathEscape(strings.Split(repo, "/")[1])+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+c.githubToken)
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	response, err := c.http.Do(request)
	if err != nil && c.direct != nil {
		// The configured proxy is flaky for api.github.com; retry directly.
		retryRequest, buildErr := http.NewRequestWithContext(requestCtx, http.MethodGet, request.URL.String(), nil)
		if buildErr == nil {
			retryRequest.Header = request.Header.Clone()
			response, err = c.direct.Do(retryRequest)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", provider, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("%s 返回 %d", provider, response.StatusCode)
	}

	var payload struct {
		Assets []ReleaseAsset `json:"assets"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析 %s 数据失败: %w", provider, err)
	}
	for index := range payload.Assets {
		if payload.Assets[index].DownloadURL == "" {
			payload.Assets[index].DownloadURL = payload.Assets[index].AlternativeDownload
		}
	}
	return payload.Assets, nil
}

// SelectPackages returns the newest valid package for every requested source.
func SelectPackages(assets []ReleaseAsset, options SelectOptions) ([]Package, error) {
	mode := strings.ToLower(strings.TrimSpace(options.Mode))
	if mode == "" {
		mode = ModeFull
	}
	if mode != ModeFull && mode != ModeRecent30D {
		return nil, domain.E(domain.KindInvalid, "同步模式无效", nil)
	}

	sources, err := normalizeSources(options.Sources)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]Package, len(sources))
	for _, asset := range assets {
		parsed, ok := parsePackage(asset, mode)
		if !ok {
			continue
		}
		current, exists := selected[parsed.Source]
		if !exists || parsed.Timestamp > current.Timestamp ||
			(parsed.Timestamp == current.Timestamp && parsed.Name > current.Name) {
			selected[parsed.Source] = parsed
		}
	}

	packages := make([]Package, 0, len(sources))
	for _, source := range sources {
		if selectedPackage, ok := selected[source]; ok {
			packages = append(packages, selectedPackage)
		}
	}
	return packages, nil
}

// ListPackages returns every recognized full or recent package in a release.
func ListPackages(assets []ReleaseAsset) []Package {
	packages := make([]Package, 0, len(assets))
	for _, asset := range assets {
		for _, mode := range []string{ModeFull, ModeRecent30D} {
			if parsed, ok := parsePackage(asset, mode); ok {
				packages = append(packages, parsed)
				break
			}
		}
	}
	sort.SliceStable(packages, func(i, j int) bool {
		if packages[i].Source != packages[j].Source {
			return packages[i].Source < packages[j].Source
		}
		if packages[i].Mode != packages[j].Mode {
			return packages[i].Mode < packages[j].Mode
		}
		return packages[i].Timestamp > packages[j].Timestamp
	})
	return packages
}

func parsePackage(asset ReleaseAsset, mode string) (Package, bool) {
	var matches []string
	switch mode {
	case ModeFull:
		matches = fullPackagePattern.FindStringSubmatch(asset.Name)
		if len(matches) != 4 {
			return Package{}, false
		}
	case ModeRecent30D:
		matches = recentPackagePattern.FindStringSubmatch(asset.Name)
		if len(matches) != 3 || matches[1] == "" {
			return Package{}, false
		}
	default:
		return Package{}, false
	}

	sourceIndex := 1
	timestampIndex := 3
	rows := int64(0)
	if mode == ModeRecent30D {
		timestampIndex = 2
	} else {
		fmt.Sscanf(matches[2], "%d", &rows)
	}
	source, ok := normalizeSource(matches[sourceIndex])
	if !ok {
		return Package{}, false
	}
	return Package{
		Source:      source,
		Mode:        mode,
		Name:        asset.Name,
		Size:        asset.Size,
		DownloadURL: asset.DownloadURL,
		Timestamp:   matches[timestampIndex],
		Rows:        rows,
	}, true
}

func normalizeSources(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{SourceX1080X, SourceSehuatang, SourceLTX, SourceFTAV}, nil
	}
	seen := make(map[string]struct{}, len(values))
	sources := make([]string, 0, len(values))
	for _, value := range values {
		source, ok := normalizeSource(value)
		if !ok {
			return nil, domain.E(domain.KindInvalid, "数据源无效："+value, nil)
		}
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		sources = append(sources, source)
	}
	return sources, nil
}

func normalizeSource(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SourceX1080X, "x-1080x":
		return SourceX1080X, true
	case SourceSehuatang, "色花堂":
		return SourceSehuatang, true
	case "ltx":
		return SourceLTX, true
	case "ftav":
		return SourceFTAV, true
	default:
		return "", false
	}
}

// DownloadAsset downloads one release asset and validates it against the size
// advertised by the release API.
func (c *ReleaseClient) DownloadAsset(ctx context.Context, asset ReleaseAsset, file *os.File, progress func(int64)) error {
	if asset.DownloadURL == "" {
		return domain.E(domain.KindInvalid, "数据包缺少下载地址", nil)
	}
	parsed, err := url.Parse(asset.DownloadURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return domain.E(domain.KindInvalid, "数据包下载地址无效", err)
	}

	meter := &downloadMeter{report: progress}
	started := time.Now()
	path, lastErr := c.downloadAttempt(ctx, asset, file, meter)
	if lastErr == nil {
		logDownloadThroughput(asset.Name, path, asset.Size, time.Since(started))
		return nil
	}
	if ctx.Err() != nil {
		return domain.E(domain.KindCanceled, "同步已取消", ctx.Err())
	}
	return domain.E(domain.KindUpstream, "下载在线数据失败", lastErr)
}

func (c *ReleaseClient) downloadAttempt(ctx context.Context, asset ReleaseAsset, file *os.File, meter *downloadMeter) (downloadPath, error) {
	type downloadStep struct {
		path     downloadPath
		download func() error
	}

	steps := []downloadStep{
		{downloadPathMultiSource, func() error {
			return c.downloadRanged(ctx, asset.DownloadURL, asset.Name, file, asset.Size, meter.set)
		}},
		{downloadPathProxySingle, func() error {
			_, err := c.downloadSingle(ctx, asset, file, meter)
			return err
		}},
	}

	failures := make([]error, 0, len(steps))
	for _, step := range steps {
		if err := resetDownloadFile(file); err != nil {
			return step.path, err
		}
		started := time.Now()
		err := step.download()
		if err == nil {
			return step.path, nil
		}
		logDownloadFailure(step.path, asset.Name, time.Since(started), meter.bytesValue(), err)
		failures = append(failures, fmt.Errorf("%s: %w", step.path, err))
		if ctx.Err() != nil {
			return step.path, ctx.Err()
		}
	}
	return steps[len(steps)-1].path, errors.Join(failures...)
}

func (c *ReleaseClient) downloadSingle(ctx context.Context, asset ReleaseAsset, file *os.File, meter *downloadMeter) (int64, error) {
	var lastErr error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		if err := resetDownloadFile(file); err != nil {
			return meter.bytesValue(), err
		}
		started := time.Now()
		written, err := c.streamTo(ctx, c.http, asset.DownloadURL, asset, file, meter.set)
		if err == nil {
			return written, nil
		}
		lastErr = err
		logDownloadFailure(downloadPathProxySingle, asset.Name, time.Since(started), written, err,
			"attempt", attempt)
		if attempt == downloadAttempts {
			break
		}
		if err := waitToRetry(ctx, c.retryDelay(attempt)); err != nil {
			return written, err
		}
	}
	return meter.bytesValue(), lastErr
}

func resetDownloadFile(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}

func waitToRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var errDownloadIdleTimeout = errors.New("下载读取空闲超时")

func newDownloadRequestContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelCauseFunc, *time.Timer) {
	requestCtx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(timeout, func() { cancel(errDownloadIdleTimeout) })
	return requestCtx, cancel, timer
}

func downloadRequestError(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), errDownloadIdleTimeout) {
		return errDownloadIdleTimeout
	}
	return err
}

type downloadPath string

type downloadMeter struct {
	mu     sync.Mutex
	report func(int64)
	bytes  int64
}

func (meter *downloadMeter) set(bytes int64) {
	meter.mu.Lock()
	defer meter.mu.Unlock()
	if bytes <= meter.bytes {
		return
	}
	meter.bytes = bytes
	if meter.report != nil {
		meter.report(bytes)
	}
}

func (meter *downloadMeter) bytesValue() int64 {
	meter.mu.Lock()
	defer meter.mu.Unlock()
	return meter.bytes
}

func logDownloadFailure(path downloadPath, packageName string, duration time.Duration, downloaded int64, err error, extra ...any) {
	attrs := []any{
		"path", string(path),
		"package", packageName,
		"duration", duration,
		"downloaded_bytes", downloaded,
		"error", err,
	}
	slog.Warn("avdb asset download failed", append(attrs, extra...)...)
}

func logDownloadThroughput(packageName string, path downloadPath, downloaded int64, duration time.Duration) {
	bytesPerSecond := float64(downloaded)
	if duration > 0 {
		bytesPerSecond /= duration.Seconds()
	}
	attrs := []any{
		"total_bytes", downloaded,
		"duration", duration,
		"average_kbps", fmt.Sprintf("%.1f KB/s", bytesPerSecond/1024),
		"path", string(path),
		"package", packageName,
	}
	if bytesPerSecond < 200*1024 {
		slog.Warn("avdb sync throughput", attrs...)
		return
	}
	slog.Info("avdb sync throughput", attrs...)
}

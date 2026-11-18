package avdb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/domain"
)

// SyncRequest selects the release packages to import.
type SyncRequest struct {
	Sources []string `json:"sources"`
	Mode    string   `json:"mode"`
}

// SyncJob identifies an accepted background synchronization job.
type SyncJob struct {
	ID       string    `json:"job_id"`
	Packages []Package `json:"packages"`
}

// SyncState is the persisted result for one data source.
type SyncState struct {
	Source   string `json:"source"`
	Mode     string `json:"mode"`
	Package  string `json:"package"`
	Imported int64  `json:"imported"`
	Total    int64  `json:"total"`
	SyncedAt string `json:"synced_at"`
}

// SyncProgress is the live in-memory progress of the active job.
type SyncProgress struct {
	Current    string `json:"current"`
	Source     string `json:"source,omitempty"`
	Phase      string `json:"phase,omitempty"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	Imported   int64  `json:"imported,omitempty"`
	Rows       int64  `json:"rows,omitempty"`
}

// SyncResult summarizes one completed background job.
type SyncResult struct {
	JobID       string    `json:"job_id"`
	Packages    []Package `json:"packages"`
	Imported    int64     `json:"imported"`
	Total       int64     `json:"total"`
	CompletedAt string    `json:"completed_at"`
}

// SyncStatus combines live progress with persisted per-source results.
type SyncStatus struct {
	Running    bool        `json:"running"`
	Current    string      `json:"current"`
	Source     string      `json:"source,omitempty"`
	Bytes      int64       `json:"bytes"`
	TotalBytes int64       `json:"total_bytes,omitempty"`
	Phase      string      `json:"phase,omitempty"`
	Imported   int64       `json:"imported,omitempty"`
	Rows       int64       `json:"rows,omitempty"`
	Last       []SyncState `json:"last"`
	Result     *SyncResult `json:"result,omitempty"`
	LastError  string      `json:"last_error,omitempty"`
}

type zipImporter interface {
	ImportZip(ctx context.Context, name string, body io.Reader) (ImportResult, error)
}

type syncStateStore interface {
	SaveSyncState(ctx context.Context, state SyncState) error
	ListSyncStates(ctx context.Context) ([]SyncState, error)
}

type packageClient interface {
	ResolveLatest(ctx context.Context, repo, mirror string) ([]ReleaseAsset, error)
	DownloadAsset(ctx context.Context, asset ReleaseAsset, file *os.File, progress func(int64)) error
}

// SyncDependencies contains the storage and upstream clients used by Sync.
type SyncDependencies struct {
	Importer zipImporter
	State    syncStateStore
	Releases packageClient
}

// SyncOptions overrides the environment-configured release locations.
type SyncOptions struct {
	Repo   string
	Mirror string
}

type syncRun struct {
	job    SyncJob
	result SyncResult
}

// SyncService serializes AVDB package downloads and imports.
type SyncService struct {
	importer zipImporter
	state    syncStateStore
	releases packageClient
	repo     string
	mirror   string

	startMu   sync.Mutex
	lifecycle sync.Mutex
	running   bool
	active    SyncProgress
	result    *SyncResult
	lastError string
}

// NewSyncService creates an online AVDB synchronization service.
func NewSyncService(dependencies SyncDependencies, options SyncOptions) *SyncService {
	repo := options.Repo
	if repo == "" {
		repo = envOrDefault("MIYABI_AVDB_RELEASE_REPO", DefaultReleaseRepo)
	}
	mirror := options.Mirror
	if mirror == "" {
		mirror = envOrDefault("MIYABI_AVDB_RELEASE_MIRROR", DefaultReleaseMirror)
	}
	releases := dependencies.Releases
	if releases == nil {
		releases = NewReleaseClient(ReleaseClientOptions{GitHubToken: os.Getenv("GITHUB_TOKEN")})
	}
	return &SyncService{
		importer: dependencies.Importer,
		state:    dependencies.State,
		releases: releases,
		repo:     repo,
		mirror:   mirror,
	}
}

// Packages lists the valid packages currently exposed by the online release.
func (s *SyncService) Packages(ctx context.Context) ([]Package, error) {
	assets, err := s.releases.ResolveLatest(ctx, s.repo, s.mirror)
	if err != nil {
		return nil, err
	}
	return ListPackages(assets), nil
}

// Start accepts one job and runs it in the background. Calls are serialized.
func (s *SyncService) Start(ctx context.Context, request SyncRequest) (SyncJob, error) {
	run, err := s.prepare(ctx, request)
	if err != nil {
		return SyncJob{}, err
	}
	go func() {
		result, syncErr := s.execute(context.WithoutCancel(ctx), run)
		s.finish(run, result, syncErr)
	}()
	return run.job, nil
}

// Sync resolves, downloads, and imports the selected packages synchronously.
func (s *SyncService) Sync(ctx context.Context, request SyncRequest) (SyncResult, error) {
	run, err := s.prepare(ctx, request)
	if err != nil {
		return SyncResult{}, err
	}
	result, err := s.execute(ctx, run)
	s.finish(run, result, err)
	return result, err
}

// Status reports live progress and the latest persisted import per source.
func (s *SyncService) Status(ctx context.Context) (SyncStatus, error) {
	if s.state == nil {
		return SyncStatus{}, domain.E(domain.KindInvalid, "资源库未初始化", nil)
	}
	last, err := s.state.ListSyncStates(ctx)
	if err != nil {
		return SyncStatus{}, err
	}
	if last == nil {
		last = []SyncState{}
	}

	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	status := SyncStatus{
		Running:    s.running,
		Current:    s.active.Current,
		Source:     s.active.Source,
		Bytes:      s.active.Bytes,
		TotalBytes: s.active.TotalBytes,
		Phase:      s.active.Phase,
		Imported:   s.active.Imported,
		Rows:       s.active.Rows,
		Last:       last,
		Result:     s.result,
		LastError:  s.lastError,
	}
	return status, nil
}

func (s *SyncService) prepare(ctx context.Context, request SyncRequest) (*syncRun, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.lifecycle.Lock()
	running := s.running
	s.lifecycle.Unlock()
	if running {
		return nil, domain.E(domain.KindBusy, "已有在线同步任务正在运行", nil)
	}
	if s.importer == nil || s.state == nil {
		return nil, domain.E(domain.KindInvalid, "资源库未初始化", nil)
	}

	assets, err := s.releases.ResolveLatest(ctx, s.repo, s.mirror)
	if err != nil {
		return nil, err
	}
	packages, err := SelectPackages(assets, SelectOptions{Sources: request.Sources, Mode: request.Mode})
	if err != nil {
		return nil, err
	}
	if len(packages) == 0 {
		return nil, domain.E(domain.KindInvalid, "所选条件下没有可更新的数据包", nil)
	}

	run := &syncRun{job: SyncJob{ID: newSyncJobID(), Packages: packages}}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.running = true
	s.active = SyncProgress{}
	s.result = nil
	s.lastError = ""
	return run, nil
}

func (s *SyncService) execute(ctx context.Context, run *syncRun) (SyncResult, error) {
	result := SyncResult{JobID: run.job.ID, Packages: run.job.Packages}
	for _, pkg := range run.job.Packages {
		if err := ctx.Err(); err != nil {
			return result, domain.E(domain.KindCanceled, "同步已取消", err)
		}
		s.setProgress(SyncProgress{
			Current:    pkg.Name,
			Source:     pkg.Source,
			Phase:      "downloading",
			TotalBytes: pkg.Size,
			Rows:       pkg.Rows,
		})

		file, err := os.CreateTemp("", "miyabi-avdb-*.zip")
		if err != nil {
			return result, err
		}
		path := file.Name()
		err = s.releases.DownloadAsset(ctx, ReleaseAsset{
			Name:        pkg.Name,
			Size:        pkg.Size,
			DownloadURL: pkg.DownloadURL,
		}, file, func(downloaded int64) {
			s.setDownloaded(downloaded)
		})
		_ = file.Close()
		if err != nil {
			_ = os.Remove(path)
			return result, err
		}

		s.setProgress(SyncProgress{
			Current:    pkg.Name,
			Source:     pkg.Source,
			Phase:      "importing",
			Bytes:      pkg.Size,
			TotalBytes: pkg.Size,
			Rows:       pkg.Rows,
		})
		imported, err := importDownloadedPackage(ctx, s.importer, pkg, path)
		_ = os.Remove(path)
		if err != nil {
			return result, err
		}

		state := SyncState{
			Source:   pkg.Source,
			Mode:     pkg.Mode,
			Package:  pkg.Name,
			Imported: imported.Imported,
			Total:    imported.Total,
			SyncedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := s.state.SaveSyncState(ctx, state); err != nil {
			return result, err
		}
		result.Imported += imported.Imported
		result.Total = imported.Total
		s.setProgress(SyncProgress{
			Current:    pkg.Name,
			Source:     pkg.Source,
			Phase:      "imported",
			Bytes:      pkg.Size,
			TotalBytes: pkg.Size,
			Imported:   imported.Imported,
			Rows:       pkg.Rows,
		})
	}
	result.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	run.result = result
	return result, nil
}

func importDownloadedPackage(ctx context.Context, importer zipImporter, pkg Package, path string) (ImportResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return ImportResult{}, err
	}

	plain, innerName, encrypted, err := decryptResourceLibrary(file, info.Size())
	if err != nil {
		return ImportResult{}, domain.E(domain.KindUpstream, "资源库包解密失败", err)
	}
	if encrypted {
		return importer.ImportZip(ctx, innerName, bytes.NewReader(plain))
	}
	return importer.ImportZip(ctx, pkg.Name, io.NewSectionReader(file, 0, info.Size()))
}

func (s *SyncService) finish(_ *syncRun, result SyncResult, err error) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.running = false
	if err != nil {
		s.lastError = domain.PublicMessage(err)
		return
	}
	completed := result
	s.result = &completed
	s.lastError = ""
}

func (s *SyncService) setProgress(progress SyncProgress) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.active = progress
}

func (s *SyncService) setDownloaded(bytes int64) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.active.Bytes = bytes
}

func newSyncJobID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("avdb-%d", time.Now().UnixNano())
	}
	return "avdb-" + hex.EncodeToString(value[:])
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

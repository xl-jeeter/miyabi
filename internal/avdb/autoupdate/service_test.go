package autoupdate

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/avdb"
	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
)

type syncerStub struct {
	mu      sync.Mutex
	status  avdb.SyncStatus
	request avdb.SyncRequest
	result  avdb.SyncResult
	err     error
	syncs   int
	starts  int
	busyErr error
}

func (stub *syncerStub) Sync(_ context.Context, request avdb.SyncRequest) (avdb.SyncResult, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.syncs++
	stub.request = request
	return stub.result, stub.err
}

// Start emulates avdb.SyncService.Start: a busy service rejects the job,
// otherwise the job is accepted and its result is visible via Status.
func (stub *syncerStub) Start(_ context.Context, request avdb.SyncRequest) (avdb.SyncJob, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.busyErr != nil {
		return avdb.SyncJob{}, stub.busyErr
	}
	stub.starts++
	stub.request = request
	if stub.err == nil {
		result := stub.result
		stub.status = avdb.SyncStatus{Running: false, Result: &result}
	} else {
		stub.status = avdb.SyncStatus{Running: false, LastError: domain.PublicMessage(stub.err)}
	}
	return avdb.SyncJob{ID: "job-1"}, nil
}

func (stub *syncerStub) Status(context.Context) (avdb.SyncStatus, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.status, nil
}

func (stub *syncerStub) Packages(context.Context) ([]avdb.Package, error) {
	return []avdb.Package{
		{Source: avdb.SourceX1080X, Mode: avdb.ModeFull, Name: "All_X1080X_2_2026-11-18.zip"},
		{Source: avdb.SourceSehuatang, Mode: avdb.ModeFull, Name: "All_sehuatang_2026-11-18.zip"},
		{Source: avdb.SourceLTX, Mode: avdb.ModeFull, Name: "All_LTX_2026-11-18.zip"},
	}, nil
}

func (stub *syncerStub) calls() int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.syncs
}

func (stub *syncerStub) startCount() int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.starts
}

func (stub *syncerStub) lastRequest() avdb.SyncRequest {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.request
}

func newService(t *testing.T) (*Service, *syncerStub) {
	t.Helper()
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stub := &syncerStub{result: avdb.SyncResult{Imported: 5, Total: 120}}
	service := New(store.Client, stub,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return service, stub
}

// due makes the next scheduled run immediate, as if the interval had elapsed.
func due(service *Service) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.nextDue = service.nextDue.Add(-24 * time.Hour)
}

// saveRaw persists a raw JSON row, emulating a corrupted or foreign payload.
func saveRaw(t *testing.T, service *Service, value string) error {
	t.Helper()
	return database.SaveSetting(t.Context(), service.database, autoUpdateSetting, json.RawMessage(value))
}

func TestSettingsDefaultWhenMissing(t *testing.T) {
	service, _ := newService(t)
	settings, err := service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || settings.IntervalHours != 12 || !settings.Notify ||
		len(settings.Sources) != 0 {
		t.Fatalf("defaults = %#v", settings)
	}
}

func TestSettingsIntervalValidation(t *testing.T) {
	service, _ := newService(t)
	for _, hours := range []int{0, 3, 5, 6, 7, 13, 48} {
		_, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{
			Enabled: true, IntervalHours: hours,
		})
		if !domain.IsKind(err, domain.KindInvalid) {
			t.Fatalf("invalid %d accepted: %v", hours, err)
		}
	}
	for _, hours := range []int{1, 2, 4, 8, 12, 24} {
		saved, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{
			Enabled: true, IntervalHours: hours,
		})
		if err != nil {
			t.Fatalf("valid %d rejected: %v", hours, err)
		}
		if saved.IntervalHours != hours {
			t.Fatalf("hours %d changed to %d", hours, saved.IntervalHours)
		}
	}
}

func TestSettingsRejectInvalidSources(t *testing.T) {
	service, _ := newService(t)
	_, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{
		IntervalHours: 12, Sources: []string{avdb.SourceX1080X, "unknown"},
	})
	if !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestSettingsNormalizeSavedRows(t *testing.T) {
	service, _ := newService(t)
	if err := saveRaw(t, service, `{"enabled":true,"interval_hours":7,"sources":["x1080x","x1080x","bogus"],"notify":false}`); err != nil {
		t.Fatal(err)
	}
	settings, err := service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Sources) != 1 || settings.Sources[0] != avdb.SourceX1080X {
		t.Fatalf("sources = %#v", settings.Sources)
	}
	if settings.IntervalHours != 12 {
		t.Fatalf("interval = %d, want fallback 12", settings.IntervalHours)
	}
	if settings.Notify {
		t.Fatal("explicit notify=false must survive, not default to true")
	}
}

func TestUpdateSettingsKeepsRunHistory(t *testing.T) {
	service, stub := newService(t)
	if _, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{Enabled: true, IntervalHours: 12}); err != nil {
		t.Fatal(err)
	}
	due(service)
	if err := service.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before.LastRunAt == "" || before.LastResult == "" {
		t.Fatalf("run summary missing: %#v", before)
	}

	// Saving a settings change must not wipe the recorded run history.
	saved, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{Enabled: true, IntervalHours: 4})
	if err != nil {
		t.Fatal(err)
	}
	if saved.LastRunAt != before.LastRunAt || saved.LastResult != before.LastResult {
		t.Fatalf("history clobbered by save: %#v", saved)
	}
	if stub.calls() != 1 {
		t.Fatalf("save must not trigger a sync: %d", stub.calls())
	}
}

func TestTickWaitsUntilDue(t *testing.T) {
	service, stub := newService(t)
	if err := service.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stub.calls() != 0 {
		t.Fatalf("tick before due ran sync: %d", stub.calls())
	}
}

func TestTickSkipsWhenDisabledOrRunning(t *testing.T) {
	service, stub := newService(t)
	if _, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{Enabled: false, IntervalHours: 12}); err != nil {
		t.Fatal(err)
	}

	stub.status.Running = true
	due(service)
	if err := service.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stub.calls() != 0 {
		t.Fatalf("sync ran while busy: %d", stub.calls())
	}

	stub.status = avdb.SyncStatus{}
	due(service)
	if err := service.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stub.calls() != 0 {
		t.Fatalf("sync ran while disabled: %d", stub.calls())
	}
}

func TestTickSyncsAndPersistsSummary(t *testing.T) {
	service, stub := newService(t)
	if _, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{
		Enabled: true, IntervalHours: 4, Sources: []string{avdb.SourceX1080X, avdb.SourceLTX},
	}); err != nil {
		t.Fatal(err)
	}

	due(service)
	if err := service.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stub.calls() != 1 {
		t.Fatalf("syncs = %d", stub.calls())
	}
	if len(stub.request.Sources) != 2 || stub.request.Sources[0] != avdb.SourceX1080X {
		t.Fatalf("request = %#v", stub.request)
	}

	settings, err := service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastRunAt == "" || settings.LastResult == "" {
		t.Fatalf("summary not persisted: %#v", settings)
	}
	if settings.LastResult != "成功：导入 5 行，库内合计 120 行" {
		t.Fatalf("summary = %q", settings.LastResult)
	}
	if settings.IntervalHours != 4 || len(settings.Sources) != 2 || !settings.Enabled {
		t.Fatalf("other fields clobbered: %#v", settings)
	}
}

func TestTickRecordsFailureSummary(t *testing.T) {
	service, stub := newService(t)
	stub.err = domain.E(domain.KindUpstream, "在线源不可达", nil)
	due(service)
	if err := service.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	settings, err := service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastResult != "失败：在线源不可达" {
		t.Fatalf("summary = %q", settings.LastResult)
	}
}

func TestRunNowRejectsWhenBusy(t *testing.T) {
	service, stub := newService(t)
	stub.busyErr = domain.E(domain.KindBusy, "已有在线同步任务正在运行", nil)
	err := service.RunNow(t.Context())
	if !domain.IsKind(err, domain.KindBusy) {
		t.Fatalf("err = %v", err)
	}
	if stub.startCount() != 0 {
		t.Fatalf("sync started while busy: %d", stub.startCount())
	}
}

func TestRunNowStartsDetachedJobAndResetsSchedule(t *testing.T) {
	service, stub := newService(t)
	if err := service.RunNow(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stub.startCount() != 1 {
		t.Fatalf("starts = %d", stub.startCount())
	}
	select {
	case <-service.Wake():
	default:
		t.Fatal("run-now must reschedule via wake")
	}
	// The restarted schedule must hold off an immediate tick.
	if err := service.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stub.startCount() != 1 {
		t.Fatalf("schedule not reset after run-now: %d", stub.startCount())
	}
}

func TestRunNowRecordsDetachedResult(t *testing.T) {
	service, stub := newService(t)
	if _, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{
		Enabled: true, IntervalHours: 12, Sources: []string{avdb.SourceX1080X},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.RunNow(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		settings, err := service.Settings(t.Context())
		return err == nil && settings.LastResult != ""
	})
	settings, err := service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastResult != "成功：导入 5 行，库内合计 120 行" {
		t.Fatalf("summary = %q", settings.LastResult)
	}
	if len(stub.lastRequest().Sources) != 1 || stub.lastRequest().Sources[0] != avdb.SourceX1080X {
		t.Fatalf("request = %#v", stub.lastRequest())
	}
}

func TestStatusReportsPackageSourcesAndNextRun(t *testing.T) {
	service, _ := newService(t)
	if err := service.RunNow(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.NextRunAt == "" {
		t.Fatalf("status = %#v", status)
	}
	if len(status.PackageSources) != 3 || status.PackageSources[0] != "LTX" {
		t.Fatalf("package sources = %#v", status.PackageSources)
	}
}

// waitFor polls condition until it holds or the deadline passes.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func TestInitializeResumesCountdownAfterRestart(t *testing.T) {
	service, stub := newService(t)
	if _, err := service.UpdateSettings(t.Context(), AutoUpdateSettings{Enabled: true, IntervalHours: 8}); err != nil {
		t.Fatal(err)
	}
	due(service)
	if err := service.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Simulate a restart: a fresh service must derive nextDue from LastRunAt
	// instead of re-syncing immediately.
	restarted := New(service.database, stub,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := restarted.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stub.calls() != 1 {
		t.Fatalf("restart re-synced before due: %d", stub.calls())
	}
}

package autoupdate

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/avdb"
	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
)

// autoUpdateSetting is the settings key holding the AutoUpdateSettings JSON row.
const autoUpdateSetting = "avdb.auto_update"

// DefaultIntervalHours is the fallback interval when none is configured.
const DefaultIntervalHours = 12

// TickInterval is how often the scheduler wakes to check whether a run is due.
// The real schedule is nextDue; this only bounds how late a missed wake is
// noticed. It must stay small enough for every allowed interval to fire on time.
const TickInterval = time.Minute

// validIntervalHours is the only set of intervals a user may choose.
var validIntervalHours = map[int]bool{1: true, 2: true, 4: true, 8: true, 12: true, 24: true}

// AutoUpdateSettings is the persisted auto-update configuration.
type AutoUpdateSettings struct {
	Enabled       bool     `json:"enabled"`
	IntervalHours int      `json:"interval_hours"`
	Sources       []string `json:"sources"`
	Notify        bool     `json:"notify"`
	LastRunAt     string   `json:"last_run_at,omitempty"`
	LastResult    string   `json:"last_result,omitempty"`
}

// DefaultSettings returns the defaults applied when no row exists yet.
func DefaultSettings() AutoUpdateSettings {
	return AutoUpdateSettings{Enabled: true, IntervalHours: DefaultIntervalHours, Notify: true}
}

// UnmarshalJSON layers decoded fields over the defaults so a partial or legacy
// row keeps defaults for the fields it does not mention.
func (settings *AutoUpdateSettings) UnmarshalJSON(data []byte) error {
	type plain AutoUpdateSettings
	value := plain(DefaultSettings())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*settings = AutoUpdateSettings(value)
	return nil
}

// Normalized returns settings with the interval forced into the whitelist and
// duplicate, invalid sources dropped.
func (settings AutoUpdateSettings) Normalized() AutoUpdateSettings {
	if !validIntervalHours[settings.IntervalHours] {
		settings.IntervalHours = DefaultIntervalHours
	}
	settings.Sources = validSources(settings.Sources)
	return settings
}

// Status is the API projection of the auto-update configuration and scheduler state.
type Status struct {
	Enabled        bool     `json:"enabled"`
	IntervalHours  int      `json:"interval_hours"`
	Sources        []string `json:"sources"`
	Notify         bool     `json:"notify"`
	LastRunAt      string   `json:"last_run_at"`
	LastResult     string   `json:"last_result"`
	Running        bool     `json:"running"`
	NextRunAt      string   `json:"next_run_at"`
	PackageSources []string `json:"package_sources"`
}

// Syncer is the SyncService surface the scheduler drives.
type Syncer interface {
	Sync(ctx context.Context, request avdb.SyncRequest) (avdb.SyncResult, error)
	Start(ctx context.Context, request avdb.SyncRequest) (avdb.SyncJob, error)
	Status(ctx context.Context) (avdb.SyncStatus, error)
	Packages(ctx context.Context) ([]avdb.Package, error)
}

// Service schedules periodic AVDB library synchronization and exposes the
// settings plus scheduler state to the API.
//
// The schedule is anchored to nextDue: Tick only syncs when a run is due, and
// saving settings or run-now resets nextDue so the new interval applies
// immediately instead of after the old one expires.
type Service struct {
	database *ent.Client
	sync     Syncer
	logger   *slog.Logger
	wake     chan struct{}

	mu      sync.Mutex
	nextDue time.Time
}

// New creates the auto-update service. wake is the channel RunPeriodic waits
// on; saving settings or triggering a run-now pushes into it to reschedule.
func New(database *ent.Client, sync Syncer, logger *slog.Logger, wake chan struct{}) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if wake == nil {
		wake = make(chan struct{}, 1)
	}
	service := &Service{database: database, sync: sync, logger: logger, wake: wake}
	service.initialize()
	return service
}

// initialize derives the first schedule anchor from the persisted last run so
// a restart resumes the countdown instead of re-syncing immediately.
func (service *Service) initialize() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	settings, err := service.Settings(ctx)
	if err != nil {
		service.logger.WarnContext(ctx, "avdb auto update settings unreadable; using defaults", "error", err)
	}
	anchor := time.Now()
	if last, parseErr := time.Parse(time.RFC3339, settings.LastRunAt); parseErr == nil {
		anchor = last
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.nextDue = anchor.Add(time.Duration(settings.IntervalHours) * time.Hour)
}

// Wake exposes the channel RunPeriodic selects on.
func (service *Service) Wake() <-chan struct{} { return service.wake }

func (service *Service) signal() {
	select {
	case service.wake <- struct{}{}:
	default:
	}
}

// Settings loads the persisted configuration, falling back to defaults when
// the row is missing or corrupted.
func (service *Service) Settings(ctx context.Context) (AutoUpdateSettings, error) {
	settings, found, err := database.LoadSetting[AutoUpdateSettings](ctx, service.database, autoUpdateSetting)
	if err != nil {
		return DefaultSettings().Normalized(), err
	}
	if !found {
		return DefaultSettings(), nil
	}
	return settings.Normalized(), nil
}

// settings serves paths that fall back to defaults when the row is unreadable.
func (service *Service) settings(ctx context.Context) AutoUpdateSettings {
	settings, err := service.Settings(ctx)
	if err != nil {
		service.logger.WarnContext(ctx, "avdb auto update settings unreadable; using defaults", "error", err)
	}
	return settings
}

// UpdateSettings validates and persists the configuration, then restarts the
// schedule so the new interval takes effect immediately. The run history
// fields are carried over from the saved row, not taken from the request.
func (service *Service) UpdateSettings(ctx context.Context, settings AutoUpdateSettings) (AutoUpdateSettings, error) {
	if !validIntervalHours[settings.IntervalHours] {
		return AutoUpdateSettings{}, domain.E(domain.KindInvalid,
			"更新间隔只支持 1/2/4/8/12/24 小时", nil)
	}
	for _, source := range settings.Sources {
		if !avdb.ValidSource(source) {
			return AutoUpdateSettings{}, domain.E(domain.KindInvalid, "数据源无效："+source, nil)
		}
	}
	normalized := settings.Normalized()
	previous := service.settings(ctx)
	normalized.LastRunAt = previous.LastRunAt
	normalized.LastResult = previous.LastResult
	if err := database.SaveSetting(ctx, service.database, autoUpdateSetting, normalized); err != nil {
		return AutoUpdateSettings{}, err
	}
	service.restartSchedule(settings.IntervalHours)
	service.signal()
	return normalized, nil
}

// restartSchedule anchors the next run at now + interval, mirroring the
// reference behaviour of restarting the timer after a configuration change.
func (service *Service) restartSchedule(intervalHours int) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.nextDue = time.Now().Add(time.Duration(intervalHours) * time.Hour)
}

// Status reports the configuration, live job state, and the next scheduled run.
// The release listing is best-effort: when the online sources are unreachable
// the section still renders, with an empty package_sources list.
func (service *Service) Status(ctx context.Context) (Status, error) {
	settings := service.settings(ctx)
	status := Status{
		Enabled:       settings.Enabled,
		IntervalHours: settings.IntervalHours,
		Sources:       settings.Sources,
		Notify:        settings.Notify,
		LastRunAt:     settings.LastRunAt,
		LastResult:    settings.LastResult,
	}
	if status.Sources == nil {
		status.Sources = []string{}
	}

	running := false
	if service.sync != nil {
		syncStatus, err := service.sync.Status(ctx)
		if err != nil {
			return Status{}, err
		}
		running = syncStatus.Running
	}
	status.Running = running

	service.mu.Lock()
	nextDue := service.nextDue
	service.mu.Unlock()
	if !nextDue.IsZero() && nextDue.After(time.Now()) {
		status.NextRunAt = nextDue.UTC().Format(time.RFC3339)
	}

	if packages, err := service.PackageSources(ctx); err == nil {
		status.PackageSources = packages
	} else {
		status.PackageSources = []string{}
		service.logger.WarnContext(ctx, "avdb auto update cannot list package sources", "error", err)
	}
	return status, nil
}

// PackageSources lists the data sources the current release exposes, so the
// frontend can offer a multi-select without duplicating the whitelist.
func (service *Service) PackageSources(ctx context.Context) ([]string, error) {
	if service.sync == nil {
		return []string{}, nil
	}
	packages, err := service.sync.Packages(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	sources := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if seen[pkg.Source] {
			continue
		}
		seen[pkg.Source] = true
		sources = append(sources, pkg.Source)
	}
	sort.Strings(sources)
	return sources, nil
}

// RunNow starts one background synchronization with the saved configuration
// and resets the schedule. A busy sync service is reported as a business error.
// The job itself runs detached, like the manual online-sync endpoint.
func (service *Service) RunNow(ctx context.Context) error {
	if service.sync == nil {
		return domain.E(domain.KindInvalid, "资源库未初始化", nil)
	}
	settings := service.settings(ctx)
	if _, err := service.sync.Start(ctx, avdb.SyncRequest{Sources: settings.Sources}); err != nil {
		return err
	}

	// Persist the summary once the detached job finishes. The watch context
	// must outlive the request, which returns as soon as the job is accepted.
	watchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), syncWatchTimeout)
	go func() {
		defer cancel()
		service.waitForRun(watchCtx, settings)
	}()
	service.restartSchedule(settings.IntervalHours)
	service.signal()
	return nil
}

// syncWatchTimeout bounds how long a manual run keeps the worker waiting for
// its summary; a full sync of every source can take tens of minutes.
const syncWatchTimeout = 2 * time.Hour

// waitForRun polls until the started job finishes, then records its summary.
// The job itself identifies itself through SyncService's serialized lifecycle:
// Start marks it running synchronously, so the first non-running status after
// acceptance reflects this job's outcome.
func (service *Service) waitForRun(ctx context.Context, settings AutoUpdateSettings) {
	for {
		status, err := service.sync.Status(ctx)
		if err != nil {
			return
		}
		if !status.Running {
			service.recordRun(ctx, settings, status.Result, status.LastError)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(syncWatchPoll):
		}
	}
}

// syncWatchPoll is how often a waiting manual run checks job progress.
const syncWatchPoll = 2 * time.Second

func (service *Service) recordRun(ctx context.Context, settings AutoUpdateSettings, result *avdb.SyncResult, syncErr string) {
	summary := ""
	if syncErr != "" {
		summary = "失败：" + syncErr
	} else if result != nil {
		summary = summaryOf(*result, nil)
	}
	if summary == "" {
		return
	}
	service.persistSummary(ctx, summary)
	if settings.Notify {
		service.logger.InfoContext(ctx, "avdb auto update manual run finished", "summary", summary)
	} else {
		service.logger.InfoContext(ctx, "avdb auto update manual run finished (notify disabled)", "summary", summary)
	}
}

// persistSummary records one run outcome, merging into the freshest saved
// settings so a summary landing after a settings change does not clobber it.
func (service *Service) persistSummary(ctx context.Context, summary string) {
	settings := service.settings(ctx)
	settings.LastRunAt = time.Now().UTC().Format(time.RFC3339)
	settings.LastResult = summary
	if err := database.SaveSetting(ctx, service.database, autoUpdateSetting, settings); err != nil {
		service.logger.WarnContext(ctx, "avdb auto update failed to persist run summary", "error", err)
	}
}

// Tick is the RunPeriodic callback: it syncs only when a run is due, skips when
// disabled or busy, and re-anchors the schedule before running.
func (service *Service) Tick(ctx context.Context) error {
	service.mu.Lock()
	due := service.nextDue
	service.mu.Unlock()
	if !due.IsZero() && time.Now().Before(due) {
		return nil
	}

	settings := service.settings(ctx)
	service.restartSchedule(settings.IntervalHours)
	if !settings.Enabled || service.sync == nil {
		return nil
	}
	if syncStatus, err := service.sync.Status(ctx); err == nil && syncStatus.Running {
		return nil
	}
	service.run(ctx, settings)
	return nil
}

func (service *Service) run(ctx context.Context, settings AutoUpdateSettings) {
	result, err := service.sync.Sync(ctx, avdb.SyncRequest{Sources: settings.Sources})
	summary := summaryOf(result, err)
	service.persistSummary(ctx, summary)

	if err != nil {
		service.logger.WarnContext(ctx, "avdb auto update failed", "summary", summary, "error", err)
		return
	}
	if settings.Notify {
		service.logger.InfoContext(ctx, "avdb auto update finished", "summary", summary)
	}
}

// summaryOf renders the short run result stored with the settings.
func summaryOf(result avdb.SyncResult, err error) string {
	if err != nil {
		return "失败：" + domain.PublicMessage(err)
	}
	return "成功：导入 " + formatCount(result.Imported) + " 行，库内合计 " + formatCount(result.Total) + " 行"
}

func formatCount(value int64) string {
	if value <= 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// validSources keeps the order while dropping duplicates and unknown sources.
func validSources(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	sources := make([]string, 0, len(values))
	for _, value := range values {
		if !avdb.ValidSource(value) || seen[value] {
			continue
		}
		seen[value] = true
		sources = append(sources, value)
	}
	if len(sources) == 0 {
		return nil
	}
	return sources
}

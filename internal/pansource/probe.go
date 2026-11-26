package pansource

import (
	"context"
	"strings"
	"time"

	"github.com/ppxb/miyabi/internal/drive/cd2"
)

// probeTimeout bounds a connection test end to end, dial included.
const probeTimeout = 5 * time.Second

// Account is one cloud account a CloudDrive2 instance serves.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProbeResult reports the outcome of a CloudDrive2 connection test.
type ProbeResult struct {
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latencyMs,omitempty"`
	Accounts  []Account `json:"accounts,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Probe connects with the supplied settings, falling back to the stored values
// for the fields left empty, and reports which accounts answered. It never
// returns an error: an unreachable instance is a failed probe, not a failure.
func (service *Service) Probe(ctx context.Context, input CD2) ProbeResult {
	stored, err := service.resolve(ctx)
	if err != nil {
		return ProbeResult{Error: "无法读取已保存的 CloudDrive2 设置"}
	}
	candidate := input.overlay(stored.CD2).withFallback(service.env.CD2)
	if err := validateCD2(candidate); err != nil {
		return ProbeResult{Error: err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	started := time.Now()
	driver, err := cd2.New(Settings{Provider: ProviderCD2, CD2: candidate}.CD2Config())
	if err != nil {
		return ProbeResult{Error: publicError(err)}
	}
	defer driver.Close()

	// Account lists the cloud accounts and reports the pinned one, so it both
	// proves the connection and answers "which account does this serve".
	account, err := driver.Account(ctx, "")
	if err != nil {
		return ProbeResult{Error: publicError(err)}
	}
	return ProbeResult{
		OK:        true,
		LatencyMs: time.Since(started).Milliseconds(),
		Accounts:  []Account{{ID: account.ID, Name: account.Name}},
	}
}

// publicError renders a probe failure. Adapter and gRPC errors describe the
// endpoint and the CloudDrive2 status, never the credentials, so the message
// is safe to show as is; an expired deadline reads better in plain words.
func publicError(err error) string {
	if strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		return "连接 CloudDrive2 超时（5 秒），请检查接入地址与网络"
	}
	return err.Error()
}

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ppxb/miyabi/internal/avdb/autoupdate"
	"github.com/ppxb/miyabi/internal/domain"
)

var (
	errInvalidInterval = domain.E(domain.KindInvalid, "更新间隔只支持 1/2/4/8/12/24 小时", nil)
	errBusySync        = domain.E(domain.KindBusy, "已有资源库同步任务正在运行", nil)
)

type autoUpdateStub struct {
	status   autoupdate.Status
	saved    autoupdate.AutoUpdateSettings
	update   autoupdate.AutoUpdateSettings
	updateOK bool
	runs     int
	busy     bool
}

func (stub *autoUpdateStub) Status(context.Context) (autoupdate.Status, error) {
	return stub.status, nil
}

func (stub *autoUpdateStub) UpdateSettings(_ context.Context, settings autoupdate.AutoUpdateSettings) (autoupdate.AutoUpdateSettings, error) {
	stub.update = settings
	if !stub.updateOK {
		return autoupdate.AutoUpdateSettings{}, errInvalidInterval
	}
	stub.saved = settings
	return stub.saved, nil
}

func (stub *autoUpdateStub) RunNow(context.Context) error {
	stub.runs++
	if stub.busy {
		return errBusySync
	}
	return nil
}

func TestAvdbAutoUpdateRoutes(t *testing.T) {
	stub := &autoUpdateStub{
		status: autoupdate.Status{
			Enabled: true, IntervalHours: 12, Notify: true,
			Running: true, NextRunAt: "2026-12-01T12:00:00Z",
			PackageSources: []string{"LTX", "x1080x"},
		},
		updateOK: true,
	}
	router := NewRouter(Dependencies{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Access:             NewAccessGateService("", ""),
		ResourceAutoUpdate: stub,
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/avdb/library/auto-update", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"running":true`) ||
		!strings.Contains(body, `"interval_hours":12`) ||
		!strings.Contains(body, `"package_sources":["LTX","x1080x"]`) {
		t.Fatalf("GET status=%d body=%s", response.Code, body)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/avdb/library/auto-update",
		strings.NewReader(`{"enabled":true,"interval_hours":8,"sources":["x1080x"],"notify":false}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"interval_hours":8`) {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
	}
	if stub.update.IntervalHours != 8 || len(stub.update.Sources) != 1 || stub.update.Notify {
		t.Fatalf("update = %#v", stub.update)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/avdb/library/auto-update/run-now", nil))
	if response.Code != http.StatusAccepted || stub.runs != 1 {
		t.Fatalf("run-now status=%d body=%s runs=%d", response.Code, response.Body.String(), stub.runs)
	}
}

func TestAvdbAutoUpdateRunNowBusyReturnsBusinessError(t *testing.T) {
	stub := &autoUpdateStub{busy: true}
	router := NewRouter(Dependencies{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Access:             NewAccessGateService("", ""),
		ResourceAutoUpdate: stub,
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/avdb/library/auto-update/run-now", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("run-now status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAvdbAutoUpdateRoutesWithoutLibrary(t *testing.T) {
	router := NewRouter(Dependencies{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Access: NewAccessGateService("", ""),
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/avdb/library/auto-update", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("GET without library status=%d body=%s", response.Code, response.Body.String())
	}
}

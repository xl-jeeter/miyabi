package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ppxb/miyabi/internal/avdb"
)

type resourceSyncStub struct {
	request avdb.SyncRequest
	status  avdb.SyncStatus
	result  avdb.SyncJob
	items   []avdb.Package
}

func (stub *resourceSyncStub) Start(_ context.Context, request avdb.SyncRequest) (avdb.SyncJob, error) {
	stub.request = request
	return stub.result, nil
}

func (stub *resourceSyncStub) Status(context.Context) (avdb.SyncStatus, error) {
	return stub.status, nil
}

func (stub *resourceSyncStub) Packages(context.Context) ([]avdb.Package, error) {
	return stub.items, nil
}

func TestAvdbSyncRoutes(t *testing.T) {
	stub := &resourceSyncStub{
		result: avdb.SyncJob{
			ID:       "job-1",
			Packages: []avdb.Package{{Source: avdb.SourceX1080X, Name: "package.zip"}},
		},
		status: avdb.SyncStatus{
			Running: true,
			Current: "package.zip",
			Bytes:   123,
			Last:    []avdb.SyncState{},
		},
		items: []avdb.Package{{Source: avdb.SourceLTX, Name: "ltx.zip"}},
	}
	router := NewRouter(Dependencies{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Access:       NewAccessGateService("", ""),
		ResourceSync: stub,
	})

	request := httptest.NewRequest(http.MethodPost, "/api/avdb/library/sync",
		strings.NewReader(`{"sources":["x1080x"],"mode":"full"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"job_id":"job-1"`) {
		t.Fatalf("POST status=%d body=%s", response.Code, response.Body.String())
	}
	if stub.request.Mode != avdb.ModeFull || len(stub.request.Sources) != 1 ||
		stub.request.Sources[0] != avdb.SourceX1080X {
		t.Fatalf("request = %#v", stub.request)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/avdb/library/sync/status", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"running":true`) ||
		!strings.Contains(response.Body.String(), `"bytes":123`) {
		t.Fatalf("status response=%d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/avdb/library/sync/packages", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"ltx.zip"`) {
		t.Fatalf("packages response=%d %s", response.Code, response.Body.String())
	}
}

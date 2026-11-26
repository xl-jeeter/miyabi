package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/pansource"
)

type panSourceStub struct {
	status    pansource.Status
	update    pansource.Status
	updateErr error
	saved     pansource.Input
	probed    pansource.CD2
	probe     pansource.ProbeResult
	probes    int
}

func (stub *panSourceStub) Status(context.Context) (pansource.Status, error) {
	return stub.status, nil
}

func (stub *panSourceStub) Update(_ context.Context, input pansource.Input) (pansource.Status, error) {
	stub.saved = input
	if stub.updateErr != nil {
		return pansource.Status{}, stub.updateErr
	}
	return stub.update, nil
}

func (stub *panSourceStub) Probe(_ context.Context, input pansource.CD2) pansource.ProbeResult {
	stub.probes++
	stub.probed = input
	return stub.probe
}

func panSourceRouter(manager PanSourceManager) http.Handler {
	return NewRouter(Dependencies{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Access:    NewAccessGateService("", ""),
		PanSource: manager,
	})
}

func TestPanSourceStatusKeepsSecretsOut(t *testing.T) {
	stub := &panSourceStub{status: pansource.Status{
		Provider: "cd2", EffectiveProvider: "cd2", RequiresRestart: false,
		CD2: pansource.CD2View{
			Endpoint: "http://host:19798", Account: "115open:xxx",
			Username: "admin", HasToken: true, HasPassword: false,
		},
	}}
	response := httptest.NewRecorder()
	panSourceRouter(stub).ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/api/settings/pan-source", nil))

	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
	for _, want := range []string{
		`"provider":"cd2"`, `"effectiveProvider":"cd2"`, `"requiresRestart":false`,
		`"endpoint":"http://host:19798"`, `"account":"115open:xxx"`, `"hasToken":true`,
		`"hasPassword":false`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET body %s is missing %s", body, want)
		}
	}
	if strings.Contains(body, `"token"`) || strings.Contains(body, `"password"`) {
		t.Fatalf("GET body leaks a secret field: %s", body)
	}
}

func TestPanSourceUpdateBindsTheWholeRequest(t *testing.T) {
	stub := &panSourceStub{update: pansource.Status{
		Provider: "cd2", EffectiveProvider: "115", RequiresRestart: true,
	}}
	request := httptest.NewRequest(http.MethodPut, "/api/settings/pan-source", strings.NewReader(
		`{"provider":"cd2","cd2":{"endpoint":"http://host:19798","token":"","account":"115open:xxx"}}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	panSourceRouter(stub).ServeHTTP(response, request)

	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"requiresRestart":true`) {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
	}
	// An empty token reaches the service untouched so it can keep the stored one.
	if stub.saved.Provider != "cd2" || stub.saved.CD2.Token != "" ||
		stub.saved.CD2.Endpoint != "http://host:19798" || stub.saved.CD2.Account != "115open:xxx" {
		t.Fatalf("PUT bound %+v", stub.saved)
	}
}

func TestPanSourceUpdateRejectsInvalidSettings(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	router := panSourceRouter(pansource.New(t.Context(), store.Client, nil,
		pansource.Settings{Provider: pansource.Provider115}))

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown provider", body: `{"provider":"webdav"}`},
		{name: "malformed endpoint", body: `{"provider":"cd2","cd2":{"endpoint":"nope","token":"t"}}`},
		{name: "no credentials", body: `{"provider":"cd2","cd2":{"endpoint":"http://host:19798"}}`},
		{name: "malformed JSON", body: `{"provider":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/api/settings/pan-source",
				strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("PUT %s status=%d body=%s", test.name, response.Code, response.Body.String())
			}
		})
	}
}

func TestPanSourceUpdateKeepsStoredSecretsOverHTTP(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	router := panSourceRouter(pansource.New(t.Context(), store.Client, nil,
		pansource.Settings{Provider: pansource.Provider115}))

	put := func(body string) string {
		request := httptest.NewRequest(http.MethodPut, "/api/settings/pan-source", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}

	put(`{"provider":"cd2","cd2":{"endpoint":"http://host:19798","token":"secret-token","username":"admin","password":"secret-pass"}}`)
	body := put(`{"provider":"cd2","cd2":{"endpoint":"http://host:19798"}}`)
	if !strings.Contains(body, `"hasToken":true`) || !strings.Contains(body, `"hasPassword":true`) ||
		!strings.Contains(body, `"username":"admin"`) {
		t.Fatalf("empty fields cleared the stored settings: %s", body)
	}
	if strings.Contains(body, "secret-") {
		t.Fatalf("PUT response leaks a secret: %s", body)
	}
}

func TestPanSourceStatusFollowsDatabaseBeforeEnvironment(t *testing.T) {
	settings := pansource.Settings{
		Provider: pansource.ProviderCD2,
		CD2:      pansource.CD2{Endpoint: "http://env:19798", Token: "env-token"},
	}
	t.Run("environment only", func(t *testing.T) {
		store, err := database.Open(t.Context(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		response := httptest.NewRecorder()
		panSourceRouter(pansource.New(t.Context(), store.Client, nil, settings)).ServeHTTP(response,
			httptest.NewRequest(http.MethodGet, "/api/settings/pan-source", nil))
		body := response.Body.String()
		if !strings.Contains(body, `"effectiveProvider":"cd2"`) ||
			!strings.Contains(body, `"requiresRestart":false`) {
			t.Fatalf("GET body=%s", body)
		}
	})
	t.Run("database overrides environment", func(t *testing.T) {
		store, err := database.Open(t.Context(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := database.SaveSetting(t.Context(), store.Client, "pan.cd2",
			pansource.CD2{Endpoint: "http://db:19798", Token: "db-token"}); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		panSourceRouter(pansource.New(t.Context(), store.Client, nil, settings)).ServeHTTP(response,
			httptest.NewRequest(http.MethodGet, "/api/settings/pan-source", nil))
		body := response.Body.String()
		// The database row is what the process started with, so it is both the
		// saved and the running value; the environment endpoint is gone.
		if !strings.Contains(body, `"endpoint":"http://db:19798"`) ||
			!strings.Contains(body, `"effectiveProvider":"cd2"`) ||
			!strings.Contains(body, `"requiresRestart":false`) {
			t.Fatalf("GET body=%s", body)
		}
	})
}

func TestPanSourceTestReportsAnUnreachableInstance(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	router := panSourceRouter(pansource.New(t.Context(), store.Client, nil,
		pansource.Settings{Provider: pansource.Provider115}))

	request := httptest.NewRequest(http.MethodPost, "/api/settings/pan-source/test", strings.NewReader(
		`{"endpoint":"http://127.0.0.1:1","token":"secret-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"ok":false`) {
		t.Fatalf("POST test status=%d body=%s", response.Code, body)
	}
	if strings.Contains(body, "secret-token") {
		t.Fatalf("test response leaks the token: %s", body)
	}
}

func TestPanSourceTestForwardsTheCandidateSettings(t *testing.T) {
	stub := &panSourceStub{probe: pansource.ProbeResult{
		OK: true, LatencyMs: 123, Accounts: []pansource.Account{{ID: "115open:xxx", Name: "115open"}},
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/settings/pan-source/test", strings.NewReader(
		`{"endpoint":"http://host:19798","account":"115open:xxx"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	panSourceRouter(stub).ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"ok":true`) ||
		!strings.Contains(body, `"latencyMs":123`) ||
		!strings.Contains(body, `"accounts":[{"id":"115open:xxx","name":"115open"}]`) {
		t.Fatalf("POST test status=%d body=%s", response.Code, body)
	}
	if stub.probed.Endpoint != "http://host:19798" || stub.probed.Account != "115open:xxx" {
		t.Fatalf("test probed %+v, want the body", stub.probed)
	}

	// An empty body probes the saved settings.
	empty := httptest.NewRecorder()
	emptyRequest := httptest.NewRequest(http.MethodPost, "/api/settings/pan-source/test", nil)
	panSourceRouter(stub).ServeHTTP(empty, emptyRequest)
	if empty.Code != http.StatusOK || stub.probes != 2 || stub.probed.Endpoint != "" {
		t.Fatalf("empty POST status=%d probes=%d probed=%+v", empty.Code, stub.probes, stub.probed)
	}
}

func TestPanSourceTestValidatesTheBody(t *testing.T) {
	stub := &panSourceStub{}
	request := httptest.NewRequest(http.MethodPost, "/api/settings/pan-source/test",
		strings.NewReader(`{"endpoint":`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	panSourceRouter(stub).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || stub.probes != 0 {
		t.Fatalf("POST status=%d probes=%d", response.Code, stub.probes)
	}
}

func TestPanSourceRoutesAreProtected(t *testing.T) {
	router := NewRouter(Dependencies{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Access:    NewAccessGateService("secret", ""),
		PanSource: &panSourceStub{},
	})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/settings/pan-source"},
		{http.MethodPut, "/api/settings/pan-source"},
		{http.MethodPost, "/api/settings/pan-source/test"},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d, want 401", route.method, route.path, response.Code)
		}
	}
}

// The wire shapes are what the settings page reads; keep them stable.
func TestPanSourceWireShapes(t *testing.T) {
	status := pansource.Status{
		Provider: "cd2", EffectiveProvider: "115", RequiresRestart: true,
		CD2: pansource.CD2View{Endpoint: "http://host:19798", Account: "115open:xxx", HasToken: true},
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"provider":"cd2","effectiveProvider":"115","requiresRestart":true,`+
		`"cd2":{"endpoint":"http://host:19798","account":"115open:xxx","username":"","hasToken":true,"hasPassword":false}}` {
		t.Fatalf("unexpected status JSON %s", encoded)
	}

	result := pansource.ProbeResult{OK: true, LatencyMs: 123,
		Accounts: []pansource.Account{{ID: "115open:xxx", Name: "115open"}}}
	encoded, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"ok":true,"latencyMs":123,"accounts":[{"id":"115open:xxx","name":"115open"}]}` {
		t.Fatalf("unexpected probe JSON %s", encoded)
	}
}

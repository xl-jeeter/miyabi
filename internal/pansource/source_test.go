package pansource

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/ent"
)

func newService(t *testing.T, env Settings) (*Service, *ent.Client) {
	t.Helper()
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	logger := slog.New(slog.NewTextHandler(new(bytes.Buffer), nil))
	return New(t.Context(), store.Client, logger, env), store.Client
}

func cd2Env() Settings {
	return Settings{
		Provider: Provider115,
		CD2:      CD2{Endpoint: "http://env:19798", Token: "env-token", Account: "115open:1"},
	}
}

func TestEnvironmentOnlyDeploymentsKeepTheirBehavior(t *testing.T) {
	env := cd2Env()
	env.Provider = ProviderCD2
	service, _ := newService(t, env)

	// No stored row: the running backend is the environment, nothing to restart.
	if status := mustStatus(t, service); status.Provider != ProviderCD2 ||
		status.EffectiveProvider != ProviderCD2 || status.RequiresRestart {
		t.Fatalf("unexpected status %+v", status)
	}
	if service.Startup() != env {
		t.Fatalf("startup settings %+v, want %+v", service.Startup(), env)
	}
	// The environment fills the fields the database does not hold.
	view := mustStatus(t, service).CD2
	if view.Endpoint != "http://env:19798" || view.Account != "115open:1" ||
		!view.HasToken || view.HasPassword || view.Username != "" {
		t.Fatalf("unexpected view %+v", view)
	}
}

func TestDatabaseProviderOverridesEnvironment(t *testing.T) {
	ctx := t.Context()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := database.SaveSetting(ctx, store.Client, providerSetting, Provider115); err != nil {
		t.Fatal(err)
	}
	env := cd2Env()
	env.Provider = ProviderCD2

	service := New(ctx, store.Client, nil, env)
	if service.Startup().Provider != Provider115 {
		t.Fatalf("startup provider %q, want the database value", service.Startup().Provider)
	}
	if status := mustStatus(t, service); status.RequiresRestart {
		t.Fatalf("status %+v reports a restart for an unchanged provider", status)
	}
}

func TestUpdateKeepsStoredSecretsWhenFieldsAreEmpty(t *testing.T) {
	ctx := t.Context()
	service, _ := newService(t, cd2Env())

	first, err := service.Update(ctx, Input{
		Provider: ProviderCD2,
		CD2: CD2{
			Endpoint: "http://host:19798", Token: "secret-token", Password: "secret pass",
			Username: "admin", Account: "115open:80005515",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.RequiresRestart || first.EffectiveProvider != Provider115 {
		t.Fatalf("switching provider must ask for a restart: %+v", first)
	}

	// A page that never received the secrets saves empty fields.
	second, err := service.Update(ctx, Input{Provider: ProviderCD2, CD2: CD2{Endpoint: "http://host:19798"}})
	if err != nil {
		t.Fatal(err)
	}
	if !second.CD2.HasToken || !second.CD2.HasPassword || second.CD2.Username != "admin" ||
		second.CD2.Account != "115open:80005515" {
		t.Fatalf("empty fields overwrote the stored settings: %+v", second.CD2)
	}
	stored, _, err := database.LoadSetting[CD2](ctx, service.database, cd2Setting)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Token != "secret-token" || stored.Password != "secret pass" {
		t.Fatalf("stored secrets changed: %+v", stored)
	}
}

func TestUpdateRejectsInvalidSettings(t *testing.T) {
	ctx := t.Context()
	for _, test := range []struct {
		name  string
		input Input
	}{
		{name: "unknown provider", input: Input{Provider: "webdav"}},
		{name: "missing endpoint", input: Input{Provider: ProviderCD2,
			CD2: CD2{Token: "token"}}},
		{name: "malformed endpoint", input: Input{Provider: ProviderCD2,
			CD2: CD2{Endpoint: "192.168.1.10", Token: "token"}}},
		{name: "unknown scheme", input: Input{Provider: ProviderCD2,
			CD2: CD2{Endpoint: "ftp://host:19798", Token: "token"}}},
		{name: "no credentials", input: Input{Provider: ProviderCD2,
			CD2: CD2{Endpoint: "http://host:19798"}}},
		{name: "username without password", input: Input{Provider: ProviderCD2,
			CD2: CD2{Endpoint: "http://host:19798", Username: "admin"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, client := newService(t, Settings{Provider: Provider115})
			if _, err := service.Update(ctx, test.input); err == nil {
				t.Fatal("expected a validation error")
			}
			if _, found, _ := database.LoadSetting[string](ctx, client, providerSetting); found {
				t.Fatal("a rejected update must not persist anything")
			}
		})
	}
}

func TestSwitchingTo115NeedsNoCloudDrive2Settings(t *testing.T) {
	service, _ := newService(t, Settings{Provider: ProviderCD2})
	if _, err := service.Update(t.Context(), Input{Provider: Provider115}); err != nil {
		t.Fatalf("switching back to 115 failed: %v", err)
	}
}

func TestBareHostPortEndpointIsAccepted(t *testing.T) {
	service, _ := newService(t, Settings{Provider: Provider115})
	if _, err := service.Update(t.Context(), Input{
		Provider: ProviderCD2,
		CD2:      CD2{Endpoint: "10.0.0.5:19798", Token: "token"},
	}); err != nil {
		t.Fatalf("host:port was rejected: %v", err)
	}
}

func TestUpdateWithoutStoredSecretsFallsBackToEnvironment(t *testing.T) {
	service, _ := newService(t, cd2Env())
	// The environment supplies the token, so the request only needs an endpoint.
	if _, err := service.Update(t.Context(), Input{
		Provider: ProviderCD2,
		CD2:      CD2{Endpoint: "http://host:19798"},
	}); err != nil {
		t.Fatalf("environment credentials were ignored: %v", err)
	}
}

func mustStatus(t *testing.T, service *Service) Status {
	t.Helper()
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestStatusNeverCarriesCredentials(t *testing.T) {
	service, _ := newService(t, Settings{Provider: Provider115})
	if _, err := service.Update(t.Context(), Input{
		Provider: ProviderCD2,
		CD2:      CD2{Endpoint: "http://host:19798", Token: "secret-token", Password: "secret pass"},
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(mustStatus(t, service))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-token", "secret pass"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("status leaks %q: %s", secret, encoded)
		}
	}
}

func TestProbeFailsWithoutPanickingOnAnUnreachableInstance(t *testing.T) {
	service, _ := newService(t, Settings{Provider: Provider115})
	result := service.Probe(t.Context(), CD2{
		Endpoint: "http://127.0.0.1:1", Token: "secret-token",
	})
	if result.OK || result.Error == "" {
		t.Fatalf("unexpected probe result %+v", result)
	}
	if bytes.Contains([]byte(result.Error), []byte("secret-token")) {
		t.Fatalf("probe error leaks the token: %s", result.Error)
	}
}

func TestProbeValidatesTheCandidateSettings(t *testing.T) {
	service, _ := newService(t, Settings{Provider: Provider115})
	result := service.Probe(t.Context(), CD2{Endpoint: "not a url"})
	if result.OK || result.Error == "" {
		t.Fatalf("unexpected probe result %+v", result)
	}
}

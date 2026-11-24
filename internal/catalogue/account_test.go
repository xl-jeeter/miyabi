package catalogue

import (
	"context"
	"errors"
	"testing"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/javdb"
)

type accountClient struct {
	stubProviderWithMagnets
	status    javdb.AccountStatus
	token     string
	authErr   error
	authCalls int
	username  string
	password  string
}

func (client *accountClient) Account() javdb.AccountStatus { return client.status }

func (client *accountClient) Authenticate(_ context.Context, username, password string) (string, error) {
	client.authCalls++
	client.username, client.password = username, password
	if client.authErr != nil {
		return "", client.authErr
	}
	return "session-token", nil
}

func (client *accountClient) SetCredentials(username, password, token string) {
	client.status = javdb.AccountStatus{LoggedIn: true, Username: username}
	client.username, client.password, client.token = username, password, token
}

func (client *accountClient) ClearCredentials() {
	client.status = javdb.AccountStatus{}
	client.token = ""
}

func TestJavDBLoginPersistsAndActivatesCredentials(t *testing.T) {
	ctx := t.Context()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &accountClient{}
	service, err := NewWithClients(ctx, store.Client, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	status, err := service.LoginJavDB(ctx, "  user@example.com ", "secret")
	if err != nil || !status.LoggedIn || status.Username != "user@example.com" {
		t.Fatalf("status = %+v, error = %v", status, err)
	}
	if client.authCalls != 1 || client.username != "user@example.com" || client.password != "secret" {
		t.Fatalf("client login = calls %d, user %q, password %q", client.authCalls, client.username, client.password)
	}
	if client.token != "session-token" {
		t.Fatalf("client token = %q", client.token)
	}
	stored, found, err := database.LoadSetting[persistedJavDBAccount](ctx, store.Client, javdbAccountSetting)
	if err != nil || !found || stored != (persistedJavDBAccount{Username: "user@example.com", Password: "secret"}) {
		t.Fatalf("stored account = %+v, found = %t, error = %v", stored, found, err)
	}
}

func TestJavDBLoginFailureKeepsAccountAndStorage(t *testing.T) {
	ctx := t.Context()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	saved := persistedJavDBAccount{Username: "old", Password: "old-secret"}
	if err := database.SaveSetting(ctx, store.Client, javdbAccountSetting, saved); err != nil {
		t.Fatal(err)
	}
	client := &accountClient{
		status:  javdb.AccountStatus{LoggedIn: true, Username: "old"},
		authErr: javdb.ErrInvalidCredentials,
	}
	service, err := NewWithClients(ctx, store.Client, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	_, err = service.LoginJavDB(ctx, "user@example.com", "wrong")
	if !domain.IsKind(err, domain.KindInvalid) || domain.PublicMessage(err) != "JavDB 用户名或密码错误" {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, javdb.ErrInvalidCredentials) {
		t.Fatal("invalid-credential cause leaked into the public error chain")
	}
	if client.status.Username != "old" {
		t.Fatalf("failed login replaced active account: %+v", client.status)
	}
	stored, found, err := database.LoadSetting[persistedJavDBAccount](ctx, store.Client, javdbAccountSetting)
	if err != nil || !found || stored != saved {
		t.Fatalf("failed login changed storage: %+v, found = %t, error = %v", stored, found, err)
	}
}

func TestJavDBLoginRejectsEmptyFieldsWithoutContactingJavDB(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &accountClient{}
	service, err := NewWithClients(t.Context(), store.Client, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	_, err = service.LoginJavDB(t.Context(), "  ", "secret")
	if !domain.IsKind(err, domain.KindInvalid) || client.authCalls != 0 {
		t.Fatalf("error = %v, client calls = %d", err, client.authCalls)
	}
}

func TestJavDBLogoutClearsPersistedAndActiveAccount(t *testing.T) {
	ctx := t.Context()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := database.SaveSetting(ctx, store.Client, javdbAccountSetting, persistedJavDBAccount{Username: "user", Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	client := &accountClient{status: javdb.AccountStatus{LoggedIn: true, Username: "user"}, token: "session-token"}
	service, err := NewWithClients(ctx, store.Client, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	if err := service.LogoutJavDB(ctx); err != nil {
		t.Fatal(err)
	}
	if client.status.LoggedIn || client.token != "" {
		t.Fatalf("active account = %+v, token = %q", client.status, client.token)
	}
	if _, found, err := database.LoadSetting[persistedJavDBAccount](ctx, store.Client, javdbAccountSetting); err != nil || found {
		t.Fatalf("account still stored: found = %t, error = %v", found, err)
	}
}

func TestLoadJavDBAccountPrefersStorageOverEnvironment(t *testing.T) {
	for _, test := range []struct {
		name     string
		stored   *persistedJavDBAccount
		envUser  string
		envPass  string
		wantUser string
		wantPass string
	}{
		{name: "stored account wins", stored: &persistedJavDBAccount{Username: "db-user", Password: "db-pass"}, envUser: "env-user", envPass: "env-pass", wantUser: "db-user", wantPass: "db-pass"},
		{name: "environment fallback", envUser: "env-user", envPass: "env-pass", wantUser: "env-user", wantPass: "env-pass"},
		{name: "incomplete storage falls back", stored: &persistedJavDBAccount{Username: "db-user"}, envUser: "env-user", envPass: "env-pass", wantUser: "env-user", wantPass: "env-pass"},
		{name: "anonymous when neither is complete", stored: &persistedJavDBAccount{Username: "db-user"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := database.Open(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if test.stored != nil {
				if err := database.SaveSetting(t.Context(), store.Client, javdbAccountSetting, *test.stored); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("MIYABI_JAVDB_USERNAME", test.envUser)
			t.Setenv("MIYABI_JAVDB_PASSWORD", test.envPass)
			account, err := loadJavDBAccount(t.Context(), store.Client)
			if err != nil {
				t.Fatal(err)
			}
			if account.Username != test.wantUser || account.Password != test.wantPass {
				t.Fatalf("account = %+v, want %s/%s", account, test.wantUser, test.wantPass)
			}
		})
	}
}

func TestNewUsesPersistedJavDBCredentials(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := database.SaveSetting(t.Context(), store.Client, javdbAccountSetting, persistedJavDBAccount{
		Username: "db-user", Password: "db-pass",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIYABI_JAVDB_USERNAME", "env-user")
	t.Setenv("MIYABI_JAVDB_PASSWORD", "env-pass")
	service, err := New(t.Context(), store.Client, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	status := service.Account(t.Context())
	if !status.LoggedIn || status.Username != "db-user" {
		t.Fatalf("account = %+v", status)
	}
}

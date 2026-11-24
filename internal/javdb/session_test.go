package javdb

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/time/rate"
)

type sessionTransport struct {
	body string
	err  error
	last url.Values
}

func (transport *sessionTransport) getJSON(context.Context, string, url.Values, any) error {
	return errors.New("unexpected GET")
}

func (transport *sessionTransport) postRaw(_ context.Context, _ string, params url.Values) ([]byte, error) {
	transport.last = params
	if transport.err != nil {
		return nil, transport.err
	}
	return []byte(transport.body), nil
}

func (*sessionTransport) closeIdleConnections() {}

func sessionClient(transport jsonTransport) *Client {
	client := &Client{limiter: rate.NewLimiter(rate.Inf, 1)}
	client.current.Store(&routeState{transport: transport})
	return client
}

func TestAuthenticatePublishesOnlyAfterSuccess(t *testing.T) {
	transport := &sessionTransport{body: `{"success":1,"data":{"token":"fresh-token"}}`}
	client := sessionClient(transport)

	token, err := client.Authenticate(t.Context(), "  user@example.com  ", "secret")
	if err != nil || token != "fresh-token" {
		t.Fatalf("token = %q, error = %v", token, err)
	}
	if transport.last.Get("username") != "user@example.com" || transport.last.Get("password") != "secret" {
		t.Fatalf("login params = %v", transport.last)
	}
	if client.hasCredentials() {
		t.Fatal("verification changed the active credentials")
	}
}

func TestAuthenticateRejectsInvalidEnvelope(t *testing.T) {
	client := sessionClient(&sessionTransport{body: `{"success":0,"message":"bad password"}`})
	_, err := client.Authenticate(t.Context(), "user", "secret")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials", err)
	}
	if client.hasCredentials() {
		t.Fatal("failed login configured an account")
	}
}

func TestAuthenticateFailureDoesNotLogPassword(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	transport := &sessionTransport{err: errors.New("Post \"https://example.test/api/v1/sessions?username=user&password=very-secret\": connection refused")}
	client := sessionClient(transport)
	_, err := client.Authenticate(t.Context(), "user", "very-secret")
	if err == nil {
		t.Fatal("expected login failure")
	}
	if strings.Contains(logs.String(), "very-secret") {
		t.Fatalf("login log exposed the password: %s", logs.String())
	}
}

func TestSetAndClearCredentialsSwapTokenAndRankings(t *testing.T) {
	client := &Client{limiter: rate.NewLimiter(rate.Inf, 1), options: Options{}}
	client.storeRankings("daily", nil)
	client.SetCredentials("user", "secret", "token")
	if status := client.Account(); !status.LoggedIn || status.Username != "user" {
		t.Fatalf("account = %+v", status)
	}
	if token := client.authToken(); token != "token" {
		t.Fatalf("token = %q", token)
	}
	if _, found := client.rankingsFromCache("daily"); found {
		t.Fatal("credential change kept account-scoped rankings")
	}

	client.ClearCredentials()
	if client.hasCredentials() || client.authToken() != "" {
		t.Fatal("logout kept credentials or token")
	}
	if status := client.Account(); status.LoggedIn || status.Username != "" {
		t.Fatalf("logged-out account = %+v", status)
	}
}

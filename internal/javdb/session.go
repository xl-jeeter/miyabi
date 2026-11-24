package javdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// ErrInvalidCredentials is returned when JavDB rejects an account login.
var ErrInvalidCredentials = errors.New("JavDB 用户名或密码错误")

type accountCredentials struct {
	Username string
	Password string
}

// authFailure reports whether an error means JavDB refused the caller instead
// of failing transiently. The App API answers 200 with an
// APIError{Action: "JWTVerificationError"} envelope once anonymous access is
// no longer allowed.
func authFailure(err error) bool {
	if err == nil {
		return false
	}
	var response *HTTPError
	if errors.As(err, &response) && response.StatusCode == 401 {
		return true
	}
	var api *APIError
	if errors.As(err, &api) {
		text := strings.ToLower(api.Action + " " + api.Message)
		for _, needle := range []string{"jwtverification", "請登錄", "请登录", "not logged in", "login required"} {
			if strings.Contains(text, needle) {
				return true
			}
		}
	}
	return false
}

// IsAuthFailure reports whether JavDB rejected the caller's authentication.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	if authFailure(err) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "jwtverificationerror") ||
		strings.Contains(text, "請登錄帳號")
}

// sessionEnvelope mirrors the session endpoint, whose success flag is the
// number 1, not a boolean.
type sessionEnvelope struct {
	Success int    `json:"success"`
	Message string `json:"message"`
	Action  string `json:"action"`
	Data    struct {
		Token string `json:"token"`
	} `json:"data"`
}

func (c *Client) credentials() *accountCredentials {
	return c.credentialsValue.Load()
}

// hasCredentials reports whether a JavDB session can be opened on demand.
func (c *Client) hasCredentials() bool { return c.credentials() != nil }

// ClearCredentials forgets the account and its token.
func (c *Client) ClearCredentials() {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	c.credentialsValue.Store(nil)
	c.tokenValue.Store(nil)
	c.clearRankingsCache()
}

// Account reports whether an account is configured. It intentionally does not
// contact JavDB; the session is verified when the first authenticated request runs.
func (c *Client) Account() AccountStatus {
	credentials := c.credentials()
	if credentials == nil {
		return AccountStatus{}
	}
	return AccountStatus{LoggedIn: true, Username: credentials.Username}
}

// Authenticate verifies the supplied account without changing the active
// session. Callers persist the credentials and then activate them with
// SetCredentials.
func (c *Client) Authenticate(ctx context.Context, username, password string) (string, error) {
	credentials := normalizeCredentials(username, password)
	if credentials == nil {
		return "", ErrInvalidCredentials
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	return c.openSession(ctx, credentials)
}

// SetCredentials makes a verified account active immediately.
func (c *Client) SetCredentials(username, password, token string) {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	credentials := normalizeCredentials(username, password)
	if credentials == nil {
		c.credentialsValue.Store(nil)
		c.tokenValue.Store(nil)
	} else {
		c.credentialsValue.Store(credentials)
		if token == "" {
			c.tokenValue.Store(nil)
		} else {
			c.tokenValue.Store(&token)
		}
	}
	c.clearRankingsCache()
}

func normalizeCredentials(username, password string) *accountCredentials {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil
	}
	return &accountCredentials{Username: username, Password: password}
}

// refreshToken opens a JavDB session with the configured account and publishes
// the resulting bearer token to every transport, including probe transports.
func (c *Client) refreshToken(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	credentials := c.credentials()
	if credentials == nil {
		return errors.New("JavDB 要求登录，但未配置 JavDB 账号密码")
	}
	token, err := c.openSession(ctx, credentials)
	if err != nil {
		return err
	}
	c.tokenValue.Store(&token)
	return nil
}

// openSession performs the existing mobile-client login protocol. Callers hold loginMu.
func (c *Client) openSession(ctx context.Context, credentials *accountCredentials) (string, error) {
	state, err := c.ensureRoute(ctx)
	if err != nil {
		slog.WarnContext(ctx, "JavDB route unavailable for login", "error", err)
		return "", err
	}
	params := url.Values{
		"username": {credentials.Username},
		"password": {credentials.Password},
	}
	body, err := state.transport.postRaw(ctx, "/api/v1/sessions", params)
	if err != nil {
		// The request URL carries credentials, so the wrapped error must not be logged.
		slog.WarnContext(ctx, "JavDB login request failed", "host", state.host)
		return "", fmt.Errorf("JavDB 登录请求失败: %w", err)
	}
	slog.InfoContext(ctx, "JavDB login response", "host", state.host, "bytes", len(body))
	var envelope sessionEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("解析 JavDB 登录响应失败: %w", err)
	}
	token := strings.TrimSpace(envelope.Data.Token)
	if token == "" {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = ErrInvalidCredentials.Error()
		}
		slog.WarnContext(ctx, "JavDB login rejected", "action", strings.TrimSpace(envelope.Action), "message", message)
		return "", fmt.Errorf("%w: %s", ErrInvalidCredentials, message)
	}
	slog.InfoContext(ctx, "JavDB session opened", "username", credentials.Username)
	return token, nil
}

func (c *Client) authToken() string {
	if pointer := c.tokenValue.Load(); pointer != nil {
		return *pointer
	}
	return ""
}

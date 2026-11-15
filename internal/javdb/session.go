package javdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

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

type sessionEnvelope struct {
	Success bool `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Token string `json:"token"`
	} `json:"data"`
}

// hasCredentials reports whether a JavDB session can be opened on demand.
func (c *Client) hasCredentials() bool {
	return c.options.Username != "" && c.options.Password != ""
}

// refreshToken opens a JavDB session with the configured account and publishes
// the resulting bearer token to every transport, including probe transports.
func (c *Client) refreshToken(ctx context.Context) error {
	if c.options.Username == "" || c.options.Password == "" {
		return errors.New("JavDB 要求登录，但未配置 JavDB 账号密码")
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	state, err := c.ensureRoute(ctx)
	if err != nil {
		return err
	}
	transport, err := routeTransport(state.host, c.fingerprint, c.options)
	if err != nil {
		return err
	}
	defer transport.closeIdleConnections()
	params := url.Values{
		"username": {c.options.Username},
		"password": {c.options.Password},
	}
	body, err := transport.postRaw(ctx, "/api/v1/sessions", params)
	if err != nil {
		return fmt.Errorf("JavDB 登录请求失败: %w", err)
	}
	var envelope sessionEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("解析 JavDB 登录响应失败: %w", err)
	}
	token := strings.TrimSpace(envelope.Data.Token)
	if token == "" {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = "JavDB 用户名或密码错误"
		}
		return errors.New(message)
	}
	c.tokenValue.Store(&token)
	return nil
}

func (c *Client) authToken() string {
	if pointer := c.tokenValue.Load(); pointer != nil {
		return *pointer
	}
	return ""
}

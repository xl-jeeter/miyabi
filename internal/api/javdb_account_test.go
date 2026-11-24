package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/javdb"
)

type accountStub struct {
	status    javdb.AccountStatus
	loginErr  error
	logoutErr error
	username  string
	password  string
	logins    int
	logouts   int
}

func (stub *accountStub) Account(context.Context) javdb.AccountStatus { return stub.status }

func (stub *accountStub) LoginJavDB(_ context.Context, username, password string) (javdb.AccountStatus, error) {
	stub.logins++
	stub.username, stub.password = username, password
	return stub.status, stub.loginErr
}

func (stub *accountStub) LogoutJavDB(context.Context) error {
	stub.logouts++
	return stub.logoutErr
}

func accountRouter(stub JavDBAccountManager) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(errorMiddleware(nil))
	router.GET("/account", javdbAccountHandler(stub))
	router.POST("/account/login", javdbLoginHandler(stub))
	router.POST("/account/logout", javdbLogoutHandler(stub))
	return router
}

func performAccountRequest(router http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestJavDBAccountHandlers(t *testing.T) {
	stub := &accountStub{status: javdb.AccountStatus{LoggedIn: true, Username: "user"}}
	router := accountRouter(stub)

	response := performAccountRequest(router, http.MethodGet, "/account", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, headers = %v", response.Code, response.Header())
	}
	var status javdb.AccountStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status != stub.status {
		t.Fatalf("status = %+v, error = %v", status, err)
	}

	response = performAccountRequest(router, http.MethodPost, "/account/login", javdbLoginInput{Username: "  user  ", Password: "secret"})
	if response.Code != http.StatusOK || stub.logins != 1 || stub.username != "user" || stub.password != "secret" {
		t.Fatalf("login status = %d, stub = %+v", response.Code, stub)
	}

	response = performAccountRequest(router, http.MethodPost, "/account/logout", nil)
	if response.Code != http.StatusOK || stub.logouts != 1 {
		t.Fatalf("logout status = %d, calls = %d", response.Code, stub.logouts)
	}
}

func TestJavDBLoginErrorsStayFormLevel(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
		body   string
	}{
		{name: "invalid credentials", err: domain.E(domain.KindInvalid, "JavDB 用户名或密码错误", nil), status: http.StatusBadRequest, body: "JavDB 用户名或密码错误"},
		{name: "expired session", err: &javdb.APIError{Action: "JWTVerificationError", Message: "expired"}, status: http.StatusUnauthorized, code: "JAVDB_UNAUTHORIZED", body: "JavDB 登录已失效，请在设置中重新登录"},
		{name: "unexpected failure", err: errors.New("database closed"), status: http.StatusInternalServerError, body: "内部服务错误"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &accountStub{loginErr: test.err}
			response := performAccountRequest(accountRouter(stub), http.MethodPost, "/account/login", javdbLoginInput{Username: "user", Password: "secret"})
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body)
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != test.body || body.Code != test.code {
				t.Fatalf("body = %+v, want error %q code %q", body, test.body, test.code)
			}
		})
	}
}

func TestJavDBLoginRequiresCredentials(t *testing.T) {
	stub := &accountStub{}
	router := accountRouter(stub)
	for _, payload := range []javdbLoginInput{
		{Username: "user"},
		{Password: "secret"},
	} {
		response := performAccountRequest(router, http.MethodPost, "/account/login", payload)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("payload %+v: status = %d", payload, response.Code)
		}
	}
	if stub.logins != 0 {
		t.Fatalf("invalid payloads reached the service: %d", stub.logins)
	}
}

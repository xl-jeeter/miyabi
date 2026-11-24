package catalogue

import (
	"context"
	"errors"
	"strings"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/javdb"
)

var errInvalidJavDBAccount = errors.New("JavDB 用户名或密码错误")

// Account returns the credentials currently active on the JavDB client.
func (service *Service) Account(context.Context) javdb.AccountStatus {
	if service.javdbAccount == nil {
		return javdb.AccountStatus{}
	}
	return service.javdbAccount.Account()
}

// LoginJavDB verifies an account with the existing session client, persists it
// and switches the running client to it without a restart.
func (service *Service) LoginJavDB(ctx context.Context, username, password string) (javdb.AccountStatus, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" || service.javdbAccount == nil {
		return javdb.AccountStatus{}, domain.E(domain.KindInvalid, errInvalidJavDBAccount.Error(), nil)
	}

	service.accountMu.Lock()
	defer service.accountMu.Unlock()
	token, err := service.javdbAccount.Authenticate(ctx, username, password)
	if err != nil {
		return javdb.AccountStatus{}, domain.E(domain.KindInvalid, errInvalidJavDBAccount.Error(), nil)
	}
	account := persistedJavDBAccount{Username: username, Password: password}
	if err := database.SaveSetting(ctx, service.database, javdbAccountSetting, account); err != nil {
		return javdb.AccountStatus{}, err
	}
	service.javdbAccount.SetCredentials(username, password, token)
	service.resetJavDBCaches()
	return javdb.AccountStatus{LoggedIn: true, Username: username}, nil
}

// LogoutJavDB removes the persisted account and the client's active token.
func (service *Service) LogoutJavDB(ctx context.Context) error {
	service.accountMu.Lock()
	defer service.accountMu.Unlock()
	if err := database.DeleteSetting(ctx, service.database, javdbAccountSetting); err != nil {
		return err
	}
	if service.javdbAccount != nil {
		service.javdbAccount.ClearCredentials()
	}
	service.resetJavDBCaches()
	return nil
}

// resetJavDBCaches discards responses that may differ by account.
func (service *Service) resetJavDBCaches() {
	service.lists.reset()
	service.details.reset()
	service.tags.reset()
	service.magnets.reset()
}

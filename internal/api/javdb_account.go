package api

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/javdb"
)

type JavDBAccountManager interface {
	Account(context.Context) javdb.AccountStatus
	LoginJavDB(context.Context, string, string) (javdb.AccountStatus, error)
	LogoutJavDB(context.Context) error
}

type javdbLoginInput struct {
	Username string `json:"username" binding:"required,max=120"`
	Password string `json:"password" binding:"required,max=512"`
}

func javdbAccountHandler(account JavDBAccountManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if account == nil {
			respond(c, nil, domain.E(domain.KindUpstream, "JavDB 客户端不可用", nil))
			return
		}
		respond(c, account.Account(c.Request.Context()), nil)
	}
}

func javdbLoginHandler(account JavDBAccountManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if account == nil {
			respond(c, nil, domain.E(domain.KindUpstream, "JavDB 客户端不可用", nil))
			return
		}
		input, ok := bindJSON[javdbLoginInput](c)
		if !ok {
			return
		}
		status, err := account.LoginJavDB(c.Request.Context(), strings.TrimSpace(input.Username), input.Password)
		if err != nil {
			if domain.IsKind(err, domain.KindInvalid) {
				c.Error(badRequest(err))
				return
			}
			respond(c, nil, err)
			return
		}
		respond(c, status, nil)
	}
}

func javdbLogoutHandler(account JavDBAccountManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if account == nil {
			respond(c, nil, domain.E(domain.KindUpstream, "JavDB 客户端不可用", nil))
			return
		}
		if err := account.LogoutJavDB(c.Request.Context()); err != nil {
			respond(c, nil, err)
			return
		}
		respond(c, javdb.AccountStatus{}, nil)
	}
}

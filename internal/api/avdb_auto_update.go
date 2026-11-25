package api

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/ppxb/miyabi/internal/avdb/autoupdate"
	"github.com/ppxb/miyabi/internal/domain"
)

// AutoUpdateManager exposes the AVDB resource library auto-update scheduler.
type AutoUpdateManager interface {
	Status(ctx context.Context) (autoupdate.Status, error)
	UpdateSettings(ctx context.Context, settings autoupdate.AutoUpdateSettings) (autoupdate.AutoUpdateSettings, error)
	RunNow(ctx context.Context) error
}

func avdbAutoUpdateStatusHandler(manager AutoUpdateManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			respond(c, nil, domain.E(domain.KindInvalid, "资源库未初始化", nil))
			return
		}
		status, err := manager.Status(c.Request.Context())
		respond(c, status, err)
	}
}

func avdbAutoUpdateUpdateHandler(manager AutoUpdateManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			respond(c, nil, domain.E(domain.KindInvalid, "资源库未初始化", nil))
			return
		}
		settings, ok := bindJSON[autoupdate.AutoUpdateSettings](c)
		if !ok {
			return
		}
		saved, err := manager.UpdateSettings(c.Request.Context(), settings)
		respond(c, saved, err)
	}
}

func avdbAutoUpdateRunNowHandler(manager AutoUpdateManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			respond(c, nil, domain.E(domain.KindInvalid, "资源库未初始化", nil))
			return
		}
		if err := manager.RunNow(c.Request.Context()); err != nil {
			respond(c, nil, err)
			return
		}
		accepted(c, gin.H{"started": true}, nil)
	}
}

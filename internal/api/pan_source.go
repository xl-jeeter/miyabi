package api

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/ppxb/miyabi/internal/pansource"
)

// PanSourceManager exposes the persisted online-library backend choice.
type PanSourceManager interface {
	Status(ctx context.Context) (pansource.Status, error)
	Update(ctx context.Context, input pansource.Input) (pansource.Status, error)
	Probe(ctx context.Context, input pansource.CD2) pansource.ProbeResult
}

func panSourceHandler(manager PanSourceManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		status, err := manager.Status(c.Request.Context())
		respond(c, status, err)
	}
}

func panSourceUpdateHandler(manager PanSourceManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		input, ok := bindJSON[pansource.Input](c)
		if !ok {
			return
		}
		status, err := manager.Update(c.Request.Context(), input)
		respond(c, status, err)
	}
}

// panSourceTestHandler probes with the body, or with the saved settings when
// the body is empty. The result carries its own error field: a refused
// connection is an answer, not a failed request.
func panSourceTestHandler(manager PanSourceManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input pansource.CD2
		if c.Request.ContentLength != 0 {
			body, ok := bindJSON[pansource.CD2](c)
			if !ok {
				return
			}
			input = body
		}
		respond(c, manager.Probe(c.Request.Context(), input), nil)
	}
}

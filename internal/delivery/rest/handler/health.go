package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

type Readiness func(ctx context.Context) error

type Health struct {
	ready Readiness
}

func NewHealth(ready Readiness) *Health { return &Health{ready: ready} }

// @Summary	Liveness probe
// @Tags		ops
// @Produce	json
// @Success	200	{object}	map[string]string
// @Router		/healthz [get]
func (h *Health) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// @Summary		Readiness probe
// @Description	Reports whether the backing dependencies answer. Both clients connect lazily, so liveness alone proves nothing about them.
// @Tags			ops
// @Produce		json
// @Success		200	{object}	map[string]string
// @Failure		503	{object}	ErrorResponse
// @Router			/readyz [get]
func (h *Health) Ready(c *gin.Context) {
	if err := h.ready(c.Request.Context()); err != nil {
		// The driver's error carries the DSN, so it is logged rather than
		// returned on an unauthenticated endpoint.
		slog.ErrorContext(c.Request.Context(), "readiness probe failed",
			"event", model.EventReadinessFailed, "error", err)
		respondError(c, http.StatusServiceUnavailable, CodeDependencyUnavailable, "a dependency is unavailable")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

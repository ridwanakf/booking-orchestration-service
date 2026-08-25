package rest

import (
	"github.com/gin-gonic/gin"
	swaggerfiles "github.com/swaggo/files"
	ginswagger "github.com/swaggo/gin-swagger"

	_ "github.com/ridwanakf/booking-orchestration-service/docs"
	"github.com/ridwanakf/booking-orchestration-service/internal/delivery/rest/handler"
	"github.com/ridwanakf/booking-orchestration-service/internal/delivery/rest/middleware"
)

func NewEngine(health *handler.Health, swaggerEnabled bool) *gin.Engine {
	engine := gin.New()

	// RequestContext runs first so the recovery handler's log line carries the
	// request id of the request that panicked.
	engine.Use(middleware.RequestContext())
	engine.Use(handler.Recovery())

	engine.GET("/healthz", health.Live)
	engine.GET("/readyz", health.Ready)

	// The UI exposes the whole API surface, so it defaults off and is opted into.
	if swaggerEnabled {
		engine.GET("/swagger/*any", ginswagger.WrapHandler(swaggerfiles.Handler))
	}

	return engine
}

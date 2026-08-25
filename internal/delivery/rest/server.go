package rest

import (
	"github.com/gin-gonic/gin"
	swaggerfiles "github.com/swaggo/files"
	ginswagger "github.com/swaggo/gin-swagger"

	_ "github.com/ridwanakf/booking-orchestration-service/docs"
	"github.com/ridwanakf/booking-orchestration-service/internal/delivery/rest/handler"
	"github.com/ridwanakf/booking-orchestration-service/internal/delivery/rest/middleware"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
)

func NewEngine(health *handler.Health, booking *handler.Booking, keys repository.APIKeyRepository, swaggerEnabled bool) *gin.Engine {
	engine := gin.New()

	// RequestContext runs first so the recovery handler's log line carries the
	// request id of the request that panicked.
	engine.Use(middleware.RequestContext())
	engine.Use(handler.Recovery())

	engine.GET("/healthz", health.Live)
	engine.GET("/readyz", health.Ready)

	// Health and docs are unauthenticated on purpose; everything a distributor
	// can reach is behind a credential.
	distributor := engine.Group("", middleware.Authenticate(keys))
	distributor.POST("/bookings", booking.Create)
	distributor.GET("/bookings/:bookingId", booking.Get)

	// The UI exposes the whole API surface, so it defaults off and is opted into.
	if swaggerEnabled {
		engine.GET("/swagger/*any", ginswagger.WrapHandler(swaggerfiles.Handler))
	}

	return engine
}

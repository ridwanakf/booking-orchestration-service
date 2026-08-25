package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
)

const headerRequestID = "X-Request-Id"

func RequestContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		c.Header(headerRequestID, id)
		c.Request = c.Request.WithContext(observability.WithRequestID(c.Request.Context(), id))
		c.Next()
	}
}

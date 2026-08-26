package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ridwanakf/booking-orchestration-service/internal/auth"
	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
)

// Authenticate resolves the caller's distributor from its credential. The
// identity never comes from the request body: a self-asserted tenant id is not
// an identity, and the idempotency-key namespace is only per-distributor if the
// distributor cannot be chosen by the caller.
func Authenticate(keys repository.APIKeyRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !ok {
			unauthorized(c)
			return
		}

		key, err := auth.Parse(strings.TrimSpace(raw))
		if err != nil {
			unauthorized(c)
			return
		}

		stored, err := keys.FindActive(c.Request.Context(), key.ID)
		if err != nil {
			// Deliberately does the work anyway, so a miss and a wrong secret
			// cost the same and the id space cannot be enumerated by timing.
			auth.VerifyMiss(key.Secret)
			unauthorized(c)
			return
		}
		if !auth.Verify(key.Secret, stored.SecretHash) {
			unauthorized(c)
			return
		}

		c.Request = c.Request.WithContext(observability.WithDistributor(c.Request.Context(), stored.DistributorID))
		c.Next()
	}
}

func unauthorized(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"status": "error", "code": "unauthorized", "message": "a valid distributor api key is required",
	})
}

package handler

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// gin's own Recovery writes to stderr through the standard log package, so a
// panic would leave the JSON stream entirely and carry no request_id, and it
// answers with an empty body rather than the error envelope every other path
// returns.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		slog.ErrorContext(c.Request.Context(), "panic recovered",
			"event", "http.panic",
			"error", fmt.Sprint(recovered),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"stack", string(debug.Stack()))

		c.AbortWithStatusJSON(http.StatusInternalServerError, ErrorResponse{
			Status:  "error",
			Code:    CodeInternalError,
			Message: "internal error",
		})
	})
}

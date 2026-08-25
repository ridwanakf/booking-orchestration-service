package handler

import "github.com/gin-gonic/gin"

const (
	CodeInvalidRequest           = "invalid_request"
	CodeBookingNotFound          = "booking_not_found"
	CodeIdempotencyKeyReused     = "idempotency_key_reused"
	CodeCallbackConflict         = "callback_conflict"
	CodeInvalidToken             = "invalid_token"
	CodeUnsupportedSupplierState = "unsupported_supplier_status"
	CodeInternalError            = "internal_error"
	CodeDependencyUnavailable    = "dependency_unavailable"
	CodeMissingReference         = "missing_supplier_reference"
)

// Code is stable and machine readable; Message is not.
type ErrorResponse struct {
	Status  string `json:"status" example:"error"`
	Code    string `json:"code" example:"booking_not_found"`
	Message string `json:"message" example:"booking not found"`
	Detail  string `json:"detail,omitempty"`
}

func respondError(c *gin.Context, status int, code, message string) {
	c.JSON(status, ErrorResponse{Status: "error", Code: code, Message: message})
}

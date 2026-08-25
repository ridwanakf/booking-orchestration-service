package handler

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/service"
)

type Callback struct {
	svc   service.BookingService
	token string
}

type CallbackRequest struct {
	BookingID         string `json:"bookingId" binding:"required" example:"0198f2c4-6d1a-7c3e-9f4b-2f6f0a1d9b10"`
	SupplierReference string `json:"supplierReference" example:"MOCK-0198F2C46D"`
	DeclineReason     string `json:"declineReason,omitempty" example:"NO_AVAILABILITY"`
	SupplierStatus    string `json:"supplierStatus" binding:"required" example:"CONFIRMED"`
}

type CallbackResponse struct {
	Applied bool   `json:"applied" example:"true"`
	Status  string `json:"status" example:"CONFIRMED"`
	Reason  string `json:"reason,omitempty" example:"duplicate"`
}

func NewCallback(svc service.BookingService, token string) *Callback {
	return &Callback{svc: svc, token: token}
}

// @Summary		Receive a supplier callback
// @Description	Authenticated by a shared token. Checks run in order: token, then booking, then status vocabulary, then state. A redelivered callback is a no-op; one contradicting a settled outcome is refused and flagged for recovery.
// @Tags			callbacks
// @Accept			json
// @Produce		json
// @Param			request				body		CallbackRequest	true	"Supplier outcome"
// @Success		200					{object}	CallbackResponse
// @Failure		400					{object}	ErrorResponse
// @Failure		401					{object}	ErrorResponse
// @Failure		404					{object}	ErrorResponse
// @Failure		409					{object}	ErrorResponse
// @Security		SupplierCallbackToken
// @Router			/supplier/callbacks [post]
func (h *Callback) Receive(c *gin.Context) {
	// Before any state is read, so an unauthenticated caller cannot probe which
	// booking ids exist. Constant-time so it cannot be probed by timing either.
	if subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Callback-Token")), []byte(h.token)) != 1 {
		slog.WarnContext(c.Request.Context(), "callback rejected",
			"event", model.EventCallbackRejected, "reason", "invalid_token")
		respondError(c, http.StatusUnauthorized, CodeInvalidToken, "invalid callback token")
		return
	}

	var req CallbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}

	id, err := uuid.Parse(req.BookingID)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeInvalidRequest, "bookingId must be a uuid")
		return
	}

	result, err := h.svc.ApplyCallback(c.Request.Context(), id, service.SupplierOutcome{
		Reference:     req.SupplierReference,
		Status:        req.SupplierStatus,
		DeclineReason: req.DeclineReason,
	})
	switch {
	case errors.Is(err, constant.ErrBookingNotFound):
		respondError(c, http.StatusNotFound, CodeBookingNotFound, "booking not found")
	case errors.Is(err, constant.ErrMissingSupplierReference):
		respondError(c, http.StatusBadRequest, CodeMissingReference,
			"a confirmation must carry the supplier's reservation reference")
	case errors.Is(err, constant.ErrUnsupportedSupplierStatus):
		respondError(c, http.StatusBadRequest, CodeUnsupportedSupplierState, "supplier status is outside this version's vocabulary")
	case errors.Is(err, constant.ErrCallbackConflict):
		respondError(c, http.StatusConflict, CodeCallbackConflict, "callback conflicts with the booking's state")
	case err != nil:
		respondError(c, http.StatusInternalServerError, CodeInternalError, "could not apply callback")
	default:
		resp := CallbackResponse{Applied: result.Applied, Status: string(result.Status)}
		if result.Duplicate {
			resp.Reason = "duplicate"
		}
		c.JSON(http.StatusOK, resp)
	}
}

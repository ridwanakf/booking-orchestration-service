package handler

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/service"
)

type Booking struct {
	svc service.BookingService
}

func NewBooking(svc service.BookingService) *Booking { return &Booking{svc: svc} }

// @Summary		Create a booking
// @Description	Idempotent per (distributorId, idempotencyKey). Replaying the same payload returns the original booking with 200 and the Idempotent-Replayed header; the same key with a different payload is refused.
// @Tags			bookings
// @Accept			json
// @Produce		json
// @Param			request	body		CreateBookingRequest	true	"Booking to create"
// @Success		201		{object}	BookingResponse
// @Success		200		{object}	BookingResponse	"Idempotent replay of an existing booking"
// @Failure		400		{object}	ErrorResponse
// @Failure		422		{object}	ErrorResponse
// @Failure		500		{object}	ErrorResponse
// @Router			/bookings [post]
func (h *Booking) Create(c *gin.Context) {
	var req CreateBookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}

	parsed, err := parseCreateRequest(req)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}

	b, created, err := h.svc.Create(c.Request.Context(), parsed)
	switch {
	case errors.Is(err, constant.ErrIdempotencyKeyReused):
		respondError(c, http.StatusUnprocessableEntity, CodeIdempotencyKeyReused,
			"this idempotency key was already used with a different payload")
		return
	case err != nil:
		respondError(c, http.StatusInternalServerError, CodeInternalError, "could not create booking")
		return
	}

	if !created {
		c.Header("Idempotent-Replayed", "true")
		c.JSON(http.StatusOK, toResponse(b))
		return
	}

	c.Header("Location", "/bookings/"+b.ID.String())
	c.JSON(http.StatusCreated, toResponse(b))
}

// @Summary	Read a booking
// @Tags		bookings
// @Produce	json
// @Param		bookingId	path		string	true	"Booking id"
// @Success	200			{object}	BookingResponse
// @Failure	400			{object}	ErrorResponse
// @Failure	404			{object}	ErrorResponse
// @Router		/bookings/{bookingId} [get]
func (h *Booking) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("bookingId"))
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeInvalidRequest, "bookingId must be a uuid")
		return
	}

	b, err := h.svc.Get(c.Request.Context(), id)
	switch {
	case errors.Is(err, constant.ErrBookingNotFound):
		respondError(c, http.StatusNotFound, CodeBookingNotFound, "booking not found")
		return
	case err != nil:
		respondError(c, http.StatusInternalServerError, CodeInternalError, "could not read booking")
		return
	}

	c.JSON(http.StatusOK, toResponse(b))
}

func parseCreateRequest(req CreateBookingRequest) (model.CreateRequest, error) {
	checkIn, err := time.Parse(model.DateLayout, req.CheckIn)
	if err != nil {
		return model.CreateRequest{}, fmt.Errorf("checkIn must be YYYY-MM-DD")
	}
	checkOut, err := time.Parse(model.DateLayout, req.CheckOut)
	if err != nil {
		return model.CreateRequest{}, fmt.Errorf("checkOut must be YYYY-MM-DD")
	}
	if !checkOut.After(checkIn) {
		return model.CreateRequest{}, fmt.Errorf("checkOut must be after checkIn")
	}

	return model.CreateRequest{
		DistributorID:  req.DistributorID,
		IdempotencyKey: req.IdempotencyKey,
		PropertyID:     req.PropertyID,
		RoomTypeID:     req.RoomTypeID,
		CheckIn:        checkIn,
		CheckOut:       checkOut,
		GuestFirstName: req.Guest.FirstName,
		GuestLastName:  req.Guest.LastName,
	}, nil
}

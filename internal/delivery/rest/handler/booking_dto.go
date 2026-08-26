package handler

import (
	"time"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

type Guest struct {
	FirstName string `json:"firstName" binding:"required,max=64" example:"Taro"`
	LastName  string `json:"lastName" binding:"required,max=64" example:"Yamada"`
}

type CreateBookingRequest struct {
	IdempotencyKey string `json:"idempotencyKey" binding:"required,max=128" example:"partner-12345"`
	PropertyID     string `json:"propertyId" binding:"required,max=64" example:"hotel-001"`
	RoomTypeID     string `json:"roomTypeId" binding:"required,max=64" example:"room-deluxe-confirm"`
	CheckIn        string `json:"checkIn" binding:"required" example:"2026-09-10"`
	CheckOut       string `json:"checkOut" binding:"required" example:"2026-09-12"`
	Guest          Guest  `json:"guest" binding:"required"`
}

type BookingResponse struct {
	BookingID         string  `json:"bookingId" example:"0198f2c4-6d1a-7c3e-9f4b-2f6f0a1d9b10"`
	Status            string  `json:"status" example:"RECEIVED"`
	SupplierReference *string `json:"supplierReference"`
	DistributorID     string  `json:"distributorId" example:"distributor-001"`
	IdempotencyKey    string  `json:"idempotencyKey" example:"partner-12345"`
	PropertyID        string  `json:"propertyId" example:"hotel-001"`
	RoomTypeID        string  `json:"roomTypeId" example:"room-deluxe-confirm"`
	CheckIn           string  `json:"checkIn" example:"2026-09-10"`
	CheckOut          string  `json:"checkOut" example:"2026-09-12"`
	Guest             Guest   `json:"guest"`
	FailureReason     *string `json:"failureReason,omitempty"`
	NeedsRecovery     bool    `json:"needsRecovery,omitempty"`
	Version           int     `json:"version" example:"3"`
	CreatedAt         string  `json:"createdAt" example:"2026-08-24T10:00:00Z"`
	UpdatedAt         string  `json:"updatedAt" example:"2026-08-24T10:00:00Z"`
}

func toResponse(b *model.Booking) BookingResponse {
	return BookingResponse{
		BookingID:         b.ID.String(),
		Status:            string(b.Status),
		SupplierReference: b.SupplierReference,
		DistributorID:     b.DistributorID,
		IdempotencyKey:    b.IdempotencyKey,
		PropertyID:        b.PropertyID,
		RoomTypeID:        b.RoomTypeID,
		CheckIn:           b.CheckIn.Format(model.DateLayout),
		CheckOut:          b.CheckOut.Format(model.DateLayout),
		Guest:             Guest{FirstName: b.GuestFirstName, LastName: b.GuestLastName},
		FailureReason:     b.FailureReason,
		NeedsRecovery:     b.NeedsRecovery,
		Version:           b.Version,
		CreatedAt:         b.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         b.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

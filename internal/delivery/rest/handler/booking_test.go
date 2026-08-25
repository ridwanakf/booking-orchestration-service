package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/delivery/rest/handler"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	svcmocks "github.com/ridwanakf/booking-orchestration-service/internal/service/mocks"
)

const validPayload = `{
	"idempotencyKey": "partner-12345",
	"distributorId": "distributor-001",
	"propertyId": "hotel-001",
	"roomTypeId": "room-deluxe-confirm",
	"checkIn": "2026-09-10",
	"checkOut": "2026-09-12",
	"guest": {"firstName": "Taro", "lastName": "Yamada"}
}`

type BookingHandlerSuite struct {
	suite.Suite
	ctrl   *gomock.Controller
	svc    *svcmocks.MockBookingService
	engine *gin.Engine
}

func TestBookingHandler(t *testing.T) {
	suite.Run(t, new(BookingHandlerSuite))
}

func (s *BookingHandlerSuite) SetupTest() {
	gin.SetMode(gin.TestMode)
	s.ctrl = gomock.NewController(s.T())
	s.svc = svcmocks.NewMockBookingService(s.ctrl)

	h := handler.NewBooking(s.svc)
	s.engine = gin.New()
	s.engine.POST("/bookings", h.Create)
	s.engine.GET("/bookings/:bookingId", h.Get)
}

func (s *BookingHandlerSuite) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	return rec
}

func (s *BookingHandlerSuite) stored() *model.Booking {
	return &model.Booking{
		ID:             uuid.New(),
		DistributorID:  "distributor-001",
		IdempotencyKey: "partner-12345",
		PropertyID:     "hotel-001",
		RoomTypeID:     "room-deluxe-confirm",
		CheckIn:        time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		CheckOut:       time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		GuestFirstName: "Taro",
		GuestLastName:  "Yamada",
		Status:         model.StatusReceived,
	}
}

func (s *BookingHandlerSuite) TestCreateReturns201WithLocation() {
	b := s.stored()
	s.svc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(b, true, nil)

	rec := s.do(http.MethodPost, "/bookings", validPayload)

	s.Equal(http.StatusCreated, rec.Code)
	s.Equal("/bookings/"+b.ID.String(), rec.Header().Get("Location"))
	s.Empty(rec.Header().Get("Idempotent-Replayed"))
}

// A replay is the distributor doing the right thing after an inconclusive
// response, so it is a success with a marker, not an error.
func (s *BookingHandlerSuite) TestReplayReturns200WithMarker() {
	s.svc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(s.stored(), false, nil)

	rec := s.do(http.MethodPost, "/bookings", validPayload)

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("true", rec.Header().Get("Idempotent-Replayed"))
}

func (s *BookingHandlerSuite) TestReusedKeyReturns422() {
	s.svc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, false, constant.ErrIdempotencyKeyReused)

	rec := s.do(http.MethodPost, "/bookings", validPayload)

	s.Equal(http.StatusUnprocessableEntity, rec.Code)
	s.Equal(handler.CodeIdempotencyKeyReused, s.errorCode(rec.Body.Bytes()))
}

func (s *BookingHandlerSuite) TestInvalidRequestsAreRejectedBeforeReachingTheService() {
	cases := []struct {
		name string
		body string
	}{
		{"missing idempotency key", `{"distributorId":"d","propertyId":"p","roomTypeId":"r","checkIn":"2026-09-10","checkOut":"2026-09-12","guest":{"firstName":"A","lastName":"B"}}`},
		{"missing guest", `{"idempotencyKey":"k","distributorId":"d","propertyId":"p","roomTypeId":"r","checkIn":"2026-09-10","checkOut":"2026-09-12"}`},
		{"checkout before checkin", `{"idempotencyKey":"k","distributorId":"d","propertyId":"p","roomTypeId":"r","checkIn":"2026-09-12","checkOut":"2026-09-10","guest":{"firstName":"A","lastName":"B"}}`},
		{"same day stay", `{"idempotencyKey":"k","distributorId":"d","propertyId":"p","roomTypeId":"r","checkIn":"2026-09-10","checkOut":"2026-09-10","guest":{"firstName":"A","lastName":"B"}}`},
		{"unparseable date", `{"idempotencyKey":"k","distributorId":"d","propertyId":"p","roomTypeId":"r","checkIn":"10-09-2026","checkOut":"2026-09-12","guest":{"firstName":"A","lastName":"B"}}`},
		{"malformed json", `{`},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			rec := s.do(http.MethodPost, "/bookings", tc.body)
			s.Equal(http.StatusBadRequest, rec.Code)
		})
	}
}

func (s *BookingHandlerSuite) TestGetReturnsTheBooking() {
	b := s.stored()
	s.svc.EXPECT().Get(gomock.Any(), b.ID).Return(b, nil)

	rec := s.do(http.MethodGet, "/bookings/"+b.ID.String(), "")

	s.Equal(http.StatusOK, rec.Code)
	var body handler.BookingResponse
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	s.Equal(b.ID.String(), body.BookingID)
	s.Equal("2026-09-10", body.CheckIn)
	s.Equal("Taro", body.Guest.FirstName)
}

func (s *BookingHandlerSuite) TestGetUnknownBookingReturns404() {
	s.svc.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, constant.ErrBookingNotFound)

	rec := s.do(http.MethodGet, "/bookings/"+uuid.NewString(), "")

	s.Equal(http.StatusNotFound, rec.Code)
}

// A malformed id never reaches the service: there is nothing to look up.
func (s *BookingHandlerSuite) TestGetWithAMalformedIDReturns400() {
	rec := s.do(http.MethodGet, "/bookings/not-a-uuid", "")

	s.Equal(http.StatusBadRequest, rec.Code)
}

func (s *BookingHandlerSuite) errorCode(body []byte) string {
	var out handler.ErrorResponse
	s.Require().NoError(json.Unmarshal(body, &out))
	return out.Code
}

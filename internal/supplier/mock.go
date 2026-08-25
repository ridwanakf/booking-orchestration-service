package supplier

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Scenarios are selected by a suffix on roomTypeId so a reviewer can drive every
// failure path with curl and nothing else.
const (
	scenarioConfirm      = "-confirm"
	scenarioReject       = "-reject"
	scenarioTimeout      = "-timeout"
	scenarioUnclassified = "-unclassified"
)

type Mock struct {
	callbackBaseURL string
	callbackToken   string
	timeoutHold     time.Duration
	callbackDelay   time.Duration
	http            *http.Client
	log             *slog.Logger
}

func NewMock(callbackBaseURL, callbackToken string, timeoutHold, callbackDelay time.Duration, log *slog.Logger) *Mock {
	return &Mock{
		callbackBaseURL: callbackBaseURL,
		callbackToken:   callbackToken,
		timeoutHold:     timeoutHold,
		callbackDelay:   callbackDelay,
		http:            &http.Client{Timeout: 10 * time.Second},
		log:             log,
	}
}

func (m *Mock) Register(engine *gin.Engine) {
	engine.POST("/mock-supplier/bookings", m.book)
}

func (m *Mock) book(c *gin.Context) {
	var req bookPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, bookResponse{Status: "ERROR", Message: "malformed request"})
		return
	}

	ref := reference(req.ClientReference)

	switch {
	case strings.HasSuffix(req.RoomTypeID, scenarioReject):
		c.JSON(http.StatusOK, bookResponse{Status: "REJECTED", DeclineCode: "NO_AVAILABILITY", Message: "no rooms for those dates"})

	case strings.HasSuffix(req.RoomTypeID, scenarioUnclassified):
		// A 200 carrying an error envelope with no decline code we know: the
		// case that must not be read as either success or business rejection.
		c.JSON(http.StatusOK, bookResponse{Status: "ERROR", Message: "downstream channel manager unavailable"})

	case strings.HasSuffix(req.RoomTypeID, scenarioTimeout):
		// Hold the connection open past the caller's deadline, then confirm out
		// of band. Twice, so duplicate delivery is demonstrable.
		go m.confirmLate(req.ClientReference, ref)
		// Bounded by the caller's context so an abandoned request releases its
		// goroutine immediately rather than holding it, and shutdown is not
		// stalled by a scenario that is meant to be slow.
		select {
		case <-time.After(m.timeoutHold):
			c.JSON(http.StatusOK, bookResponse{Status: "CONFIRMED", Reference: ref})
		case <-c.Request.Context().Done():
		}

	default:
		c.JSON(http.StatusOK, bookResponse{Status: "CONFIRMED", Reference: ref})
	}
}

func (m *Mock) confirmLate(bookingID, ref string) {
	time.Sleep(m.callbackDelay)
	for i := range 2 {
		m.postCallback(bookingID, ref, i+1)
	}
}

func (m *Mock) postCallback(bookingID, ref string, delivery int) {
	body, err := json.Marshal(map[string]string{
		"bookingId":         bookingID,
		"supplierReference": ref,
		"supplierStatus":    "CONFIRMED",
	})
	if err != nil {
		return
	}

	req, err := http.NewRequest(http.MethodPost, m.callbackBaseURL+"/supplier/callbacks", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Callback-Token", m.callbackToken)

	resp, err := m.http.Do(req)
	if err != nil {
		m.log.Warn("mock supplier could not deliver its callback",
			"event", "supplier.callback", "booking_id", bookingID, "delivery", delivery, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	m.log.Info("mock supplier delivered a callback",
		"event", "supplier.callback", "booking_id", bookingID, "delivery", delivery, "status", resp.StatusCode)
}

// One reference per client reference, so a repeat of the same booking always
// gets the same answer. That models a supplier whose create is idempotent,
// which most real ones are not (see the RFC's domain section).
//
// Hashed rather than truncated: booking ids are UUIDv7 and therefore share a
// time-ordered prefix, so slicing the front collides for bookings created in
// the same window. That collision is the exact failure the unique index on
// (supplier_id, supplier_idempotency_key) exists to catch.
func reference(clientReference string) string {
	sum := sha256.Sum256([]byte(clientReference))
	return "MOCK-" + strings.ToUpper(hex.EncodeToString(sum[:5]))
}

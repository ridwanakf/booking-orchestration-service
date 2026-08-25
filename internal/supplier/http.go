package supplier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

// Decline codes we recognize from the supplier contract. Anything outside this
// set is not a business rejection, however final the HTTP status looks.
var declineCodes = map[string]bool{
	"NO_AVAILABILITY":    true,
	"RATE_NOT_AVAILABLE": true,
	"PROPERTY_CLOSED":    true,
	"GUEST_REJECTED":     true,
}

type HTTPClient struct {
	baseURL  string
	deadline time.Duration
	http     *http.Client
}

type bookPayload struct {
	ClientReference string `json:"clientReference"`
	PropertyID      string `json:"propertyId"`
	RoomTypeID      string `json:"roomTypeId"`
	CheckIn         string `json:"checkIn"`
	CheckOut        string `json:"checkOut"`
	GuestFirstName  string `json:"guestFirstName"`
	GuestLastName   string `json:"guestLastName"`
}

type bookResponse struct {
	Status      string `json:"status"`
	Reference   string `json:"reference"`
	DeclineCode string `json:"declineCode"`
	Message     string `json:"message"`
}

func NewHTTPClient(baseURL string, deadline time.Duration) *HTTPClient {
	// Keep-alives are off deliberately. Reusing an idle connection is what lets
	// net/http replay a request transparently, and a replayed create is a second
	// booking that no attempt counter ever saw. One connection per attempt also
	// makes the byte counter below unambiguous.
	transport := &http.Transport{
		DialContext:       dialCounting(&net.Dialer{Timeout: 10 * time.Second}),
		DisableKeepAlives: true,
	}

	return &HTTPClient{
		baseURL:  baseURL,
		deadline: deadline,
		http:     &http.Client{Transport: transport},
	}
}

func (c *HTTPClient) Book(ctx context.Context, req BookRequest) Result {
	started := time.Now()

	body, err := json.Marshal(bookPayload{
		ClientReference: req.IdempotencyKey,
		PropertyID:      req.PropertyID,
		RoomTypeID:      req.RoomTypeID,
		CheckIn:         req.CheckIn.Format(model.DateLayout),
		CheckOut:        req.CheckOut.Format(model.DateLayout),
		GuestFirstName:  req.GuestFirstName,
		GuestLastName:   req.GuestLastName,
	})
	if err != nil {
		return Result{Outcome: OutcomePreSendFailure, Reason: fmt.Sprintf("encode request: %v", err), Latency: time.Since(started)}
	}

	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()

	// Counting bytes at the socket is the only honest answer to "could the
	// supplier have seen this?". The transport's WroteRequest hook fires once
	// the request is buffered, which is before anything reaches the wire, so
	// trusting it would withdraw doubt from a booking that was never sent.
	//
	// The baseline is taken at GotConn, which fires once the connection is
	// fully established. Over TLS the handshake writes to the same socket, so
	// counting from zero would make every https request look sent.
	ctx, written := withWriteCounter(ctx)
	var baseline atomic.Int64
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { baseline.Store(written.Load()) },
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bookings", bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: OutcomePreSendFailure, Reason: fmt.Sprintf("build request: %v", err), Latency: time.Since(started)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Deliberately not named "Idempotency-Key": net/http treats that header as
	// permission to replay the request transparently.
	httpReq.Header.Set("X-Supplier-Idempotency", req.IdempotencyKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if written.Load() == baseline.Load() {
			return Result{Outcome: OutcomeNotSent, Reason: err.Error(), Latency: time.Since(started)}
		}
		return Result{Outcome: OutcomeAmbiguous, Reason: err.Error(), Latency: time.Since(started)}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{Outcome: OutcomeAmbiguous, Reason: err.Error(), Latency: time.Since(started)}
	}

	return c.classify(resp.StatusCode, raw, time.Since(started))
}

// Decline classification runs before status-class classification, so a
// recognized decline inside a 5xx is a rejection rather than an ambiguity, and
// an unrecognized code inside a 200 is an ambiguity rather than a confirmation.
func (c *HTTPClient) classify(status int, raw []byte, latency time.Duration) Result {
	var parsed bookResponse
	_ = json.Unmarshal(raw, &parsed)

	if declineCodes[parsed.DeclineCode] {
		return Result{Outcome: OutcomeRejected, Reason: parsed.DeclineCode, Latency: latency}
	}

	if status == http.StatusOK || status == http.StatusCreated {
		if parsed.Status == "CONFIRMED" && parsed.Reference != "" {
			return Result{Outcome: OutcomeConfirmed, Reference: parsed.Reference, Latency: latency}
		}
		// A 200 carrying an error envelope, or a confirmation with no reference
		// to hold onto. The bytes arrived, so nothing is proven either way.
		return Result{Outcome: OutcomeAmbiguous, Reason: unclassified(parsed, status), Latency: latency}
	}

	return Result{Outcome: OutcomeAmbiguous, Reason: unclassified(parsed, status), Latency: latency}
}

func unclassified(parsed bookResponse, status int) string {
	if parsed.Message != "" {
		return fmt.Sprintf("unclassified answer (http %d): %s", status, parsed.Message)
	}
	return fmt.Sprintf("unclassified answer (http %d)", status)
}

package booking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
	"github.com/ridwanakf/booking-orchestration-service/internal/orchestrator"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
	"github.com/ridwanakf/booking-orchestration-service/internal/service"
)

// Long enough to catch a distributor's retry loop, short enough that a genuine
// rebooking of the same stay is not flagged forever.
const duplicateSuspectWindow = 24 * time.Hour

var _ service.BookingService = (*Service)(nil)

// Anything outside this set is real supplier truth we cannot represent, so it is
// refused and flagged rather than guessed at.
var applicableStatus = map[string]model.Status{
	"CONFIRMED": model.StatusConfirmed,
	"REJECTED":  model.StatusRejected,
}

type Service struct {
	repo       repository.BookingRepository
	orch       orchestrator.Orchestrator
	supplierID string
	log        *slog.Logger
}

func New(repo repository.BookingRepository, orch orchestrator.Orchestrator, supplierID string, log *slog.Logger) *Service {
	return &Service{repo: repo, orch: orch, supplierID: supplierID, log: log}
}

// Create is idempotent per (distributor, key). The bool reports whether this
// call created the booking; false means the caller is seeing a replay.
func (s *Service) Create(ctx context.Context, req model.CreateRequest) (*model.Booking, bool, error) {
	// The credential decides the tenant; a body value is accepted and ignored.
	req.DistributorID = observability.Distributor(ctx)

	fingerprint := req.Fingerprint()

	id, err := uuid.NewV7()
	if err != nil {
		return nil, false, fmt.Errorf("generate booking id: %w", err)
	}

	requestID := observability.RequestIDPtr(ctx)

	stored, created, err := s.repo.InsertOrLoad(ctx, model.Booking{
		ID:                 id,
		DistributorID:      req.DistributorID,
		IdempotencyKey:     req.IdempotencyKey,
		RequestFingerprint: fingerprint,
		SupplierID:         s.supplierID,
		PropertyID:         req.PropertyID,
		RoomTypeID:         req.RoomTypeID,
		CheckIn:            req.CheckIn,
		CheckOut:           req.CheckOut,
		GuestFirstName:     req.GuestFirstName,
		GuestLastName:      req.GuestLastName,
		Status:             model.StatusReceived,
	}, requestID)
	if err != nil {
		return nil, false, err
	}

	if !created {
		return s.replay(ctx, stored, fingerprint)
	}

	s.log.InfoContext(ctx, "booking created",
		"event", "booking.created",
		"booking_id", stored.ID,
		"distributor_id", stored.DistributorID)

	s.reportDuplicateSuspect(ctx, stored)
	s.startWorkflow(ctx, stored)

	return stored, true, nil
}

// Scoped to the authenticated distributor, answering not-found rather than
// forbidden so the endpoint cannot be used to discover which ids exist.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*model.Booking, error) {
	b, err := s.repo.GetByID(ctx, id)
	if err == nil && b.DistributorID != observability.Distributor(ctx) {
		return nil, constant.ErrBookingNotFound
	}
	if err != nil {
		if errors.Is(err, constant.ErrBookingNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("get booking: %w", err)
	}
	return b, nil
}

func (s *Service) ApplyCallback(ctx context.Context, id uuid.UUID, outcome service.SupplierOutcome) (service.CallbackResult, error) {
	requestID := observability.RequestIDPtr(ctx)

	// Looked up before the vocabulary is checked, so a refusal is always recorded
	// against a real row. The other order lets anyone holding the shared callback
	// token flag arbitrary booking ids.
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return service.CallbackResult{}, err
	}

	if target, ok := applicableStatus[outcome.Status]; ok && target == model.StatusConfirmed && outcome.Reference == "" {
		s.log.ErrorContext(ctx, "confirmation carried no supplier reference",
			"event", model.EventCallbackRejected, "booking_id", id)
		s.recordRefusal(ctx, id, current.Status, model.EventCallbackRejected, outcome, "confirmation without a reference")
		return service.CallbackResult{}, constant.ErrMissingSupplierReference
	}

	target, ok := applicableStatus[outcome.Status]
	if !ok {
		// Dropping this would lose the only signal that our row is knowingly stale.
		s.log.ErrorContext(ctx, "callback carried a status outside this version's vocabulary",
			"event", model.EventCallbackRejected, "booking_id", id, "supplier_status", outcome.Status)
		s.recordRefusal(ctx, id, current.Status, model.EventCallbackRejected, outcome, "status outside this version's vocabulary")
		if err := s.repo.Flag(ctx, id); err != nil {
			s.log.ErrorContext(ctx, "could not flag the unsupported status", "booking_id", id, "error", err)
		}
		return service.CallbackResult{}, constant.ErrUnsupportedSupplierStatus
	}

	// Nothing was authorized, so an honest supplier cannot know this booking.
	// Recorded but not flagged: there is no truth here to preserve.
	if current.Status == model.StatusReceived {
		s.log.WarnContext(ctx, "callback for a booking that was never sent",
			"event", model.EventConflict, "booking_id", id, "supplier_status", outcome.Status)
		reason := "callback while still RECEIVED"
		if err := s.repo.AppendRefusal(ctx, id, model.Event{
			ToStatus: current.Status, EventType: model.EventConflict,
			RequestID: requestID, SupplierStatusCode: &outcome.Status, SupplierReason: &reason,
		}); err != nil {
			s.log.ErrorContext(ctx, "could not record the refusal", "booking_id", id, "error", err)
		}
		return service.CallbackResult{}, constant.ErrCallbackConflict
	}

	if current.Status.Settled() {
		if current.Status == target && sameReference(current.SupplierReference, outcome.Reference) {
			s.log.InfoContext(ctx, "duplicate callback ignored",
				"event", model.EventSupplierCallback, "booking_id", id, "applied", false, "duplicate", true)
			return service.CallbackResult{Status: current.Status, Duplicate: true}, nil
		}
		return service.CallbackResult{}, s.conflict(ctx, id, current.Status, target, outcome, "settled outcome contradicted")
	}

	reference, reason := outcomeDetail(target, outcome)

	// Superseding whatever attempt is outstanding, with one re-read on conflict:
	// a retry moving the row in the gap does not make a confirmation inapplicable.
	applied, err := s.repo.Apply(ctx, id, s.callbackTransition(current.Status, target, reference, reason, outcome, requestID))
	if errors.Is(err, constant.ErrTransitionConflict) {
		// The same comparison the settled branch makes, so whether a divergent
		// reservation number is audited does not depend on who won a race.
		if applied == target && sameReference(current.SupplierReference, outcome.Reference) {
			return service.CallbackResult{Status: applied, Duplicate: true}, nil
		}
		if applied.Settled() || model.Transition(applied, target) != nil {
			return service.CallbackResult{}, s.conflict(ctx, id, applied, target, outcome, "booking moved beyond this outcome")
		}
		_, err = s.repo.Apply(ctx, id, s.callbackTransition(applied, target, reference, reason, outcome, requestID))
		switch {
		case errors.Is(err, constant.ErrTransitionConflict):
			return service.CallbackResult{}, s.conflict(ctx, id, applied, target, outcome, "lost the settle race twice")
		case err != nil:
			// Not a conflict: a transient failure. Surfacing it as 409 would
			// tell the supplier to stop redelivering a confirmation we just
			// failed to write.
			return service.CallbackResult{}, err
		}
	} else if err != nil {
		return service.CallbackResult{}, err
	}

	s.log.InfoContext(ctx, "callback applied",
		"event", model.EventSupplierCallback, "booking_id", id, "applied", true, "duplicate", false,
		"from", current.Status, "to", target)

	if err := s.orch.SignalOutcome(ctx, id, string(target)); err != nil {
		s.log.WarnContext(ctx, "could not signal the workflow",
			"event", model.EventSupplierCallback, "booking_id", id, "error", err)
	}

	return service.CallbackResult{Status: target, Applied: true}, nil
}

// A replay of the same payload is the distributor doing the right thing after an
// inconclusive response. A different payload under one key is a client bug, and
// answering with the original booking would hide it.
func (s *Service) replay(ctx context.Context, stored *model.Booking, fingerprint string) (*model.Booking, bool, error) {
	if stored.RequestFingerprint != fingerprint {
		s.log.WarnContext(ctx, "idempotency key reused with a different payload",
			"event", model.EventDuplicateRequest, "reason", "fingerprint_mismatch",
			"booking_id", stored.ID, "distributor_id", stored.DistributorID)
		return nil, false, constant.ErrIdempotencyKeyReused
	}

	s.log.InfoContext(ctx, "idempotent replay",
		"event", model.EventDuplicateRequest, "reason", "replay",
		"booking_id", stored.ID, "distributor_id", stored.DistributorID)
	return stored, false, nil
}

// Observability only: a distributor that loses its key store makes a real second
// booking, and that should be visible without being blocked here.
func (s *Service) reportDuplicateSuspect(ctx context.Context, b *model.Booking) {
	prior, found, err := s.repo.FindPriorByFingerprint(ctx, b.DistributorID, b.RequestFingerprint, b.IdempotencyKey, duplicateSuspectWindow)
	if err != nil {
		s.log.ErrorContext(ctx, "duplicate suspect lookup failed",
			"event", model.EventDuplicateSuspect, "booking_id", b.ID, "error", err)
		return
	}
	if !found {
		return
	}

	s.log.WarnContext(ctx, "same booking under a new idempotency key",
		"event", model.EventDuplicateSuspect, "booking_id", b.ID,
		"distributor_id", b.DistributorID, "prior_booking_id", prior)
}

// The row is committed, so a failure here costs latency, never the booking. The
// context is detached: a disconnecting distributor already has its booking, and
// cancelling the start on its behalf only delays confirmation.
func (s *Service) startWorkflow(ctx context.Context, b *model.Booking) {
	if _, err := s.orch.StartBooking(context.WithoutCancel(ctx), b.ID); err != nil {
		s.log.ErrorContext(ctx, "could not start booking workflow",
			"event", "booking.workflow_start_failed", "booking_id", b.ID, "error", err)
	}
}

func (s *Service) callbackTransition(from, to model.Status, reference, reason *string, outcome service.SupplierOutcome, requestID *string) repository.Transition {
	status := outcome.Status
	return repository.Transition{
		From: from, To: to,
		Marker:             repository.MarkerSupersede,
		EventType:          model.EventSupplierCallback,
		SupplierReference:  reference,
		FailureReason:      reason,
		ClearRecoveryFlag:  true,
		RequestID:          requestID,
		SupplierStatusCode: &status,
	}
}

func (s *Service) conflict(ctx context.Context, id uuid.UUID, have, want model.Status, outcome service.SupplierOutcome, reason string) error {
	s.log.ErrorContext(ctx, "callback conflicts with the booking's state",
		"event", model.EventConflict, "booking_id", id, "expected", have, "got", want, "reason", reason)

	// A flag says "look at this"; the lineage row says what was actually asserted,
	// including a reservation number our row does not name.
	s.recordRefusal(ctx, id, have, model.EventConflict, outcome, reason)
	if err := s.repo.Flag(ctx, id); err != nil {
		s.log.ErrorContext(ctx, "could not flag the conflict", "booking_id", id, "error", err)
	}
	return constant.ErrCallbackConflict
}

func (s *Service) recordRefusal(ctx context.Context, id uuid.UUID, current model.Status, event string, outcome service.SupplierOutcome, reason string) {
	status, ref := outcome.Status, outcome.Reference
	e := model.Event{
		ToStatus: current, EventType: event,
		RequestID: observability.RequestIDPtr(ctx), SupplierStatusCode: &status, SupplierReason: &reason,
	}
	if ref != "" {
		e.PayloadDigest = &ref
	}
	if err := s.repo.AppendRefusal(ctx, id, e); err != nil {
		s.log.ErrorContext(ctx, "could not record the refusal", "booking_id", id, "error", err)
	}
}

// Writing a reference onto a declined booking would record a reservation that
// does not exist.
func outcomeDetail(target model.Status, outcome service.SupplierOutcome) (reference, reason *string) {
	if target == model.StatusRejected {
		declined := outcome.DeclineReason
		if declined == "" {
			declined = constant.ReasonSupplierDeclined
		}
		return nil, &declined
	}
	if outcome.Reference == "" {
		return nil, nil
	}
	return &outcome.Reference, nil
}

// A stored nil is not "matches anything": a reservation number we do not have is
// new information, and calling it a duplicate discards recovery's only handle.
func sameReference(stored *string, incoming string) bool {
	if stored == nil {
		return incoming == ""
	}
	return *stored == incoming
}

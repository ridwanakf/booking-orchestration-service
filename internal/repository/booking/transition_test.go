package booking_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
	repo "github.com/ridwanakf/booking-orchestration-service/internal/repository/booking"
	repomocks "github.com/ridwanakf/booking-orchestration-service/internal/repository/booking/mocks"
)

type row struct{ scan func(dest ...any) error }

func (r row) Scan(dest ...any) error { return r.scan(dest...) }

// The guarded statement returns two nullable columns: what the update applied,
// and what was actually there if it did not.
func outcome(applied, current *model.Status) row {
	return row{scan: func(dest ...any) error {
		*(dest[0].(**model.Status)) = applied
		*(dest[1].(**model.Status)) = current
		return nil
	}}
}

func status(v model.Status) *model.Status { return &v }

type TransitionSuite struct {
	suite.Suite
	ctrl *gomock.Controller
	db   *repomocks.MockDB
	repo *repo.Repo
	ctx  context.Context
	id   uuid.UUID
}

func TestTransition(t *testing.T) {
	suite.Run(t, new(TransitionSuite))
}

func (s *TransitionSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.db = repomocks.NewMockDB(s.ctrl)
	s.repo = repo.New(s.db)
	s.ctx = context.Background()
	s.id = uuid.New()
}

func (s *TransitionSuite) transition(from, to model.Status) repository.Transition {
	return repository.Transition{From: from, To: to, Marker: repository.MarkerSupersede, EventType: model.EventTransition}
}

// The domain refuses the edge before the database is asked, so an illegal
// transition costs no round trip and cannot depend on what a row happens to say.
func (s *TransitionSuite) TestAnIllegalEdgeNeverReachesTheDatabase() {
	_, err := s.repo.Apply(s.ctx, s.id, s.transition(model.StatusConfirmed, model.StatusPending))

	s.ErrorIs(err, constant.ErrInvalidTransition)
}

// Doubt is never withdrawn: proof that one attempt never left says nothing
// about an earlier one that may have arrived.
func (s *TransitionSuite) TestUnknownNeverReturnsToPending() {
	_, err := s.repo.Apply(s.ctx, s.id, s.transition(model.StatusUnknown, model.StatusPending))

	s.ErrorIs(err, constant.ErrInvalidTransition)
}

func (s *TransitionSuite) TestAppliedTransitionReturnsTheNewStatus() {
	s.db.EXPECT().QueryRow(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(outcome(status(model.StatusConfirmed), status(model.StatusPending)))

	got, err := s.repo.Apply(s.ctx, s.id, s.transition(model.StatusPending, model.StatusConfirmed))

	s.Require().NoError(err)
	s.Equal(model.StatusConfirmed, got)
}

// Losing the race is a named outcome carrying the state that beat us, not a
// generic failure: the caller decides whether that state is acceptable.
func (s *TransitionSuite) TestALostRaceReportsTheStatusThatWon() {
	s.db.EXPECT().QueryRow(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(outcome(nil, status(model.StatusConfirmed)))

	got, err := s.repo.Apply(s.ctx, s.id, s.transition(model.StatusPending, model.StatusUnknown))

	s.ErrorIs(err, constant.ErrTransitionConflict)
	s.Equal(model.StatusConfirmed, got, "the caller needs to know a callback already confirmed it")
}

func (s *TransitionSuite) TestAMissingRowIsNotAConflict() {
	s.db.EXPECT().QueryRow(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(outcome(nil, nil))

	_, err := s.repo.Apply(s.ctx, s.id, s.transition(model.StatusPending, model.StatusUnknown))

	s.ErrorIs(err, constant.ErrBookingNotFound)
}

// An owned marker without an attempt number is a programming error, and it must
// not silently degrade into a write that guards on nothing.
func (s *TransitionSuite) TestAnOwnedMarkerRequiresAnAttemptNumber() {
	t := s.transition(model.StatusPending, model.StatusConfirmed)
	t.Marker = repository.MarkerOwned

	_, err := s.repo.Apply(s.ctx, s.id, t)

	s.ErrorIs(err, constant.ErrInvalidTransition)
}

func (s *TransitionSuite) TestAuthorizeReturnsTheAttemptItClaimed() {
	s.db.EXPECT().QueryRow(gomock.Any(), gomock.Any(), s.id, 2, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(row{scan: func(dest ...any) error {
			attempt := 1
			*(dest[0].(**int)) = &attempt
			*(dest[1].(**model.Status)) = status(model.StatusReceived)
			return nil
		}})

	attempt, previous, err := s.repo.Authorize(s.ctx, s.id, 2, "KEY", nil)

	s.Require().NoError(err)
	s.Equal(1, attempt, "the attempt number is what bounds the retry budget")
	s.Equal(model.StatusReceived, previous, "the prior status decides whether a pre-send failure may settle FAILED")
}

// An exhausted budget, a settled booking, and an attempt already outstanding
// all surface as a refusal to authorize, which is what stops a second call.
func (s *TransitionSuite) TestAuthorizeRefusesWhenTheGuardDoesNotHold() {
	s.db.EXPECT().QueryRow(gomock.Any(), gomock.Any(), s.id, 2, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(row{scan: func(dest ...any) error {
			*(dest[0].(**int)) = nil
			*(dest[1].(**model.Status)) = status(model.StatusConfirmed)
			return nil
		}})

	_, previous, err := s.repo.Authorize(s.ctx, s.id, 2, "KEY", nil)

	s.ErrorIs(err, constant.ErrTransitionConflict)
	s.Equal(model.StatusConfirmed, previous)
}

func (s *TransitionSuite) TestResolveStaleMarker() {
	s.Run("a marker was outstanding", func() {
		s.db.EXPECT().QueryRow(gomock.Any(), gomock.Any(), s.id, gomock.Any()).
			Return(row{scan: func(dest ...any) error {
				stale := 2
				*(dest[0].(**int)) = &stale
				return nil
			}})

		attempt, resolved, err := s.repo.ResolveStaleMarker(s.ctx, s.id)
		s.Require().NoError(err)
		s.True(resolved)
		s.Equal(2, attempt)
	})

	s.Run("nothing outstanding", func() {
		s.db.EXPECT().QueryRow(gomock.Any(), gomock.Any(), s.id, gomock.Any()).
			Return(row{scan: func(dest ...any) error {
				*(dest[0].(**int)) = nil
				return nil
			}})

		_, resolved, err := s.repo.ResolveStaleMarker(s.ctx, s.id)
		s.Require().NoError(err)
		s.False(resolved)
	})
}

func (s *TransitionSuite) TestFlagOnAMissingRow() {
	s.db.EXPECT().Exec(gomock.Any(), gomock.Any(), s.id).
		Return(pgconn.NewCommandTag("UPDATE 0"), nil)

	s.ErrorIs(s.repo.Flag(s.ctx, s.id), constant.ErrBookingNotFound)
}

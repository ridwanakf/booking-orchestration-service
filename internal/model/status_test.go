package model_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

type StatusSuite struct {
	suite.Suite
	all []model.Status
}

func TestStatus(t *testing.T) {
	suite.Run(t, new(StatusSuite))
}

func (s *StatusSuite) SetupTest() {
	s.all = []model.Status{
		model.StatusReceived, model.StatusPending, model.StatusUnknown,
		model.StatusConfirmed, model.StatusRejected, model.StatusFailed, model.StatusCancelled,
	}
}

func (s *StatusSuite) TestAllowedTransitions() {
	cases := []struct {
		name string
		from model.Status
		to   model.Status
	}{
		{"the first attempt is authorized", model.StatusReceived, model.StatusPending},
		{"orchestration failure before any attempt", model.StatusReceived, model.StatusFailed},
		{"the supplier confirms", model.StatusPending, model.StatusConfirmed},
		{"the supplier declines", model.StatusPending, model.StatusRejected},
		{"an attempt ends with no answer", model.StatusPending, model.StatusUnknown},
		{"pre-send failure, or every attempt proven not sent", model.StatusPending, model.StatusFailed},
		{"a later attempt or callback confirms", model.StatusUnknown, model.StatusConfirmed},
		{"a later attempt or callback declines", model.StatusUnknown, model.StatusRejected},
		{"operator decision", model.StatusUnknown, model.StatusFailed},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			s.NoError(model.Transition(tc.from, tc.to))
		})
	}
}

func (s *StatusSuite) TestRefusedTransitions() {
	cases := []struct {
		name string
		from model.Status
		to   model.Status
	}{
		{"doubt is never withdrawn once created", model.StatusUnknown, model.StatusPending},
		{"a healthy send never writes doubt", model.StatusReceived, model.StatusUnknown},
		{"a confirmed booking never reopens", model.StatusConfirmed, model.StatusPending},
		{"a confirmed booking is never re-doubted", model.StatusConfirmed, model.StatusUnknown},
		{"a rejected booking never confirms", model.StatusRejected, model.StatusConfirmed},
		{"a failed booking never confirms", model.StatusFailed, model.StatusConfirmed},
		{"a cancelled booking never reopens", model.StatusCancelled, model.StatusPending},
		{"doubt is never skipped on the way to confirmed", model.StatusReceived, model.StatusConfirmed},
		{"a received booking is never rejected without an attempt", model.StatusReceived, model.StatusRejected},
		{"a pending booking never returns to received", model.StatusPending, model.StatusReceived},
		{"nothing reaches cancelled in this version", model.StatusConfirmed, model.StatusCancelled},
		{"an attempt does not re-enter doubt as a transition", model.StatusUnknown, model.StatusUnknown},
		{"unknown source is refused", model.Status("BOGUS"), model.StatusPending},
		{"unknown target is refused", model.StatusPending, model.Status("BOGUS")},
		{"empty status is refused", model.Status(""), model.StatusPending},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			err := model.Transition(tc.from, tc.to)
			s.Require().Error(err)
			s.ErrorIs(err, constant.ErrInvalidTransition)
		})
	}
}

func (s *StatusSuite) TestSettledSet() {
	for _, st := range []model.Status{model.StatusConfirmed, model.StatusRejected, model.StatusFailed, model.StatusCancelled} {
		s.True(st.Settled(), "%s should be settled", st)
	}
	for _, st := range []model.Status{model.StatusReceived, model.StatusPending, model.StatusUnknown} {
		s.False(st.Settled(), "%s should not be settled", st)
	}
}

// A settled booking has no automatic exits, which is the property the sweep and
// the callback path both rely on.
func (s *StatusSuite) TestSettledStatesHaveNoOutgoingTransitions() {
	for _, from := range s.all {
		if !from.Settled() {
			continue
		}
		for _, to := range s.all {
			s.Error(model.Transition(from, to), "%s should not exit to %s", from, to)
		}
	}
}

func (s *StatusSuite) TestValid() {
	for _, st := range s.all {
		s.True(st.Valid(), "%s should be valid", st)
	}
	s.False(model.Status("BOGUS").Valid())
	s.False(model.Status("").Valid())
}

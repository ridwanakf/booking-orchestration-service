package model

import (
	"fmt"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
)

type Status string

const (
	StatusReceived  Status = "RECEIVED"
	StatusPending   Status = "PENDING"
	StatusUnknown   Status = "UNKNOWN"
	StatusConfirmed Status = "CONFIRMED"
	StatusRejected  Status = "REJECTED"
	StatusFailed    Status = "FAILED"
	StatusCancelled Status = "CANCELLED"
)

// CANCELLED is modelled but unreachable here: it belongs to the cancellation
// flow this version does not implement.
var settled = map[Status]bool{
	StatusConfirmed: true,
	StatusRejected:  true,
	StatusFailed:    true,
	StatusCancelled: true,
}

// Doubt does not live in this table. An attempt is recorded by the in-flight
// marker, not by a status change, so a booking mid-send stays PENDING and only
// reaches UNKNOWN when an attempt ends without an answer. There is deliberately
// no edge out of UNKNOWN back to PENDING: proof that one attempt never left
// says nothing about an earlier one that may have arrived.
var transitions = map[Status]map[Status]bool{
	StatusReceived: {
		StatusPending: true,
		StatusFailed:  true,
	},
	StatusPending: {
		StatusConfirmed: true,
		StatusRejected:  true,
		StatusUnknown:   true,
		StatusFailed:    true,
	},
	StatusUnknown: {
		StatusConfirmed: true,
		StatusRejected:  true,
		StatusFailed:    true,
	},
}

func (s Status) Valid() bool {
	if settled[s] {
		return true
	}
	_, ok := transitions[s]
	return ok
}

func (s Status) Settled() bool { return settled[s] }

func Transition(from, to Status) error {
	if !from.Valid() || !to.Valid() || !transitions[from][to] {
		return fmt.Errorf("%w: %s to %s", constant.ErrInvalidTransition, from, to)
	}
	return nil
}

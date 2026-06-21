package run

import (
	"fmt"

	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// StateMachine is a pure validation engine that checks whether a state
// transition is legal. It has zero side effects and no database access.
type StateMachine struct{}

// NewStateMachine returns a ready StateMachine.
func NewStateMachine() *StateMachine {
	return &StateMachine{}
}

// ValidTransitions defines all legal state transitions in the run lifecycle.
//
//	PENDING  ──→ QUEUED ──→ ASSIGNED ──→ PLANNING ──→ PLANNED ──→ AWAITING_APPROVAL ──→ APPLYING ──→ APPLIED
//	  │            │            │             │            │              │                  │
//	  └──→ CANCELLED ←─────────┴─────────────┴──→ FAILED ←┴──────────────┴──────────────────┴──→ FAILED
//	                   │         │                        │                                        (also FAILED)
//	                   │         └──→ APPLYING            PLANNED  ──→ POLICY_REJECTED
//	                   │                                   PLANNED  ──→ DISCARDED
//	                   └──→ FAILED                         AWAITING_APPROVAL ──→ DISCARDED
var ValidTransitions = map[RunState][]RunState{
	StatePending:          {StateQueued, StateCancelled},
	StateQueued:           {StateAssigned, StateCancelled},
	StateAssigned:         {StatePlanning, StateApplying, StateCancelled, StateQueued, StateFailed},
	StatePlanning:         {StatePlanned, StateFailed, StateCancelled},
	StatePlanned:          {StateAwaitingApproval, StateApplying, StatePolicyRejected, StateDiscarded, StateCancelled},
	StateAwaitingApproval: {StateApplying, StateDiscarded, StateCancelled},
	StateApplying:         {StateApplied, StateFailed, StateCancelled},
}

// TerminalStates contains the states in which a run is considered finished.
// No further transitions are allowed from a terminal state.
var TerminalStates = map[RunState]bool{
	StateApplied: true, StateFailed: true,
	StateCancelled: true, StateDiscarded: true, StatePolicyRejected: true,
}

// ErrInvalidTransition is returned when a state change is not in
// ValidTransitions.
var ErrInvalidTransition = domainerr.New("INVALID_TRANSITION", 422, "invalid state transition")

// ErrTerminal is returned when attempting to transition from a terminal state.
var ErrTerminal = domainerr.New("TERMINAL_STATE", 422, "run is in a terminal state")

// ErrUnknownState is returned when a state is not recognised.
var ErrUnknownState = domainerr.New("UNKNOWN_STATE", 500, "unknown run state")

// Transition validates that moving from current to next is legal. It returns
// nil if the transition is allowed, or one of ErrInvalidTransition,
// ErrTerminal, ErrUnknownState.
func (sm *StateMachine) Transition(current, next RunState) error {
	if TerminalStates[current] {
		return ErrTerminal
	}
	allowed, ok := ValidTransitions[current]
	if !ok {
		return ErrUnknownState
	}
	for _, s := range allowed {
		if s == next {
			return nil
		}
	}
	return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, current, next)
}

// IsTerminal returns true if the state is a terminal state.
func (sm *StateMachine) IsTerminal(state RunState) bool {
	return TerminalStates[state]
}

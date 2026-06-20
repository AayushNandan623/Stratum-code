package run

import (
	"errors"
	"fmt"
	"testing"
)

// allStates lists every RunState defined in the spec.
var allStates = []RunState{
	StatePending, StateQueued, StateAssigned, StatePlanning,
	StatePlanned, StateAwaitingApproval, StateApplying,
	StateApplied, StateFailed, StateCancelled, StateDiscarded,
	StatePolicyRejected,
}

// legalTransitions defines the authoritative set of allowed transitions.
// Any pair not in this map (or not listed in the []RunState) is illegal.
var legalTransitions = map[RunState][]RunState{
	StatePending:          {StateQueued, StateCancelled},
	StateQueued:           {StateAssigned, StateCancelled},
	StateAssigned:         {StatePlanning, StateCancelled, StateQueued},
	StatePlanning:         {StatePlanned, StateFailed, StateCancelled},
	StatePlanned:          {StateAwaitingApproval, StateApplying, StatePolicyRejected, StateDiscarded, StateCancelled},
	StateAwaitingApproval: {StateApplying, StateDiscarded, StateCancelled},
	StateApplying:         {StateApplied, StateFailed, StateCancelled},
}

func TestValidTransitionsCoverAllSpecEntries(t *testing.T) {
	for curr, targets := range legalTransitions {
		got := ValidTransitions[curr]
		if len(got) != len(targets) {
			t.Errorf("ValidTransitions[%s] has %d entries, want %d", curr, len(got), len(targets))
			continue
		}
		wantSet := make(map[RunState]bool, len(targets))
		for _, s := range targets {
			wantSet[s] = true
		}
		for _, s := range got {
			if !wantSet[s] {
				t.Errorf("ValidTransitions[%s] contains unexpected %s", curr, s)
			}
		}
	}
}

func TestTerminalStatesAreNotInValidTransitions(t *testing.T) {
	for ts := range TerminalStates {
		if _, ok := ValidTransitions[ts]; ok {
			t.Errorf("terminal state %s appears as a source in ValidTransitions", ts)
		}
	}
}

func TestAllStatesCovered(t *testing.T) {
	// every non-terminal state must appear as a source in ValidTransitions
	for _, s := range allStates {
		if TerminalStates[s] {
			continue
		}
		if _, ok := ValidTransitions[s]; !ok {
			t.Errorf("state %s is neither terminal nor a source in ValidTransitions", s)
		}
	}
}

func TestLegalTransitions(t *testing.T) {
	sm := NewStateMachine()
	for src, targets := range legalTransitions {
		for _, dst := range targets {
			t.Run(fmt.Sprintf("%s_to_%s", src, dst), func(t *testing.T) {
				if err := sm.Transition(src, dst); err != nil {
					t.Errorf("expected nil, got %v", err)
				}
			})
		}
	}
}

func TestSelfTransitionsAreIllegal(t *testing.T) {
	sm := NewStateMachine()
	for _, s := range allStates {
		if TerminalStates[s] {
			continue
		}
		t.Run(fmt.Sprintf("self_%s", s), func(t *testing.T) {
			err := sm.Transition(s, s)
			if err == nil {
				t.Errorf("expected error for self-transition %s → %s", s, s)
			}
		})
	}
}

func TestIllegalTransitions(t *testing.T) {
	sm := NewStateMachine()
	for _, src := range allStates {
		if TerminalStates[src] {
			continue
		}
		allowed := make(map[RunState]bool)
		for _, dst := range legalTransitions[src] {
			allowed[dst] = true
		}
		for _, dst := range allStates {
			if allowed[dst] || src == dst {
				continue
			}
			t.Run(fmt.Sprintf("illegal_%s_to_%s", src, dst), func(t *testing.T) {
				err := sm.Transition(src, dst)
				if err == nil {
					t.Errorf("expected error for %s → %s", src, dst)
				}
				if !errors.Is(err, ErrInvalidTransition) {
					t.Errorf("expected ErrInvalidTransition, got %v", err)
				}
			})
		}
	}
}

func TestTerminalStateTransitionRejected(t *testing.T) {
	sm := NewStateMachine()
	for ts := range TerminalStates {
		for _, dst := range allStates {
			if ts == dst {
				continue
			}
			t.Run(fmt.Sprintf("terminal_%s_to_%s", ts, dst), func(t *testing.T) {
				err := sm.Transition(ts, dst)
				if err == nil {
					t.Errorf("expected error for terminal transition %s → %s", ts, dst)
				}
				if !errors.Is(err, ErrTerminal) {
					t.Errorf("expected ErrTerminal, got %v", err)
				}
			})
		}
	}
}

func TestIsTerminal(t *testing.T) {
	sm := NewStateMachine()
	for ts := range TerminalStates {
		if !sm.IsTerminal(ts) {
			t.Errorf("IsTerminal(%s) = false, want true", ts)
		}
	}
	for _, s := range []RunState{StatePending, StateQueued, StateAssigned, StatePlanning, StatePlanned, StateAwaitingApproval, StateApplying} {
		if sm.IsTerminal(s) {
			t.Errorf("IsTerminal(%s) = true, want false", s)
		}
	}
}

func TestUnknownState(t *testing.T) {
	sm := NewStateMachine()
	err := sm.Transition("UNKNOWN_STATE", StateQueued)
	if err == nil {
		t.Fatal("expected error for unknown state")
	}
	if !errors.Is(err, ErrUnknownState) {
		t.Errorf("expected ErrUnknownState, got %v", err)
	}
}

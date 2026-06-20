// Package clock provides a Clock abstraction over the system clock so that
// time-dependent code can be tested deterministically without touching
// package time directly.
package clock

import (
	"sync"
	"time"
)

// Clock abstracts the passage of time. Production code uses New(); tests use
// NewFake to advance time manually.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
	// Since returns the duration elapsed since t.
	Since(t time.Time) time.Duration
}

// realClock is the production implementation backed by the system clock.
type realClock struct{}

// New returns a Clock backed by the system clock.
func New() Clock {
	return realClock{}
}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) Since(t time.Time) time.Duration {
	return time.Since(t)
}

// FakeClock is a manually controlled Clock for tests. Time does not advance on
// its own; callers move it forward with Advance. It is safe for concurrent use.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a FakeClock initialized to now.
func NewFake(now time.Time) *FakeClock {
	return &FakeClock{now: now}
}

func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *FakeClock) Since(t time.Time) time.Duration {
	return f.Now().Sub(t)
}

// Advance moves the fake clock forward by d.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

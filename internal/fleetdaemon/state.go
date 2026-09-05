package fleetdaemon

import (
	"sync"
	"time"

	"texteditor/shared/dto"
)

// liveState is the daemon's per-model serving state (state-machine.md §2).
type liveState struct {
	state  dto.LiveState
	bytes  int64
	total  int64
	since  time.Time
	procID int // pid of the runner process, 0 when none
}

// stateRegistry owns in-memory live state for every model (the daemon owns live
// state; ADR-0025, interface.md §1). Serve lifecycle transitions mutate it;
// status/list read it.
type stateRegistry struct {
	mu     sync.Mutex
	states map[string]*liveState
}

func newStateRegistry(models []string) *stateRegistry {
	r := &stateRegistry{states: map[string]*liveState{}}
	for _, n := range models {
		r.states[n] = &liveState{state: dto.LiveUnknown}
	}
	return r
}

func (s *stateRegistry) get(name string) (dto.LiveState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[name]
	if !ok {
		return dto.LiveUnknown, false
	}
	if st.state == dto.LiveUnknown {
		return dto.LiveDown, true // unknown → down for status purposes
	}
	return st.state, true
}

func (s *stateRegistry) set(name string, state dto.LiveState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[name]; ok {
		st.state = state
		st.since = time.Now()
	}
}

// progress returns bytes/total for a provisioning model; ok is false unless the
// model is currently provisioning.
func (s *stateRegistry) progress(name string) (bytes, total int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, exists := s.states[name]
	if !exists || st.state != dto.LiveProvisioning {
		return 0, 0, false
	}
	return st.bytes, st.total, true
}

// setProgress updates provisioning progress; it also flips the state to
// provisioning if not already there.
func (s *stateRegistry) setProgress(name string, bytes, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[name]
	if !ok {
		return
	}
	st.state = dto.LiveProvisioning
	st.bytes = bytes
	st.total = total
	st.since = time.Now()
}

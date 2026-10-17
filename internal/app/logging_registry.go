package app

import "fmt"

func (s *Sessions) reserveSessionStart(id int, managed *managedSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.startValidationErrorLocked(id, nil); err != nil {
		return err
	}
	if s.starting == nil {
		s.starting = make(map[int]*managedSession)
	}
	s.starting[id] = managed
	return nil
}

func (s *Sessions) startValidationErrorLocked(id int, managed *managedSession) error {
	if s.shuttingDown {
		return fmt.Errorf("sessions are shutting down")
	}
	if _, taken := s.sessions[id]; taken {
		return fmt.Errorf("session %d already exists", id)
	}
	starting := s.starting[id]
	if starting != nil && starting != managed {
		return fmt.Errorf("session %d already exists", id)
	}
	if managed != nil && starting != managed {
		return fmt.Errorf("session %d start reservation was released", id)
	}
	if len(s.sessions)+len(s.starting) >= maxSessions && managed == nil {
		return fmt.Errorf("session limit of %d reached", maxSessions)
	}
	return nil
}

// Finalization runs without Sessions.mu so slow storage cannot block process
// RPCs. The owner check prevents a delayed old exit callback deleting a newer
// process that reused the same ID.
func (s *Sessions) finalizeSession(managed *managedSession, reason string) error {
	state, finalizationErr := managed.logging.Finalize(reason)
	s.mu.Lock()
	if s.sessions[state.ID] == managed {
		delete(s.sessions, state.ID)
	}
	if s.starting[state.ID] == managed {
		delete(s.starting, state.ID)
	}
	s.mu.Unlock()
	return finalizationErr
}

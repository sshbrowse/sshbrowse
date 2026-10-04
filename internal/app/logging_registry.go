package app

import "fmt"

type retainedLoggingState struct {
	state           SessionLoggingState
	finalizationErr error
}

// An entry reserves one pane's ID from setup through process exit. Retirement
// replaces its owner with a retained result; pane release then removes it.
// Sessions.mu protects the registry, including release during finalization.
type loggingEntry struct {
	owner    *managedSession
	retained retainedLoggingState
	released bool
}

func (s *Sessions) reserveSessionStart(id int, managed *managedSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.startValidationErrorLocked(id, nil); err != nil {
		return err
	}
	// An exited session may reuse its ID once its retained result is replaced.
	delete(s.loggingOwners, id)
	if len(s.loggingOwners) >= maxLoggingHistory {
		return fmt.Errorf("retained session logging limit of %d reached", maxLoggingHistory)
	}
	if s.loggingOwners == nil {
		s.loggingOwners = make(map[int]*loggingEntry)
	}
	s.loggingOwners[id] = &loggingEntry{owner: managed}
	return nil
}

func (s *Sessions) startValidationErrorLocked(id int, managed *managedSession) error {
	if s.shuttingDown {
		return fmt.Errorf("sessions are shutting down")
	}
	if _, taken := s.sessions[id]; taken {
		return fmt.Errorf("session %d already exists", id)
	}
	entry := s.loggingOwners[id]
	if entry != nil && entry.owner != nil && entry.owner != managed {
		return fmt.Errorf("session %d already exists", id)
	}
	if managed != nil && (entry == nil || entry.owner != managed) {
		return fmt.Errorf("session %d start reservation was released", id)
	}
	// Every starting or live process owns exactly one registry entry.
	ownedSessions := 0
	for _, entry := range s.loggingOwners {
		if entry.owner != nil {
			ownedSessions++
		}
	}
	if ownedSessions >= maxSessions && managed == nil {
		return fmt.Errorf("session limit of %d reached", maxSessions)
	}
	return nil
}

func (s *Sessions) retainedLoggingResultLocked(id int) (retainedLoggingState, bool) {
	entry := s.loggingOwners[id]
	if entry == nil || entry.owner != nil {
		return retainedLoggingState{}, false
	}
	return entry.retained, true
}

func (s *Sessions) recordingPathForID(id int) (string, error) {
	s.mu.Lock()
	entry := s.loggingOwners[id]
	if entry == nil {
		s.mu.Unlock()
		return "", fmt.Errorf("session %d has no retained logging state", id)
	}
	if entry.released {
		s.mu.Unlock()
		return "", fmt.Errorf("session logging state has been released")
	}
	owner := entry.owner
	state := entry.retained.state
	s.mu.Unlock()
	if owner != nil {
		state = owner.logging.State()
	}
	return validRecordingPath(state)
}

// ReleaseLoggingState drops a pane's retained state. A live or starting owner
// keeps its reservation, with release remembered until finalization completes.
func (s *Sessions) ReleaseLoggingState(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.loggingOwners[id]; entry != nil {
		if entry.owner == nil {
			delete(s.loggingOwners, id)
		} else {
			entry.released = true
		}
	}
	return nil
}

// Finalization runs without Sessions.mu so slow storage cannot block process
// RPCs. The entry remains reserved until the atomic retirement below. Close and
// OnExit reuse the controller's cached terminal result and never close twice.
func (s *Sessions) finalizeSession(managed *managedSession, reason string) error {
	state, finalizationErr := managed.logging.Finalize(reason)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[state.ID] == managed {
		delete(s.sessions, state.ID)
	}
	if entry := s.loggingOwners[state.ID]; entry != nil && entry.owner == managed {
		if entry.released {
			delete(s.loggingOwners, state.ID)
		} else {
			*entry = loggingEntry{retained: retainedLoggingState{state: state, finalizationErr: finalizationErr}}
		}
	}
	return finalizationErr
}

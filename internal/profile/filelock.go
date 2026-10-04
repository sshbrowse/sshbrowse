package profile

import (
	"errors"
	"log"
)

type fileLock interface {
	Close() error
}

// Lock release is best-effort at process shutdown, but it is still reported:
// the operating system releases both supported lock types when the process
// exits, while a release failure is useful evidence during normal operation.
func releaseFileLock(lock fileLock) {
	if err := lock.Close(); err != nil {
		log.Printf("profile: release file lock: %v", err)
	}
}

// WithFileLock serializes a read-modify-write operation that shares the
// profile's sidecar lock. The path identifies the profile whose lock is used;
// callers still choose which data files to read or replace inside action.
func WithFileLock(path string, action func() error) error {
	if action == nil {
		return errors.New("file lock action is required")
	}
	lock, err := acquireFileLock(path)
	if err != nil {
		return err
	}
	defer releaseFileLock(lock)
	return action()
}

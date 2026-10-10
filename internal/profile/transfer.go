package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Snapshot is the portable profile data exchanged by the application. It
// excludes store metadata while preserving connection fields and dependencies.
type Snapshot struct {
	Connections []Connection    `json:"connections"`
	Folders     []string        `json:"folders"`
	Onboarding  OnboardingState `json:"onboarding"`
}

type TransferResult struct {
	Added   int      `json:"added"`
	Skipped int      `json:"skipped"`
	Actions []string `json:"actions"` // one action per incoming connection, in file order
}

// Snapshot reads a consistent view while holding both the process mutex and
// the sidecar lock used by all other profile writers.
func (s *Store) Snapshot() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lockFile, err := acquireFileLock(s.path)
	if err != nil {
		return Snapshot{}, err
	}
	defer releaseFileLock(lockFile)
	file, err := s.load()
	if err != nil {
		return Snapshot{}, err
	}
	// Export and replacement must remain available when a hand-edited route
	// is broken. Incoming data and merge results are validated separately.
	return Snapshot{
		Connections: file.Connections,
		Folders:     file.Folders,
		Onboarding:  file.Onboarding,
	}, nil
}

// SnapshotRevision returns a stable SHA-256 revision for a snapshot. Slice
// order is significant because it is also display order in the profile UI.
func SnapshotRevision(snapshot Snapshot) string {
	// Snapshot fields are concrete JSON types, so marshal cannot fail.
	data, _ := json.Marshal(snapshot)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// NormalizeSnapshot normalizes fields using the same rules as saved profile
// records, while retaining record order, forward order, provenance, and the
// explicit folder order (including empty folders).
func NormalizeSnapshot(snapshot Snapshot) (Snapshot, error) {
	connections := make([]Connection, len(snapshot.Connections))
	byID := make(map[string]Connection, len(snapshot.Connections))
	for i, connection := range snapshot.Connections {
		if connection.Provenance != nil {
			provenance := *connection.Provenance
			connection.Provenance = &provenance
		}
		connection, err := normalise(connection)
		if err != nil {
			return Snapshot{}, fmt.Errorf("connection %d: %w", i+1, err)
		}
		connection.ID = strings.TrimSpace(connection.ID)
		if connection.ID == "" {
			return Snapshot{}, fmt.Errorf("connection %d: ID is required", i+1)
		}
		if connection.Host == "" {
			return Snapshot{}, fmt.Errorf("connection %q: host is required", connection.ID)
		}
		if connection.Name == "" {
			connection.Name = connection.Host
		}
		if connection.Port < 0 || connection.Port > 65535 {
			return Snapshot{}, fmt.Errorf("connection %q: port must be between 0 and 65535", connection.ID)
		}
		if _, exists := byID[connection.ID]; exists {
			return Snapshot{}, fmt.Errorf("duplicate connection ID %q", connection.ID)
		}
		connections[i] = connection
		byID[connection.ID] = connection
	}

	folders := make([]string, 0, len(snapshot.Folders))
	seenFolders := make(map[string]bool, len(snapshot.Folders))
	addFolder := func(path string) {
		if path == "" {
			return
		}
		parts := strings.Split(path, "/")
		for i := range parts {
			ancestor := strings.Join(parts[:i+1], "/")
			if !seenFolders[ancestor] {
				seenFolders[ancestor] = true
				folders = append(folders, ancestor)
			}
		}
	}
	for _, folder := range snapshot.Folders {
		folder, err := NormalizeFolderPath(folder)
		if err != nil {
			return Snapshot{}, fmt.Errorf("folder: %w", err)
		}
		addFolder(folder)
	}
	for _, connection := range connections {
		addFolder(connection.Folder)
	}

	// Missing hops are allowed so an archive remains portable when it refers to
	// a deliberately external profile. Cycles and routes beyond the resolver's
	// supported limit are still invalid.
	for _, connection := range connections {
		seen := map[string]bool{connection.ID: true}
		current := connection
		hops := 0
		for current.JumpConnectionID != "" {
			if hops >= MaxJumpHops {
				return Snapshot{}, fmt.Errorf("connection %q: saved jump chain exceeds %d hops", connection.ID, MaxJumpHops)
			}
			next, exists := byID[current.JumpConnectionID]
			if !exists {
				break
			}
			if seen[next.ID] {
				return Snapshot{}, fmt.Errorf("connection %q: saved jump connections contain a cycle", connection.ID)
			}
			hops++
			seen[next.ID] = true
			current = next
		}
	}

	if connections == nil {
		connections = []Connection{}
	}
	if folders == nil {
		folders = []string{}
	}
	normalized := Snapshot{Connections: connections, Folders: folders, Onboarding: snapshot.Onboarding}
	data, err := json.MarshalIndent(fileFormat{
		Version: currentFileVersion, Connections: normalized.Connections,
		Folders: normalized.Folders, Onboarding: normalized.Onboarding,
	}, "", "  ")
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) >= maxConnectionsFileSize {
		return Snapshot{}, errors.New("snapshot exceeds the 1 MiB profile size limit")
	}
	return normalized, nil
}

// SnapshotWarnings reports dependencies that are not embedded in a portable
// profile after normalization. Warnings intentionally avoid putting host names
// or local paths in diagnostic text.
func SnapshotWarnings(snapshot Snapshot) []string {
	warnings := make([]string, 0, 4)
	byID := make(map[string]Connection, len(snapshot.Connections))
	for _, connection := range snapshot.Connections {
		byID[connection.ID] = connection
	}
	missingJump := false
	externalJump := false
	externalKey := false
	externalConfig := false
	for _, connection := range snapshot.Connections {
		if connection.JumpConnectionID != "" {
			if _, exists := byID[connection.JumpConnectionID]; !exists {
				missingJump = true
			}
		}
		if connection.JumpHost != "" {
			externalJump = true
		}
		if connection.IdentityFile != "" {
			externalKey = true
		}
		if connection.Provenance != nil && connection.Provenance.SSHConfigAlias != "" {
			externalConfig = true
		}
	}
	if missingJump {
		warnings = append(warnings, "Some saved jump connections are missing from this snapshot.")
	}
	if externalJump {
		warnings = append(warnings, "Some connections use raw OpenSSH ProxyJump routing outside this snapshot.")
	}
	if externalKey {
		warnings = append(warnings, "Some connections refer to identity files outside this snapshot.")
	}
	if externalConfig {
		warnings = append(warnings, "Some connections include SSH config provenance that is not embedded in this snapshot.")
	}
	return warnings
}

// ApplySnapshot applies a normalized profile snapshot atomically. The caller
// passes the revision shown during preview so an intervening profile change
// cannot be overwritten accidentally. beforeReplace runs under the profile
// lock after all validation and size checks, immediately before the write.
func (s *Store) ApplySnapshot(incoming Snapshot, replace bool, expectedRevision string, beforeReplace func(Snapshot) error) (TransferResult, error) {
	var result TransferResult
	incoming, err := NormalizeSnapshot(incoming)
	if err != nil {
		return result, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lockFile, err := acquireFileLock(s.path)
	if err != nil {
		return result, err
	}
	defer releaseFileLock(lockFile)
	currentFile, err := s.load()
	if err != nil {
		return result, err
	}
	current := Snapshot{Connections: currentFile.Connections, Folders: currentFile.Folders, Onboarding: currentFile.Onboarding}
	if expectedRevision == "" || expectedRevision != SnapshotRevision(current) {
		return result, errors.New("profile changed since transfer preview; review the updated profile and try again")
	}

	var final Snapshot
	if replace {
		final = incoming
		result.Added = len(incoming.Connections)
	} else {
		validatedCurrent, err := NormalizeSnapshot(current)
		if err != nil {
			return result, fmt.Errorf("current profile: %w; restore a backup to replace the library", err)
		}
		final, result, err = mergeSnapshots(validatedCurrent, incoming)
		if err != nil {
			return TransferResult{}, err
		}
	}
	final, err = NormalizeSnapshot(final)
	if err != nil {
		return TransferResult{}, err
	}
	file := fileFormat{Version: currentFileVersion, Connections: final.Connections, Folders: final.Folders, Onboarding: final.Onboarding}
	serialized, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return TransferResult{}, err
	}
	// Match Store.write's strict bound before asking the caller to create a
	// recovery copy, so an oversized transfer has no external side effects.
	if len(serialized) >= maxConnectionsFileSize {
		return TransferResult{}, fmt.Errorf("profile: serialized %s exceeds the 1 MiB size limit", s.path)
	}
	if beforeReplace != nil {
		if err := beforeReplace(current); err != nil {
			return TransferResult{}, fmt.Errorf("profile: create recovery backup: %w", err)
		}
	}
	if err := s.write(file); err != nil {
		return TransferResult{}, err
	}
	return result, nil
}

// PreviewMerge uses the same merge rules as ApplySnapshot without writing data.
func PreviewMerge(current, incoming Snapshot) (TransferResult, error) {
	incoming, err := NormalizeSnapshot(incoming)
	if err != nil {
		return TransferResult{}, err
	}
	current, err = NormalizeSnapshot(current)
	if err != nil {
		return TransferResult{}, fmt.Errorf("current profile: %w; restore a backup to replace the library", err)
	}
	final, result, err := mergeSnapshots(current, incoming)
	if err == nil {
		_, err = NormalizeSnapshot(final)
	}
	return result, err
}

func mergeSnapshots(current, incoming Snapshot) (Snapshot, TransferResult, error) {
	var result TransferResult
	result.Actions = make([]string, 0, len(incoming.Connections))
	currentByID := make(map[string]Connection, len(current.Connections))
	usedIDs := make(map[string]bool, len(current.Connections)+len(incoming.Connections))
	for _, connection := range current.Connections {
		currentByID[connection.ID] = connection
		usedIDs[connection.ID] = true
	}
	incomingIDs := make(map[string]bool, len(incoming.Connections))
	for _, connection := range incoming.Connections {
		incomingIDs[connection.ID] = true
		usedIDs[connection.ID] = true
	}

	// IDs that collide with local records receive stable IDs derived from this
	// exact normalized archive. Re-importing the same archive can therefore
	// recognize its previous copies. Targets are assigned before dependents so
	// jump references can be compared after their final ID mapping is known.
	revision := SnapshotRevision(incoming)
	missingIDs := make(map[string]string)
	for _, connection := range incoming.Connections {
		target := connection.JumpConnectionID
		if target == "" || incomingIDs[target] || missingIDs[target] != "" {
			continue
		}
		missingID, err := missingTransferID(revision, target, usedIDs)
		if err != nil {
			return Snapshot{}, TransferResult{}, err
		}
		missingIDs[target] = missingID
		usedIDs[missingID] = true
	}
	// Reuse unowned missing-hop IDs above so repeated imports match. Reserve
	// every local missing target before allocating real connection IDs below,
	// so importing a record cannot silently repair an unrelated broken route.
	missingLocalHops := make(map[string]bool)
	for _, connection := range current.Connections {
		if target := connection.JumpConnectionID; target != "" {
			if _, exists := currentByID[target]; !exists {
				missingLocalHops[target] = true
				usedIDs[target] = true
			}
		}
	}
	ids := make(map[string]string, len(incoming.Connections))
	for _, connection := range incoming.Connections {
		ids[connection.ID] = connection.ID
	}
	ordered := mergeDependencyOrder(incoming.Connections, incomingIDs)
	for _, connection := range ordered {
		if _, exists := currentByID[connection.ID]; !exists && !missingLocalHops[connection.ID] {
			continue
		}
		desired, err := remapConnection(connection, ids, incomingIDs, missingIDs)
		if err != nil {
			return Snapshot{}, TransferResult{}, err
		}
		if reflect.DeepEqual(currentByID[connection.ID], desired) {
			continue
		}
		allocated := false
		for salt := 0; salt < 1024; salt++ {
			candidate := transferID(revision, connection.ID, salt)
			if incomingIDs[candidate] && candidate != connection.ID {
				continue
			}
			ids[connection.ID] = candidate
			desired, err = remapConnection(connection, ids, incomingIDs, missingIDs)
			if err != nil {
				return Snapshot{}, TransferResult{}, err
			}
			if existing, exists := currentByID[candidate]; exists {
				if reflect.DeepEqual(existing, desired) {
					allocated = true // Exact record from an earlier import.
					break
				}
				continue
			}
			if usedIDs[candidate] {
				continue
			}
			usedIDs[candidate] = true
			allocated = true
			break
		}
		if !allocated {
			return Snapshot{}, TransferResult{}, errors.New("could not allocate a unique imported connection ID")
		}
	}

	folders := append([]string{}, current.Folders...)
	for _, folder := range incoming.Folders {
		folders = appendUniqueFolder(folders, folder)
	}
	connections := append([]Connection{}, current.Connections...)
	for _, connection := range incoming.Connections {
		desired, err := remapConnection(connection, ids, incomingIDs, missingIDs)
		if err != nil {
			return Snapshot{}, TransferResult{}, err
		}
		if existing, ok := currentByID[desired.ID]; ok {
			if reflect.DeepEqual(existing, desired) {
				result.Skipped++
				result.Actions = append(result.Actions, "skip")
				continue
			}
			return Snapshot{}, TransferResult{}, fmt.Errorf("imported connection ID %q conflicts with an existing connection", desired.ID)
		}
		connections = append(connections, desired)
		result.Added++
		if desired.ID != connection.ID {
			result.Actions = append(result.Actions, "copy")
		} else {
			result.Actions = append(result.Actions, "add")
		}
	}
	onboarding := OnboardingState{
		SSHConfigImportAnswered: current.Onboarding.SSHConfigImportAnswered || incoming.Onboarding.SSHConfigImportAnswered,
	}
	return Snapshot{Connections: connections, Folders: folders, Onboarding: onboarding}, result, nil
}

func remapConnection(connection Connection, ids map[string]string, incomingIDs map[string]bool, missingIDs map[string]string) (Connection, error) {
	connection.ID = ids[connection.ID]
	if connection.JumpConnectionID == "" {
		return connection, nil
	}
	if incomingIDs[connection.JumpConnectionID] {
		connection.JumpConnectionID = ids[connection.JumpConnectionID]
		return connection, nil
	}
	connection.JumpConnectionID = missingIDs[connection.JumpConnectionID]
	if connection.JumpConnectionID == "" {
		return Connection{}, errors.New("could not resolve an external jump reference")
	}
	return connection, nil
}

func mergeDependencyOrder(connections []Connection, incomingIDs map[string]bool) []Connection {
	byID := make(map[string]Connection, len(connections))
	for _, connection := range connections {
		byID[connection.ID] = connection
	}
	seen := make(map[string]bool, len(connections))
	ordered := make([]Connection, 0, len(connections))
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		connection := byID[id]
		if incomingIDs[connection.JumpConnectionID] {
			visit(connection.JumpConnectionID)
		}
		ordered = append(ordered, connection)
	}
	for _, connection := range connections {
		visit(connection.ID)
	}
	return ordered
}

func transferID(revision, sourceID string, salt int) string {
	data := []byte("sshbrowse-transfer-v1\x00" + revision + "\x00" + sourceID + "\x00" + strconv.Itoa(salt))
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:8])
}

func missingTransferID(revision, missingID string, usedIDs map[string]bool) (string, error) {
	for salt := 0; salt < 1024; salt++ {
		data := []byte("sshbrowse-missing-hop-v1\x00" + revision + "\x00" + missingID + "\x00" + strconv.Itoa(salt))
		digest := sha256.Sum256(data)
		candidate := "missing-" + hex.EncodeToString(digest[:8])
		if !usedIDs[candidate] {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate a missing jump reference ID")
}

// Package workspaces stores named workspace layouts. It deliberately contains
// connection references only; terminal output, credentials, and live session
// identifiers do not belong in this package's data model.
package workspaces

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"sshbrowse/internal/atomicfile"
	"sshbrowse/internal/profile"
)

const (
	fileVersion       = 1
	maxFileBytes      = 1 * 1024 * 1024
	maxWorkspaces     = 100
	maxNameRunes      = 100
	maxNameBytes      = 256
	maxTabs           = 20
	maxPanesPerTab    = 9
	maxConnectionID   = 128
	maxWorkspaceID    = 64
	workspaceFileName = "workspaces.json"
)

// Pane records the kind of session and, for saved SSH sessions, a connection
// reference. It never carries a runtime session ID.
type Pane struct {
	Kind         string `json:"kind"`
	ConnectionID string `json:"connectionId,omitempty"`
}

type Tab struct {
	Panes        []Pane `json:"panes"`
	SelectedPane int    `json:"selectedPane"`
}

type Layout struct {
	Tabs      []Tab `json:"tabs"`
	ActiveTab int   `json:"activeTab"`
}

type Workspace struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Layout Layout `json:"layout"`
}

// State is returned to the frontend at startup.
type State struct {
	Workspaces []Workspace `json:"workspaces"`
	Warning    string      `json:"warning"`
}

type workspaceFile struct {
	Version    int         `json:"version"`
	Workspaces []Workspace `json:"workspaces"`
}

// Store reads and writes the bounded named-workspace file.
type Store struct {
	workspacePath string
	lockPath      string
	mu            sync.Mutex
}

// NewStore derives workspaces.json next to connectionsPath.
func NewStore(connectionsPath string) *Store {
	directory := filepath.Dir(connectionsPath)
	return &Store{
		workspacePath: filepath.Join(directory, workspaceFileName),
		lockPath:      connectionsPath,
	}
}

// Load returns valid named workspaces. Malformed files are reported as
// warnings so they cannot prevent startup.
func (s *Store) Load() State {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := State{Workspaces: []Workspace{}}
	file, err := s.readWorkspaceFile()
	if err != nil {
		state.Warning = "Saved workspaces could not be loaded; the file was left unchanged."
	} else {
		state.Workspaces = file.Workspaces
	}
	return state
}

// Save creates a named workspace with a new stable ID.
func (s *Store) Save(name string, layout Layout) (Workspace, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Workspace{}, err
	}
	layout, err = normalizeLayout(layout)
	if err != nil {
		return Workspace{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var workspace Workspace
	err = profile.WithFileLock(s.lockPath, func() error {
		file, err := s.readWorkspaceFile()
		if err != nil {
			return err
		}
		if len(file.Workspaces) >= maxWorkspaces {
			return fmt.Errorf("no more than %d workspaces can be saved", maxWorkspaces)
		}
		if workspaceNameExists(file.Workspaces, name, "") {
			return fmt.Errorf("workspace %q already exists", name)
		}
		id, err := newID()
		if err != nil {
			return err
		}
		workspace = Workspace{ID: id, Name: name, Layout: layout}
		file.Workspaces = append(file.Workspaces, workspace)
		return s.writeWorkspaceFile(file)
	})
	return workspace, err
}

// Rename changes a workspace's display name while preserving its identity.
func (s *Store) Rename(id, name string) (Workspace, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Workspace{}, err
	}
	if !validWorkspaceID(id) {
		return Workspace{}, errors.New("workspace ID is invalid")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var workspace Workspace
	err = profile.WithFileLock(s.lockPath, func() error {
		file, err := s.readWorkspaceFile()
		if err != nil {
			return err
		}
		index := findWorkspace(file.Workspaces, id)
		if index < 0 {
			return fmt.Errorf("workspace %q not found", id)
		}
		if workspaceNameExists(file.Workspaces, name, id) {
			return fmt.Errorf("workspace %q already exists", name)
		}
		file.Workspaces[index].Name = name
		if err := s.writeWorkspaceFile(file); err != nil {
			return err
		}
		workspace = file.Workspaces[index]
		return nil
	})
	return workspace, err
}

// Delete removes a named workspace by stable ID.
func (s *Store) Delete(id string) error {
	if !validWorkspaceID(id) {
		return errors.New("workspace ID is invalid")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return profile.WithFileLock(s.lockPath, func() error {
		file, err := s.readWorkspaceFile()
		if err != nil {
			return err
		}
		index := findWorkspace(file.Workspaces, id)
		if index < 0 {
			return fmt.Errorf("workspace %q not found", id)
		}
		file.Workspaces = append(file.Workspaces[:index], file.Workspaces[index+1:]...)
		return s.writeWorkspaceFile(file)
	})
}

func (s *Store) readWorkspaceFile() (workspaceFile, error) {
	file, err := readWorkspaceFile(s.workspacePath)
	if errors.Is(err, os.ErrNotExist) {
		return workspaceFile{Version: fileVersion, Workspaces: []Workspace{}}, nil
	}
	return file, err
}

func (s *Store) writeWorkspaceFile(file workspaceFile) error {
	file.Version = fileVersion
	if file.Workspaces == nil {
		file.Workspaces = []Workspace{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxFileBytes {
		return fmt.Errorf("serialized saved workspaces exceed the %d MiB size limit", maxFileBytes/(1024*1024))
	}
	if err := os.MkdirAll(filepath.Dir(s.workspacePath), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteReplace(s.workspacePath, data, 0o600)
}

func readWorkspaceFile(path string) (workspaceFile, error) {
	data, err := readBounded(path)
	if err != nil {
		return workspaceFile{}, err
	}
	var file workspaceFile
	if err := decodeStrict(data, &file); err != nil {
		return workspaceFile{}, errors.New("saved workspace file contains malformed JSON or unsupported fields")
	}
	if file.Version != fileVersion {
		return workspaceFile{}, fmt.Errorf("%s has unsupported version %d", path, file.Version)
	}
	if file.Workspaces == nil {
		return workspaceFile{}, fmt.Errorf("%s has no workspace list", path)
	}
	if len(file.Workspaces) > maxWorkspaces {
		return workspaceFile{}, fmt.Errorf("%s contains more than %d workspaces", path, maxWorkspaces)
	}
	var fields workspaceFields
	if err := json.Unmarshal(data, &fields); err != nil || len(fields.Workspaces) != len(file.Workspaces) {
		return workspaceFile{}, errors.New("saved workspace file has missing required fields")
	}
	seenIDs := make(map[string]bool, len(file.Workspaces))
	seenNames := make(map[string]bool, len(file.Workspaces))
	for i := range file.Workspaces {
		workspace := &file.Workspaces[i]
		layoutFields := &fields.Workspaces[i].Layout
		if layoutFields.ActiveTab == nil || layoutFields.Tabs == nil || len(layoutFields.Tabs) != len(workspace.Layout.Tabs) {
			return workspaceFile{}, fmt.Errorf("workspace %d has missing layout fields", i)
		}
		if !validWorkspaceID(workspace.ID) {
			return workspaceFile{}, fmt.Errorf("workspace %d has an invalid ID", i)
		}
		if seenIDs[workspace.ID] {
			return workspaceFile{}, errors.New("saved workspace IDs are duplicated")
		}
		seenIDs[workspace.ID] = true
		name, err := normalizeName(workspace.Name)
		if err != nil || name != workspace.Name {
			return workspaceFile{}, fmt.Errorf("workspace %d has an invalid name", i)
		}
		nameKey := strings.ToLower(name)
		if seenNames[nameKey] {
			return workspaceFile{}, errors.New("saved workspace names are duplicated")
		}
		seenNames[nameKey] = true
		for j := range workspace.Layout.Tabs {
			tabFields := &layoutFields.Tabs[j]
			tab := &workspace.Layout.Tabs[j]
			if tabFields.SelectedPane == nil || tabFields.Panes == nil || len(tabFields.Panes) != len(tab.Panes) {
				return workspaceFile{}, fmt.Errorf("workspace %d has missing tab fields", i)
			}
			for k := range tab.Panes {
				connectionID := tabFields.Panes[k].ConnectionID
				if len(connectionID) > 0 && bytes.Equal(bytes.TrimSpace(connectionID), []byte("null")) {
					return workspaceFile{}, fmt.Errorf("workspace %d has a null connection ID", i)
				}
			}
		}
		layout, err := normalizeLayout(workspace.Layout)
		if err != nil {
			return workspaceFile{}, fmt.Errorf("workspace %d has an invalid layout: %w", i, err)
		}
		workspace.Layout = layout
	}
	return file, nil
}

// workspaceFields tracks layout selections that can decode as valid zero
// values, plus explicit null connection IDs. workspaceFile is the main decode.
type workspaceFields struct {
	Workspaces []struct {
		Layout struct {
			Tabs []struct {
				Panes []struct {
					ConnectionID json.RawMessage `json:"connectionId"`
				} `json:"panes"`
				SelectedPane *int `json:"selectedPane"`
			} `json:"tabs"`
			ActiveTab *int `json:"activeTab"`
		} `json:"layout"`
	} `json:"workspaces"`
}

func readBounded(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s exceeds the 1 MiB size limit", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%s exceeds the 1 MiB size limit", path)
	}
	return data, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("contains more than one JSON value")
		}
		return err
	}
	return nil
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("workspace name is required")
	}
	if !utf8.ValidString(name) {
		return "", errors.New("workspace name must be valid UTF-8")
	}
	if utf8.RuneCountInString(name) > maxNameRunes || len(name) > maxNameBytes {
		return "", fmt.Errorf("workspace name must be at most %d characters and %d bytes", maxNameRunes, maxNameBytes)
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("workspace name cannot contain control characters")
	}
	return name, nil
}

func normalizeLayout(layout Layout) (Layout, error) {
	if layout.Tabs == nil {
		layout.Tabs = []Tab{}
	}
	if len(layout.Tabs) > maxTabs {
		return Layout{}, fmt.Errorf("layout cannot contain more than %d tabs", maxTabs)
	}
	if len(layout.Tabs) == 0 {
		if layout.ActiveTab != -1 {
			return Layout{}, errors.New("an empty layout must have activeTab -1")
		}
		return layout, nil
	}
	if layout.ActiveTab < 0 || layout.ActiveTab >= len(layout.Tabs) {
		return Layout{}, errors.New("activeTab is outside the tab list")
	}
	for i := range layout.Tabs {
		tab := &layout.Tabs[i]
		if len(tab.Panes) == 0 {
			return Layout{}, fmt.Errorf("tab %d must contain at least one pane", i)
		}
		if len(tab.Panes) > maxPanesPerTab {
			return Layout{}, fmt.Errorf("tab %d cannot contain more than %d panes", i, maxPanesPerTab)
		}
		if tab.SelectedPane < 0 || tab.SelectedPane >= len(tab.Panes) {
			return Layout{}, fmt.Errorf("selectedPane for tab %d is outside the pane list", i)
		}
		for j := range tab.Panes {
			pane := &tab.Panes[j]
			switch pane.Kind {
			case "ssh", "sftp":
				if !validConnectionID(pane.ConnectionID) {
					return Layout{}, fmt.Errorf("pane %d in tab %d requires a valid connection ID", j, i)
				}
			case "local":
				if pane.ConnectionID != "" {
					return Layout{}, fmt.Errorf("local pane %d in tab %d cannot have a connection ID", j, i)
				}
			case "unavailable":
				if pane.ConnectionID != "" {
					return Layout{}, fmt.Errorf("unavailable pane %d in tab %d cannot have a connection ID", j, i)
				}
			default:
				return Layout{}, fmt.Errorf("pane %d in tab %d has an unsupported kind", j, i)
			}
		}
	}
	return layout, nil
}

func validConnectionID(id string) bool {
	if id == "" || len(id) > maxConnectionID || strings.TrimSpace(id) != id {
		return false
	}
	return utf8.ValidString(id) && strings.IndexFunc(id, unicode.IsControl) < 0
}

func validWorkspaceID(id string) bool {
	if id == "" || len(id) > maxWorkspaceID || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func workspaceNameExists(workspaces []Workspace, name, exceptID string) bool {
	key := strings.ToLower(name)
	for _, workspace := range workspaces {
		if workspace.ID != exceptID && strings.ToLower(workspace.Name) == key {
			return true
		}
	}
	return false
}

func findWorkspace(workspaces []Workspace, id string) int {
	for i := range workspaces {
		if workspaces[i].ID == id {
			return i
		}
	}
	return -1
}

func newID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(randomBytes), nil
}

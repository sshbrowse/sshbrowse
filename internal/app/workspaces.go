package app

import "sshbrowse/internal/workspaces"

// Workspaces exposes named workspace layouts to the frontend.
// The store never starts sessions; the caller must explicitly turn a returned
// layout into sessions after user confirmation.
type Workspaces struct {
	store *workspaces.Store
}

func NewWorkspaces(store *workspaces.Store) *Workspaces {
	return &Workspaces{store: store}
}

func (w *Workspaces) Load() workspaces.State {
	return w.store.Load()
}

func (w *Workspaces) Save(name string, layout workspaces.Layout) (workspaces.Workspace, error) {
	return w.store.Save(name, layout)
}

func (w *Workspaces) Rename(id, name string) (workspaces.Workspace, error) {
	return w.store.Rename(id, name)
}

func (w *Workspaces) Delete(id string) error {
	return w.store.Delete(id)
}

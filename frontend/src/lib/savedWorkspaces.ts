import type { Connection } from "../../bindings/sshbrowse/internal/profile/models";
import { maximumTabs, sessionIds, type Session, type Tab } from "./tabs.ts";
import type { WorkspaceState } from "./workspace.ts";

export interface WorkspacePane {
  kind: "ssh" | "sftp" | "local" | "unavailable";
  connectionId?: string;
}
export interface SavedLayoutTab {
  panes: WorkspacePane[];
  selectedPane: number;
}
export interface SavedLayout {
  tabs: SavedLayoutTab[];
  activeTab: number;
}
export interface SavedWorkspace {
  id: string;
  name: string;
  layout: SavedLayout;
}
export type RestoredSession = Session & { autofocus: boolean };

// Use positions and saved connection references, never process IDs or connection snapshots.
export function captureWorkspace(state: WorkspaceState): SavedLayout {
  const byId = new Map(state.sessions.map((session) => [session.id, session]));
  return {
    tabs: state.tabs.map((tab) => {
      const ids = sessionIds(tab);
      return {
        panes: ids.map((id): WorkspacePane => {
          const session = byId.get(id);
          if (!session) return { kind: "unavailable" };
          if (session.unavailable) {
            return session.unavailable.connectionId
              ? { kind: session.command, connectionId: session.unavailable.connectionId }
              : { kind: "unavailable" };
          }
          if (session.connection === null) return { kind: "local" };
          return session.connection.id
            ? { kind: session.command, connectionId: session.connection.id }
            : { kind: "unavailable" };
        }),
        selectedPane: tab.kind === "session" ? 0 : ids.indexOf(tab.selectedSessionId),
      };
    }),
    activeTab: state.activeId === null ? -1 : state.tabs.findIndex((tab) => tab.id === state.activeId),
  };
}

export function workspaceSummary(layout: SavedLayout, connections: readonly Connection[]) {
  const savedIds = new Set(connections.map((connection) => connection.id));
  const panes = layout.tabs.flatMap((tab) => tab.panes);
  return {
    tabs: layout.tabs.length,
    local: panes.filter((pane) => pane.kind === "local").length,
    remote: panes.filter((pane) => (pane.kind === "ssh" || pane.kind === "sftp") && savedIds.has(pane.connectionId ?? "")).length,
    unavailable: panes.filter((pane) => pane.kind === "unavailable" || ((pane.kind === "ssh" || pane.kind === "sftp") && !savedIds.has(pane.connectionId ?? ""))).length,
  };
}

// Detect availability changes while a confirmation is open. Restoring a formerly
// missing reference must not start a session that the confirmation did not list.
export function workspaceAvailability(layout: SavedLayout, connections: readonly Connection[]): string {
  const savedIds = new Set(connections.map((connection) => connection.id));
  return layout.tabs.flatMap((tab) => tab.panes)
    .map((pane) => pane.kind === "local" ? "local"
      : pane.kind !== "unavailable" && savedIds.has(pane.connectionId ?? "") ? "remote" : "unavailable")
    .join(",");
}

// Called only by an explicit Open/Restore action. Appending preserves every existing session.
export function appendWorkspace(
  current: WorkspaceState<RestoredSession>,
  layout: SavedLayout,
  connections: readonly Connection[],
  nextTabId: number,
  nextSessionId: number,
): WorkspaceState<RestoredSession> & { nextTabId: number; nextSessionId: number } {
  if (current.tabs.length + layout.tabs.length > maximumTabs) {
    throw new Error(`Opening this workspace would exceed the limit of ${maximumTabs} tabs. Close tabs first.`);
  }
  const byId = new Map(connections.map((connection) => [connection.id, connection]));
  const sessions = [...current.sessions];
  const addedTabs: Tab[] = [];
  for (const [tabIndex, tab] of layout.tabs.entries()) {
    const ids = tab.panes.map((pane, paneIndex) => {
      const id = nextSessionId++;
      const connection = pane.connectionId ? byId.get(pane.connectionId) : undefined;
      const unavailable = pane.kind !== "local" && !connection;
      sessions.push({
        id,
        connection: connection ? structuredClone(connection) : null,
        command: pane.kind === "sftp" ? "sftp" : "ssh",
        status: unavailable ? "closed" : "connecting",
        processInstanceId: null,
        autofocus: tabIndex === layout.activeTab && paneIndex === tab.selectedPane,
        ...(unavailable ? { unavailable: { connectionId: pane.connectionId ?? "" } } : {}),
      });
      return id;
    });
    const id = nextTabId++;
    addedTabs.push(ids.length === 1
      ? { id, kind: "session", sessionId: ids[0] }
      : { id, kind: "workspace", sessionIds: ids, selectedSessionId: ids[tab.selectedPane] });
  }
  return {
    tabs: [...current.tabs, ...addedTabs],
    sessions,
    activeId: addedTabs[layout.activeTab]?.id ?? current.activeId,
    nextTabId,
    nextSessionId,
  };
}

// Only one write runs at a time and only the newest waiting layout is retained.
// flush orders recovery resolution after older writes, preventing stale overwrites.
export function createWorkspaceWriter(write: (layout: SavedLayout) => Promise<unknown>, onerror: (error: unknown) => void) {
  let pending: SavedLayout | null = null;
  let running: Promise<void> | null = null;
  async function drain() {
    while (pending) {
      const layout = pending;
      pending = null;
      try { await write(layout); } catch (error) { onerror(error); }
    }
  }
  function start() {
    running = drain().finally(() => {
      running = null;
      if (pending) start();
    });
  }
  return {
    push(layout: SavedLayout) {
      pending = layout;
      if (!running) start();
    },
    async flush() { while (running) await running; },
  };
}

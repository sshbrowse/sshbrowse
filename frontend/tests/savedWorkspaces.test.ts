import assert from "node:assert/strict";
import test from "node:test";
import { appendWorkspace, captureWorkspace, createWorkspaceWriter, workspaceAvailability, workspaceSummary, type SavedLayout, type RestoredSession } from "../src/lib/savedWorkspaces.ts";
import type { Connection } from "../bindings/sshbrowse/internal/profile/models";
import type { WorkspaceState } from "../src/lib/workspace.ts";

function connection(id: string, host = "placeholder.invalid"): Connection {
  return { id, host, name: "Placeholder", folder: "", user: "", port: 0, identityFile: "", jumpHost: "", agentForwarding: false, x11Forwarding: false, localForwards: [], remoteForwards: [], dynamicForwards: [] };
}
function session(id: number, saved: Connection | null): RestoredSession {
  return { id, connection: saved, command: "ssh", status: "live", processInstanceId: id + 1000, autofocus: true };
}
const layout: SavedLayout = {
  tabs: [
    { panes: [{ kind: "ssh", connectionId: "one" }, { kind: "sftp", connectionId: "one" }, { kind: "ssh", connectionId: "deleted" }], selectedPane: 1 },
    { panes: [{ kind: "local" }], selectedPane: 0 },
    { panes: [{ kind: "unavailable" }], selectedPane: 0 },
  ], activeTab: 0,
};

test("capture stores only connection references and positions, including unsaved hosts", () => {
  const remote = connection("one");
  remote.user = "excluded-user";
  remote.identityFile = "excluded-key-path";
  const current: WorkspaceState = {
    sessions: [session(51, remote), session(52, null), session(53, connection(""))],
    tabs: [{ id: 61, kind: "workspace", sessionIds: [52, 51], selectedSessionId: 51 }, { id: 62, kind: "session", sessionId: 53 }], activeId: 61,
  };
  assert.deepEqual(captureWorkspace(current), {
    tabs: [{ panes: [{ kind: "local" }, { kind: "ssh", connectionId: "one" }], selectedPane: 1 }, { panes: [{ kind: "unavailable" }], selectedPane: 0 }], activeTab: 0,
  });
  const serialized = JSON.stringify(captureWorkspace(current));
  for (const excluded of ["placeholder.invalid", "excluded-user", "excluded-key-path", "1051", "processInstanceId", "status"]) assert.ok(!serialized.includes(excluded));
});

test("restore appends, resolves current settings, keeps duplicates and missing panes in position", () => {
  const existing = session(10, null);
  const current: WorkspaceState<RestoredSession> = { sessions: [existing], tabs: [{ id: 20, kind: "session", sessionId: 10 }], activeId: 20 };
  const saved = connection("one", "edited.invalid");
  const restored = appendWorkspace(current, layout, [saved], 21, 11);
  assert.equal(restored.sessions[0], existing);
  assert.equal(restored.tabs[0], current.tabs[0]);
  assert.equal(restored.activeId, 21);
  assert.deepEqual(restored.tabs[1], { id: 21, kind: "workspace", sessionIds: [11, 12, 13], selectedSessionId: 12 });
  assert.equal(restored.sessions[1].connection?.host, "edited.invalid");
  assert.equal(restored.sessions[2].command, "sftp");
  assert.equal(restored.sessions[1].autofocus, false);
  assert.equal(restored.sessions[2].autofocus, true);
  assert.deepEqual(restored.sessions[3].unavailable, { connectionId: "deleted" });
  assert.equal(restored.sessions[3].status, "closed");
  assert.equal(restored.sessions[4].unavailable, undefined);
  assert.deepEqual(restored.sessions[5].unavailable, { connectionId: "" });
  assert.equal(restored.nextTabId, 24);
  assert.equal(restored.nextSessionId, 16);
  assert.deepEqual(captureWorkspace(restored).tabs.slice(1), layout.tabs);
  saved.host = "later.invalid";
  assert.equal(restored.sessions[1].connection?.host, "edited.invalid");
  assert.deepEqual(workspaceSummary(layout, [saved]), { tabs: 3, remote: 2, local: 1, unavailable: 2 });
});

test("tab limit refuses the entire restore without changing current state", () => {
  const current: WorkspaceState<RestoredSession> = { sessions: [session(1, null)], tabs: Array.from({ length: 20 }, (_, index) => ({ id: index + 1, kind: "session", sessionId: 1 })), activeId: 1 };
  assert.throws(() => appendWorkspace(current, layout, [], 21, 2), /exceed.*20 tabs/);
  assert.equal(current.tabs.length, 20);
  assert.equal(current.sessions.length, 1);
});

test("confirmation detects references becoming available while accepting current settings", () => {
  const saved = connection("one");
  const confirmed = workspaceAvailability(layout, [saved]);
  saved.host = "edited.invalid";
  assert.equal(workspaceAvailability(layout, [saved]), confirmed);
  assert.notEqual(workspaceAvailability(layout, [saved, connection("deleted")]), confirmed);
  assert.notEqual(workspaceAvailability(layout, []), confirmed);
});

test("autosave bounds the queue, flush waits, and write errors do not stall newer layouts", async () => {
  let release!: () => void;
  const blocked = new Promise<void>((resolve) => { release = resolve; });
  const writes: SavedLayout[] = [];
  const errors: unknown[] = [];
  const writer = createWorkspaceWriter(async (value) => {
    writes.push(value);
    if (writes.length === 1) { await blocked; throw new Error("disk full"); }
  }, (error) => errors.push(error));
  const empty: SavedLayout = { tabs: [], activeTab: -1 };
  writer.push(empty);
  writer.push({ ...layout, activeTab: 1 });
  writer.push(layout);
  let flushed = false;
  const flush = writer.flush().then(() => { flushed = true; });
  await Promise.resolve();
  assert.equal(flushed, false);
  release();
  await flush;
  assert.deepEqual(writes, [empty, layout]);
  assert.equal(errors.length, 1);
  writer.push(empty);
  await writer.flush();
  assert.equal(writes.length, 3);
});

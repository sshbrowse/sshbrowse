import assert from "node:assert/strict";
import test from "node:test";

import {
  actionConnections,
  connectionAddress,
  connectionsToMove,
  folderContextMenuName,
  openConnectionsInNewTab,
  selectedFolderDestination,
  selectedConnections,
  validDropFolder,
} from "../src/lib/sidebarActions.ts";
import { placeNewSession, type SessionPlacement } from "../src/lib/routing.ts";
import { maximumTabs, sessionIds, type Tab } from "../src/lib/tabs.ts";

function connection(id: string, folder = "") {
  return { id, name: id, user: id, host: `${id}.example`, port: 22, folder };
}

const connections = [connection("a"), connection("b", "prod"), connection("c", "prod/db")];

test("Connect in New Tab preserves existing tabs and follows tiling and capacity limits", () => {
  const cases = [
    { tiling: false, selected: 1, existing: 1, sizes: [1], attempts: 1 },
    { tiling: true, selected: 1, existing: 1, sizes: [1], attempts: 1 },
    { tiling: false, selected: 3, existing: 1, sizes: [1, 1, 1], attempts: 3 },
    { tiling: true, selected: 3, existing: 1, sizes: [3], attempts: 3 },
    { tiling: true, selected: 12, existing: 1, sizes: [9, 3], attempts: 12 },
    { tiling: true, selected: 12, existing: 19, sizes: [9], attempts: 10 },
    { tiling: false, selected: 3, existing: 19, sizes: [1], attempts: 2 },
    { tiling: true, selected: 3, existing: 20, sizes: [], attempts: 1 },
  ];
  for (const scenario of cases) {
    const workspace: Tab = { id: 1, kind: "workspace", sessionIds: [1, 2], selectedSessionId: 2 };
    const originalTabs: Tab[] = [workspace, ...Array.from({ length: scenario.existing - 1 }, (_, index): Tab => ({
      id: index + 2, kind: "session", sessionId: index + 3,
    }))];
    const originalSnapshot = structuredClone(originalTabs);
    let placement: SessionPlacement = { tabs: originalTabs, activeId: 1, nextTabId: scenario.existing + 1 };
    let attempts = 0;
    const selected = Array.from({ length: scenario.selected }, (_, index) => connection(String(index)));
    openConnectionsInNewTab(selected, (target, _autofocus, useTiling = scenario.tiling) => {
      attempts++;
      const next = placeNewSession(placement.tabs, placement.activeId, 100 + Number(target.id), useTiling, placement.nextTabId);
      if (next.tabs.length > maximumTabs) return false;
      placement = next;
      return true;
    });
    const newTabs = placement.tabs.slice(scenario.existing);
    const openedIds = newTabs.flatMap(sessionIds);
    assert.deepEqual(newTabs.map((tab) => sessionIds(tab).length), scenario.sizes, JSON.stringify(scenario));
    assert.deepEqual(openedIds, Array.from({ length: openedIds.length }, (_, index) => 100 + index));
    assert.equal(attempts, scenario.attempts);
    assert.deepEqual(originalTabs, originalSnapshot);
    assert.deepEqual(placement.tabs.slice(0, scenario.existing), originalSnapshot);
    assert.equal(placement.tabs[0], workspace);
    for (const tab of newTabs) {
      assert.equal(tab.kind, sessionIds(tab).length === 1 ? "session" : "workspace");
    }
  }
});

test("sidebar action targets preserve source order and selection semantics", () => {
  assert.deepEqual(selectedConnections(connections, new Set(["c", "a"])), [connections[0], connections[2]]);
  assert.deepEqual(actionConnections(connections, ["c", "a"], "c"), [connections[0], connections[2]]);
  assert.deepEqual(actionConnections(connections, ["c", "a"], "b"), [connections[1]]);
});

test("connection addresses are shared by display and clipboard paths", () => {
  assert.equal(connectionAddress(connection("a")), "a@a.example:22");
  assert.equal(connectionAddress({ ...connection("a"), user: "", port: 0 }), "a.example");
});

test("folder drops reject invalid destinations and connection moves skip no-ops", () => {
  const folder = { kind: "folder" as const, path: "prod" };
  assert.equal(validDropFolder(folder, "prod"), null);
  assert.equal(validDropFolder(folder, "prod/db"), null);
  assert.equal(validDropFolder(folder, ""), null);
  assert.equal(validDropFolder(folder, "archive"), "archive");
  assert.deepEqual(connectionsToMove(connections, ["b", "c"], "prod"), [connections[2]]);
});

test("folder menus offer Connect all for direct or nested connections only", () => {
  assert.equal(folderContextMenuName(connections, "prod"), "saved-folder");
  assert.equal(folderContextMenuName(connections, "prod/db"), "saved-folder");
  assert.equal(folderContextMenuName([connection("deep", "prod/db/replicas")], "prod"), "saved-folder");
  assert.equal(folderContextMenuName(connections, "empty"), "saved-folder-empty");
  assert.equal(folderContextMenuName([connection("other", "production")], "prod"), "saved-folder-empty");
  assert.equal(folderContextMenuName([], "prod"), "saved-folder-empty");
});

test("new connections use the selected folder and fall back to the top level", () => {
  const folders = ["prod", "prod/db", "empty"];
  assert.equal(selectedFolderDestination(folders, "prod"), "prod");
  assert.equal(selectedFolderDestination(folders, "prod/db"), "prod/db");
  assert.equal(selectedFolderDestination(folders, "empty"), "empty");
  assert.equal(selectedFolderDestination(folders, null), "");
  assert.equal(selectedFolderDestination(folders, ""), "");
  assert.equal(selectedFolderDestination(folders, "deleted"), "");
});

import assert from "node:assert/strict";
import test from "node:test";
import { sessionContextMenu, sessionForContextMenu } from "../src/lib/sessionMenu.ts";

const session = {
  id: 2,
  processInstanceId: 22,
  loggingControl: { target: "2:22", canStart: true, canStop: false },
};

test("tab and tile menus offer the current logging action and retain layout actions when unavailable", () => {
  assert.deepEqual(sessionContextMenu(session, false), { name: "session-tab-start", target: "2:22" });
  assert.deepEqual(sessionContextMenu(session, true), { name: "session-tile-start", target: "2:22" });
  const logging = { ...session, loggingControl: { ...session.loggingControl, canStart: false, canStop: true } };
  assert.equal(sessionContextMenu(logging, true).name, "session-tile-stop");
  const busy = { ...session, loggingControl: { ...session.loggingControl, canStart: false } };
  assert.equal(sessionContextMenu(busy, true).name, "session-tile-idle");
  assert.deepEqual(sessionContextMenu({ id: 3, processInstanceId: null }, false), {
    name: "session-tab-idle", target: "3:null",
  });
  const restarted = { ...session, processInstanceId: 23 };
  assert.equal(sessionContextMenu(restarted, true).name, "session-tile-idle");
});

test("close and detach target the right-clicked session without falling back to the selected session", () => {
  const sessions = [{ id: 1, processInstanceId: 11 }, session, { id: 3, processInstanceId: null }];
  assert.equal(sessionForContextMenu(sessions, "2:22"), session);
  assert.equal(sessionForContextMenu(sessions, "3:null"), sessions[2]);
  for (const target of [undefined, null, "", "2:21", "2", 2, {}, ["2:22"], "4:44"]) {
    assert.equal(sessionForContextMenu(sessions, target), null);
  }
  assert.equal(sessionForContextMenu([{ ...session, processInstanceId: 23 }], "2:22"), null);
  assert.equal(sessionForContextMenu([], "2:22"), null);
});

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";
import { canCheckForUpdates, claimStartupUpdateCheck, githubReleaseURL } from "../src/lib/updates.ts";
import { preferenceKeys } from "../src/lib/storage.ts";

// Execute the real App event callbacks without requiring a desktop WebView.
// TypeScript's AST keeps this harness independent of callback formatting.
const source = readFileSync(new URL("../src/App.svelte", import.meta.url), "utf8");
const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'));
const ast = ts.createSourceFile("App.ts", script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
const callbacks = new Map<string, string>();
const actions = new Map<string, string>();
function visit(node: ts.Node) {
  if (ts.isFunctionDeclaration(node) && node.name && ["emitCheckForUpdates", "emitDownloadUpdate", "emitRestartUpdate", "setCheckUpdatesOnStartup"].includes(node.name.text)) {
    actions.set(node.name.text, node.getText(ast));
  }
  if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.name.text.startsWith("offUpdate") && node.initializer && ts.isCallExpression(node.initializer)) {
    const callback = node.initializer.arguments[1];
    if (callback) callbacks.set(node.name.text, callback.getText(ast));
  }
  ts.forEachChild(node, visit);
}
visit(ast);

function app(storageUnavailable = false) {
  const values = new Map<string, string>();
  const emitted: string[] = [];
  const timers = new Map<number, () => void>();
  const timerDelays: number[] = [];
  let nextTimerId = 1;
  const storage = { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value); } };
  const state = {
    updateInfo: null, startupCheckScheduled: false, checkUpdatesOnStartup: true,
    startupPromptWanted: true, updatePrompt: null, updateStatus: "", updateError: false,
    updateBusy: false, updateAvailable: false, updateReady: false, updateReleaseURL: "", backupBusy: false,
    updateCheckStartedAt: 0, updateCheckResultTimer: null, manualUpdateCheckMinimumDurationMs: 1000,
    setTimeout: (callback: () => void, delay: number) => { const id = nextTimerId++; timers.set(id, callback); timerDelays.push(delay); return id; },
    clearTimeout: (id: number) => { timers.delete(id); },
    canCheckForUpdates, claimStartupUpdateCheck, githubReleaseURL,
    startupUpdateCheckEvent: "update:startup-check",
    checkForUpdatesMenuEvent: "menu:checkForUpdates",
    updateDownloadRequestEvent: "wails:updater:download",
    updateRestartRequestEvent: "wails:updater:restart",
    preferenceKeys,
    persistPreference: (key: string, value: string) => { values.set(key, value); },
    Events: { Emit: (name: string) => { emitted.push(name); return Promise.resolve(); } },
    localStorage: storage,
  };
  if (storageUnavailable) Object.defineProperty(state, "localStorage", { get() { throw new Error("storage denied"); } });
  const context = vm.createContext(state);
  function send(name: string, data?: unknown) {
    const callback = callbacks.get(name);
    assert.ok(callback, `missing ${name} callback`);
    context.event = { data };
    const code = ts.transpile(`(${callback})(event)`, { target: ts.ScriptTarget.ES2022 });
    vm.runInContext(code, context);
  }
  function runTimers() {
    const pending = [...timers.values()];
    timers.clear();
    for (const callback of pending) callback();
  }
  function runAction(name: string, ...args: unknown[]) {
    const action = actions.get(name);
    assert.ok(action, `missing ${name} action`);
    context.args = args;
    vm.runInContext(ts.transpile(`(${action})(...args)`, { target: ts.ScriptTarget.ES2022 }), context);
  }
  return { state, send, emitted, runTimers, timerDelays, timers, runAction, values };
}
const release = { automatic: true, checked: true, version: "2.0.0", releaseURL: "https://github.com/sshbrowse/sshbrowse/releases/tag/v2.0.0", error: "" };

test("backup operations block update actions and preference writes until they finish", () => {
  const { state, runAction, emitted, values } = app();
  values.set(preferenceKeys.checkUpdatesOnStartup, "true");
  state.backupBusy = true;
  for (const name of ["emitCheckForUpdates", "emitDownloadUpdate", "emitRestartUpdate"]) runAction(name);
  runAction("setCheckUpdatesOnStartup", false);
  assert.deepEqual(emitted, []);
  assert.equal(state.checkUpdatesOnStartup, true);
  assert.equal(values.get(preferenceKeys.checkUpdatesOnStartup), "true");

  state.backupBusy = false;
  for (const name of ["emitCheckForUpdates", "emitDownloadUpdate", "emitRestartUpdate"]) runAction(name);
  runAction("setCheckUpdatesOnStartup", false);
  assert.deepEqual(emitted, [state.checkForUpdatesMenuEvent, state.updateDownloadRequestEvent, state.updateRestartRequestEvent]);
  assert.equal(state.checkUpdatesOnStartup, false);
  assert.equal(values.get(preferenceKeys.checkUpdatesOnStartup), "false");
});

test("startup info schedules one metadata check, honors opt-out, and survives denied localStorage", () => {
  for (const unavailable of [false, true]) {
    const { send, emitted } = app(unavailable);
    send("offUpdateInfo", { availability: "package-manager" });
    send("offUpdateInfo", { availability: "package-manager" });
    assert.deepEqual(emitted, ["update:startup-check"]);
  }
  const disabled = app();
  disabled.state.checkUpdatesOnStartup = false;
  disabled.send("offUpdateInfo", { availability: "supported" });
  assert.deepEqual(disabled.emitted, []);
  const development = app();
  development.send("offUpdateInfo", { availability: "development" });
  assert.deepEqual(development.emitted, []);
});

test("automatic failures leave the interface untouched and never request a download", () => {
  const { state, send, emitted } = app();
  send("offUpdateCheckResult", { ...release, version: "", error: "offline" });
  assert.equal(state.updateStatus, "");
  assert.equal(state.updateError, false);
  assert.equal(state.updateBusy, false);
  assert.equal(state.updatePrompt, null);
  assert.deepEqual(emitted, []);
});

test("automatic results show a notice only for a validated release and an enabled preference", () => {
  const { state, send, emitted } = app();
  send("offUpdateCheckResult", release);
  assert.equal(state.updateAvailable, true);
  assert.equal((state.updatePrompt as unknown as { version: string }).version, "2.0.0");
  assert.equal(state.updateReleaseURL, release.releaseURL);
  assert.deepEqual(emitted, []);
  const invalid = app();
  invalid.send("offUpdateCheckResult", { ...release, releaseURL: "https://example.invalid/" });
  assert.equal(invalid.state.updatePrompt, null);
  assert.equal(invalid.state.updateReleaseURL, "");
  const disabled = app();
  disabled.state.checkUpdatesOnStartup = false;
  disabled.send("offUpdateCheckResult", release);
  assert.equal(disabled.state.updatePrompt, null);
});

test("manual results report failures and success even with startup checks disabled", () => {
  const { state, send, runTimers } = app();
  state.checkUpdatesOnStartup = false;
  send("offUpdateStarted");
  assert.equal(state.updateBusy, true);
  send("offUpdateCheckResult", { ...release, automatic: false, version: "", error: "offline" });
  runTimers();
  assert.equal(state.updateBusy, false);
  assert.equal(state.updateError, true);
  assert.match(state.updateStatus, /offline/);
  send("offUpdateStarted");
  send("offUpdateCheckResult", { ...release, automatic: false });
  runTimers();
  assert.equal(state.updateAvailable, true);
  assert.equal(state.updateBusy, false);
  assert.equal(state.updateError, false);
  assert.equal(state.updatePrompt, null);
});

test("late automatic results cannot replace a manual check or staged update", () => {
  const manual = app();
  manual.send("offUpdateStarted");
  manual.send("offUpdateCheckResult", release);
  assert.equal(manual.state.updateStatus, "Checking for updates…");
  assert.equal(manual.state.updateBusy, true);
  manual.send("offUpdateCheckResult", { ...release, automatic: false, version: "" });
  manual.send("offUpdateCheckResult", release);
  assert.equal(manual.state.updateStatus, "Checking for updates…");
  assert.ok(manual.timerDelays[0] > 0 && manual.timerDelays[0] <= 1000);
  manual.runTimers();
  assert.equal(manual.state.updateStatus, "SSHBrowse is up to date.");
  assert.equal(manual.state.updatePrompt, null);
  const ready = app();
  ready.send("offUpdateReady");
  ready.send("offUpdateCheckResult", release);
  assert.equal(ready.state.updateReady, true);
  assert.equal(ready.state.updateAvailable, false);
  assert.equal(ready.state.updateStatus, "Update ready. Restart to use the new version.");
});

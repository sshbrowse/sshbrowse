import assert from "node:assert/strict";
import test from "node:test";
import {
  applyBackupPreferences,
  captureBackupPreferences,
  validateBackupPreferences,
} from "../src/lib/backupPreferences.ts";
import { preferenceKeys } from "../src/lib/storage.ts";

function storage(initial: Record<string, string> = {}) {
  const entries = new Map(Object.entries(initial));
  return {
    getItem(key: string) {
      return entries.get(key) ?? null;
    },
    setItem(key: string, value: string) {
      entries.set(key, value);
    },
    removeItem(key: string) {
      entries.delete(key);
    },
    snapshot() {
      return Object.fromEntries(entries);
    },
  };
}

test("capture exports every effective preference and excludes the transient update throttle", () => {
  const source = storage({
    [preferenceKeys.sidebarWidth]: "999",
    [preferenceKeys.sidebarVisible]: "false",
    [preferenceKeys.collapsedFolders]: JSON.stringify(["prod", "dev"]),
    [preferenceKeys.interfaceScale]: "1.26",
    [preferenceKeys.lastStartupUpdateCheck]: "123456789",
    "sshbrowse.future.setting": "future",
  });

  const result = captureBackupPreferences(source);
  assert.equal(Object.keys(result).length, 15);
  assert.equal(result[preferenceKeys.sidebarWidth], "360");
  assert.equal(result[preferenceKeys.sidebarVisible], "false");
  assert.equal(result[preferenceKeys.collapsedFolders], '["dev","prod"]');
  assert.equal(result[preferenceKeys.interfaceScale], "1.3");
  assert.equal(result[preferenceKeys.lastStartupUpdateCheck], undefined);
  assert.equal(result["sshbrowse.future.setting"], undefined);
  assert.equal(result[preferenceKeys.themeName], "classic");
});

test("capture surfaces storage read failures instead of exporting defaults", () => {
  assert.throws(() => captureBackupPreferences({
    getItem() {
      throw new Error("storage unavailable");
    },
    setItem() {},
  }), /storage unavailable/);
});

test("validation requires the exact known settings and rejects unsafe values", () => {
  const valid = captureBackupPreferences(storage());
  assert.deepEqual(validateBackupPreferences(valid), valid);
  assert.doesNotThrow(() => validateBackupPreferences({ ...valid, [preferenceKeys.interfaceScale]: "1" }));

  const { [preferenceKeys.themeName]: _theme, ...missing } = valid;
  assert.throws(() => validateBackupPreferences(missing), /incomplete/);
  assert.throws(
    () => validateBackupPreferences({ ...valid, "sshbrowse.unknown": "true" }),
    /incomplete|unsupported/,
  );
  assert.throws(
    () => validateBackupPreferences({ ...valid, [preferenceKeys.sidebarVisible]: "yes" }),
    /invalid/,
  );
  assert.throws(
    () => validateBackupPreferences({ ...valid, [preferenceKeys.terminalScrollbackLines]: "50001" }),
    /invalid/,
  );
  assert.throws(
    () => validateBackupPreferences({ ...valid, [preferenceKeys.collapsedFolders]: '["ok",""]' }),
    /invalid/,
  );
  for (const path of [".", "Team/../Prod", "Team//Prod", " Team", "Team\tProd", "x".repeat(256)]) {
    assert.throws(
      () => validateBackupPreferences({ ...valid, [preferenceKeys.collapsedFolders]: JSON.stringify([path]) }),
      /invalid/,
      `rejected folder path ${JSON.stringify(path)}`,
    );
  }
  assert.throws(
    () => validateBackupPreferences({ ...valid, [preferenceKeys.collapsedFolders]: '["Team","Team"]' }),
    /invalid/,
  );
});

test("a local storage write failure rolls back all preferences and skips the backend operation", async () => {
  const initial = {
    [preferenceKeys.sidebarVisible]: "false",
    [preferenceKeys.lastStartupUpdateCheck]: "987654321",
    "sshbrowse.future.setting": "preserve",
  };
  const target = storage(initial);
  const imported = captureBackupPreferences(storage({ [preferenceKeys.sidebarVisible]: "true" }));
  const originalSetItem = target.setItem;
  let failedOnce = false;
  target.setItem = (key, value) => {
    if (key === preferenceKeys.terminalScrollbackLines && !failedOnce) {
      failedOnce = true;
      throw new Error("quota exceeded");
    }
    originalSetItem(key, value);
  };
  let called = false;

  await assert.rejects(
    applyBackupPreferences(target, imported, async () => {
      called = true;
      return "unexpected";
    }),
    /quota exceeded/,
  );
  assert.equal(called, false);
  assert.deepEqual(target.snapshot(), initial);
});

test("backend failure restores exact prior settings and preserves transient keys", async () => {
  const initial = {
    [preferenceKeys.sidebarVisible]: "false",
    [preferenceKeys.lastStartupUpdateCheck]: "987654321",
    "sshbrowse.future.setting": "preserve",
  };
  const target = storage(initial);
  const imported = captureBackupPreferences(storage({ [preferenceKeys.sidebarVisible]: "true" }));

  await assert.rejects(
    applyBackupPreferences(target, imported, async () => {
      assert.equal(target.getItem(preferenceKeys.sidebarVisible), "true");
      throw new Error("backend commit failed");
    }),
    /backend commit failed/,
  );
  assert.deepEqual(target.snapshot(), initial);
});

test("successful apply returns the backend result and writes imported settings", async () => {
  const target = storage({
    [preferenceKeys.sidebarVisible]: "false",
    [preferenceKeys.lastStartupUpdateCheck]: "987654321",
    "sshbrowse.future.setting": "preserve",
  });
  const imported = captureBackupPreferences(storage({
    [preferenceKeys.sidebarVisible]: "true",
    [preferenceKeys.themeName]: "fjord",
  }));

  const result = await applyBackupPreferences(target, imported, async () => ({ added: 3, skipped: 1 }));
  assert.deepEqual(result, { added: 3, skipped: 1 });
  assert.equal(target.getItem(preferenceKeys.sidebarVisible), "true");
  assert.equal(target.getItem(preferenceKeys.themeName), "fjord");
  assert.equal(target.getItem(preferenceKeys.lastStartupUpdateCheck), "987654321");
  assert.equal(target.getItem("sshbrowse.future.setting"), "preserve");
});

test("null preferences leave storage untouched while still committing the import", async () => {
  const initial = { [preferenceKeys.sidebarVisible]: "false" };
  const target = storage(initial);
  const result = await applyBackupPreferences(target, null, async () => "committed");
  assert.equal(result, "committed");
  assert.deepEqual(target.snapshot(), initial);
});

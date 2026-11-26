import assert from "node:assert/strict";
import test from "node:test";
import { githubReleaseURL, canCheckForUpdates } from "../src/lib/updates.ts";

test("release links only open this repository's GitHub release pages", () => {
  const releaseURL = "https://github.com/sshbrowse/sshbrowse/releases/tag/v0.2.0";
  assert.equal(githubReleaseURL(releaseURL), releaseURL);
  for (const url of [undefined, "", "javascript:alert(1)", "https://example.invalid/releases/tag/v0.2.0", "https://github.com/other/repo/releases/tag/v0.2.0", "https://user@github.com/sshbrowse/sshbrowse/releases/tag/v0.2.0"]) {
    assert.equal(githubReleaseURL(url), "");
  }
});

test("only supported installations and package-manager releases check for updates", () => {
  assert.equal(canCheckForUpdates("supported"), true);
  assert.equal(canCheckForUpdates("package-manager"), true);
  for (const availability of [undefined, "development", "unsupported", "unavailable"]) assert.equal(canCheckForUpdates(availability), false);
});

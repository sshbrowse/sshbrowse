import assert from "node:assert/strict";
import test from "node:test";

import { contrastRatio, customThemeProperties, defaultCustomPalette } from "../src/lib/customPalette.ts";
import { terminalMinimumContrastRatio, terminalThemeFor } from "../src/lib/appearance.ts";

test("neutral terminal colors stay constant across interface themes", () => {
  const warm = terminalThemeFor("warm", "neutral");
  const fjord = terminalThemeFor("fjord", "neutral");
  assert.deepEqual(fjord, warm);
  assert.equal(warm.background, "#000000");
  assert.notEqual(terminalThemeFor("warm", "follow").background, warm.background);
  assert.equal(terminalThemeFor("fjord", "follow").background, "#0d191d");
});

test("terminal contrast keeps the high contrast protection", () => {
  assert.equal(terminalMinimumContrastRatio("contrast", "follow"), 7);
  assert.equal(terminalMinimumContrastRatio("contrast", "neutral"), 7);
  assert.equal(terminalMinimumContrastRatio("classic", "follow"), 4.5);
  assert.equal(terminalMinimumContrastRatio("classic", "neutral"), 4.5);
  assert.equal(terminalMinimumContrastRatio("moss", "follow"), 4.5);
  assert.equal(terminalThemeFor("moss", "follow").background, "#171d19");
  assert.equal(terminalThemeFor("moss", "follow").foreground, "#e6eadf");
});

test("Classic defines readable ANSI colors instead of xterm defaults", () => {
  const theme = terminalThemeFor("classic", "follow");
  assert.equal(theme.background, "#101418");
  for (const key of ["foreground", "red", "green", "yellow", "blue", "magenta", "cyan", "white", "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite"] as const) {
    assert.ok(contrastRatio(theme[key]!, theme.background!) >= 6, key);
  }
});

test("custom terminal colors remain readable on dark, light, and middle backgrounds", () => {
  for (const background of ["#101418", "#ffffff", "#808080", "#007777", "#ffcc88"]) {
    const palette = { ...defaultCustomPalette, surface: background, accent: background, terminal: background };
    const theme = terminalThemeFor("custom", "follow", palette);
    assert.equal(theme.background, background);
    for (const key of ["foreground", "cursor", "red", "green", "yellow", "blue", "magenta", "cyan", "white", "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite"] as const) {
      assert.ok(contrastRatio(theme[key]!, background) >= 4.5, `${background}: ${key}`);
    }
    const roles = customThemeProperties(palette);
    assert.ok(contrastRatio(roles["text-primary"], background) >= 4.5);
    assert.ok(contrastRatio(roles["text-secondary"], background) >= 4.5);
    assert.ok(contrastRatio(roles.accent, background) >= 3);
    assert.ok(contrastRatio(roles["status-error-foreground"], roles["status-error"]) >= 4.5);
    assert.ok(contrastRatio(roles["status-error-foreground"], roles["status-error-hover"]) >= 4.5);
    for (const role of ["surface-raised", "control-hover", "selection", "input-surface"]) {
      assert.ok(contrastRatio(roles["text-primary"], roles[role]) >= 4.5, `${background}: ${role}`);
      assert.ok(contrastRatio(roles["text-secondary"], roles[role]) >= 4.5, `${background}: muted ${role}`);
    }
    assert.deepEqual(terminalThemeFor("custom", "neutral", palette), terminalThemeFor("classic", "neutral"));
  }
});

import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";
import type { IBufferRange, ITerminalOptions, Terminal as TerminalType } from "@xterm/xterm";
import type { ISearchResultChangeEvent } from "@xterm/addon-search";

import {
  SearchSelectionCopyGuard,
  terminalSearchCommandFor,
  terminalSearchHighlightLimit,
  terminalSearchRefreshDelay,
  terminalSearchResultLabel,
  terminalSearchBufferIsSafe,
  terminalSearchView,
  TerminalSearchSession,
} from "../src/lib/terminalSearch.ts";

const { Terminal } = createRequire(import.meta.url)("@xterm/xterm") as { Terminal: typeof TerminalType };

function searchTerminal(options: ITerminalOptions = {}) {
  const terminal = new Terminal({ cols: 80, rows: 5, scrollback: 50000, allowProposedApi: true, ...options });
  const guard = new SearchSelectionCopyGuard();
  let selection: IBufferRange | undefined;
  // The real buffer and addon run without opening a DOM renderer. Stub only
  // public selection methods, mirroring xterm's browser selection service.
  terminal.select = (x, y, length) => {
    if (!guard.preservingSelection) {
      guard.run(() => {
        selection = {
          start: { x, y },
          end: { x: (x + length) % terminal.cols, y: y + Math.floor((x + length) / terminal.cols) },
        };
      });
    }
  };
  terminal.getSelectionPosition = () => selection;
  terminal.hasSelection = () => selection !== undefined;
  terminal.clearSelection = () => {
    if (!guard.preservingSelection) {
      selection = undefined;
    }
  };
  terminal.getSelection = () => {
    if (!selection) return "";
    let text = "";
    for (let row = selection.start.y; row <= selection.end.y; row++) {
      const start = row === selection.start.y ? selection.start.x : 0;
      const end = row === selection.end.y ? selection.end.x : terminal.cols;
      const line = terminal.buffer.active.getLine(row);
      if (row > selection.start.y && !line?.isWrapped) text += "\n";
      text += line?.translateToString(false, start, end) ?? "";
    }
    return text;
  };
  const results: ISearchResultChangeEvent[] = [];
  const errors: string[] = [];
  let queryFocused = true;
  const session = new TerminalSearchSession(terminal, guard, {
    onResults: (result) => results.push(result),
    onError: (message) => errors.push(message),
    isQueryFocused: () => queryFocused,
  });
  return {
    terminal,
    session,
    results,
    errors,
    blurQuery() { queryFocused = false; },
    focusQuery() { queryFocused = true; session.refresh(); },
    write(text: string) { return new Promise<void>((resolve) => terminal.write(text, resolve)); },
    dispose() { session.dispose(); terminal.dispose(); },
  };
}

function waitForRefresh() {
  return new Promise((resolve) => setTimeout(resolve, terminalSearchRefreshDelay + 50));
}

function keyEvent(overrides: Partial<KeyboardEvent> = {}) {
  return {
    key: "Enter",
    shiftKey: false,
    ctrlKey: false,
    altKey: false,
    metaKey: false,
    isComposing: false,
    keyCode: 13,
    ...overrides,
  };
}

test("find input uses Enter and Shift+Enter to navigate and Escape to close", () => {
  assert.equal(terminalSearchCommandFor(keyEvent()), "next");
  assert.equal(terminalSearchCommandFor(keyEvent({ shiftKey: true })), "previous");
  assert.equal(terminalSearchCommandFor(keyEvent({ key: "Escape" })), "close");
  assert.equal(terminalSearchCommandFor(keyEvent({ key: "f" })), null);
});

test("find input leaves clipboard modifiers and IME confirmation alone", () => {
  for (const modifier of ["ctrlKey", "altKey", "metaKey", "isComposing"] as const) {
    assert.equal(terminalSearchCommandFor(keyEvent({ [modifier]: true })), null);
  }
  assert.equal(terminalSearchCommandFor(keyEvent({ keyCode: 229 })), null);
  for (const key of ["c", "v", "x", "a"]) {
    assert.equal(terminalSearchCommandFor(keyEvent({ key, metaKey: true })), null);
    assert.equal(terminalSearchCommandFor(keyEvent({ key, ctrlKey: true })), null);
  }
});

test("find feedback reports no matches, selected matches, and a capped count", () => {
  assert.equal(terminalSearchResultLabel("", { resultIndex: -1, resultCount: 0 }), "");
  assert.equal(terminalSearchResultLabel("needle", { resultIndex: -1, resultCount: 0 }), "No matches");
  assert.equal(terminalSearchResultLabel("needle", { resultIndex: 2, resultCount: 5 }), "3 of 5");
  assert.equal(terminalSearchResultLabel("needle", { resultIndex: -1, resultCount: 5 }), "5 matches");
  for (const resultIndex of [-1, 0, terminalSearchHighlightLimit - 1]) {
    assert.equal(terminalSearchResultLabel("needle", { resultIndex, resultCount: terminalSearchHighlightLimit }), "1000+ matches");
  }
});

test("search selections do not copy, including a delayed addon refresh", async () => {
  const guard = new SearchSelectionCopyGuard();
  const copied: string[] = [];
  const selectionChanged = (selection: string) => {
    if (!guard.active) {
      copied.push(selection);
    }
  };
  const addonSelect = (selection: string) => guard.run(() => selectionChanged(selection));

  addonSelect("first search result");
  await Promise.resolve().then(() => addonSelect("refreshed search result"));
  selectionChanged("ordinary mouse selection");
  selectionChanged("ordinary select-all");

  assert.deepEqual(copied, ["ordinary mouse selection", "ordinary select-all"]);
});

test("selection suppression ends even if selecting fails", () => {
  const guard = new SearchSelectionCopyGuard();
  assert.throws(() => guard.run(() => {
    assert.equal(guard.active, true);
    guard.run(() => assert.equal(guard.active, true));
    assert.equal(guard.active, true);
    throw new Error("selection failed");
  }), /selection failed/);
  assert.equal(guard.active, false);
});

test("actual addon refresh sees an overwritten line even when the cursor ends in the same place", async () => {
  const search = searchTerminal();
  try {
    await search.write("foo");
    assert.equal(search.session.find("foo", false, "next", true), true);
    assert.equal(search.results.at(-1)?.resultCount, 1);
    await search.write("\rbar");
    await waitForRefresh();
    assert.equal(search.results.at(-1)?.resultCount, 0);
    assert.equal(search.terminal.hasSelection(), false);
    assert.equal(search.session.find("foo", false, "next"), false);
  } finally {
    search.dispose();
  }
});

test("unfocused search pauses refreshes and catches up on focus without moving the viewport", async () => {
  const search = searchTerminal({ rows: 3 });
  try {
    await search.write("needle\r\nmanual\r\n" + "other\r\n".repeat(10));
    assert.equal(search.session.find("needle", false, "next", true), true);
    search.blurQuery();
    search.terminal.select(0, 1, 6);
    const selection = search.terminal.getSelectionPosition();
    await search.write("needle\r\n");
    // Scroll after scheduling the refresh; it must not restore an older viewport.
    search.terminal.scrollToLine(3);
    await waitForRefresh();
    assert.equal(search.terminal.buffer.active.viewportY, 3);
    assert.deepEqual(search.terminal.getSelectionPosition(), selection);
    assert.equal(search.terminal.getSelection(), "manual");
    assert.equal(search.results.at(-1)?.resultCount, 1, "background output does not rescan history");
    search.focusQuery();
    assert.equal(search.terminal.buffer.active.viewportY, 3);
    assert.equal(search.results.at(-1)?.resultCount, 2);
  } finally {
    search.dispose();
  }
});

test("refocusing clean search results preserves the match and leaves Next available", async () => {
  const search = searchTerminal();
  try {
    await search.write("foo foo foo");
    search.session.find("foo", false, "next", true);
    const selection = search.terminal.getSelectionPosition();
    const resultCount = search.results.length;
    for (let attempt = 0; attempt < 3; attempt++) {
      search.blurQuery();
      search.focusQuery();
      assert.deepEqual(search.terminal.getSelectionPosition(), selection);
    }
    assert.equal(search.results.length, resultCount, "clean results do not need another scan");
    search.session.find("foo", false, "next");
    assert.equal(search.terminal.getSelectionPosition()?.start.x, 4);
  } finally {
    search.dispose();
  }
});

test("refocusing dirty results refreshes the count without advancing an unchanged match", async () => {
  const search = searchTerminal();
  try {
    await search.write("foo foo");
    search.session.find("foo", false, "next", true);
    search.session.find("foo", false, "next");
    const selection = search.terminal.getSelectionPosition();
    search.blurQuery();
    await search.write(" foo");
    search.focusQuery();
    assert.deepEqual(search.terminal.getSelectionPosition(), selection);
    assert.equal(search.results.at(-1)?.resultCount, 3);
  } finally {
    search.dispose();
  }
});

test("Next and Previous advance from an unchanged match after dirty cache rebuild", async () => {
  const search = searchTerminal();
  try {
    await search.write("foo foo foo");
    search.session.find("foo", false, "next", true);
    assert.equal(search.terminal.getSelectionPosition()?.start.x, 0);
    await search.write("\rfoo foo foo");
    search.session.find("foo", false, "next");
    assert.equal(search.terminal.getSelectionPosition()?.start.x, 4);
    await search.write("\rfoo foo foo");
    search.session.find("foo", false, "previous");
    assert.equal(search.terminal.getSelectionPosition()?.start.x, 0);
    const resultCount = search.results.length;
    await waitForRefresh();
    assert.equal(search.results.length, resultCount, "explicit navigation cancels the pending refresh");
  } finally {
    search.dispose();
  }
});

test("Next does not skip a replacement when the previously selected match was overwritten", async () => {
  const search = searchTerminal();
  try {
    await search.write("foo foo foo");
    search.session.find("foo", false, "next", true);
    await search.write("\rbar foo foo");
    search.session.find("foo", false, "next");
    assert.equal(search.terminal.getSelectionPosition()?.start.x, 4);
  } finally {
    search.dispose();
  }
});

test("Previous searches before the original anchor when its match was overwritten", async () => {
  const search = searchTerminal();
  try {
    await search.write("foo xxx foo foo");
    search.session.find("foo", false, "next", true);
    search.session.find("foo", false, "next");
    assert.equal(search.terminal.getSelectionPosition()?.start.x, 8);
    await search.write("\rfoo xxx zzz foo");
    search.session.find("foo", false, "previous");
    assert.equal(search.terminal.getSelectionPosition()?.start.x, 0);
  } finally {
    search.dispose();
  }
});

test("disposing search cancels delayed refresh without changing ordinary selection", async () => {
  const search = searchTerminal();
  try {
    await search.write("foo manual");
    search.session.find("foo", false, "next", true);
    await search.write("\rfoo manual");
    search.session.dispose();
    search.terminal.select(4, 0, 6);
    const resultsBeforeClose = search.results.length;
    await waitForRefresh();
    assert.equal(search.results.length, resultsBeforeClose);
    assert.equal(search.terminal.getSelection(), "manual");
  } finally {
    search.dispose();
  }
});

test("scrollback trimming invalidates matches through public APIs", async () => {
  const search = searchTerminal({ rows: 3 });
  try {
    await search.write("needle\r\n" + "other\r\n".repeat(10));
    search.session.find("needle", false, "next", true);
    assert.equal(search.results.at(-1)?.resultCount, 1);
    search.terminal.options.scrollback = 0;
    search.session.invalidate();
    await waitForRefresh();
    assert.equal(search.results.at(-1)?.resultCount, 0);
  } finally {
    search.dispose();
  }
});

test("large wrapped output fails before entering the addon and recovers after history is removed", async () => {
  const search = searchTerminal({ cols: 2 });
  try {
    await search.write("a".repeat(3000));
    assert.equal(terminalSearchBufferIsSafe(search.terminal), false);
    assert.equal(search.session.find("z", false, "next", true), false);
    assert.equal(search.errors.at(-1), "Search unavailable: wrapped output is too large.");
    assert.equal(search.results.at(-1)?.resultCount, 0);
    search.terminal.options.scrollback = 0;
    search.session.invalidate();
    await waitForRefresh();
    assert.equal(terminalSearchBufferIsSafe(search.terminal), true);
    assert.equal(search.errors.at(-1), "");
    assert.equal(search.results.at(-1)?.resultCount, 0);
  } finally {
    search.dispose();
  }
});

test("wrapped work budget allows fifty thousand flat rows even in a wide terminal", async () => {
  const search = searchTerminal({ cols: 300, rows: 2 });
  try {
    await search.write("x\r\n".repeat(50000));
    assert.ok(search.terminal.buffer.active.length >= 50000);
    assert.equal(terminalSearchBufferIsSafe(search.terminal), true);
    assert.equal(search.session.find("missing", false, "next", true), false);
    assert.equal(search.errors.at(-1), "");
  } finally {
    search.dispose();
  }
});

test("wrapped-line guard accepts its row boundary and rejects a longer run", async () => {
  const search = searchTerminal({ cols: 2 });
  try {
    await search.write("a".repeat(2 * 256));
    assert.equal(terminalSearchBufferIsSafe(search.terminal), true);
    await search.write("aa");
    assert.equal(terminalSearchBufferIsSafe(search.terminal), false);
  } finally {
    search.dispose();
  }
});

test("wrapped work budget also bounds several individually permitted runs", async () => {
  const search = searchTerminal();
  try {
    await search.write(("a".repeat(80 * 255) + "\r\n").repeat(4));
    assert.equal(terminalSearchBufferIsSafe(search.terminal), false);
    assert.equal(search.session.find("missing", false, "next", true), false);
    assert.equal(search.errors.at(-1), "Search unavailable: wrapped output is too large.");
  } finally {
    search.dispose();
  }
});

test("invalidations share one refresh deadline instead of postponing it", async (context) => {
  const search = searchTerminal();
  try {
    await search.write("foo");
    context.mock.timers.enable({ apis: ["setTimeout"] });
    search.session.find("foo", false, "next", true);
    const initialResults = search.results.length;
    search.session.invalidate();
    context.mock.timers.tick(terminalSearchRefreshDelay / 4);
    search.session.invalidate();
    context.mock.timers.tick(terminalSearchRefreshDelay / 4);
    search.session.invalidate();
    context.mock.timers.tick(terminalSearchRefreshDelay / 2);
    assert.equal(search.results.length, initialResults + 1);
    context.mock.timers.tick(terminalSearchRefreshDelay);
    assert.equal(search.results.length, initialResults + 1);
  } finally {
    search.dispose();
  }
});

test("continuous output limits focused refreshes to one scan per second", async (context) => {
  const search = searchTerminal();
  try {
    await search.write("foo");
    search.session.find("foo", false, "next", true);
    const initialResults = search.results.length;
    context.mock.timers.enable({ apis: ["setTimeout"] });
    for (let elapsed = 0; elapsed < 3000; elapsed += 20) {
      search.session.invalidate();
      context.mock.timers.tick(20);
      assert.equal(search.results.length - initialResults, Math.floor((elapsed + 20) / 1000));
    }
  } finally {
    search.dispose();
  }
});

test("a pending refresh does no work after search loses focus", async (context) => {
  const search = searchTerminal();
  try {
    await search.write("foo");
    search.session.find("foo", false, "next", true);
    const initialResults = search.results.length;
    context.mock.timers.enable({ apis: ["setTimeout"] });
    search.session.invalidate();
    search.blurQuery();
    context.mock.timers.tick(terminalSearchRefreshDelay);
    for (let update = 0; update < 5; update++) {
      search.session.invalidate();
      context.mock.timers.tick(terminalSearchRefreshDelay);
    }
    assert.equal(search.results.length, initialResults);
    search.focusQuery();
    assert.equal(search.results.length, initialResults + 1);
    context.mock.timers.tick(terminalSearchRefreshDelay);
    assert.equal(search.results.length, initialResults + 1);
  } finally {
    search.dispose();
  }
});

test("unexpected addon failure reports a bounded error and pauses automatic retries", async () => {
  const search = searchTerminal();
  try {
    await search.write("foo");
    const loadAddon = search.terminal.loadAddon;
    search.terminal.loadAddon = () => { throw new Error("fixture failure"); };
    assert.equal(search.session.find("foo", false, "next", true), false);
    assert.equal(search.errors.at(-1), "Could not search terminal output.");
    assert.equal(search.results.at(-1)?.resultCount, 0);
    const initialResults = search.results.length;
    search.terminal.loadAddon = loadAddon;
    await search.write("\rfoo");
    await waitForRefresh();
    assert.equal(search.results.length, initialResults);
    assert.equal(search.session.find("foo", false, "next", true), true);
    assert.equal(search.errors.at(-1), "");
  } finally {
    search.dispose();
  }
});

test("search finds retained wrapped output with zero scrollback without changing the real first row", async () => {
  const search = searchTerminal({ cols: 80, rows: 24, scrollback: 0 });
  try {
    await search.write("a".repeat(80 * 30) + "foo");
    assert.equal(search.terminal.buffer.active.getLine(0)?.isWrapped, true);
    assert.equal(search.session.find("foo", false, "next", true), true);
    assert.equal(search.results.at(-1)?.resultCount, 1);
    assert.equal(search.terminal.getSelection(), "foo");
    assert.equal(search.terminal.buffer.active.getLine(0)?.isWrapped, true);
  } finally {
    search.dispose();
  }
});

test("search follows a trimmed wrapped head across retained rows, including wide cells", async () => {
  const search = searchTerminal({ cols: 8, rows: 3, scrollback: 0 });
  try {
    await search.write("a".repeat(8 * 6) + "abc中文def" + "b".repeat(7));
    assert.equal(search.terminal.buffer.active.getLine(0)?.isWrapped, true);
    assert.equal(search.session.find("abc中文def", false, "next", true), true);
    assert.equal(search.terminal.getSelection(), "abc中文def");
    assert.deepEqual(search.terminal.getSelectionPosition(), { start: { x: 0, y: 0 }, end: { x: 2, y: 1 } });
    assert.equal(search.session.find("中文", false, "next", true), true);
    assert.deepEqual(search.terminal.getSelectionPosition(), { start: { x: 3, y: 0 }, end: { x: 7, y: 0 } });
    assert.equal(search.terminal.buffer.active.getLine(0)?.isWrapped, true);
  } finally {
    search.dispose();
  }
});

test("search buffer view respects retained bounds and follows normal and alternate buffers", async () => {
  const search = searchTerminal({ cols: 8, rows: 3, scrollback: 0 });
  try {
    await search.write("a".repeat(8 * 6) + "abc中文def" + "b".repeat(7));
    const view = terminalSearchView(search.terminal);
    const normal = view.buffer.active;
    assert.equal(normal.getLine(-1), undefined);
    assert.equal(normal.getLine(normal.length), undefined);
    assert.equal(normal.getLine(normal.length + 1), undefined);
    assert.equal(normal.getLine(0)?.isWrapped, false);
    assert.equal(search.terminal.buffer.active.getLine(0)?.isWrapped, true);
    await search.write("\r\nordinary");
    assert.equal(search.session.find("ordinary", false, "next", true), true);
    await search.write("\x1b[?1049hALT中文");
    assert.equal(view.buffer.active.type, "alternate");
    assert.notEqual(view.buffer.active, normal);
    assert.equal(search.session.find("ALT中文", false, "next", true), true);
    await search.write("\x1b[?1049l");
    assert.equal(view.buffer.active.type, "normal");
    assert.equal(view.buffer.active, normal);
    assert.equal(search.session.find("ordinary", false, "next", true), true);
  } finally {
    search.dispose();
  }
});

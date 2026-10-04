import { SearchAddon, type ISearchOptions, type ISearchResultChangeEvent } from "@xterm/addon-search";
import type { IBuffer, IDisposable, Terminal } from "@xterm/xterm";

export const terminalSearchHighlightLimit = 1000;
export const terminalSearchQueryLimit = 1024;
// Full-buffer scans are synchronous; limit automatic rebuilds to once a second.
export const terminalSearchRefreshDelay = 1000;
const terminalSearchWrappedLineLimit = 256;
const terminalSearchScannedCellLimit = 8 * 1024 * 1024;

function delegatedProperty(target: object, property: string | symbol): unknown {
  const value = Reflect.get(target, property, target);
  return typeof value === "function" ? value.bind(target) : value;
}

export function terminalSearchView(terminal: Terminal): Terminal {
  const buffers = new WeakMap<IBuffer, IBuffer>();
  function bufferView(buffer: IBuffer): IBuffer {
    let view = buffers.get(buffer);
    if (view) {
      return view;
    }
    view = new Proxy(buffer, {
      get(target, property) {
        if (property === "getLine") {
          return (row: number) => {
            // xterm's circular list can expose stale rows outside the retained
            // range. The addon probes adjacent lines, so these checks are essential.
            if (row < 0 || row >= target.length) {
              return undefined;
            }
            const line = target.getLine(row);
            if (row !== 0 || !line?.isWrapped) {
              return line;
            }
            // The start of a wrapped line may already have been discarded.
            // Search the retained tail as a new line without changing xterm's buffer.
            return new Proxy(line, {
              get(lineTarget, lineProperty) {
                return lineProperty === "isWrapped" ? false : delegatedProperty(lineTarget, lineProperty);
              },
            });
          };
        }
        return delegatedProperty(target, property);
      },
    });
    buffers.set(buffer, view);
    return view;
  }
  const namespace = new Proxy(terminal.buffer, {
    get(target, property) {
      return property === "active" ? bufferView(target.active) : delegatedProperty(target, property);
    },
  });
  return new Proxy(terminal, {
    get(target, property) {
      return property === "buffer" ? namespace : delegatedProperty(target, property);
    },
  });
}

class RetainedBufferSearchAddon extends SearchAddon {
  override activate(terminal: Terminal): void {
    super.activate(terminalSearchView(terminal));
  }
}

export function terminalSearchBufferIsSafe(terminal: Pick<Terminal, "buffer" | "cols">): boolean {
  const buffer = terminal.buffer.active;
  let wrappedRows = 0;
  let scannedCells = 0;
  for (let row = 0; row < buffer.length; row++) {
    if (!buffer.getLine(row)?.isWrapped) {
      wrappedRows = 0;
    }
    wrappedRows++;
    // The addon recursively rescans wrapped continuations, giving quadratic
    // work. Bound that repeated work; ordinary unwrapped rows add no overhead.
    scannedCells += terminal.cols * (wrappedRows - 1);
    if (wrappedRows > terminalSearchWrappedLineLimit || scannedCells > terminalSearchScannedCellLimit) {
      return false;
    }
  }
  return true;
}

export function terminalSearchResultLabel(query: string, result: ISearchResultChangeEvent): string {
  if (query.length === 0) {
    return "";
  }
  if (result.resultCount === 0) {
    return "No matches";
  }
  // The addon stops counting at the decoration limit, so this is a lower bound.
  if (result.resultCount >= terminalSearchHighlightLimit) {
    return `${terminalSearchHighlightLimit}+ matches`;
  }
  if (result.resultIndex < 0) {
    return `${result.resultCount} matches`;
  }
  return `${result.resultIndex + 1} of ${result.resultCount}`;
}

export type TerminalSearchCommand = "next" | "previous" | "close";

export function terminalSearchCommandFor(event: Pick<KeyboardEvent,
  "key" | "shiftKey" | "ctrlKey" | "altKey" | "metaKey" | "isComposing" | "keyCode"
>): TerminalSearchCommand | null {
  if (event.isComposing || event.keyCode === 229 || event.ctrlKey || event.altKey || event.metaKey) {
    return null;
  }
  if (event.key === "Escape") {
    return "close";
  }
  if (event.key === "Enter") {
    return event.shiftKey ? "previous" : "next";
  }
  return null;
}

export class SearchSelectionCopyGuard {
  private depth = 0;
  private preservedSelectionDepth = 0;

  get active(): boolean {
    return this.depth > 0;
  }

  get preservingSelection(): boolean {
    return this.preservedSelectionDepth > 0;
  }

  preserveSelection<T>(search: () => T): T {
    this.preservedSelectionDepth++;
    try {
      return this.run(search);
    } finally {
      this.preservedSelectionDepth--;
    }
  }

  run<T>(select: () => T): T {
    this.depth++;
    try {
      return select();
    } finally {
      this.depth--;
    }
  }
}

interface TerminalSearchCallbacks {
  onResults: (result: ISearchResultChangeEvent) => void;
  onError: (message: string) => void;
  isQueryFocused: () => boolean;
}

export class TerminalSearchSession {
  private readonly terminal: Terminal;
  private readonly selectionGuard: SearchSelectionCopyGuard;
  private readonly callbacks: TerminalSearchCallbacks;
  private addon: SearchAddon | undefined;
  private listeners: IDisposable[];
  private refreshTimer: ReturnType<typeof setTimeout> | undefined;
  private query = "";
  private caseSensitive = false;
  private dirty = false;
  private disposed = false;
  private failure: "wrapped" | "addon" | null = null;
  private bufferSafetyKnown = false;
  private result: ISearchResultChangeEvent = { resultIndex: -1, resultCount: 0 };

  constructor(
    terminal: Terminal,
    selectionGuard: SearchSelectionCopyGuard,
    callbacks: TerminalSearchCallbacks,
  ) {
    this.terminal = terminal;
    this.selectionGuard = selectionGuard;
    this.callbacks = callbacks;
    // Register before loading the addon so its own output refresh is cancelled
    // before it can reuse stale cached lines or replace a manual selection.
    this.listeners = [
      terminal.onWriteParsed(() => this.invalidate()),
      terminal.onResize(() => this.invalidate()),
    ];
  }

  find(query: string, caseSensitive: boolean, direction: "next" | "previous", incremental = false): boolean {
    if (this.disposed) {
      return false;
    }
    this.cancelRefresh();
    if (query !== this.query || caseSensitive !== this.caseSensitive) {
      this.addon?.clearDecorations();
    }
    this.query = query.slice(0, terminalSearchQueryLimit);
    this.caseSensitive = caseSensitive;
    this.failure = null;
    this.callbacks.onError("");
    return this.findMatch(direction, incremental);
  }

  invalidate(): void {
    if (this.disposed) {
      return;
    }
    this.dirty = true;
    this.bufferSafetyKnown = false;
    this.addon?.dispose();
    this.addon = undefined;
    if (this.query === "" || this.failure === "addon" || this.refreshTimer !== undefined
      || !this.callbacks.isQueryFocused()) {
      return;
    }
    this.refreshTimer = setTimeout(() => {
      this.refreshTimer = undefined;
      this.refresh();
    }, terminalSearchRefreshDelay);
  }

  refresh(): void {
    if (this.disposed || !this.dirty || this.query === "" || this.failure === "addon"
      || !this.callbacks.isQueryFocused()) {
      return;
    }
    this.cancelRefresh();
    // Capture the viewport after any intervening user scroll. A fresh addon
    // starts from the current selection, so a refresh does not advance a match.
    const viewport = this.terminal.buffer.active.viewportY;
    try {
      this.findMatch("next", true);
    } finally {
      this.terminal.scrollToLine(viewport);
    }
  }

  clearActiveDecoration(): void {
    this.cancelRefresh();
    this.addon?.clearActiveDecoration();
    this.result = { ...this.result, resultIndex: -1 };
    this.callbacks.onResults(this.result);
  }

  dispose(): void {
    this.disposed = true;
    this.cancelRefresh();
    for (const listener of this.listeners) {
      listener.dispose();
    }
    this.addon?.dispose();
    this.addon = undefined;
  }

  private cancelRefresh(): void {
    if (this.refreshTimer !== undefined) {
      clearTimeout(this.refreshTimer);
      this.refreshTimer = undefined;
    }
  }

  private findMatch(direction: "next" | "previous", incremental: boolean): boolean {
    try {
      if (!this.bufferSafetyKnown) {
        if (!terminalSearchBufferIsSafe(this.terminal)) {
          this.fail("wrapped", "Search unavailable: wrapped output is too large.");
          return false;
        }
        this.bufferSafetyKnown = true;
        if (this.failure === "wrapped") {
          this.failure = null;
          this.callbacks.onError("");
        }
      }
      const rebuild = this.addon === undefined;
      if (rebuild) {
        this.addon = new RetainedBufferSearchAddon({ highlightLimit: terminalSearchHighlightLimit });
        this.terminal.loadAddon(this.addon);
        this.addon.onDidChangeResults((result) => {
          this.result = this.selectionGuard.preservingSelection ? { ...result, resultIndex: -1 } : result;
          this.callbacks.onResults(this.result);
        });
      }
      const options: ISearchOptions = {
        caseSensitive: this.caseSensitive,
        incremental,
        decorations: {
          matchBackground: "#665521",
          matchBorder: "#d4b44b",
          matchOverviewRuler: "#d4b44b",
          activeMatchBackground: "#886526",
          activeMatchBorder: "#ffdf80",
          activeMatchColorOverviewRuler: "#ffdf80",
        },
      };
      if (rebuild && this.dirty && !incremental && this.terminal.hasSelection()) {
        // A fresh addon expands to the right before its first reverse search.
        // Prime the query without moving the original anchor for Previous.
        if (direction === "previous") {
          this.selectionGuard.preserveSelection(() => this.addon!.findNext(this.query, { ...options, incremental: true }));
        } else if (this.selectionMatchesQuery()) {
          // Next advances an unchanged match, but must not skip a replacement
          // when the previously selected text was overwritten.
          this.addon!.findNext(this.query, { ...options, incremental: true });
        }
      }
      this.dirty = false;
      return direction === "previous"
        ? this.addon!.findPrevious(this.query, options)
        : this.addon!.findNext(this.query, options);
    } catch {
      this.fail("addon", "Could not search terminal output.");
      return false;
    }
  }

  private fail(reason: "wrapped" | "addon", message: string): void {
    this.addon?.dispose();
    this.addon = undefined;
    this.failure = reason;
    this.cancelRefresh();
    this.result = { resultIndex: -1, resultCount: 0 };
    this.callbacks.onResults(this.result);
    this.callbacks.onError(message);
  }

  private selectionMatchesQuery(): boolean {
    const selection = this.terminal.getSelection();
    return this.query !== "" && (this.caseSensitive
      ? selection === this.query
      : selection.toLowerCase() === this.query.toLowerCase());
  }
}

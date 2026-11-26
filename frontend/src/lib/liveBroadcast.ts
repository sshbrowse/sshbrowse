import type { BroadcastSnapshot } from "./routing";
import { sessionIds, type Session, type Tab } from "./tabs.ts";

export const maximumLiveBroadcastQueuedBytes = 64 * 1024;
export const defaultLiveBroadcastWriteTimeoutMs = 10_000;

export interface LiveBroadcastState {
  snapshot: BroadcastSnapshot[];
}

export interface LiveBroadcastWriter {
  enqueue(input: string): boolean;
  enqueueTo(processInstanceId: number, input: string): boolean;
  writePasteLine(input: string, signal: AbortSignal, onWrite?: (processInstanceId: number, pending: Promise<void>) => void): Promise<boolean>;
  stop(reason?: string): void;
  isStopped(): boolean;
  pendingBytes(): number;
}

export function liveBroadcastStartError(
  snapshot: readonly BroadcastSnapshot[],
  focusedSessionId: number | null,
): string | null {
  if (snapshot.length < 2) {
    return "Live input needs at least two live terminals in this tab.";
  }
  if (focusedSessionId === null || !snapshot.some((target) => target.logicalSessionId === focusedSessionId)) {
    return "Focus a live terminal in this tab to start live input.";
  }
  return null;
}

export function liveBroadcastSnapshotInvalidationReason(
  snapshot: readonly BroadcastSnapshot[],
  sessions: readonly Pick<Session, "id" | "status" | "processInstanceId">[],
  focusedSessionId: number | null,
): string | null {
  const sessionsById = new Map(sessions.map((session) => [session.id, session]));
  for (const target of snapshot) {
    const session = sessionsById.get(target.logicalSessionId);
    if (session === undefined) {
      return `Live broadcast stopped: ${target.label} disappeared.`;
    }
    if (session.status !== "live") {
      return `Live broadcast stopped: ${target.label} is no longer live.`;
    }
    if (session.processInstanceId !== target.processInstanceId) {
      return `Live broadcast stopped: ${target.label} changed process instances.`;
    }
  }
  if (focusedSessionId === null || !snapshot.some((target) => target.logicalSessionId === focusedSessionId)) {
    return "Live broadcast stopped: focus moved outside the broadcast snapshot.";
  }
  return null;
}

// Selection and tab order do not change a broadcast's layout context.
export function liveBroadcastLayoutInvalidationReason(
  activeTabId: number | null,
  previousTabs: readonly Tab[],
  nextTabs: readonly Tab[],
): string | null {
  const previous = previousTabs.find((tab) => tab.id === activeTabId);
  const next = nextTabs.find((tab) => tab.id === activeTabId);
  if (previous !== undefined && next !== undefined && previous.kind === next.kind) {
    const previousIds = sessionIds(previous);
    const nextIds = sessionIds(next);
    if (previousIds.length === nextIds.length && previousIds.every((id) => nextIds.includes(id))) {
      return null;
    }
  }
  return "Live broadcast stopped because the terminal layout changed. Start a new broadcast to continue.";
}

export function liveBroadcastPasteError(
  snapshot: readonly BroadcastSnapshot[] | null,
  sessions: readonly Pick<Session, "id" | "status" | "processInstanceId">[],
  focusedSessionId: number | null,
  sessionId: number,
  processInstanceId: number | null,
  recipientCount: number,
): string | null {
  if (snapshot === null) {
    return "The live broadcast changed before the paste was confirmed. Nothing was pasted.";
  }
  const target = snapshot.find((candidate) => candidate.logicalSessionId === sessionId);
  if (target === undefined || target.processInstanceId !== processInstanceId || snapshot.length !== recipientCount) {
    return "The live broadcast changed before the paste was confirmed. Nothing was pasted.";
  }
  const invalidationReason = liveBroadcastSnapshotInvalidationReason(snapshot, sessions, focusedSessionId);
  return invalidationReason === null ? null : `${invalidationReason} Nothing was pasted.`;
}

export function liveBroadcastPasteGenerationError(
  capturedGeneration: number,
  currentGeneration: number,
): string | null {
  return capturedGeneration === currentGeneration
    ? null
    : "The live broadcast changed before the paste was confirmed. Nothing was pasted.";
}

export interface LiveBroadcastPasteState {
  generation: number;
  recipientCount: number | null;
}

export async function pasteClipboardForBroadcastGeneration(
  readClipboard: () => Promise<string>,
  currentState: () => LiveBroadcastPasteState,
  paste: (text: string, capturedState: LiveBroadcastPasteState) => void,
  rejectStale: (capturedState: LiveBroadcastPasteState, currentState: LiveBroadcastPasteState) => void,
): Promise<void> {
  const capturedState = currentState();
  const text = await readClipboard();
  const latestState = currentState();
  if (capturedState.generation !== latestState.generation) {
    rejectStale(capturedState, latestState);
    return;
  }
  paste(text, capturedState);
}

export interface LiveBroadcastWriterOptions {
  maximumQueuedBytes?: number;
  writeTimeoutMs?: number;
}

export function createLiveBroadcastWriter(
  snapshot: readonly BroadcastSnapshot[],
  write: (processInstanceId: number, input: string) => Promise<void>,
  onStop: (reason: string) => void,
  options: LiveBroadcastWriterOptions = {},
): LiveBroadcastWriter {
  const maximumQueuedBytes = options.maximumQueuedBytes ?? maximumLiveBroadcastQueuedBytes;
  const writeTimeoutMs = options.writeTimeoutMs ?? defaultLiveBroadcastWriteTimeoutMs;
  if (!Number.isFinite(maximumQueuedBytes) || maximumQueuedBytes <= 0) {
    throw new RangeError("Live broadcast queue limit must be finite and positive.");
  }
  if (!Number.isFinite(writeTimeoutMs) || writeTimeoutMs <= 0) {
    throw new RangeError("Live broadcast write timeout must be finite and positive.");
  }

  const targets = snapshot.map((target) => ({ ...target }));
  interface PasteLine {
    signal: AbortSignal;
    onWrite?: (processInstanceId: number, pending: Promise<void>) => void;
    complete: (written: boolean) => void;
  }
  interface QueuedInput {
    input: string;
    bytes: number;
    processInstanceId?: number;
    paste?: PasteLine;
  }
  const queue: QueuedInput[] = [];
  let queuedBytes = 0;
  let stopped = false;
  let draining = false;
  let drainScheduled = false;
  let activeTimeoutHandle: ReturnType<typeof setTimeout> | undefined;

  function stop(reason?: string) {
    if (stopped) {
      return;
    }
    stopped = true;
    for (const next of queue) next.paste?.complete(false);
    queue.length = 0;
    queuedBytes = 0;
    if (reason !== undefined) {
      onStop(reason);
    }
  }

  async function writeChunk(next: QueuedInput): Promise<boolean> {
    const targetIndexes = next.processInstanceId === undefined
      ? targets.map((_target, index) => index)
      : targets.flatMap((target, index) => target.processInstanceId === next.processInstanceId ? [index] : []);
    if (targetIndexes.length === 0) {
      throw new Error(`Live broadcast source process ${next.processInstanceId} is no longer in the snapshot.`);
    }
    const timeout = new Promise<never>((_, reject) => {
      activeTimeoutHandle = setTimeout(() => reject(new Error("target write timed out")), writeTimeoutMs);
    });
    let wrote = false;
    const results = await Promise.allSettled(targetIndexes.map(async (targetIndex) => {
      const target = targets[targetIndex];
      await Promise.race([
        // stop() also cancels writes scheduled but not yet issued to the backend.
        Promise.resolve().then(() => {
          if (stopped || next.paste?.signal.aborted) return;
          const pending = write(target.processInstanceId, next.input);
          next.paste?.onWrite?.(target.processInstanceId, pending);
          wrote = true;
          return pending;
        }),
        timeout,
      ]);
    }));
    if (activeTimeoutHandle !== undefined) {
      clearTimeout(activeTimeoutHandle);
      activeTimeoutHandle = undefined;
    }
    const failureIndex = results.findIndex((result) => result.status === "rejected");
    const failure = results[failureIndex];
    if (failure?.status === "rejected") {
      const message = failure.reason instanceof Error ? failure.reason.message : String(failure.reason);
      const failedTarget = targets[targetIndexes[failureIndex]];
      throw new Error(`Live broadcast stopped because a write to ${failedTarget.label} failed: ${message}. Some targets may have received input before the failure and may have diverged.`);
    }
    return wrote;
  }

  async function drain() {
    if (draining) {
      return;
    }
    draining = true;
    try {
      while (!stopped && queue.length > 0) {
        const next = queue.shift();
        if (next === undefined) {
          break;
        }
        queuedBytes -= next.bytes;
        try {
          const written = await writeChunk(next);
          next.paste?.complete(written);
        } catch (error) {
          stop(error instanceof Error ? error.message : String(error));
          next.paste?.complete(false);
        }
      }
    } finally {
      draining = false;
      if (!stopped && queue.length > 0) {
        scheduleDrain();
      }
    }
  }

  function scheduleDrain() {
    if (drainScheduled || stopped) {
      return;
    }
    drainScheduled = true;
    queueMicrotask(() => {
      drainScheduled = false;
      void drain();
    });
  }

  return {
    enqueue(input) {
      return enqueueInput(input);
    },
    enqueueTo(processInstanceId, input) {
      return enqueueInput(input, processInstanceId);
    },
    writePasteLine(input, signal, onWrite) {
      return new Promise((complete) => {
        if (signal.aborted || !enqueueInput(input, undefined, { signal, onWrite, complete })) {
          complete(false);
        }
      });
    },
    stop,
    isStopped() {
      return stopped;
    },
    pendingBytes() {
      return queuedBytes;
    },
  };

  function enqueueInput(input: string, processInstanceId?: number, paste?: PasteLine): boolean {
    if (stopped) {
      return false;
    }
    const bytes = new TextEncoder().encode(input).byteLength;
    if (bytes > maximumQueuedBytes || queuedBytes + bytes > maximumQueuedBytes) {
      stop(`Live broadcast stopped: the pending input queue exceeded ${maximumQueuedBytes / 1024} KiB.`);
      return false;
    }
    if (bytes === 0) {
      paste?.complete(true);
      return true;
    }
    queue.push({ input, bytes, processInstanceId, paste });
    queuedBytes += bytes;
    scheduleDrain();
    return true;
  }
}

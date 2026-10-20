import type { Snippet } from "svelte";

export type RequestGeneration = {
  next(): number;
  invalidate(): number;
  isCurrent(requestId: number): boolean;
};

export type LatestRequestHandlers<T> = {
  onSuccess(result: T): void;
  onError(error: unknown): void;
  onFinally(): void;
};

export type SessionCloseErrorNotice = {
  id: number;
  message: string;
};

export function createRequestGeneration(): RequestGeneration {
  let generation = 0;
  const advance = () => ++generation;
  return {
    next: advance,
    invalidate: advance,
    isCurrent: (requestId) => requestId === generation,
  };
}

// A stale settings request must not apply its result, report its error, or
// clear the loading/saving state owned by the newer request.
export async function runLatestRequest<T>(
  generation: RequestGeneration,
  requestId: number,
  request: () => Promise<T>,
  handlers: LatestRequestHandlers<T>,
): Promise<void> {
  try {
    const result = await request();
    if (generation.isCurrent(requestId)) {
      handlers.onSuccess(result);
    }
  } catch (error) {
    if (generation.isCurrent(requestId)) {
      handlers.onError(error);
    }
  } finally {
    if (generation.isCurrent(requestId)) {
      handlers.onFinally();
    }
  }
}

export function appendSessionCloseError(
  notices: SessionCloseErrorNotice[],
  notice: SessionCloseErrorNotice,
  maximum = 3,
): SessionCloseErrorNotice[] {
  return [...notices, notice].slice(-maximum);
}

export function dismissSessionCloseError(
  notices: SessionCloseErrorNotice[],
  noticeId: number,
): SessionCloseErrorNotice[] {
  return notices.filter(({ id }) => id !== noticeId);
}

// A recording action RPC may resolve after an event or after the pane
// has moved on to another process. Only apply its result to the request's live
// process when no newer state update has arrived in the meantime.
export function canApplyLoggingResult(
  resultId: number,
  processInstanceId: number | null,
  requestRevision: number,
  currentRevision: number,
): boolean {
  return resultId === processInstanceId && requestRevision === currentRevision;
}

export type SessionRecordingMenuState = {
  target: string;
  canStart: boolean;
  canStop: boolean;
};

export type SessionRecordingControl = SessionRecordingMenuState & { active: boolean; indicator: Snippet };

// Context menus pin a process generation; Shell actions use the selected session.
// A stale menu must never redirect an action to a restarted or different session.
export function recordingTargetForMenu(
  sessions: readonly { id: number; loggingControl?: SessionRecordingMenuState }[],
  selectedSessionId: number | null,
  menuData: unknown,
  start: boolean,
): string | null {
  const session = menuData === undefined || menuData === null
    ? sessions.find((session) => session.id === selectedSessionId)
    : typeof menuData === "string"
      ? sessions.find((session) => session.loggingControl?.target === menuData)
      : undefined;
  const control = session?.loggingControl;
  return control && (start ? control.canStart : control.canStop) ? control.target : null;
}

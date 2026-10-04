export type SessionLoggingState = {
  id: number;
  active: boolean;
  path: string;
  error: string;
};

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

export function shouldDeferLoggingStateRelease(
  processId: number | null,
  pendingOpenProcessId: number | null,
): boolean {
  return processId !== null && processId === pendingOpenProcessId;
}

export function isSessionLoggingStateOwnedByPane(
  processId: number,
  currentProcessId: number | null,
  loggingProcessId: number | null,
): boolean {
  return processId === currentProcessId || processId === loggingProcessId;
}

export async function closeAndReleaseLoggingState(
  closeSession: () => Promise<unknown>,
  releaseLoggingState: () => Promise<unknown>,
  onCloseError: (error: unknown) => void,
  onReleaseError: (error: unknown) => void,
): Promise<void> {
  try {
    try {
      await closeSession();
    } catch (error) {
      onCloseError(error);
    }
  } finally {
    try {
      await releaseLoggingState();
    } catch (error) {
      onReleaseError(error);
    }
  }
}

// A GetLoggingState or action RPC may resolve after an event or after the pane
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

export function isLoggingEventForCurrentProcess(
  eventId: number,
  processInstanceId: number | null,
): boolean {
  return eventId === processInstanceId;
}

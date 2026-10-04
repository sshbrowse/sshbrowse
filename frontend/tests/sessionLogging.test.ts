import assert from "node:assert/strict";
import test from "node:test";
import {
  appendSessionCloseError,
  canApplyLoggingResult,
  closeAndReleaseLoggingState,
  createRequestGeneration,
  dismissSessionCloseError,
  isSessionLoggingStateOwnedByPane,
  isLoggingEventForCurrentProcess,
  runLatestRequest,
  shouldDeferLoggingStateRelease,
} from "../src/lib/sessionLogging.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

test("logging events are accepted only for the pane's current recording process", () => {
  assert.equal(isLoggingEventForCurrentProcess(42, 42), true);
  assert.equal(isLoggingEventForCurrentProcess(41, 42), false);
  assert.equal(isLoggingEventForCurrentProcess(42, null), false);
});

test("late logging RPC results cannot replace a newer event or another process state", () => {
  assert.equal(canApplyLoggingResult(42, 42, 7, 7), true);
  assert.equal(canApplyLoggingResult(42, 42, 7, 8), false);
  assert.equal(canApplyLoggingResult(41, 42, 7, 7), false);
  assert.equal(canApplyLoggingResult(42, null, 7, 7), false);
});

test("out-of-order logging settings loads leave the newest result and loading state intact", async () => {
  const requests = createRequestGeneration();
  const firstResult = deferred<string>();
  const secondResult = deferred<string>();
  let settings = "initial";
  let loading = false;
  let error = "";
  const handlers = {
    onSuccess: (value: string) => { settings = value; },
    onError: (value: unknown) => { error = String(value); },
    onFinally: () => { loading = false; },
  };

  const firstRequestId = requests.next();
  loading = true;
  const first = runLatestRequest(requests, firstRequestId, () => firstResult.promise, handlers);
  const secondRequestId = requests.next();
  loading = true;
  const second = runLatestRequest(requests, secondRequestId, () => secondResult.promise, handlers);

  secondResult.resolve("new settings");
  await second;
  firstResult.reject(new Error("stale load failed"));
  await first;

  assert.equal(settings, "new settings");
  assert.equal(error, "");
  assert.equal(loading, false);
});

test("a save supersedes a pending load, including its failure and finalizer", async () => {
  const requests = createRequestGeneration();
  const loadResult = deferred<string>();
  const saveResult = deferred<string>();
  let settings = "initial";
  let loading = false;
  let saving = false;
  let error = "";

  const loadId = requests.next();
  loading = true;
  const load = runLatestRequest(requests, loadId, () => loadResult.promise, {
    onSuccess: (value) => { settings = value; },
    onError: (value) => { error = String(value); },
    onFinally: () => { loading = false; },
  });

  const saveId = requests.next();
  loading = false;
  saving = true;
  const save = runLatestRequest(requests, saveId, () => saveResult.promise, {
    onSuccess: (value) => { settings = value; },
    onError: (value) => { error = String(value); },
    onFinally: () => { saving = false; },
  });

  saveResult.resolve("saved settings");
  await save;
  loadResult.reject(new Error("outdated load failed"));
  await load;

  assert.equal(settings, "saved settings");
  assert.equal(error, "");
  assert.equal(loading, false);
  assert.equal(saving, false);
});

test("closing Settings invalidates late logging settings errors", async () => {
  const requests = createRequestGeneration();
  const result = deferred<string>();
  let error = "";
  let loading = true;
  const requestId = requests.next();
  const request = runLatestRequest(requests, requestId, () => result.promise, {
    onSuccess: () => {},
    onError: (value) => { error = String(value); },
    onFinally: () => { loading = false; },
  });

  requests.invalidate();
  loading = false;
  result.reject(new Error("closed page load failed"));
  await request;

  assert.equal(error, "");
  assert.equal(loading, false);
});

test("unmount defers logging-state release until the matching Open request settles", async () => {
  const openResult = deferred<void>();
  const processId = 17;
  let pendingOpenId: number | null = processId;
  let released = false;
  const releaseIfReady = () => {
    if (!shouldDeferLoggingStateRelease(processId, pendingOpenId)) {
      released = true;
    }
  };

  const opening = openResult.promise.finally(() => { pendingOpenId = null; });
  releaseIfReady();
  assert.equal(released, false);

  openResult.resolve(undefined);
  await opening;
  releaseIfReady();
  assert.equal(released, true);
  assert.equal(shouldDeferLoggingStateRelease(processId, processId + 1), false);
  assert.equal(shouldDeferLoggingStateRelease(processId, null), false);
});

test("a late Open error after reconnect releases its old ID without changing current pane state", async () => {
  const oldOpen = deferred<void>();
  const oldProcessId = 23;
  let pendingOpenId: number | null = oldProcessId;
  let currentProcessId: number | null = oldProcessId;
  let loggingProcessId: number | null = oldProcessId;
  const released: number[] = [];
  let visibleError = "";

  const oldConnection = (async () => {
    try {
      await oldOpen.promise;
    } catch (error) {
      if (!isSessionLoggingStateOwnedByPane(oldProcessId, currentProcessId, loggingProcessId)) {
        released.push(oldProcessId);
        return;
      }
      if (currentProcessId === oldProcessId) {
        visibleError = String(error);
      }
    }
  })();

  currentProcessId = null;
  assert.equal(shouldDeferLoggingStateRelease(oldProcessId, pendingOpenId), true);
  const newProcessId = oldProcessId + 1;
  currentProcessId = newProcessId;
  loggingProcessId = newProcessId;
  pendingOpenId = newProcessId;
  oldOpen.reject(new Error("stale Open failed"));
  await oldConnection;

  assert.deepEqual(released, [oldProcessId]);
  assert.equal(visibleError, "");
  assert.equal(currentProcessId, newProcessId);
  assert.equal(pendingOpenId, newProcessId);
});

test("session close errors stay bounded and can be dismissed", () => {
  const first = { id: 1, message: "first" };
  const notices = appendSessionCloseError(
    appendSessionCloseError(
      appendSessionCloseError([first], { id: 2, message: "second" }),
      { id: 3, message: "third" },
    ),
    { id: 4, message: "fourth" },
  );

  assert.deepEqual(notices.map(({ id }) => id), [2, 3, 4]);
  assert.deepEqual(dismissSessionCloseError(notices, 3), [
    { id: 2, message: "second" },
    { id: 4, message: "fourth" },
  ]);
});

test("a close failure is reported after pane removal and before retained log state is released", async () => {
  const closeResult = deferred<void>();
  let paneMounted = true;
  const calls: string[] = [];
  let reportedError = "";

  const closing = closeAndReleaseLoggingState(
    () => closeResult.promise,
    async () => { calls.push("released"); },
    (error) => {
      assert.equal(paneMounted, false);
      reportedError = String(error);
      calls.push("reported");
    },
    (error) => { calls.push(`release failed: ${String(error)}`); },
  );
  paneMounted = false;
  closeResult.reject(new Error("log flush failed"));
  await closing;

  assert.equal(reportedError, "Error: log flush failed");
  assert.deepEqual(calls, ["reported", "released"]);
});

import assert from "node:assert/strict";
import test from "node:test";
import {
  appendSessionCloseError,
  canApplyLoggingResult,
  createRequestGeneration,
  dismissSessionCloseError,
  runLatestRequest,
  recordingTargetForMenu,
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

const recordingSessions = [
  { id: 1, loggingControl: { target: "1:11", canStart: true, canStop: false } },
  { id: 2, loggingControl: { target: "2:22", canStart: false, canStop: true } },
  { id: 3, loggingControl: { target: "3:33", canStart: false, canStop: false } },
  { id: 4 },
];

test("Shell recording actions use the selected session and the requested action", () => {
  assert.equal(recordingTargetForMenu(recordingSessions, 1, undefined, true), "1:11");
  assert.equal(recordingTargetForMenu(recordingSessions, 2, null, false), "2:22");
  assert.equal(recordingTargetForMenu(recordingSessions, 1, undefined, false), null);
  assert.equal(recordingTargetForMenu(recordingSessions, 2, undefined, true), null);
  assert.equal(recordingTargetForMenu(recordingSessions, null, undefined, true), null);
});

test("a recording context menu targets its own session regardless of selection", () => {
  assert.equal(recordingTargetForMenu(recordingSessions, 1, "2:22", false), "2:22");
  assert.equal(recordingTargetForMenu(recordingSessions, 2, "1:11", true), "1:11");
  assert.equal(recordingTargetForMenu(recordingSessions, 1, "2:22", true), null);
});

test("recording menus reject stale processes, closed or busy sessions, and malformed targets", () => {
  for (const target of ["1:10", "5:55", "3:33", "", 1, {}, ["1:11"]]) {
    assert.equal(recordingTargetForMenu(recordingSessions, 1, target, true), null);
    assert.equal(recordingTargetForMenu(recordingSessions, 1, target, false), null);
  }
  assert.equal(recordingTargetForMenu(recordingSessions, 4, undefined, true), null);
});

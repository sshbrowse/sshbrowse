import assert from "node:assert/strict";
import test from "node:test";
import { bracketedPasteEnd, bracketedPasteStart, finishPacedPaste, sendPacedPaste } from "../src/lib/terminalPaste.ts";

test("paced paste preserves blank lines, trailing CR, and the opening bracket", async () => {
  const controller = new AbortController();
  const writes: string[] = [];
  const progress: number[] = [];
  const text = "first\r\rfinal 🧪\r";
  await sendPacedPaste(bracketedPasteStart + text + bracketedPasteEnd, true, 1, controller.signal,
    async (line) => {
      writes.push(line);
      return true;
    },
    ({ sent, total }) => {
      assert.equal(total, 3);
      progress.push(sent);
    },
  );
  assert.deepEqual(writes, [bracketedPasteStart + "first\r", "\r", "final 🧪\r"]);
  assert.deepEqual(progress, [0, 1, 2, 3]);
});

test("cancelling during a delay stops before the next line", async () => {
  const controller = new AbortController();
  const writes: string[] = [];
  await sendPacedPaste(bracketedPasteStart + "one\rtwo" + bracketedPasteEnd, true, 10_000, controller.signal,
    async (line) => {
      writes.push(line);
      return true;
    },
    ({ sent }) => {
      if (sent === 1) setTimeout(() => controller.abort(), 0);
    },
  );
  assert.deepEqual(writes, [bracketedPasteStart + "one\r"]);
});

test("the closing bracket waits for an issued write even after the paste stops", async () => {
  const writes: string[] = [];
  const { promise, resolve } = Promise.withResolvers<void>();
  const finished = finishPacedPaste(promise, async () => { writes.push(bracketedPasteEnd); });
  await finishPacedPaste(Promise.resolve(), async () => { writes.push("other terminal closed"); });
  assert.deepEqual(writes, ["other terminal closed"]);
  writes.push("last line");
  resolve();
  await finished;
  assert.deepEqual(writes, ["other terminal closed", "last line", bracketedPasteEnd]);
});

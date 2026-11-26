export const bracketedPasteStart = "\x1b[200~";
export const bracketedPasteEnd = "\x1b[201~";

export interface PacedPasteProgress {
  sent: number;
  total: number;
}

function waitBetweenLines(delayMs: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(finish, delayMs);
    signal.addEventListener("abort", finish, { once: true });

    function finish() {
      clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    }
  });
}

// xterm normalizes pasted line breaks to CR. Keep each CR with its preceding
// line, including blank lines, and don't count an empty line after a final CR.
export async function sendPacedPaste(
  input: string,
  bracketed: boolean,
  delayMs: number,
  signal: AbortSignal,
  write: (line: string, opensBracket: boolean) => Promise<boolean>,
  progress: (value: PacedPasteProgress) => void,
): Promise<void> {
  const text = bracketed ? input.slice(bracketedPasteStart.length, -bracketedPasteEnd.length) : input;
  let total = text.endsWith("\r") ? 0 : 1;
  for (const _ of text.matchAll(/\r/g)) total++;
  progress({ sent: 0, total });

  let sent = 0;
  let offset = 0;
  while (!signal.aborted && offset < text.length) {
    const newline = text.indexOf("\r", offset);
    const end = newline < 0 ? text.length : newline + 1;
    const opensBracket = bracketed && offset === 0;
    const line = (opensBracket ? bracketedPasteStart : "") + text.slice(offset, end);
    if (!await write(line, opensBracket)) break;
    offset = end;
    progress({ sent: ++sent, total });
    if (!signal.aborted && offset < text.length) {
      await waitBetweenLines(delayMs, signal);
    }
  }
}

// A frontend timeout doesn't cancel a PTY write. Wait for each last issued
// write before sending its closing bracket or releasing its input gate.
export async function finishPacedPaste(
  pending: Promise<void> | undefined,
  closeBracket: () => Promise<void>,
): Promise<void> {
  // A failed write may still have written all or part of the opening bracket.
  await pending?.catch(() => {});
  try {
    await closeBracket();
  } catch {
    throw new Error("Could not close the bracketed paste.");
  }
}

export async function writePasteWithTimeout(
  pending: Promise<unknown>,
  timeoutMs: number,
): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      pending,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error("Terminal write timed out. Close the terminal if it stays unresponsive.")), timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

<script lang="ts">
  import { onDestroy, onMount, tick } from "svelte";
  import X from "@lucide/svelte/icons/x";
  import BroadcastCommandConfirmDialog from "./BroadcastCommandConfirmDialog.svelte";
  import {
    broadcastCommandLineCount,
    broadcastCommandPreview,
    broadcastCommandError,
    maximumBroadcastCommandBytes,
    type BroadcastDelivery,
    type BroadcastSnapshot,
  } from "./routing";
  import { currentPlatform, type ShortcutPlatform } from "./shortcuts";
  import { utf8ByteLength } from "./terminalInput";
  import { liveBroadcastStartError, type LiveBroadcastState } from "./liveBroadcast";
  import type { BroadcastMode } from "./storage";

  let {
    recipients,
    activeId,
    focusedId,
    mode,
    onmodechange,
    enabled,
    liveBroadcast,
    broadcastError,
    sendCommand,
    startLiveBroadcast,
    stopLiveBroadcast,
    onclose,
    onsendingchange,
    onconfirmationchange,
  }: {
    recipients: BroadcastSnapshot[];
    activeId: number | null;
    focusedId: number | null;
    mode: BroadcastMode;
    onmodechange: (mode: BroadcastMode) => void;
    enabled: boolean;
    liveBroadcast: LiveBroadcastState | null;
    broadcastError: string;
    sendCommand: (recipients: BroadcastSnapshot[], command: string) => Promise<BroadcastDelivery[]>;
    startLiveBroadcast: () => string | null;
    stopLiveBroadcast: () => void;
    onclose: () => void;
    onsendingchange: (sending: boolean) => void;
    onconfirmationchange: (confirming: boolean) => void;
  } = $props();

  let command = $state("");
  let sending = $state(false);
  let validationError = $state<string | null>(null);
  let deliveries = $state<BroadcastDelivery[] | null>(null);
  let commandInput = $state<HTMLTextAreaElement>();
  let commandConfirmation = $state<{
    tabId: number | null;
    recipients: BroadcastSnapshot[];
    command: string;
    lineCount: number;
    byteCount: number;
    preview: string;
  } | null>(null);
  const shortcutPlatform: ShortcutPlatform = currentPlatform();
  const recipientCount = $derived(recipients.length);
  const commandLineCount = $derived(broadcastCommandLineCount(command));
  const liveStartError = $derived(liveBroadcastStartError(recipients, focusedId));

  onMount(() => {
    if (enabled && mode === "command") commandInput?.focus();
  });

  onDestroy(() => {
    if (commandConfirmation !== null) {
      onconfirmationchange(false);
    }
  });

  function setMode(nextMode: BroadcastMode) {
    if (liveBroadcast !== null || mode === nextMode) {
      return;
    }
    onmodechange(nextMode);
    validationError = null;
    deliveries = null;
    void tick().then(() => {
      if (enabled && mode === "command") commandInput?.focus();
    });
  }

  async function sendSnapshot(snapshot: BroadcastSnapshot[], commandToSend: string) {
    if (sending) {
      return;
    }
    sending = true;
    onsendingchange(true);
    deliveries = null;
    try {
      deliveries = await sendCommand(snapshot, commandToSend);
      if (deliveries.every((delivery) => delivery.error === null)) {
        command = "";
      }
    } catch (error) {
      validationError = String(error);
    } finally {
      sending = false;
      onsendingchange(false);
      await tick();
      if (enabled && mode === "command") commandInput?.focus();
    }
  }

  async function submit() {
    if (sending || commandConfirmation !== null || !enabled) {
      return;
    }
    validationError = broadcastCommandError(command);
    if (validationError !== null) {
      return;
    }
    if (recipientCount === 0) {
      validationError = "No live terminals in this tab.";
      return;
    }

    if (commandLineCount > 1) {
      commandConfirmation = {
        tabId: activeId,
        recipients,
        command,
        lineCount: commandLineCount,
        byteCount: utf8ByteLength(command),
        preview: broadcastCommandPreview(command),
      };
      onconfirmationchange(true);
      return;
    }
    await sendSnapshot(recipients, command);
  }

  function startLive() {
    if (!enabled || liveBroadcast !== null) {
      return;
    }
    validationError = startLiveBroadcast();
  }

  function finishConfirmation(confirmed: boolean) {
    const request = commandConfirmation;
    commandConfirmation = null;
    onconfirmationchange(false);
    if (confirmed && request !== null) {
      const recipientsChanged = request.tabId !== activeId || request.recipients.length !== recipientCount ||
        request.recipients.some((recipient, index) =>
          recipient.logicalSessionId !== recipients[index].logicalSessionId ||
          recipient.processInstanceId !== recipients[index].processInstanceId);
      if (!recipientsChanged) {
        void sendSnapshot(request.recipients, request.command);
        return;
      }
      validationError = "The broadcast recipients changed. Review the command again.";
    }
    void tick().then(() => {
      if (enabled && mode === "command") commandInput?.focus();
    });
  }

  export function dismissConfirmation() {
    if (commandConfirmation !== null) {
      finishConfirmation(false);
    }
  }

  function isSendShortcut(event: KeyboardEvent): boolean {
    if (event.key !== "Enter" || event.shiftKey || event.altKey) {
      return false;
    }
    return shortcutPlatform === "mac"
      ? event.metaKey && !event.ctrlKey
      : event.ctrlKey && !event.metaKey;
  }

  function handleCommandKeydown(event: KeyboardEvent) {
    if (isSendShortcut(event)) {
      event.preventDefault();
      void submit();
    }
  }

  function closeOnEscape(event: KeyboardEvent) {
    if (
      event.key === "Escape" &&
      mode === "command" &&
      liveBroadcast === null &&
      enabled &&
      commandConfirmation === null &&
      !sending &&
      event.target instanceof Element &&
      event.target.closest(".broadcast-bar")
    ) {
      event.preventDefault();
      event.stopPropagation();
      onclose();
    }
  }

  function validatePaste(event: ClipboardEvent) {
    const pasted = event.clipboardData?.getData("text");
    const input = commandInput;
    if (!pasted || !input) return;
    const start = input.selectionStart ?? command.length;
    const end = input.selectionEnd ?? start;
    const candidate = command.slice(0, start) + pasted + command.slice(end);
    const error = broadcastCommandError(candidate);
    if (error !== null) {
      event.preventDefault();
      validationError = error;
      deliveries = null;
    }
  }
</script>

<svelte:window onkeydown={closeOnEscape} />

<section id="broadcast-command-bar" class="broadcast-bar" aria-label="Broadcast">
  {#if liveBroadcast}
    <div class="live-strip" role="status">
      <strong>Live input on · {liveBroadcast.snapshot.length} terminals</strong>
      <span>Every keystroke is mirrored</span>
      <button class="stop-live" type="button" onpointerdown={(event) => event.preventDefault()} onclick={stopLiveBroadcast}>Stop live input</button>
    </div>
    {#if broadcastError}
      <div class="broadcast-status" role="alert"><span class="error">{broadcastError}</span></div>
    {/if}
  {:else}
    <div class="broadcast-heading">
      {#if activeId !== null}
        <div class="mode-switch" role="group" aria-label="Broadcast mode">
          <button type="button" aria-pressed={mode === "command"} disabled={sending || commandConfirmation !== null || !enabled} onclick={() => setMode("command")}>Command</button>
          <button type="button" aria-pressed={mode === "live"} disabled={sending || commandConfirmation !== null || !enabled} onclick={() => setMode("live")}>Live input</button>
        </div>
        <span class="recipient-summary" title={recipients.map((recipient) => recipient.label).join(", ")} aria-live="polite">
          {recipientCount === 0 ? "No live terminals in this tab" : recipientCount === 1 ? `To ${recipients[0].label} · This tab` : `${recipientCount} live terminals in this tab`}
        </span>
      {/if}
      <button class="close" type="button" aria-label="Close broadcast bar" title="Close" disabled={sending || commandConfirmation !== null} onclick={onclose}><X size={16} /></button>
    </div>

    {#if activeId === null}
      <p class="empty-state">Open a terminal to send a command or mirror input.</p>
    {:else if mode === "command"}
      <form class="broadcast-form" onsubmit={(event) => { event.preventDefault(); void submit(); }}>
        <textarea
          class="command"
          bind:this={commandInput}
          bind:value={command}
          maxlength={maximumBroadcastCommandBytes}
          autocomplete="off"
          spellcheck="false"
          aria-label="Command"
          rows={Math.min(5, command.split(/\r\n|\r|\n/).length)}
          placeholder="Enter a command or block"
          disabled={sending || commandConfirmation !== null}
          onpaste={validatePaste}
          onkeydown={handleCommandKeydown}
          oninput={() => { validationError = null; deliveries = null; }}
        ></textarea>

        <button class="send" type="submit" aria-keyshortcuts={shortcutPlatform === "mac" ? "Meta+Enter" : "Control+Enter"} disabled={!enabled || sending || commandConfirmation !== null || recipientCount === 0 || command.trim() === ""}>
          {sending ? "Sending…" : commandLineCount > 1 ? "Review…" : recipientCount === 0 ? "Send" : `Send to ${recipientCount}`}
          {#if !sending}<kbd aria-hidden="true">{shortcutPlatform === "mac" ? "⌘↵" : "Ctrl+↵"}</kbd>{/if}
        </button>
      </form>

      <div class="broadcast-status" aria-live="polite">
        {#if validationError}
          <span class="error">{validationError}</span>
        {:else if deliveries}
          {@const sentCount = deliveries.filter((delivery) => delivery.error === null).length}
          {@const failures = deliveries.filter((delivery) => delivery.error !== null)}
          <span>{sentCount > 0 ? `Sent to ${sentCount} terminal${sentCount === 1 ? "" : "s"}.` : "No deliveries confirmed."}</span>
          {#each failures as failure (failure.logicalSessionId)}
            <span class="error">{failure.label}: {failure.error}</span>
          {/each}
        {/if}
      </div>
    {:else}
      <div class="live-setup">
        <strong class="live-warning" role="alert">Every keystroke is mirrored</strong>
        <button class="start-live" type="button" disabled={!enabled || liveStartError !== null} onclick={startLive}>Start live input</button>
      </div>
      <div class="broadcast-status" aria-live="polite">
        {#if liveStartError}<span class="reminder">{liveStartError}</span>{/if}
        {#if validationError}<span class="error">{validationError}</span>{/if}
        {#if broadcastError}<span class="error">{broadcastError}</span>{/if}
      </div>
    {/if}
  {/if}
</section>

{#if commandConfirmation}
  <BroadcastCommandConfirmDialog
    lineCount={commandConfirmation.lineCount}
    byteCount={commandConfirmation.byteCount}
    recipientLabels={commandConfirmation.recipients.map((recipient) => recipient.label)}
    preview={commandConfirmation.preview}
    onclose={finishConfirmation}
  />
{/if}

<style>
  .broadcast-bar {
    container-type: inline-size;
    position: relative;
    z-index: 3;
    flex: none;
    display: grid;
    gap: 8px;
    padding: 6px 12px;
    border-bottom: 1px solid var(--border-subtle);
    background: var(--surface);
    color: var(--text-secondary);
    font: var(--ui-font-body) var(--font-ui);
  }
  .live-strip,
  .live-setup {
    display: flex;
    align-items: center;
  }
  .broadcast-heading {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    min-height: 32px;
    gap: 8px;
  }
  .mode-switch {
    display: flex;
    gap: 2px;
    padding: 2px;
    border-radius: var(--radius-control);
    background: var(--input-surface);
  }
  .mode-switch button {
    min-height: 28px;
    padding: 2px 8px;
    border: 0;
    border-radius: var(--radius-control);
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
  }
  .mode-switch button:hover:not(:disabled) { background: var(--control-hover); }
  .mode-switch button[aria-pressed="true"] {
    background: var(--surface-raised);
    color: var(--text-primary);
  }
  .recipient-summary {
    min-width: 0;
    overflow: hidden;
    text-align: right;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .close,
  .stop-live {
    border: 0;
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    cursor: default;
  }
  .close:hover,
  .stop-live:hover {
    background: var(--control-hover);
    color: var(--text-primary);
  }
  .close {
    grid-column: 3;
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    margin-left: auto;
    border-radius: var(--radius-control);
  }
  .broadcast-form {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: start;
    gap: 8px;
  }
  .live-setup {
    justify-content: space-between;
    gap: 8px;
  }
  .command {
    width: 100%;
    min-width: 0;
    min-height: 40px;
    max-height: 112px;
    padding: 7px 10px;
    resize: vertical;
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
    background: var(--input-surface);
    color: var(--text-primary);
    font: var(--ui-font-body) ui-monospace, "SF Mono", Menlo, monospace;
    line-height: 1.35;
  }
  .send,
  .start-live {
    flex: none;
    min-height: 32px;
    padding: 0 12px;
    border: 0;
    border-radius: var(--radius-control);
    background: var(--accent);
    color: var(--accent-foreground);
    font: inherit;
    font-weight: 500;
    white-space: nowrap;
  }
  .send { min-height: 40px; }
  .send kbd {
    margin-left: 4px;
    font: inherit;
    opacity: 0.8;
  }
  .send:hover:not(:disabled),
  .start-live:hover:not(:disabled) {
    filter: brightness(1.08);
  }
  .send:disabled,
  .start-live:disabled {
    border: 1px solid var(--border);
    background: var(--surface-raised);
    color: var(--text-disabled);
  }
  .close:disabled,
  .mode-switch button:disabled {
    opacity: 0.5;
  }
  .broadcast-status {
    display: flex;
    flex-wrap: wrap;
    gap: 3px 10px;
    max-height: 44px;
    overflow-y: auto;
    font-size: var(--ui-font-small);
    line-height: 1.35;
  }
  .broadcast-status:empty { position: absolute; }
  .reminder {
    color: var(--text-secondary);
  }
  .empty-state { margin: 0; padding: 4px 0 6px; }
  .error {
    color: var(--status-error);
  }
  .live-warning {
    padding: 4px 0;
    color: var(--status-warning);
    font-size: var(--ui-font-small);
    line-height: 1.4;
  }
  .live-strip {
    min-height: 36px;
    gap: 12px;
    margin: -6px -12px;
    padding: 4px 12px;
    border-left: 2px solid var(--status-warning);
    background: var(--status-warning-surface);
  }
  .live-strip strong {
    color: var(--status-warning);
    letter-spacing: 0.02em;
    white-space: nowrap;
  }
  .live-strip span {
    flex: 1;
    color: var(--text-secondary);
  }
  .stop-live {
    flex: none;
    height: 28px;
    margin-left: auto;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
    color: var(--text-primary);
  }
  @container (max-width: 600px) {
    .live-strip span {
      display: none;
    }
  }
  @container (max-width: 420px) {
    .recipient-summary {
      grid-column: 1 / -1;
      grid-row: 2;
      text-align: left;
    }
    .close { grid-row: 1; }
    .broadcast-form { grid-template-columns: minmax(0, 1fr); }
    .send { justify-self: end; }
    .live-setup, .live-strip { flex-wrap: wrap; }
    .start-live { margin-left: auto; }
    .live-strip strong { white-space: normal; }
  }
</style>

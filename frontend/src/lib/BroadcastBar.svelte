<script lang="ts">
  import { onDestroy, onMount, tick } from "svelte";
  import ChevronDown from "@lucide/svelte/icons/chevron-down";
  import X from "@lucide/svelte/icons/x";
  import BroadcastCommandConfirmDialog from "./BroadcastCommandConfirmDialog.svelte";
  import {
    broadcastCommandLineCount,
    broadcastCommandPreview,
    broadcastCommandError,
    broadcastRecipients,
    maximumBroadcastCommandBytes,
    snapshotBroadcastRecipients,
    type BroadcastDelivery,
    type BroadcastScope,
    type BroadcastSnapshot,
  } from "./routing";
  import { currentPlatform, type ShortcutPlatform } from "./shortcuts";
  import { utf8ByteLength } from "./terminalInput";
  import { liveBroadcastStartError, type LiveBroadcastState } from "./liveBroadcast";
  import type { Session, Tab } from "./tabs";
  import { focusedSessionId } from "./workspace";

  let {
    sessions,
    tabs,
    displayLabels,
    activeId,
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
    sessions: Session[];
    tabs: Tab[];
    displayLabels: ReadonlyMap<number, string>;
    activeId: number | null;
    enabled: boolean;
    liveBroadcast: LiveBroadcastState | null;
    broadcastError: string;
    sendCommand: (recipients: BroadcastSnapshot[], command: string) => Promise<BroadcastDelivery[]>;
    startLiveBroadcast: (snapshot: BroadcastSnapshot[]) => string | null;
    stopLiveBroadcast: () => void;
    onclose: () => void;
    onsendingchange: (sending: boolean) => void;
    onconfirmationchange: (confirming: boolean) => void;
  } = $props();

  type BroadcastMode = "command" | "live";

  let mode = $state<BroadcastMode>("command");
  let scope = $state<BroadcastScope>("current");
  let command = $state("");
  let excludedSessionIds = $state<number[]>([]);
  let sending = $state(false);
  let validationError = $state<string | null>(null);
  let deliveries = $state<BroadcastDelivery[] | null>(null);
  let commandInput = $state<HTMLTextAreaElement>();
  let recipientPicker = $state<HTMLDetailsElement>();
  let commandConfirmation = $state<{
    recipients: BroadcastSnapshot[];
    command: string;
    lineCount: number;
    byteCount: number;
    preview: string;
  } | null>(null);
  const shortcutPlatform: ShortcutPlatform = currentPlatform();
  const recipients = $derived(broadcastRecipients(sessions, tabs, activeId, scope, displayLabels));
  const selectedRecipients = $derived(
    recipients.filter((recipient) => recipient.available && !excludedSessionIds.includes(recipient.logicalSessionId)),
  );
  const selectedCount = $derived(selectedRecipients.length);
  const focusedId = $derived(focusedSessionId(tabs, activeId));
  const liveStartError = $derived(
    liveBroadcastStartError(snapshotBroadcastRecipients(recipients, new Set(excludedSessionIds)), focusedId),
  );

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
    mode = nextMode;
    if (recipientPicker) recipientPicker.open = false;
    validationError = null;
    deliveries = null;
    void tick().then(() => {
      if (enabled && mode === "command") commandInput?.focus();
    });
  }

  function setScope(nextScope: BroadcastScope) {
    scope = nextScope;
    excludedSessionIds = [];
    validationError = null;
    deliveries = null;
  }

  function setIncluded(logicalSessionId: number, included: boolean) {
    if (included) {
      excludedSessionIds = excludedSessionIds.filter((id) => id !== logicalSessionId);
    } else if (!excludedSessionIds.includes(logicalSessionId)) {
      excludedSessionIds = [...excludedSessionIds, logicalSessionId];
    }
    validationError = null;
    deliveries = null;
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
    const snapshot = snapshotBroadcastRecipients(recipients, new Set(excludedSessionIds));
    if (recipientPicker) recipientPicker.open = false;
    if (snapshot.length === 0) {
      validationError = "Select at least one available session.";
      return;
    }

    if (broadcastCommandLineCount(command) > 1) {
      commandConfirmation = {
        recipients: snapshot,
        command,
        lineCount: broadcastCommandLineCount(command),
        byteCount: utf8ByteLength(command),
        preview: broadcastCommandPreview(command),
      };
      onconfirmationchange(true);
      return;
    }
    await sendSnapshot(snapshot, command);
  }

  function startLive() {
    if (!enabled || liveBroadcast !== null) {
      return;
    }
    const snapshot = snapshotBroadcastRecipients(recipients, new Set(excludedSessionIds));
    if (recipientPicker) recipientPicker.open = false;
    validationError = startLiveBroadcast(snapshot);
  }

  function finishConfirmation(confirmed: boolean) {
    const request = commandConfirmation;
    commandConfirmation = null;
    onconfirmationchange(false);
    if (confirmed && request !== null) {
      void sendSnapshot(request.recipients, request.command);
    } else {
      void tick().then(() => {
        if (enabled && mode === "command") commandInput?.focus();
      });
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
    if (event.key === "Escape" && recipientPicker?.open) {
      event.preventDefault();
      event.stopPropagation();
      recipientPicker.open = false;
      return;
    }
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

  function closePickerOutside(event: PointerEvent) {
    if (recipientPicker?.open && event.target instanceof Node && !recipientPicker.contains(event.target)) {
      recipientPicker.open = false;
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

<svelte:window onkeydown={closeOnEscape} onpointerdown={closePickerOutside} />

<section id="broadcast-command-bar" class="broadcast-bar" aria-label="Broadcast">
  {#if liveBroadcast}
    <div class="live-strip" role="status">
      <strong>LIVE INPUT · {liveBroadcast.snapshot.length} terminals</strong>
      <span>Every keystroke is mirrored</span>
      <button class="stop-live" type="button" onpointerdown={(event) => event.preventDefault()} onclick={stopLiveBroadcast}>Stop</button>
    </div>
    {#if broadcastError}
      <div class="broadcast-status" role="alert"><span class="error">{broadcastError}</span></div>
    {/if}
  {:else}
    <div class="broadcast-heading">
      <strong>Broadcast</strong>
      {#if sessions.length > 0}
        <div class="select-control mode-select">
          <select
            aria-label="Broadcast mode"
            value={mode}
            disabled={sending || commandConfirmation !== null || !enabled}
            onchange={(event) => setMode(event.currentTarget.value as BroadcastMode)}
          >
            <option value="command">Send a command</option>
            <option value="live">Live input</option>
          </select>
        </div>
      {/if}
      <button class="close" type="button" aria-label="Close broadcast bar" title="Close" disabled={sending || commandConfirmation !== null} onclick={onclose}><X size={16} /></button>
    </div>

    {#if sessions.length === 0}
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

        <div class="destination-row">
          {@render RecipientPicker(recipients, excludedSessionIds, sending, setIncluded)}
          <button class="send" type="submit" disabled={sending || selectedCount === 0 || command.trim() === ""}>{sending ? "Sending…" : selectedCount === 0 ? "Send" : `Send to ${selectedCount}`}</button>
        </div>
      </form>

      <div class="broadcast-status" aria-live="polite">
        <span class="command-caution">Send only at a command prompt.</span>
        <span class="send-shortcut">{shortcutPlatform === "mac" ? "⌘↵ to send" : "Ctrl+↵ to send"}</span>
        {#if validationError}
          <span class="error">{validationError}</span>
        {:else if deliveries}
          {@const sentCount = deliveries.filter((delivery) => delivery.error === null).length}
          {@const failures = deliveries.filter((delivery) => delivery.error !== null)}
          <span>{sentCount > 0 ? `Sent to ${sentCount} session${sentCount === 1 ? "" : "s"}.` : "No deliveries confirmed."}</span>
          {#each failures as failure (failure.logicalSessionId)}
            <span class="error">{failure.label}: {failure.error}</span>
          {/each}
        {/if}
      </div>
    {:else}
      <div class="live-setup">
        <div class="live-warning" role="alert">
          <strong>Every keystroke is mirrored.</strong>
          <span>Keys and approved paste go to every selected terminal, including the focused one.</span>
        </div>
        <div class="destination-row">
          {@render RecipientPicker(recipients, excludedSessionIds, false, setIncluded)}
          <button class="start-live" type="button" disabled={!enabled || liveStartError !== null} onclick={startLive}>Start live input</button>
        </div>
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
    recipientCount={commandConfirmation.recipients.length}
    preview={commandConfirmation.preview}
    onclose={finishConfirmation}
  />
{/if}

{#snippet RecipientPicker(
  recipients: ReturnType<typeof broadcastRecipients>,
  excludedSessionIds: number[],
  sending: boolean,
  setIncluded: (logicalSessionId: number, included: boolean) => void,
)}
  <details class="recipient-picker" bind:this={recipientPicker}>
    <summary title={selectedRecipients.map((recipient) => recipient.label).join(", ")}>
      <span class="recipient-count" aria-live="polite">To {selectedCount} terminal{selectedCount === 1 ? "" : "s"}</span>
      <span class="recipient-scope">· {scope === "current" ? "Current tab" : "All tabs"}</span>
      <ChevronDown class="picker-chevron" size={14} />
    </summary>
    <div class="recipient-menu">
      <strong>Recipients</strong>
      <fieldset class="scope-options" aria-label="Recipient scope">
        <label><input type="radio" name="broadcast-scope" checked={scope === "current"} disabled={sending} onchange={() => setScope("current")} /> Current tab</label>
        <label><input type="radio" name="broadcast-scope" checked={scope === "all"} disabled={sending} onchange={() => setScope("all")} /> All tabs</label>
      </fieldset>
      <span class="scope-hint">Terminals in the selected scope</span>
      {@render RecipientList(recipients, excludedSessionIds, sending, setIncluded)}
    </div>
  </details>
{/snippet}

{#snippet RecipientList(
  recipients: ReturnType<typeof broadcastRecipients>,
  excludedSessionIds: number[],
  sending: boolean,
  setIncluded: (logicalSessionId: number, included: boolean) => void,
)}
  <div class="recipient-list" aria-label="Broadcast recipients">
    {#each recipients as recipient (recipient.logicalSessionId)}
      <label class:unavailable={!recipient.available} title={recipient.available ? recipient.label : `${recipient.label}: ${recipient.status}`}>
        <input
          type="checkbox"
          checked={recipient.available && !excludedSessionIds.includes(recipient.logicalSessionId)}
          disabled={!recipient.available || sending}
          onchange={(event) => setIncluded(recipient.logicalSessionId, event.currentTarget.checked)}
        />
        <span class="recipient-name">{recipient.label}</span>
        {#if !recipient.available}
          <small class="recipient-status">{recipient.status}</small>
        {:else if recipient.logicalSessionId === focusedId}
          <small class="recipient-status">Focused</small>
        {/if}
      </label>
    {:else}
      <span class="no-recipients">No sessions in scope</span>
    {/each}
  </div>
{/snippet}

<style>
  .broadcast-bar {
    container-type: inline-size;
    position: relative;
    z-index: 3;
    flex: none;
    display: grid;
    gap: 4px;
    padding: 6px 12px;
    border-bottom: 1px solid var(--border-subtle);
    background: var(--surface);
    color: var(--text-secondary);
    font: var(--ui-font-body) var(--font-ui);
  }
  .broadcast-heading,
  .destination-row,
  .live-strip {
    display: flex;
    align-items: center;
  }
  .broadcast-heading {
    min-height: 32px;
    gap: 12px;
  }
  .broadcast-heading strong,
  .recipient-menu > strong {
    color: var(--text-primary);
    font-weight: 500;
  }
  .mode-select { flex: none; }
  .mode-select select {
    min-height: 32px;
    padding: 4px 32px 4px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
    background: var(--input-surface);
    color: var(--text-primary);
    font: inherit;
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
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    margin-left: auto;
    border-radius: var(--radius-control);
  }
  .broadcast-form,
  .live-setup {
    display: grid;
    min-width: 0;
    width: 100%;
    overflow: visible;
    gap: 2px;
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
  .destination-row {
    gap: 8px;
    min-width: 0;
    min-height: 32px;
  }
  .recipient-picker {
    position: relative;
    flex: 1;
    min-width: 0;
  }
  .recipient-picker summary {
    display: flex;
    width: max-content;
    max-width: 100%;
    align-items: center;
    gap: 7px;
    min-height: 32px;
    min-width: 0;
    padding: 4px 0;
    border: 0;
    border-radius: var(--radius-control);
    background: transparent;
    color: var(--text-secondary);
    cursor: default;
    list-style: none;
  }
  .recipient-picker summary::-webkit-details-marker {
    display: none;
  }
  .recipient-picker summary:hover {
    background: var(--control-hover);
  }
  .recipient-picker summary:focus-visible,
  .broadcast-bar button:focus-visible {
    outline: 2px solid var(--focus-ring);
    outline-offset: 2px;
  }
  .recipient-count {
    min-width: 0;
    overflow: hidden;
    color: var(--text-primary);
    font-weight: 500;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .recipient-scope {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .recipient-picker :global(.picker-chevron) {
    flex: none;
    color: var(--text-muted);
  }
  .recipient-menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    z-index: 10;
    width: min(360px, max(100%, 280px));
    max-width: 100cqw;
    max-height: 280px;
    padding: 12px;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-panel);
    background: var(--surface-raised);
    box-shadow: var(--shadow-panel);
  }
  .recipient-list {
    display: grid;
    gap: 2px;
  }
  .scope-options {
    display: flex;
    flex-wrap: wrap;
    min-width: 0;
    gap: 8px 16px;
    margin: 8px 0;
    padding: 0;
    border: 0;
  }
  .scope-options label {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 30px;
    color: var(--text-primary);
  }
  .scope-options input { margin: 0; accent-color: var(--accent); }
  .scope-hint {
    display: block;
    margin-bottom: 4px;
    font-size: var(--ui-font-small);
  }
  .recipient-list label {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 32px;
    padding: 4px 8px;
    border-radius: 4px;
    color: var(--text-primary);
    white-space: nowrap;
  }
  .recipient-list label:hover {
    background: var(--control-hover);
  }
  .recipient-list label.unavailable {
    color: var(--text-muted);
  }
  .recipient-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .recipient-list input {
    appearance: none;
    display: grid;
    place-content: center;
    flex: none;
    width: 18px;
    height: 18px;
    margin: 0;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--input-surface);
  }
  .recipient-list input:checked { border-color: var(--accent); background: var(--accent); }
  .recipient-list input:checked::before {
    content: "";
    width: 5px;
    height: 9px;
    border-right: 2px solid var(--accent-foreground);
    border-bottom: 2px solid var(--accent-foreground);
    transform: translateY(-1px) rotate(45deg);
  }
  .recipient-list input:disabled { opacity: .5; }
  .recipient-status,
  .no-recipients {
    color: var(--text-muted);
    font-size: var(--ui-font-small);
  }
  .no-recipients {
    padding: 8px;
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
  .mode-select select:disabled {
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
  .reminder {
    color: var(--text-secondary);
  }
  .send-shortcut { margin-left: auto; }
  .empty-state { margin: 0; padding: 4px 0 6px; }
  .command-caution {
    color: var(--status-warning);
    font-size: var(--ui-font-small);
  }
  .error {
    color: var(--status-error);
  }
  .live-warning {
    display: block;
    padding: 4px 0;
    font-size: var(--ui-font-small);
    line-height: 1.4;
  }
  .live-warning strong {
    color: var(--status-warning);
    margin-right: 6px;
  }
  .live-warning span {
    color: var(--text-secondary);
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
  @container (max-width: 380px) {
    .broadcast-heading,
    .destination-row { flex-wrap: wrap; }
    .mode-select { order: 3; }
    .recipient-picker { flex-basis: 100%; }
    .send,
    .start-live { margin-left: auto; }
    .live-strip { flex-wrap: wrap; }
    .live-strip strong { white-space: normal; }
  }
</style>

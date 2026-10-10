<script lang="ts">
  import { onDestroy } from "svelte";
  import * as BackupService from "../../bindings/sshbrowse/internal/app/backup";
  import type { Connection } from "../../bindings/sshbrowse/internal/profile/models";
  import { errorMessage } from "./errors";
  import { connectionAddress } from "./sidebarActions";
  import {
    applyBackupPreferences,
    captureBackupPreferences,
    type BackupPreferenceMap,
  } from "./backupPreferences";

  type ImportPreview = {
    token: string;
    connections: Connection[];
    folders: string[];
    preferences: BackupPreferenceMap | null;
    warnings: string[];
    existingCount: number;
    mergeActions: string[];
    mergeError: string;
  };
  type ApplyResult = { added: number; skipped: number; recoveryPath: string };

  let { sessionsOpen, oncomplete, onbusychange }: {
    sessionsOpen: boolean;
    oncomplete: (preferencesChanged: boolean) => Promise<void>;
    onbusychange: (busy: boolean) => void;
  } = $props();

  let busy = $state(false);
  let preview = $state<ImportPreview | null>(null);
  let replaceConnections = $state(false);
  let confirmReplacement = $state(false);
  let restorePreferences = $state(false);
  let includePreferences = $state(true);
  let connectionFilter = $state("");
  let status = $state("");
  let statusIsError = $state(false);
  let lastResult = $state<ApplyResult | null>(null);

  let filteredConnections = $derived((preview?.connections ?? [])
    .map((connection, index) => ({ connection, index }))
    .filter(({ connection }) => [connection.name, connection.host, connection.user, connection.folder]
      .some((value) => value.toLowerCase().includes(connectionFilter.trim().toLowerCase()))));
  let jumpNames = $derived(new Map((preview?.connections ?? []).map((connection) => [connection.id, connection.name])));
  let addCount = $derived(preview?.mergeActions?.filter((action) => action === "add").length ?? 0);
  let skipCount = $derived(preview?.mergeActions?.filter((action) => action === "skip").length ?? 0);
  let copyCount = $derived(preview?.mergeActions?.filter((action) => action === "copy").length ?? 0);
  let sessionBlocked = $derived(sessionsOpen && (replaceConnections || restorePreferences));
  let canApply = $derived(
    preview !== null
      && !busy
      && !sessionBlocked
      && (replaceConnections || !preview?.mergeError)
      && (!replaceConnections || confirmReplacement),
  );

  function setBusy(value: boolean) {
    if (busy === value) return;
    busy = value;
    onbusychange(value);
  }

  function showError(error: unknown) {
    status = errorMessage(error);
    statusIsError = true;
  }

  async function exportBackup() {
    if (busy) return;
    status = "";
    statusIsError = false;
    lastResult = null;
    setBusy(true);
    try {
      const path = await BackupService.Export(includePreferences ? captureBackupPreferences(localStorage) : null);
      if (path) {
        status = "Backup saved.";
      }
    } catch (error) {
      showError(error);
    } finally {
      setBusy(false);
    }
  }

  async function chooseImport() {
    if (busy) return;
    status = "";
    statusIsError = false;
    lastResult = null;
    setBusy(true);
    try {
      if (preview) {
        await BackupService.Cancel(preview.token);
        preview = null;
      }
      const nextPreview = await BackupService.Preview();
      preview = nextPreview as ImportPreview | null;
      connectionFilter = "";
      replaceConnections = false;
      confirmReplacement = false;
      restorePreferences = false;
    } catch (error) {
      showError(error);
    } finally {
      setBusy(false);
    }
  }

  async function cancelPreview() {
    if (!preview || busy) return;
    setBusy(true);
    status = "";
    statusIsError = false;
    try {
      await BackupService.Cancel(preview.token);
      preview = null;
      replaceConnections = false;
      confirmReplacement = false;
      restorePreferences = false;
    } catch (error) {
      showError(error);
    } finally {
      setBusy(false);
    }
  }

  async function applyImport() {
    if (!preview || !canApply) return;
    const currentPreview = preview;
    const preferencesChanged = restorePreferences && currentPreview.preferences !== null;
    const importedPreferences = preferencesChanged ? currentPreview.preferences : null;
    status = "";
    statusIsError = false;
    lastResult = null;
    setBusy(true);
    try {
      const currentPreferences = captureBackupPreferences(localStorage);
      const result = await applyBackupPreferences(localStorage, importedPreferences, () =>
        BackupService.Apply(currentPreview.token, replaceConnections, currentPreferences));
      preview = null;
      lastResult = result as ApplyResult;

      // The backend has committed at this point. A failed UI refresh must not
      // roll preferences back or imply that the imported data was reverted.
      try {
        await oncomplete(preferencesChanged);
        status = "Import complete.";
      } catch (error) {
        status = `Import completed, but the workspace could not refresh: ${errorMessage(error)}`;
        statusIsError = true;
      }
    } catch (error) {
      showError(error);
    } finally {
      setBusy(false);
    }
  }

  onDestroy(() => {
    const token = preview?.token;
    if (token) {
      // The app can close while a preview is waiting for confirmation. Release
      // its short-lived backend token on a best-effort basis during teardown.
      void BackupService.Cancel(token).catch(() => {});
    }
  });
</script>

<section aria-label="Backup actions">
  <p class="intro">Move your saved connections and settings to another computer.</p>

  <div class="backup-card">
    <div class="backup-row">
      <div class="copy-block">
        <strong>Export backup</strong>
        <span>Save all connections and folders to a file.</span>
        <label class="export-preferences">
          <input type="checkbox" bind:checked={includePreferences} disabled={busy} aria-describedby="export-preferences-hint" />
          Include app settings
        </label>
        <small id="export-preferences-hint" class="preferences-hint">Theme, terminal preferences, and interface layout.</small>
      </div>
      <button class="action" type="button" disabled={busy} onclick={exportBackup}>Export backup…</button>
    </div>
    <div class="backup-row">
      <div class="copy-block">
        <strong>Import backup</strong>
        <span>Review a file before adding or restoring connections.</span>
      </div>
      <button class="action" type="button" disabled={busy} onclick={chooseImport}>{preview ? "Choose another file…" : "Import backup…"}</button>
    </div>
  </div>

  <div class="backup-details">
    <p>SSH keys and OpenSSH configuration are not copied. Move them separately and update key paths on the new computer.</p>
    <p>Backups contain hostnames, usernames, and local paths. Keep them private.</p>
  </div>

  {#if status}
    <p class="status" class:error={statusIsError} role={statusIsError ? "alert" : "status"}>{status}</p>
  {/if}
  {#if lastResult}
    <div class="result" role="status">
      <span>Added {lastResult.added}; skipped {lastResult.skipped} unchanged.</span>
      <span>Recovery backup, kept until the next import: <code>{lastResult.recoveryPath}</code></span>
    </div>
  {/if}

  {#if preview}
    <div class="preview" aria-labelledby="preview-heading">
      <div class="preview-heading">
        <div>
          <strong id="preview-heading">Import preview</strong>
          <span>{preview.connections.length} connections · {preview.folders.length} folders</span>
        </div>
        <button class="quiet" type="button" disabled={busy} onclick={cancelPreview}>Cancel import</button>
      </div>

      <fieldset disabled={busy}>
        <legend>How to import</legend>
        <label class="choice">
          <input type="radio" name="import-mode" checked={!replaceConnections} onchange={() => { replaceConnections = false; confirmReplacement = false; }} />
          <span><strong>Add to saved connections</strong><small>Keep existing connections. Skip unchanged records; keep changed records as separate copies.</small></span>
        </label>
        <label class="choice">
          <input type="radio" name="import-mode" checked={replaceConnections} onchange={() => { replaceConnections = true; confirmReplacement = false; }} />
          <span><strong>Replace saved connections</strong><small>Restore the connections and folders in this file.</small></span>
        </label>
      </fieldset>

      {#if replaceConnections}
        <p class="plan">Replace {preview.existingCount} saved connections with {preview.connections.length} from this file.</p>
      {:else if preview.mergeError}
        <p class="block-message" role="alert">Cannot add this backup: {preview.mergeError}</p>
      {:else}
        <p class="plan">{addCount} to add · {skipCount} unchanged · {copyCount} separate {copyCount === 1 ? "copy" : "copies"}</p>
      {/if}

      {#if preview.connections.length > 0}
        <input class="filter" type="search" aria-label="Filter imported connections" placeholder="Filter connections…" bind:value={connectionFilter} />
        <div class="connection-list" role="region" aria-label="Connections in backup">
          {#each filteredConnections as { connection, index } (connection.id)}
            <details class="connection">
              <summary>
                <span class="connection-label"><strong>{connection.name}</strong><small>{connectionAddress(connection)}</small></span>
                <span class="import-action">{replaceConnections ? "Restore" : preview.mergeActions?.[index] === "skip" ? "Skip" : preview.mergeActions?.[index] === "copy" ? "Copy" : preview.mergeError ? "" : "Add"}</span>
              </summary>
              <dl class="connection-details">
                <div><dt>Folder</dt><dd>{connection.folder || "Root"}</dd></div>
                {#if connection.identityFile}<div><dt>Key path</dt><dd>{connection.identityFile}</dd></div>{/if}
                {#if connection.jumpConnectionId}<div><dt>Jump connection</dt><dd>{jumpNames.get(connection.jumpConnectionId) || "Missing from backup"}</dd></div>{/if}
                {#if connection.jumpHost}<div><dt>Jump route</dt><dd>{connection.jumpHost}</dd></div>{/if}
                {#if connection.agentForwarding}<div><dt>Agent forwarding</dt><dd>Enabled</dd></div>{/if}
                {#if connection.x11Forwarding}<div><dt>X11 forwarding</dt><dd>Enabled</dd></div>{/if}
                {#if connection.localForwards?.length}<div><dt>Local forwards</dt><dd>{connection.localForwards.join(", ")}</dd></div>{/if}
                {#if connection.remoteForwards?.length}<div><dt>Remote forwards</dt><dd>{connection.remoteForwards.join(", ")}</dd></div>{/if}
                {#if connection.dynamicForwards?.length}<div><dt>Dynamic forwards</dt><dd>{connection.dynamicForwards.join(", ")}</dd></div>{/if}
              </dl>
            </details>
          {/each}
          {#if filteredConnections.length === 0}<p class="muted">No matching connections.</p>{/if}
        </div>
      {/if}
      {#if preview.folders.length > 0}
        <details class="folders">
          <summary>Review folders</summary>
          <ul>{#each preview.folders as folder (folder)}<li>{folder}</li>{/each}</ul>
        </details>
      {/if}

      {#if preview.warnings.length > 0}
        <div class="warnings" role="status">
          <strong>Before connecting</strong>
          <ul>{#each preview.warnings as warning (warning)}<li>{warning}</li>{/each}</ul>
        </div>
      {/if}

      {#if preview.preferences}
        <label class="choice preferences" class:disabled-choice={sessionsOpen}>
          <input type="checkbox" bind:checked={restorePreferences} disabled={busy || sessionsOpen} />
          <span><strong>Restore app preferences</strong><small>Use the appearance, terminal, and workspace settings from this file.</small></span>
        </label>
      {:else}
        <p class="muted preferences">This file has no app preferences. Your current settings will be kept.</p>
      {/if}
      {#if replaceConnections}
        <label class="confirm-choice">
          <input type="checkbox" bind:checked={confirmReplacement} disabled={busy} />
          <span>I understand this will replace my saved connections and folders.</span>
        </label>
      {/if}
      {#if sessionsOpen}
        <p class="block-message">Close all tabs to replace connections or restore preferences. You can still add connections.</p>
      {/if}
      <div class="preview-actions">
        <button class="action primary" type="button" disabled={!canApply} onclick={applyImport}>{busy ? "Working…" : replaceConnections ? "Restore backup" : "Add connections"}</button>
        <span class="muted">A recovery backup is saved before importing.</span>
      </div>
    </div>
  {/if}
</section>

<style>
  .intro, .copy-block span, .backup-details, .status, .muted { color: var(--text-secondary); font-size: var(--ui-font-small); line-height: 1.45; }
  .intro { margin: 0 0 24px; }
  .backup-card, .preview { border: 1px solid var(--border-subtle); border-radius: var(--radius-panel); background: var(--surface); }
  .backup-card { overflow: hidden; }
  .backup-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 16px; padding: 16px; }
  .backup-row + .backup-row { border-top: 1px solid var(--border-subtle); }
  .copy-block { display: flex; flex: 1 1 280px; min-width: 0; flex-direction: column; gap: 4px; }
  .copy-block strong { font-weight: 400; }
  .export-preferences { display: flex; align-items: center; gap: 9px; min-height: 28px; margin-top: 6px; font-size: var(--ui-font-body); }
  .preferences-hint { margin-left: 27px; color: var(--text-secondary); font-size: var(--ui-font-small); line-height: 1.4; }
  input[type="radio"] { accent-color: var(--accent); }
  input[type="checkbox"] {
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
  input[type="checkbox"]:checked { border-color: var(--accent); background: var(--accent); }
  input[type="checkbox"]:checked::before {
    content: "";
    width: 5px;
    height: 9px;
    border-right: 2px solid var(--accent-foreground);
    border-bottom: 2px solid var(--accent-foreground);
    transform: translateY(-1px) rotate(45deg);
  }
  input[type="checkbox"]:disabled { opacity: .5; }
  .backup-details { display: grid; gap: 8px; margin: 16px 0 0; }
  .backup-details p { margin: 0; }
  .action, .quiet { min-height: 36px; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--toolbar-control); color: var(--text-primary); font: inherit; cursor: default; }
  .action { flex-shrink: 0; padding: 0 12px; }
  .action:hover:not(:disabled), .quiet:hover:not(:disabled) { background: var(--toolbar-control-hover); }
  .action:disabled, .quiet:disabled { opacity: .5; }
  .action.primary { border-color: var(--accent); background: var(--accent); color: var(--accent-foreground); }
  .action.primary:hover:not(:disabled) { background: var(--accent-hover); }
  .quiet { padding: 0 10px; background: transparent; }
  .status { margin: 20px 0 0; color: var(--text-primary); }
  .status.error { color: var(--status-error); }
  .result { display: flex; flex-direction: column; gap: 5px; margin-top: 12px; font-size: var(--ui-font-small); line-height: 1.45; }
  code { overflow-wrap: anywhere; color: var(--text-secondary); font: inherit; }
  .preview { margin-top: 24px; padding: 16px; }
  .preview-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .preview-heading > div { display: flex; flex-direction: column; gap: 3px; }
  .preview-heading > div span { color: var(--text-secondary); font-size: var(--ui-font-small); }
  fieldset { display: grid; gap: 10px; min-width: 0; margin: 18px 0 0; padding: 0; border: 0; }
  legend { margin-bottom: 9px; font-size: var(--ui-font-small); font-weight: 500; }
  .choice, .confirm-choice { display: flex; align-items: flex-start; gap: 10px; font-size: var(--ui-font-small); line-height: 1.4; }
  .choice input, .confirm-choice input { flex: none; margin: 3px 0 0; }
  .choice span { display: flex; flex-direction: column; gap: 2px; }
  .choice small { color: var(--text-secondary); font-size: inherit; }
  .plan { margin: 18px 0 12px; font-size: var(--ui-font-small); }
  .filter { box-sizing: border-box; width: 100%; min-height: 34px; margin-bottom: 8px; padding: 6px 10px; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--surface); color: var(--text-primary); font: inherit; }
  .connection-list { max-height: 280px; overflow: auto; border: 1px solid var(--border-subtle); border-radius: var(--radius-control); }
  .connection-list > p { padding: 0 12px; }
  .connection + .connection { border-top: 1px solid var(--border-subtle); }
  .connection summary { display: flex; align-items: center; gap: 12px; padding: 9px 12px; cursor: default; }
  .connection summary::before { content: "▸"; color: var(--text-secondary); }
  .connection[open] summary::before { content: "▾"; }
  .connection-label { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: 2px; overflow-wrap: anywhere; }
  .connection-label strong { font-size: var(--ui-font-small); font-weight: 500; }
  .connection-label small, .import-action { color: var(--text-secondary); font-size: var(--ui-font-small); }
  .connection-details { display: grid; gap: 5px; margin: 0; padding: 0 12px 12px 32px; font-size: var(--ui-font-small); }
  .connection-details div { display: grid; grid-template-columns: 120px 1fr; gap: 12px; }
  .connection-details dt { color: var(--text-secondary); }
  .connection-details dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
  .folders { margin-top: 12px; font-size: var(--ui-font-small); }
  .folders ul { max-height: 180px; overflow: auto; padding-left: 20px; }
  .warnings { margin-top: 14px; padding: 10px 12px; border-radius: var(--radius-control); background: var(--surface-raised); font-size: var(--ui-font-small); }
  .warnings ul { margin: 6px 0 0; padding-left: 18px; }
  .warnings li + li { margin-top: 5px; }
  .preferences, .confirm-choice { margin-top: 16px; }
  .disabled-choice { opacity: .6; }
  .block-message { margin: 12px 0 0; color: var(--status-error); font-size: var(--ui-font-small); line-height: 1.4; }
  .preview-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; margin-top: 16px; }
  @media (max-width: 560px) {
    .preview-heading { align-items: flex-start; flex-direction: column; }
    .connection-details div { grid-template-columns: 1fr; gap: 2px; }
  }
</style>

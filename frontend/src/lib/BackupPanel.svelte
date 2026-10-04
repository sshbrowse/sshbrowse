<script lang="ts">
  import { onDestroy } from "svelte";
  import * as BackupService from "../../bindings/sshbrowse/internal/app/backup";
  import type { Connection } from "../../bindings/sshbrowse/internal/profile/models";
  import { errorMessage } from "./errors";
  import {
    applyBackupPreferences,
    captureBackupPreferences,
    type BackupPreferenceMap,
  } from "./backupPreferences";

  type ImportPreview = {
    token: string;
    connections: Connection[];
    folders: string[];
    preferences: BackupPreferenceMap;
    warnings: string[];
    existingCount: number;
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
  let status = $state("");
  let statusIsError = $state(false);
  let lastResult = $state<ApplyResult | null>(null);

  let displayItems = $derived.by(() => {
    if (!preview) return [];
    return [
      ...preview.folders.map((folder) => ({ kind: "Folder", label: folder })),
      ...preview.connections.map((connection) => ({
        kind: "Connection",
        label: connection.name || connection.host || "Unnamed connection",
      })),
    ];
  });
  let visibleItems = $derived(displayItems.slice(0, 20));
  let visibleWarnings = $derived((preview?.warnings ?? []).slice(0, 20));
  let sessionBlocked = $derived(sessionsOpen && (replaceConnections || restorePreferences));
  let canApply = $derived(
    preview !== null
      && !busy
      && !sessionBlocked
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
      const path = await BackupService.Export(captureBackupPreferences(localStorage));
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
    const preferencesChanged = restorePreferences;
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
  <p class="intro">Move your saved connections and settings between computers.</p>

  <div class="backup-card">
    <div class="backup-row">
      <div class="copy-block">
        <strong>Export backup</strong>
        <span>Save connections, folders, and app preferences.</span>
      </div>
      <button class="action" type="button" disabled={busy} onclick={exportBackup}>Export backup</button>
    </div>
    <div class="backup-row">
      <div class="copy-block">
        <strong>Import backup</strong>
        <span>Choose a backup and review its contents.</span>
      </div>
      <button class="action" type="button" disabled={busy} onclick={chooseImport}>{preview ? "Choose another backup…" : "Import backup…"}</button>
    </div>
  </div>

  <div class="backup-details">
    <p>SSH keys and OpenSSH configuration must be copied separately.</p>
    <details>
      <summary>What’s included?</summary>
      <div class="details-content">
        <p>Backups include saved connections, folders, and app preferences.</p>
        <p>SSH keys, OpenSSH configuration, logs, and open tabs are excluded. Identity and key paths are saved as references; copy those files separately.</p>
      </div>
    </details>
  </div>

    {#if status}
      <p class="status" class:error={statusIsError} role={statusIsError ? "alert" : "status"}>{status}</p>
    {/if}

    {#if lastResult}
      <div class="result" role="status">
        <span>Added {lastResult.added}; skipped {lastResult.skipped} existing connections.</span>
        <span>Recovery backup, kept until the next import: <code>{lastResult.recoveryPath}</code></span>
      </div>
    {/if}

    {#if preview}
      <div class="preview" aria-labelledby="preview-heading">
        <div class="preview-heading">
          <div>
            <strong id="preview-heading">Import preview</strong>
            <span>SSHBrowse backup</span>
          </div>
          <button class="quiet" type="button" disabled={busy} onclick={cancelPreview}>Discard preview</button>
        </div>
        <dl class="counts">
          <div><dt>Connections in file</dt><dd>{preview.connections.length}</dd></div>
          <div><dt>Folders in file</dt><dd>{preview.folders.length}</dd></div>
          <div><dt>Connections already saved</dt><dd>{preview.existingCount}</dd></div>
        </dl>

        {#if visibleItems.length > 0}
          <div class="item-preview">
            <strong>Names</strong>
            <ul>
              {#each visibleItems as item, index (`${item.kind}-${index}`)}
                <li><span class="item-kind">{item.kind}</span><span class="item-name">{item.label}</span></li>
              {/each}
            </ul>
            {#if displayItems.length > visibleItems.length}
              <span class="muted">Showing {visibleItems.length} of {displayItems.length} names.</span>
            {/if}
          </div>
        {/if}

        {#if visibleWarnings.length > 0}
          <div class="warnings" role="status">
            <strong>Review these notes</strong>
            <ul>
              {#each visibleWarnings as warning, index (`${index}-${warning}`)}
                <li>{warning}</li>
              {/each}
            </ul>
            {#if preview.warnings.length > visibleWarnings.length}
              <span class="muted">Showing {visibleWarnings.length} of {preview.warnings.length} notes.</span>
            {/if}
          </div>
        {/if}

        <fieldset disabled={busy}>
          <legend>Import mode</legend>
          <label class="choice">
            <input type="radio" name="import-mode" checked={!replaceConnections} onchange={() => { replaceConnections = false; confirmReplacement = false; }} />
            <span><strong>Add connections and folders</strong><small>Keep existing saved data and skip duplicate connections.</small></span>
          </label>
          <label class="choice">
            <input type="radio" name="import-mode" checked={replaceConnections} onchange={() => { replaceConnections = true; confirmReplacement = false; }} />
            <span><strong>Restore backup</strong><small>Replaces saved connections and folders. A recovery backup is kept until the next import.</small></span>
          </label>
          <label class="choice subordinate" class:disabled-choice={sessionsOpen}>
            <input type="checkbox" bind:checked={restorePreferences} disabled={sessionsOpen} />
            <span><strong>Restore app preferences</strong><small>Off by default. Restores the saved appearance and workspace settings.</small></span>
          </label>
        </fieldset>

        {#if replaceConnections}
          <label class="confirm-choice">
            <input type="checkbox" bind:checked={confirmReplacement} disabled={busy} />
            <span>I understand this will replace my saved connections and folders.</span>
          </label>
        {/if}

        {#if sessionsOpen}
          <p class="block-message">Close all tabs to restore a backup or its preferences. You can still add connections.</p>
        {/if}

        <div class="preview-actions">
          <button class="action primary" type="button" disabled={!canApply} onclick={applyImport}>{busy ? "Working…" : replaceConnections ? "Restore backup" : "Add imported items"}</button>
          {#if replaceConnections && confirmReplacement}
            <span class="muted">A recovery backup will be kept at the reported path.</span>
          {/if}
        </div>
      </div>
    {/if}
</section>

<style>
  .intro, .copy-block span, .backup-details, .status, .muted { color: var(--text-secondary); font-size: var(--ui-font-small); line-height: 1.45; }
  .intro { margin: 0 0 24px; }
  .backup-card, .preview { border: 1px solid var(--border-subtle); border-radius: var(--radius-panel); background: var(--surface); }
  .backup-card { overflow: hidden; }
  .backup-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 16px; min-height: 74px; padding: 12px 16px; }
  .backup-row + .backup-row { border-top: 1px solid var(--border-subtle); }
  .copy-block { display: flex; flex: 1 1 280px; min-width: 0; flex-direction: column; gap: 3px; }
  .copy-block strong { font-weight: 400; }
  .backup-details { margin: 16px 0 0; }
  .backup-details p { margin: 0; }
  .backup-details details { margin-top: 8px; }
  .backup-details summary { width: fit-content; color: var(--text-primary); cursor: default; }
  .details-content { display: grid; gap: 8px; max-width: 62ch; margin-top: 12px; }
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
  .counts { display: flex; flex-wrap: wrap; gap: 12px 28px; margin: 16px 0; }
  .counts div { display: flex; flex-direction: column; gap: 3px; }
  .counts dt { color: var(--text-secondary); font-size: var(--ui-font-small); }
  .counts dd { margin: 0; font-variant-numeric: tabular-nums; }
  .item-preview, .warnings { margin-top: 14px; font-size: var(--ui-font-small); }
  .item-preview ul, .warnings ul { max-height: 190px; overflow: auto; margin: 7px 0; padding-left: 0; list-style: none; }
  .item-preview li { display: flex; gap: 10px; min-width: 0; padding: 4px 0; border-top: 1px solid var(--border-subtle); }
  .item-kind { flex: 0 0 82px; color: var(--text-secondary); }
  .item-name { min-width: 0; overflow-wrap: anywhere; }
  .warnings { padding: 11px 12px; border: 1px solid var(--border-subtle); border-radius: var(--radius-control); background: var(--surface-raised); }
  .warnings ul { max-height: 140px; padding-left: 18px; list-style: disc; }
  .warnings li { margin: 3px 0; overflow-wrap: anywhere; }
  fieldset { display: grid; gap: 10px; min-width: 0; margin: 18px 0 0; padding: 0; border: 0; }
  legend { margin-bottom: 9px; font-size: var(--ui-font-small); font-weight: 500; }
  .choice, .confirm-choice { display: flex; align-items: flex-start; gap: 10px; font-size: var(--ui-font-small); line-height: 1.4; }
  .choice input, .confirm-choice input { flex: none; margin: 3px 0 0; accent-color: var(--accent); }
  .choice span { display: flex; flex-direction: column; gap: 2px; }
  .choice small { color: var(--text-secondary); font-size: inherit; }
  .subordinate { margin-left: 25px; }
  .disabled-choice { opacity: .6; }
  .confirm-choice { margin-top: 14px; }
  .block-message { margin: 12px 0 0; color: var(--status-error); font-size: var(--ui-font-small); line-height: 1.4; }
  .preview-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 3px 12px; margin-top: 14px; }
  .preview-actions .muted { overflow-wrap: anywhere; }
  @media (max-width: 560px) {
    .preview-heading { align-items: flex-start; flex-direction: column; }
    .counts { gap: 12px 20px; }
    .item-kind { flex-basis: 74px; }
  }
</style>

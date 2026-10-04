<script lang="ts">
  import { untrack } from "svelte";
  import X from "@lucide/svelte/icons/x";
  import type { Connection } from "../../bindings/sshbrowse/internal/profile/models";
  import { workspaceSummary, type SavedLayout, type SavedWorkspace } from "./savedWorkspaces";
  import { errorMessage } from "./errors";

  let { workspaces, recovery, recoveryUnavailable, current, connections, reviewRecovery, onsave, onrename, ondelete, onopen, onrestore, ondiscard, onclose }: {
    workspaces: SavedWorkspace[];
    recovery: SavedLayout | null;
    recoveryUnavailable: boolean;
    current: SavedLayout;
    connections: Connection[];
    reviewRecovery: boolean;
    onsave: (name: string) => Promise<void>;
    onrename: (id: string, name: string) => Promise<void>;
    ondelete: (id: string) => Promise<void>;
    onopen: (layout: SavedLayout) => Promise<void>;
    onrestore: () => Promise<void>;
    ondiscard: () => Promise<void>;
    onclose: () => void;
  } = $props();

  type Action = { kind: "open" | "rename" | "delete"; workspace: SavedWorkspace } | { kind: "restore" | "discard" };
  let action = $state<Action | null>(untrack(() => reviewRecovery ? { kind: recoveryUnavailable ? "discard" : "restore" } : null));
  let name = $state("");
  let renamedName = $state("");
  let error = $state("");
  let busy = $state(false);
  let dialog: HTMLDialogElement;
  let summary = $derived(action?.kind === "open" ? workspaceSummary(action.workspace.layout, connections)
    : action?.kind === "restore" && recovery ? workspaceSummary(recovery, connections) : null);
  let currentSummary = $derived(workspaceSummary(current, connections));

  $effect(() => { if (!dialog.open) dialog.showModal(); });
  export function dismiss() { if (!busy && dialog.open) dialog.close(); }
  function choose(next: Action) {
    action = next;
    error = "";
    if (next.kind === "rename") renamedName = next.workspace.name;
  }
  async function run(operation: () => Promise<void>, close = false) {
    if (busy) return;
    busy = true;
    error = "";
    try {
      await operation();
      action = null;
      name = "";
      if (close) dialog.close();
    } catch (err) { error = errorMessage(err); }
    finally { busy = false; }
  }
  function confirm() {
    const selected = action;
    if (!selected) return;
    switch (selected.kind) {
      case "open": return run(() => onopen(selected.workspace.layout), true);
      case "restore": return run(onrestore, true);
      case "discard": return run(ondiscard);
      case "rename": return run(() => onrename(selected.workspace.id, renamedName));
      case "delete": return run(() => ondelete(selected.workspace.id));
    }
  }
</script>

<dialog bind:this={dialog} onclose={onclose} oncancel={(event) => { if (busy) event.preventDefault(); }} aria-labelledby="workspace-heading">
  <header>
    <h2 id="workspace-heading">Workspaces</h2>
    <button type="button" class="close" aria-label="Close workspaces" onclick={dismiss} disabled={busy}><X size={16} /></button>
  </header>
  <div class="body">
    {#if action}
      <form onsubmit={(event) => { event.preventDefault(); confirm(); }}>
        {#if action.kind === "open" || action.kind === "restore"}
          <h3>{action.kind === "open" ? `Open “${action.workspace.name}”?` : "Restore previous workspace?"}</h3>
          <p>This adds {summary?.tabs ?? 0} {summary?.tabs === 1 ? "tab" : "tabs"} and starts {summary?.remote ?? 0} SSH/SFTP {summary?.remote === 1 ? "session" : "sessions"} and {summary?.local ?? 0} local {summary?.local === 1 ? "terminal" : "terminals"}. Existing sessions stay open.</p>
          {#if summary?.unavailable}<p>{summary.unavailable} panes will be unavailable because their connections are missing or were not saved.</p>{/if}
          <p>Connections use their current saved settings.</p>
        {:else if action.kind === "rename"}
          <h3>Rename “{action.workspace.name}”</h3>
          <label for="renamed-workspace">Name</label>
          <input id="renamed-workspace" bind:value={renamedName} required maxlength="100" autocomplete="off" />
        {:else if action.kind === "delete"}
          <h3>Delete “{action.workspace.name}”?</h3>
          <p>This removes the saved workspace. Its open sessions stay open.</p>
        {:else}
          <h3>Discard previous workspace?</h3>
          <p>This removes {recoveryUnavailable ? "the unreadable recovery data" : "the recovery offer"} and keeps your current tabs. It cannot be undone.</p>
        {/if}
        <div class="actions">
          <button type="button" disabled={busy} onclick={() => { action = null; error = ""; }}>Back</button>
          <button type="submit" class="primary" disabled={busy}>
            {action.kind === "open" ? "Open workspace" : action.kind === "restore" ? "Restore workspace" : action.kind === "rename" ? "Rename" : action.kind === "delete" ? "Delete workspace" : "Discard recovery"}
          </button>
        </div>
      </form>
    {:else}
      {#if recovery || recoveryUnavailable}
        <section aria-label="Restart recovery">
          <h3>Previous workspace</h3>
          <p>{recoveryUnavailable ? "Recovery data could not be read. Discard it to enable automatic saving again." : "Available until restored or discarded."}</p>
          <div class="actions">
            <button disabled={busy} onclick={() => choose({ kind: "discard" })}>Discard…</button>
            {#if recovery}<button disabled={busy} onclick={() => choose({ kind: "restore" })}>Restore…</button>{/if}
          </div>
        </section>
      {/if}
      <form onsubmit={(event) => { event.preventDefault(); run(() => onsave(name)); }}>
        <h3>Save current workspace</h3>
        <label for="workspace-name">Name</label>
        <!-- svelte-ignore a11y_autofocus -- initial focus belongs inside this modal -->
        <input id="workspace-name" bind:value={name} required maxlength="100" autocomplete="off" autofocus />
        <p>Save tab order, tiles, active tab, and selected panes.</p>
        {#if currentSummary.unavailable}<p>{currentSummary.unavailable} panes have no available saved connection. Unsaved host settings are not stored.</p>{/if}
        <div class="actions"><button type="submit" class="primary" disabled={busy || current.tabs.length === 0}>Save workspace</button></div>
      </form>
      <section aria-label="Saved workspaces">
        <h3>Saved workspaces</h3>
        {#if workspaces.length === 0}<p>No saved workspaces.</p>{/if}
        <ul>
          {#each workspaces as workspace (workspace.id)}
            <li>
              <span class="name">{workspace.name}</span>
              <button disabled={busy} aria-label={`Open ${workspace.name}`} onclick={() => choose({ kind: "open", workspace })}>Open…</button>
              <button disabled={busy} aria-label={`Rename ${workspace.name}`} onclick={() => choose({ kind: "rename", workspace })}>Rename…</button>
              <button disabled={busy} aria-label={`Delete ${workspace.name}`} onclick={() => choose({ kind: "delete", workspace })}>Delete…</button>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  </div>
</dialog>

<style>
  dialog { width: min(580px, calc(100vw - 32px)); max-height: calc(100vh - 48px); box-sizing: border-box; padding: 0; border: 1px solid var(--border); border-radius: var(--radius-dialog); background: var(--surface-overlay); color: var(--text-primary); font: var(--ui-font-body) var(--font-ui); box-shadow: var(--shadow-panel); overflow: auto; }
  dialog::backdrop { background: var(--overlay-backdrop); }
  header { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 15px 18px; }
  h2 { margin: 0; font-size: var(--ui-font-heading); font-weight: 500; }
  h3 { margin: 0 0 10px; font-size: var(--ui-font-body); font-weight: 500; }
  .body { padding: 0 18px 18px; }
  section, form { margin: 0; padding: 14px 0; }
  section + form, form + section { border-top: 1px solid var(--border); }
  p { margin: 8px 0; color: var(--text-secondary); line-height: 1.45; }
  label { display: block; margin-bottom: 6px; color: var(--text-secondary); }
  input { width: 100%; box-sizing: border-box; min-height: 36px; padding: 7px 9px; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--input-surface); color: var(--text-primary); font: inherit; }
  ul { padding: 0; margin: 0; list-style: none; }
  li { display: flex; align-items: center; gap: 6px; padding: 6px 0; }
  .name { flex: 1; overflow: hidden; text-overflow: ellipsis; }
  button { min-height: 30px; padding: 0 10px; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--surface-raised); color: var(--text-primary); font: inherit; }
  button:hover:not(:disabled) { background: var(--control-hover); }
  button:focus-visible { outline: 2px solid var(--focus-ring); outline-offset: 2px; }
  .close { display: grid; place-items: center; padding: 0; width: 30px; border: 0; background: transparent; color: var(--text-muted); }
  .actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }
  .primary { border-color: var(--accent); background: var(--accent); color: var(--accent-foreground); }
  .primary:hover:not(:disabled) { background: var(--accent-hover); }
  .error { color: var(--status-error); }
</style>

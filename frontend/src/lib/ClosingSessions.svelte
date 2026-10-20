<script lang="ts">
  let dialog: HTMLDialogElement;
  $effect(() => { dialog.showModal(); });
</script>

<dialog bind:this={dialog} oncancel={(event) => event.preventDefault()} aria-labelledby="closing-heading">
  <span class="spinner" aria-hidden="true"></span>
  <h2 id="closing-heading">Closing sessions…</h2>
</dialog>

<style>
  dialog {
    width: min(360px, calc(100vw - 32px));
    padding: 24px;
    border: 1px solid var(--border);
    border-radius: var(--radius-dialog);
    background: var(--surface-overlay);
    color: var(--text-primary);
    font: var(--ui-font-body) var(--font-ui);
    text-align: center;
    box-shadow: var(--shadow-panel);
  }
  dialog::backdrop { background: var(--overlay-backdrop); }
  h2 { margin: 14px 0 0; font-size: var(--ui-font-dialog-title); font-weight: 500; }
  .spinner {
    display: inline-block;
    width: 24px;
    height: 24px;
    border: 2px solid var(--border);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spinner { animation: none; } }
</style>

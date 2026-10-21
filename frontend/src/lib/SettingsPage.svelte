<script lang="ts">
  import { onMount } from "svelte";
  import BackupPanel from "./BackupPanel.svelte";
  import ArrowLeft from "@lucide/svelte/icons/arrow-left";
  import Check from "@lucide/svelte/icons/check";
  import Palette from "@lucide/svelte/icons/palette";
  import SlidersHorizontal from "@lucide/svelte/icons/sliders-horizontal";
  import Terminal from "@lucide/svelte/icons/terminal";
  import X from "@lucide/svelte/icons/x";
  import { customThemeStyle, defaultCustomPalette, isHexColor, type CustomPalette } from "./customPalette";
  import type { TerminalColors, TerminalFontName, ThemeName } from "./appearance";
  import type { UpdateInfo } from "./menuEvents";
  import { terminalScrollbackMinimum, terminalScrollbackMaximum, type UiSize } from "./storage";
  import { canCheckForUpdates } from "./updates";

  let {
    sessionsOpen,
    backupBusy,
    onbackupcomplete,
    onbackupbusychange,
    themeName,
    customPalette,
    terminalColors,
    uiSize,
    interfaceScale,
    terminalFontSize,
    terminalFontName,
    rightClickToPaste,
    copyOnSelection,
    terminalPasteWarningsDisabled,
    terminalScrollbackLines,
    sidebarWidth,
    updateInfo,
    updateStatus,
    updateError,
    updateBusy,
    updateAvailable,
    updateReady,
    checkUpdatesOnStartup,
    updateReleaseURL,
    loggingDirectory = "",
    loggingSettingsLoading = false,
    loggingSettingsReady = true,
    loggingSettingsBusy = false,
    loggingSettingsStatus = "",
    loggingSettingsError = "",
    onreloadloggingsettings = () => {},
    onchooseloggingdirectory = () => {},
    onstartupupdatechange,
    onreleasenotes,
    oncheckforupdates,
    ondownloadupdate,
    onrestartupdate,
    onthemechange,
    oncustompalettechange,
    onterminalcolorschange,
    onuisizechange,
    oninterfacescalechange,
    onterminalfontsizechange,
    onterminalfontchange,
    onrightclickpastechange,
    oncopyselectionchange,
    onpastewarningschange,
    onterminalscrollbackchange,
    onclose,
  }: {
    sessionsOpen: boolean;
    backupBusy: boolean;
    onbackupcomplete: (preferencesChanged: boolean) => Promise<void>;
    onbackupbusychange: (busy: boolean) => void;
    themeName: ThemeName;
    customPalette: CustomPalette;
    terminalColors: TerminalColors;
    uiSize: UiSize;
    interfaceScale: number;
    terminalFontSize: number;
    terminalFontName: TerminalFontName;
    rightClickToPaste: boolean;
    copyOnSelection: boolean;
    terminalPasteWarningsDisabled: boolean;
    terminalScrollbackLines: number;
    sidebarWidth: number;
    updateInfo: UpdateInfo | null;
    updateStatus: string;
    updateError: boolean;
    updateBusy: boolean;
    updateAvailable: boolean;
    updateReady: boolean;
    checkUpdatesOnStartup: boolean;
    updateReleaseURL: string;
    loggingDirectory?: string;
    loggingSettingsLoading?: boolean;
    loggingSettingsReady?: boolean;
    loggingSettingsBusy?: boolean;
    loggingSettingsStatus?: string;
    loggingSettingsError?: string;
    onreloadloggingsettings?: () => void;
    onchooseloggingdirectory?: () => void;
    onstartupupdatechange: (enabled: boolean) => void;
    onreleasenotes: () => void;
    oncheckforupdates: () => void;
    ondownloadupdate: () => void;
    onrestartupdate: () => void;
    onthemechange: (theme: ThemeName) => void;
    oncustompalettechange: (palette: CustomPalette) => void;
    onterminalcolorschange: (colors: TerminalColors) => void;
    onuisizechange: (size: UiSize) => void;
    oninterfacescalechange: (scale: number) => void;
    onterminalfontsizechange: (size: number) => void;
    onterminalfontchange: (font: TerminalFontName) => void;
    onrightclickpastechange: (enabled: boolean) => void;
    oncopyselectionchange: (enabled: boolean) => void;
    onpastewarningschange: (disabled: boolean) => void;
    onterminalscrollbackchange: (lines: number) => void;
    onclose: () => void;
  } = $props();

  const themes: { name: ThemeName; label: string }[] = [
    { name: "classic", label: "Classic Graphite" },
    { name: "warm", label: "Warm" },
    { name: "moss", label: "Moss" },
    { name: "fjord", label: "Fjord" },
    { name: "oled", label: "OLED Black" },
    { name: "contrast", label: "High Contrast" },
    { name: "custom", label: "Custom" },
  ];

  const paletteFields: { key: keyof CustomPalette; label: string }[] = [
    { key: "surface", label: "Surface" }, { key: "accent", label: "Accent" }, { key: "terminal", label: "Terminal" },
  ];
  let paletteDraft = $state({ ...defaultCustomPalette });
  $effect(() => { paletteDraft = { ...customPalette }; });

  function editPalette(key: keyof CustomPalette, value: string) {
    paletteDraft[key] = value;
    if (isHexColor(value)) oncustompalettechange({ ...customPalette, [key]: value.toLowerCase() });
  }

  type Section = "general" | "appearance" | "terminal";
  const sectionLabels: Record<Section, string> = {
    general: "General",
    appearance: "Appearance",
    terminal: "Terminal",
  };
  const sections: Section[] = ["general", "appearance", "terminal"];
  let selectedSection = $state<Section>("general");
  let pageHeading: HTMLHeadingElement;
  let pageContent: HTMLElement;
  // Apply explicitly: lowering the live limit discards retained terminal output.
  let scrollbackDraft = $state<number | undefined>(undefined);

  $effect(() => {
    scrollbackDraft = terminalScrollbackLines;
  });

  onMount(() => {
    pageHeading.focus();
  });

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "Escape" && !event.defaultPrevented) {
      event.preventDefault();
      onclose();
    }
  }

  function selectSection(section: Section) {
    selectedSection = section;
    pageContent.scrollTop = 0;
  }

  function applyScrollback(event: SubmitEvent) {
    event.preventDefault();
    if (scrollbackDraft !== undefined && Number.isInteger(scrollbackDraft)
      && scrollbackDraft >= terminalScrollbackMinimum && scrollbackDraft <= terminalScrollbackMaximum) {
      onterminalscrollbackchange(scrollbackDraft);
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<section class="settings-screen" aria-label="Settings">
  <div class="settings-layout" style={`--settings-nav-width: ${Math.max(220, sidebarWidth)}px`}>
    <nav class="settings-nav" aria-label="Settings sections">
      <div class="nav-heading"><span>SSHBrowse</span><strong>Settings</strong></div>
      <div class="nav-links">
        {#each sections as section}
          <button
            type="button"
            disabled={backupBusy}
            class:active={selectedSection === section}
            aria-current={selectedSection === section ? "page" : undefined}
            onclick={() => selectSection(section)}
          >
            {#if section === "general"}
              <SlidersHorizontal class="nav-icon" size={15} aria-hidden="true" />
            {:else if section === "appearance"}
              <Palette class="nav-icon" size={15} aria-hidden="true" />
            {:else}
              <Terminal class="nav-icon" size={15} aria-hidden="true" />
            {/if}
            <span>{sectionLabels[section]}</span>
          </button>
        {/each}
      </div>
      <button class="back" type="button" disabled={backupBusy} onclick={onclose}><ArrowLeft size={16} /> Back to workspace</button>
    </nav>
    <div class="settings-main">
      <button class="close" type="button" disabled={backupBusy} aria-label="Close Settings" title="Close Settings" onclick={onclose}><X size={16} /></button>
      <main bind:this={pageContent} class="page-content">
        <div class="page-inner">
          <header class="page-heading">
            <h1 bind:this={pageHeading} tabindex="-1">{sectionLabels[selectedSection]}</h1>
          </header>
          {#if selectedSection === "general"}
            <section aria-labelledby="updates-heading">
              <div class="section-heading">
                <h2 id="updates-heading">Updates</h2>
              </div>
              <div class="setting-group">
                <div class="setting-row">
                  <div><strong>Current version</strong><span>{updateInfo?.version ?? "Loading…"}</span></div>
                  {#if canCheckForUpdates(updateInfo?.availability)}
                    {#if updateReady}
                      <button class="update-button" type="button" disabled={backupBusy || updateBusy} onclick={onrestartupdate}>Restart to update</button>
                    {:else if updateAvailable}
                      {#if updateInfo?.availability === "supported"}
                        <button class="update-button" type="button" disabled={backupBusy || updateBusy} onclick={ondownloadupdate}>Download update</button>
                      {:else}
                        <button class="update-button" type="button" disabled={backupBusy} onclick={onreleasenotes}>View release</button>
                      {/if}
                    {:else}
                      <button class="update-button" type="button" disabled={backupBusy || updateBusy} onclick={oncheckforupdates}>Check for updates</button>
                    {/if}
                  {/if}
                </div>
                {#if canCheckForUpdates(updateInfo?.availability)}
                  <div class="setting-row">
                    <div><strong>Check for updates on startup</strong></div>
                    <button class="switch" type="button" role="switch" disabled={backupBusy} aria-label="Check for updates on startup" aria-checked={checkUpdatesOnStartup} onclick={() => onstartupupdatechange(!checkUpdatesOnStartup)}><span></span></button>
                  </div>
                {/if}
                {#if updateInfo?.message && updateInfo.availability !== "supported"}
                  <p class="update-guidance">{updateInfo.message}</p>
                {/if}
                {#if updateStatus}
                  <p class="update-status" class:error={updateError} role={updateError ? "alert" : "status"}>{updateStatus}</p>
                {/if}
                {#if updateReleaseURL}
                  <p class="update-guidance"><button class="update-button" type="button" disabled={backupBusy} onclick={onreleasenotes}>Release notes ↗</button></p>
                {/if}
              </div>
            </section>
            <section aria-labelledby="backup-heading">
              <div class="section-heading">
                <h2 id="backup-heading">Backup &amp; restore</h2>
              </div>
              <BackupPanel {sessionsOpen} oncomplete={onbackupcomplete} onbusychange={onbackupbusychange} />
            </section>
          {/if}
          {#if selectedSection === "appearance"}
            <section aria-labelledby="theme-heading">
              <div class="section-heading">
                <h2 id="theme-heading">Interface theme</h2>
              </div>
              <div class="theme-grid">
                {#each themes as theme (theme.name)}
                  <button
                    type="button"
                    class="theme-card"
                    class:selected={themeName === theme.name}
                    aria-pressed={themeName === theme.name}
                    onclick={() => onthemechange(theme.name)}
                  >
                    <span class="preview" data-theme={theme.name} data-terminal-colors={terminalColors} style={theme.name === "custom" ? customThemeStyle(customPalette, terminalColors === "neutral") : undefined} aria-hidden="true">
                      <span class="preview-chrome"><i></i><i></i></span>
                      <span class="preview-body">
                        <span class="preview-sidebar"><i></i><i class="selected"><b></b></i><i></i></span>
                        <span class="preview-main">
                          <span class="preview-toolbar"><i></i><i></i><i></i></span>
                          <span class="preview-terminal"><i></i><i></i><i></i></span>
                        </span>
                      </span>
                    </span>
                    <span class="card-label">{theme.label}<span class="selected-mark" aria-hidden="true">{#if themeName === theme.name}<Check size={14} />{/if}</span></span>
                  </button>
                {/each}
              </div>
            </section>
            {#if themeName === "custom"}
              <section aria-labelledby="custom-palette-heading">
                <div class="section-heading">
                  <h2 id="custom-palette-heading">Custom palette</h2>
                  <p>Text and terminal colors adjust automatically for contrast.</p>
                </div>
                <div class="palette-controls">
                  {#each paletteFields as field}
                    <div class="palette-field">
                      <label for={`palette-${field.key}`}>{field.label}</label>
                      <div class="palette-inputs">
                        <input type="color" aria-label={`${field.label} color`} value={customPalette[field.key]} oninput={(event) => editPalette(field.key, event.currentTarget.value)} />
                        <input id={`palette-${field.key}`} class="hex-input" type="text" maxlength="7" spellcheck="false" aria-invalid={!isHexColor(paletteDraft[field.key])} aria-describedby="palette-hint" value={paletteDraft[field.key]} oninput={(event) => editPalette(field.key, event.currentTarget.value)} />
                      </div>
                    </div>
                  {/each}
                </div>
                <p id="palette-hint" class="hint">Six-digit hex colors. Terminal applies with Follow interface.</p>
                <button class="update-button palette-reset" type="button" onclick={() => oncustompalettechange({ ...defaultCustomPalette })}>Reset palette</button>
              </section>
            {/if}
            <section aria-labelledby="terminal-text-heading">
              <div class="section-heading">
                <h2 id="terminal-text-heading">Terminal display</h2>
              </div>
              <div class="setting-group">
                <div class="setting-row">
                  <div><strong>Terminal font</strong><span>Falls back to system monospace</span></div>
                  <div class="select-control">
                    <select aria-label="Terminal font" value={terminalFontName} onchange={(event) => onterminalfontchange(event.currentTarget.value as TerminalFontName)}>
                      <option value="system">System monospace</option>
                      <option value="jetbrains">JetBrains Mono</option>
                      <option value="menlo">Menlo</option>
                      <option value="consolas">Consolas</option>
                      <option value="dejavu">DejaVu Sans Mono</option>
                    </select>
                  </div>
                </div>
                <div class="setting-row">
                  <div><strong>Default font size</strong></div>
                  <div class="stepper" role="group" aria-label="Terminal default font size">
                    <button type="button" aria-label="Decrease terminal default font size" disabled={terminalFontSize <= 8} onclick={() => onterminalfontsizechange(terminalFontSize - 1)}>−</button>
                    <output aria-live="polite">{terminalFontSize} px</output>
                    <button type="button" aria-label="Increase terminal default font size" disabled={terminalFontSize >= 32} onclick={() => onterminalfontsizechange(terminalFontSize + 1)}>+</button>
                  </div>
                </div>
                <div class="setting-row">
                  <div><strong>Terminal colors</strong></div>
                  <div class="segmented" role="group" aria-label="Terminal colors">
                    <button type="button" aria-pressed={terminalColors === "follow"} class:active={terminalColors === "follow"} onclick={() => onterminalcolorschange("follow")}>Follow interface</button>
                    <button type="button" aria-pressed={terminalColors === "neutral"} class:active={terminalColors === "neutral"} onclick={() => onterminalcolorschange("neutral")}>Neutral black</button>
                  </div>
                </div>
              </div>
            </section>
            <section aria-labelledby="type-heading">
              <div class="section-heading">
                <h2 id="type-heading">Interface</h2>
              </div>
              <div class="setting-group">
                <div class="setting-row">
                  <div><strong>Interface text</strong></div>
                  <div class="segmented" role="group" aria-label="Interface text size">
                    <button type="button" aria-pressed={uiSize === "standard"} class:active={uiSize === "standard"} onclick={() => onuisizechange("standard")}>Standard</button>
                    <button type="button" aria-pressed={uiSize === "large"} class:active={uiSize === "large"} onclick={() => onuisizechange("large")}>Large</button>
                  </div>
                </div>
                <div class="setting-row">
                  <div><strong>Interface scale</strong><span>Panels and controls; excludes terminal text</span></div>
                  <div class="stepper" role="group" aria-label="Interface scale">
                    <button type="button" aria-label="Decrease interface scale" disabled={interfaceScale <= 0.8} onclick={() => oninterfacescalechange(interfaceScale - 0.1)}>−</button>
                    <output aria-live="polite">{Math.round(interfaceScale * 100)}%</output>
                    <button type="button" aria-label="Increase interface scale" disabled={interfaceScale >= 1.6} onclick={() => oninterfacescalechange(interfaceScale + 0.1)}>+</button>
                  </div>
                </div>
              </div>
            </section>
          {/if}
          {#if selectedSection === "terminal"}
            <section aria-labelledby="terminal-heading">
              <div class="section-heading">
                <h2 id="terminal-heading">History &amp; input</h2>
              </div>
              <div class="setting-group">
                <div class="setting-row">
                  <div>
                    <label for="terminal-scrollback-lines"><strong>Scrollback lines</strong></label>
                    <span id="scrollback-description">History for all terminals. 0 disables; maximum 50,000 lines.</span>
                  </div>
                  <form class="scrollback-control" onsubmit={applyScrollback}>
                    <input
                      id="terminal-scrollback-lines"
                      type="number"
                      min={terminalScrollbackMinimum}
                      max={terminalScrollbackMaximum}
                      step="1"
                      required
                      aria-describedby="scrollback-description"
                      bind:value={scrollbackDraft}
                    />
                    <button class="update-button" type="submit" disabled={scrollbackDraft === undefined || scrollbackDraft === terminalScrollbackLines}>Apply</button>
                  </form>
                </div>
                <div class="setting-row">
                  <div><strong>Copy selection automatically</strong></div>
                  <button class="switch" type="button" role="switch" aria-label="Copy selection automatically" aria-checked={copyOnSelection} onclick={() => oncopyselectionchange(!copyOnSelection)}><span></span></button>
                </div>
                <div class="setting-row">
                  <div><strong>Right-click to paste</strong><span>Shift + right-click opens the context menu</span></div>
                  <button class="switch" type="button" role="switch" aria-label="Right-click to paste" aria-checked={rightClickToPaste} onclick={() => onrightclickpastechange(!rightClickToPaste)}><span></span></button>
                </div>
                <div class="setting-row">
                  <div><strong>Warn before pasting multiple lines</strong></div>
                  <button class="switch" type="button" role="switch" aria-label="Warn before pasting multiple lines" aria-checked={!terminalPasteWarningsDisabled} onclick={() => onpastewarningschange(!terminalPasteWarningsDisabled)}><span></span></button>
                </div>
              </div>
            </section>
          {/if}
          {#if selectedSection === "terminal"}
            <section aria-labelledby="logging-heading">
              <div class="section-heading">
                <h2 id="logging-heading">Session logs</h2>
                <p>Save terminal output as text, up to 100 MiB per log.</p>
              </div>
              <div class="setting-group">
                <div class="setting-row">
                  <div><strong>Log folder</strong><span>Organized by day</span></div>
                  <div class="directory-picker">
                    <input aria-label="Log folder" class="technical" value={loggingDirectory} title={loggingDirectory} readonly />
                    <button class="update-button" type="button" disabled={loggingSettingsLoading || !loggingSettingsReady || loggingSettingsBusy} onclick={onchooseloggingdirectory}>
                      {loggingSettingsBusy ? "Saving…" : "Choose…"}
                    </button>
                  </div>
                </div>
              </div>
              {#if loggingSettingsError}
                <p class="logging-settings-status error" role="alert">{loggingSettingsError}</p>
                {#if !loggingSettingsReady}
                  <div class="logging-settings-actions">
                    <button class="update-button" type="button" disabled={loggingSettingsLoading} onclick={onreloadloggingsettings}>Retry loading settings</button>
                  </div>
                {/if}
              {:else if loggingSettingsLoading}
                <p class="logging-settings-status" role="status">Loading logging settings…</p>
              {:else if loggingSettingsStatus}
                <p class="logging-settings-status" role="status">{loggingSettingsStatus}</p>
              {/if}
            </section>
          {/if}
        </div>
      </main>
    </div>
  </div>
</section>

<style>
  .settings-screen {
    position: absolute;
    z-index: 10;
    inset: 0;
    overflow: hidden;
    background: var(--canvas);
    color: var(--text-primary);
    font: var(--ui-font-body) var(--font-ui);
  }
  :global(.app.linux-window-chrome) .settings-screen { top: calc(46px * var(--interface-scale)); }
  .settings-layout {
    display: grid;
    grid-template-columns: minmax(170px, min(var(--settings-nav-width), 34%)) minmax(0, 1fr);
    width: 100%;
    height: 100%;
    zoom: var(--interface-scale);
  }
  .settings-nav { display: flex; flex-direction: column; min-width: 0; min-height: 0; border-right: 1px solid var(--sidebar-border); background: var(--sidebar); }
  .nav-heading { display: flex; flex: none; flex-direction: column; gap: 3px; padding: 18px 20px; }
  :global(.app.mac-window-chrome) .nav-heading { padding: 48px 20px 18px; }
  .nav-heading span { color: var(--sidebar-muted-foreground); font-size: var(--ui-font-tiny); }
  .nav-heading strong { color: var(--text-primary); font-size: var(--ui-font-heading); font-weight: 500; }
  .nav-links { display: flex; flex-direction: column; gap: 4px; min-height: 0; overflow-y: auto; padding: 10px; }
  .nav-links button, .back {
    display: flex;
    flex-shrink: 0;
    align-items: center;
    gap: 10px;
    min-height: 36px;
    padding: 8px 12px;
    border: 0;
    border-radius: var(--radius-control);
    background: transparent;
    color: var(--sidebar-muted-foreground);
    font: inherit;
    text-align: left;
    cursor: default;
  }
  .nav-links :global(.nav-icon) { flex: none; color: var(--icon-muted); }
  .nav-links button:hover, .back:hover { background: var(--sidebar-row-hover); color: var(--sidebar-foreground); }
  .nav-links button.active { background: var(--sidebar-row-active); color: var(--text-primary); font-weight: 500; }
  .back { display: flex; align-items: center; gap: 10px; margin: auto 10px 10px; width: calc(100% - 20px); }
  .settings-main { position: relative; display: flex; flex-direction: column; min-width: 0; overflow: hidden; background: var(--canvas); }
  .close {
    position: absolute;
    top: 12px;
    right: 16px;
    z-index: 1;
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    border: 0;
    border-radius: var(--radius-control);
    background: transparent;
    color: var(--icon-muted);
    cursor: default;
  }
  .close:hover { background: var(--control-hover); color: var(--text-primary); }
  .page-content { flex: 1; min-height: 0; overflow-y: auto; padding: 52px clamp(24px, 4vw, 64px) 80px; }
  .page-inner { max-width: 760px; margin: 0 auto; }
  h1 { margin: 0; font-size: var(--ui-font-title); font-weight: 500; line-height: 1.25; letter-spacing: -0.02em; }
  h1:focus { outline: none; }
  .page-heading { margin-bottom: 28px; padding-bottom: 20px; border-bottom: 1px solid var(--border-subtle); }
  .page-inner section + section { margin-top: 32px; }
  .section-heading { margin-bottom: 16px; }
  h2 { margin: 0; font-size: var(--ui-font-heading); font-weight: 500; }
  .section-heading p, .hint { margin: 6px 0 0; color: var(--text-secondary); font-size: var(--ui-font-small); }
  .theme-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 156px), 1fr)); gap: 10px; max-width: 760px; }
  .theme-card {
    min-width: 0;
    padding: 9px;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-panel);
    background: var(--surface);
    color: var(--text-primary);
    font: inherit;
    text-align: left;
    cursor: default;
  }
  .theme-card:hover { background: var(--surface-raised); }
  .theme-card.selected { border-color: var(--accent); box-shadow: none; }
  .preview {
    display: flex;
    height: 64px;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--canvas);
  }
  .preview-chrome { display: flex; flex: none; align-items: center; justify-content: space-between; height: 12px; padding: 0 5px; background: var(--chrome); }
  .preview-chrome i { width: 22%; height: 2px; border-radius: 2px; background: var(--chrome-foreground); opacity: .7; }
  .preview-chrome i:last-child { width: 8%; }
  .preview-body { display: flex; flex: 1; min-height: 0; }
  .preview-sidebar { display: flex; flex: none; flex-direction: column; gap: 3px; width: 28%; padding: 5px 6px; background: var(--sidebar); }
  .preview-sidebar i { display: flex; align-items: center; height: 10px; padding: 0 4px; border-radius: 2px; }
  .preview-sidebar i::after { width: 72%; height: 2px; border-radius: 2px; background: var(--sidebar-muted-foreground); content: ""; opacity: .85; }
  .preview-sidebar i:nth-child(3)::after { width: 55%; }
  .preview-sidebar i.selected { background: var(--sidebar-row-active); }
  .preview-sidebar i.selected::after { background: var(--sidebar-foreground); }
  .preview-sidebar b { width: 3px; height: 3px; margin-right: 4px; border-radius: 50%; background: var(--accent); }
  .preview-main { display: flex; flex: 1; min-width: 0; flex-direction: column; padding: 5px 6px; background: var(--canvas); }
  .preview-toolbar { display: flex; align-items: center; gap: 5px; height: 13px; margin-bottom: 4px; }
  .preview-toolbar i { width: 16%; height: 4px; border-radius: 2px; background: var(--toolbar-foreground); opacity: .55; }
  .preview-toolbar i:first-child { width: 24%; height: 7px; background: var(--accent); opacity: .9; }
  .preview-toolbar i:last-child { margin-left: auto; }
  .preview-terminal { display: flex; flex: 1; flex-direction: column; gap: 4px; padding: 5px 8px; background: var(--terminal-background); }
  .preview-terminal i { display: block; height: 2px; border-radius: 2px; background: var(--terminal-foreground); opacity: .58; }
  .preview-terminal i:nth-child(1) { width: 65%; }
  .preview-terminal i:nth-child(2) { width: 48%; }
  .preview-terminal i:nth-child(3) { width: 36%; opacity: .9; }
  .card-label { display: flex; align-items: center; justify-content: space-between; gap: 6px; margin-top: 9px; font-weight: 500; }
  .selected-mark { color: var(--accent); }
  .setting-group { overflow: hidden; border: 1px solid var(--border-subtle); border-radius: var(--radius-panel); background: var(--surface); }
  .setting-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 16px; min-height: 68px; padding: 14px 18px; }
  .setting-row + .setting-row { border-top: 1px solid var(--border-subtle); }
  .setting-row > div:first-child { flex: 1 1 240px; min-width: 0; }
  .setting-row strong, .setting-row span { display: block; }
  .setting-row strong { font-weight: 400; }
  .setting-row div:first-child span { margin-top: 3px; color: var(--text-secondary); font-size: var(--ui-font-small); }
  .update-button { min-height: 36px; padding: 0 12px; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--toolbar-control); color: var(--text-primary); font: inherit; cursor: default; }
  .update-button:hover:not(:disabled) { background: var(--toolbar-control-hover); }
  .update-button:disabled { opacity: .5; }
  .update-guidance, .update-status { margin: 0; padding: 0 18px 14px; color: var(--text-secondary); font-size: var(--ui-font-small); line-height: 1.4; }
  .update-status { color: var(--text-primary); }
  .update-status.error { color: var(--status-error); }
  .segmented, .stepper { display: flex; flex: none; align-items: center; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--input-surface); }
  .scrollback-control { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; max-width: 100%; }
  .segmented { flex-wrap: wrap; max-width: 100%; }
  .scrollback-control input { width: 110px; min-height: 36px; padding: 0 10px; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--input-surface); color: var(--text-primary); font: inherit; }
  .segmented button, .stepper button { min-height: 34px; padding: 0 10px; border: 0; border-radius: 4px; background: transparent; color: var(--text-secondary); font: inherit; cursor: default; }
  .segmented button:hover, .stepper button:hover:not(:disabled) { background: var(--control-hover); color: var(--text-primary); }
  .segmented button.active { background: var(--selection); color: var(--text-primary); }
  .select-control {
    flex: none;
    width: min(100%, 240px);
  }
  select {
    min-width: 0;
    padding: 7px 32px 7px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
    background: var(--input-surface);
    color: var(--text-primary);
    font: inherit;
  }
  .directory-picker { display: flex; flex: 1 1 340px; align-items: center; gap: 8px; min-width: 0; }
  .directory-picker input { flex: 1; min-width: 0; min-height: 38px; box-sizing: border-box; padding: 0 10px; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--input-surface); color: var(--text-secondary); font: var(--ui-font-small) ui-monospace, "SF Mono", Menlo, monospace; }
  .logging-settings-status { margin: 12px 0 0; color: var(--text-secondary); font-size: var(--ui-font-small); }
  .logging-settings-status.error { color: var(--status-error); }
  .logging-settings-actions { display: flex; justify-content: flex-end; margin-top: 14px; }
  .stepper button { min-width: 34px; font-size: 20px; }
  .stepper button:disabled { opacity: .45; }
  output { min-width: 52px; text-align: center; font-variant-numeric: tabular-nums; }
  .palette-controls { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 16px; }
  .palette-field label { display: block; margin-bottom: 8px; font-size: var(--ui-font-small); color: var(--text-secondary); }
  .palette-inputs { display: flex; align-items: center; gap: 8px; }
  .palette-inputs input { height: 36px; border: 1px solid var(--border); border-radius: var(--radius-control); background: var(--input-surface); color: var(--text-primary); }
  .palette-inputs input[type="color"] { width: 40px; padding: 3px; }
  .hex-input { width: 104px; min-width: 0; padding: 0 8px; font: var(--ui-font-small) ui-monospace, monospace; }
  .hex-input[aria-invalid="true"] { border-color: var(--status-error); }
  .palette-reset { margin-top: 12px; }
  .hint { margin-top: 12px; line-height: 1.4; }
  .switch {
    display: flex;
    flex: none;
    align-items: center;
    width: 46px;
    height: 28px;
    padding: 3px;
    border: 1px solid var(--border);
    border-radius: 20px;
    background: var(--border-subtle);
    cursor: default;
  }
  .switch span { width: 20px; height: 20px; border-radius: 50%; background: var(--text-secondary); transition: transform 120ms ease; }
  .switch[aria-checked="true"] { background: var(--accent); border-color: var(--accent); }
  .switch[aria-checked="true"] span { background: var(--accent-foreground); transform: translateX(18px); }
  @media (prefers-reduced-motion: reduce) {
    .switch span { transition: none; }
  }
  @media (max-width: 920px) {
    .setting-row { align-items: flex-start; flex-direction: column; gap: 9px; }
    .setting-row > div:first-child { flex: none; width: 100%; }
    .directory-picker { flex: none; width: 100%; }
  }
  @media (max-width: 700px) {
    .nav-heading { padding: 16px 12px; }
    .nav-links { padding: 12px 6px; }
    .back { margin: auto 6px 6px; width: calc(100% - 12px); }
    .page-content { padding: 24px 18px 56px; }
  }
</style>

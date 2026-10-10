# Using SSHBrowse

## Connections and sessions

- Create connections with **Shell > New Connection…** or import OpenSSH aliases
  with **Shell > Import from SSH Config…**. Organize them in sidebar folders.
- Double-click a saved connection, or enter `user@host:port` in the new-tab picker.
  The **Shell** menu also opens SFTP sessions and local terminals.
- Use tabs or enable **View > Toggle Tiling Mode** to work with several panes.
- Set a saved connection or raw ProxyJump route as a jump host when needed.

Your system's OpenSSH handles authentication, host keys, and SSH configuration.

## Terminal tools

- **Find in Terminal…** searches the selected pane. Use Cmd+F on macOS or
  Ctrl+Shift+F on Linux and Windows. Adjust retained history in
  **Settings > Terminal > Scrollback lines**.
- Broadcast sends commands or live input to multiple terminals. Choose the mode
  and use the recipient menu to select the tab scope and individual terminals.
  Check the visible recipient count before sending. Live input requires at least
  two live terminals, including the focused terminal; use **Stop** to end it.

## Backup and restore

Use **Settings > Backup & restore** to move connections, folders, and jump-host
links between computers. Export can also include app preferences.

Import previews the changes. **Add to saved connections** keeps your library;
**Replace saved connections** replaces it. Preferences stay unchanged unless
you select **Restore app preferences**. Close all tabs before replacing or
restoring preferences.

SSH keys and OpenSSH configuration must be moved separately; update key paths
on the new computer. Keep backups private. Each import saves the previous setup
to `before-import.sshbrowse.json`, overwritten by the next import. Import it
with **Replace saved connections** to recover your setup.

## Appearance

Settings > Appearance includes compact theme previews, terminal font and color
controls, and interface sizing. Classic Graphite is the default; saved theme
choices are retained. Custom lets you choose surface, accent, and terminal
background colors. Text and ANSI colors adjust for readability. Select Follow
interface to use the custom terminal background, or Neutral black to keep it
black. Custom colors are included in Backup & restore; older backups use the
default custom palette. Restoring new backups with preferences requires a
version of SSHBrowse that supports custom palettes.

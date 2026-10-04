# Backup and restore

Open **Settings > Backup & restore** to export or import your SSHBrowse setup.
Import first shows a preview; it never opens a session.

## SSHBrowse backups

A versioned JSON backup includes saved connections, their order and jump-host
links, folders (including empty folders), SSH-config import history, and portable
preferences such as appearance, clipboard behavior, and scrollback limits.

It includes identity-file paths and SSH host aliases, but does not copy private
keys, OpenSSH configuration, known hosts, agent credentials, terminal output,
logs, open tabs, or window position. Move required SSH files separately and
adjust identity-file paths when changing computers. Treat backups as private:
they contain hostnames, usernames, paths, and forwarding settings.

Import defaults to **Add connections**, preserving the existing library and
preferences. Identical records with the same IDs are skipped. Conflicting IDs
are remapped together with their saved jump-host links. Missing saved jump
connections remain missing rather than binding to an unrelated local entry.

**Restore backup** replaces connections and folders. Restoring preferences is
optional. Close all tabs before replacing the setup or restoring preferences.
Review the preview and explicitly confirm replacement. Malformed files,
unsupported versions, duplicate IDs, cyclic jump routes, and oversized libraries
are rejected before changing connections. A library changed since preview must
be previewed again. Restore can replace a current library with invalid saved
jump routing; adding connections requires valid current routing.

Before each import, SSHBrowse saves the previous setup to
`before-import.sshbrowse.json` beside its connection file. This recovery file is
replaced by the next import. Copy or export it elsewhere if you need to retain
it. Import that file with **Restore backup** to recover the previous setup.

If the current library already has cyclic or overlong jump routes, exports and
the recovery file preserve those errors. Import rejects those files until the
routes are corrected. Use a healthy backup to replace damaged routing; retain
the recovery file for inspecting or manually repairing the old entries.

Connection changes use one locked atomic file replacement. Preferences use
WebView storage: failed writes or a rejected connection import trigger a
preference rollback, and rollback failures are reported. An application crash
between preference and connection writes can leave preferences partially
restored; the two stores do not form a single crash-safe transaction.

Backup files are limited to 2 MiB. The saved connection library retains
its 1 MiB limit. A preview expires after 30 minutes. Import requires a readable,
current connection file in its supported JSON format.

# SSHBrowse

SSHBrowse is a desktop app for SSH, SFTP, and local terminals on macOS, Windows, and Linux. It is built for people looking for a modern alternative to SecureCRT, MobaXterm, and other session managers.

Organize connections, work in tabs or tiles, and send commands or live input to multiple terminals at once.

Currently in beta. Feedback, bug reports, and contributions are welcome.

[Releases](https://github.com/sshbrowse/sshbrowse/releases) · [Report a bug](https://github.com/sshbrowse/sshbrowse/issues/new/choose) · [Contribute](CONTRIBUTING.md)

[![Four SSHBrowse sessions connected to an EVPN network lab in a tiled workspace](assets/network-lab.png)](assets/network-lab.png)

## Your OpenSSH, not ours

SSHBrowse uses your system's `ssh` and `sftp` clients for connections, authentication, host-key verification, and SSH configuration.

It does not proxy SSH traffic or store passwords or private keys.

## Highlights

- Save and organize SSH connections in folders
- Import aliases from your OpenSSH config
- Connect through jump hosts or ProxyJump routes
- Open SSH, SFTP, and local terminal sessions
- Search terminal output and configure scrollback history
- [Record terminal output](docs/usage.md#session-logging) to text files
- Work with tabs or tiled terminals
- Save and reopen named workspace layouts
- Send commands or live input to multiple sessions

## Platforms

| Platform | Package | Notes |
| --- | --- | --- |
| macOS 15+ (Apple silicon and Intel) | Universal DMG | Not notarized; Gatekeeper may require manual approval. |
| Linux x86-64 | Binary, DEB or RPM | Requires GTK4, WebKitGTK 6.0, and OpenSSH. Packages are not repository-signed. |
| Windows 11 x64 | Per-user installer | Requires OpenSSH Client and WebView2 Runtime. Installer is unsigned; SmartScreen may warn. |

Choose the package for your architecture and verify its SHA-256 hash against `SHA256SUMS`.

## Get started

Download the latest package from [Releases](https://github.com/sshbrowse/sshbrowse/releases).

For Linux, see the [installation guide](docs/installation.md).

After launching SSHBrowse, you can:

- Create a saved connection
- Import aliases with **Shell > Import from SSH Config…**
- Move saved connections and preferences with **Settings > General > Backup & restore**
- Enter `user@host:port` directly in the new-tab picker

See the [usage guide](docs/usage.md) for session and terminal tools.

## Help and contributing

Release builds check GitHub for updates at startup, at most once a day. Disable checks in **Settings > General > Updates**. Downloads and restarts require your approval. See [Linux update options](docs/installation.md).

For connection problems, try the same destination with system OpenSSH (`ssh alias` or `ssh user@host`). [Report bugs](https://github.com/sshbrowse/sshbrowse/issues/new/choose) with credentials and private host details removed. Report vulnerabilities privately through [SECURITY.md](SECURITY.md).

Suggestions and focused pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licensing

SSHBrowse is free for personal, non-commercial use. Professional, workplace, or commercial use requires a paid license.

The source code is publicly available for inspection and private personal modification. Independent redistribution, including modified versions, requires authorization; GitHub's viewing and forking rights still apply.

See [LICENSE](LICENSE) for SSHBrowse's terms and [THIRD_PARTY_NOTICES.txt](docs/legal/THIRD_PARTY_NOTICES.txt) for third-party licenses. Run `sshbrowse --licenses` to print both.

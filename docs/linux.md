# Linux installation

Requires Linux x86-64, GTK4, WebKitGTK 6.0, and OpenSSH.

## DEB or RPM package

Download the package for your distribution from [Releases](https://github.com/sshbrowse/sshbrowse/releases) and compare its SHA-256 hash with `SHA256SUMS`. Install it through your package manager.

For updates, download and install the newer package.

## Per-user installation

For in-app updates, use the [installer script](../build/linux/install.sh). It verifies binary and icon checksums and installs them with a desktop launcher without sudo. Run as your regular user:

```bash
curl -fsSLO https://github.com/sshbrowse/sshbrowse/releases/latest/download/install.sh && bash install.sh
```

Launch from your desktop menu or `~/.local/bin/sshbrowse`. Use the app for future updates.

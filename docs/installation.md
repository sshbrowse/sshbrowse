# Linux installation

Requires Linux x86-64, GTK4, WebKitGTK 6.0, and OpenSSH.

## DEB or RPM package

Download the package for your distribution from [Releases](https://github.com/sshbrowse/sshbrowse/releases) and compare its SHA-256 hash with `SHA256SUMS`. Install it through your package manager.

To verify downloaded release files together, run `sha256sum --check --ignore-missing SHA256SUMS`. The manifest also lists the icon downloaded from the repository.

For updates, download and install the newer package.

## Per-user installation

For in-app updates, use the [installer script](../build/linux/install.sh) from this repository. It downloads the latest release binary and the icon from that release's source tag, verifies their checksums, and installs them with a desktop launcher without sudo. Run as your regular user:

```bash
curl -fLsS --connect-timeout 15 --max-time 300 \
  -o install.sh https://raw.githubusercontent.com/sshbrowse/sshbrowse/main/build/linux/install.sh &&
bash install.sh
```

Launch from your desktop menu or `~/.local/bin/sshbrowse`. Use the app for future updates.

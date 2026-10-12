#!/usr/bin/env bash
set -euo pipefail
umask 022
fetch() { curl -fLsS --connect-timeout 15 --max-time 300 "$@"; }

[[ "$(uname -s)/$(uname -m)" == Linux/x86_64 ]] || { echo 'This installer requires Linux x86_64.' >&2; exit 1; }
(( EUID != 0 )) || { echo 'Run as your regular user, without sudo.' >&2; exit 1; }
home_dir="$(cd -P -- "$HOME" && printf '%s.' "$PWD")"
home_dir="${home_dir%.}"
case "$home_dir" in *%*|*$'\n'*|*$'\r'*) echo 'Unsupported characters in HOME.' >&2; exit 1 ;; esac
binary="$home_dir/.local/bin/sshbrowse"
[[ ! -e "$binary" && ! -L "$binary" ]] || { echo 'SSHBrowse is already installed; use its in-app updater.' >&2; exit 1; }

mkdir -p -- "$home_dir/.local/bin"
stage="$(mktemp -d "$home_dir/.local/bin/.sshbrowse-install.XXXXXX")"
trap 'rm -rf -- "$stage"' EXIT
cd -- "$stage"
repository=https://github.com/sshbrowse/sshbrowse
release="$(fetch -o /dev/null -w '%{url_effective}' "$repository/releases/latest")"
tag="${release##*/}"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Could not resolve a stable SSHBrowse release.' >&2; exit 1; }
download="${release/\/tag\//\/download\/}"
fetch "$download/SHA256SUMS" -o SHA256SUMS
fetch "$download/SSHBrowse-Linux-x86_64" -o SSHBrowse-Linux-x86_64
# Keep the repository icon aligned with the selected binary release.
fetch "https://raw.githubusercontent.com/sshbrowse/sshbrowse/$tag/build/appicon.png" -o sshbrowse.png
for asset in SSHBrowse-Linux-x86_64 sshbrowse.png; do
  awk -v file="$asset" '$2 == file { print; found = 1; exit } END { exit !found }' SHA256SUMS | sha256sum --check -
done
chmod 0755 SSHBrowse-Linux-x86_64
timeout --kill-after=2s 15s ./SSHBrowse-Linux-x86_64 --licenses >/dev/null

# Exec paths have two escaping layers: desktop strings and quoted arguments.
desktop_exec="${binary//\\/\\\\\\\\}"
desktop_exec="${desktop_exec//\"/\\\\\"}"
desktop_exec="${desktop_exec//\$/\\\\\$}"
desktop_exec="${desktop_exec//\`/\\\\\`}"
cat > sshbrowse.desktop <<EOF
[Desktop Entry]
Type=Application
Name=SSHBrowse
Exec="$desktop_exec"
Icon=sshbrowse
EOF
install -Dm 0644 sshbrowse.png "$home_dir/.local/share/icons/hicolor/256x256/apps/sshbrowse.png"
install -Dm 0644 sshbrowse.desktop "$home_dir/.local/share/applications/io.github.sshbrowse.sshbrowse.desktop"
mv -T -- SSHBrowse-Linux-x86_64 "$binary"
printf 'Installed SSHBrowse. Launch it with %s\n' "$binary"

#!/usr/bin/env bash
set -euo pipefail
installer="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/install.sh"
stage="$(mktemp -d)"
trap 'rm -rf -- "$stage"' EXIT
mkdir -p "$stage/assets" "$stage/tools"
export SSHBROWSE_TEST_ASSETS="$stage/assets"
cat > "$stage/assets/SSHBrowse-Linux-x86_64" <<'SH'
#!/bin/sh
[ "$1" = --licenses ] && exit 0
printf '%s' "$0" > "$SSHBROWSE_TEST_MARKER"
SH
printf 'icon\n' > "$stage/assets/sshbrowse.png"
(cd "$stage/assets" && sha256sum SSHBrowse-Linux-x86_64 sshbrowse.png > SHA256SUMS)
cat > "$stage/tools/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
output='' url=''
while (( $# )); do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    --connect-timeout|--max-time|-w) shift 2 ;;
    https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
if [[ "$url" == https://github.com/sshbrowse/sshbrowse/releases/latest ]]; then
  printf 'https://github.com/sshbrowse/sshbrowse/releases/tag/v1.2.3'
  exit 0
fi
[[ "$url" == https://github.com/sshbrowse/sshbrowse/releases/download/v1.2.3/* ]] || exit 2
asset="${url##*/}"
[[ "$asset" != "${SSHBROWSE_TEST_FAIL:-}" ]] || exit 22
cp -- "$SSHBROWSE_TEST_ASSETS/$asset" "$output"
SH
chmod +x "$stage/tools/curl"
export PATH="$stage/tools:$PATH"
for unsupported in "$stage/home-percent%" "$stage/home-newline"$'\n'; do
  export HOME="$unsupported"
  mkdir -p "$HOME" "${HOME%$'\n'}"
  if bash "$installer" > "$stage/output" 2>&1; then exit 1; fi
  grep -q 'Unsupported characters' "$stage/output"
  [[ ! -e "$HOME/.local" && ! -e "${HOME%$'\n'}/.local" ]]
done
export HOME="$stage/home space \"quote\" dollar\$ backtick\` slash\\"
mkdir -p "$HOME"
bash "$installer" > "$stage/output"
binary="$HOME/.local/bin/sshbrowse"
desktop="$HOME/.local/share/applications/io.github.sshbrowse.sshbrowse.desktop"
[[ -x "$binary" && -f "$desktop" && -f "$HOME/.local/share/icons/hicolor/256x256/apps/sshbrowse.png" ]]
cmp "$binary" "$stage/assets/SSHBrowse-Linux-x86_64"
if bash "$installer" > "$stage/output" 2>&1; then exit 1; fi
grep -q 'already installed' "$stage/output"
cmp "$binary" "$stage/assets/SSHBrowse-Linux-x86_64"
if python3 -c 'import gi; gi.require_version("Gio", "2.0")' >/dev/null 2>&1; then
  export SSHBROWSE_TEST_MARKER="$stage/launched"
  python3 - "$desktop" <<'PY'
import os, sys, time
from gi.repository import Gio
assert Gio.DesktopAppInfo.new_from_filename(sys.argv[1]).launch([], None)
for _ in range(50):
    if os.path.exists(os.environ['SSHBROWSE_TEST_MARKER']):
        break
    time.sleep(0.1)
with open(os.environ['SSHBROWSE_TEST_MARKER']) as marker:
    assert marker.read() == os.path.join(os.environ['HOME'], '.local/bin/sshbrowse')
PY
fi
for failure in download checksum missing-checksum; do
  export HOME="$stage/home-$failure"
  mkdir -p "$HOME"
  case "$failure" in
    download) export SSHBROWSE_TEST_FAIL=sshbrowse.png ;;
    checksum) unset SSHBROWSE_TEST_FAIL; printf 'corrupt\n' >> "$stage/assets/SSHBrowse-Linux-x86_64" ;;
    missing-checksum)
      (cd "$stage/assets" && sha256sum sshbrowse.png > SHA256SUMS) ;;
  esac
  if bash "$installer" > "$stage/output" 2>&1; then echo "$failure unexpectedly succeeded" >&2; exit 1; fi
  [[ ! -e "$HOME/.local/bin/sshbrowse" && ! -e "$HOME/.local/share/applications/io.github.sshbrowse.sshbrowse.desktop" ]]
  [[ -z "$(find "$HOME" -name '.sshbrowse-install.*' -print)" ]]
done
echo 'Linux bootstrap installer checks passed.'

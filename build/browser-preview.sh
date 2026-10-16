#!/usr/bin/env bash
set -euo pipefail

repository_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
port="${1-19245}"
if [[ $# -gt 1 || ! "$port" =~ ^[0-9]{1,5}$ ]] || ((10#$port < 1024 || 10#$port > 65535)); then
  echo "Usage: $0 [port: 1024-65535]" >&2
  exit 2
fi
port="$((10#$port))"

for command in wails3 curl; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "Missing $command. See CONTRIBUTING.md for setup." >&2
    exit 1
  fi
done

umask 077
runtime_dir="$(mktemp -d /tmp/sshbrowse-browser-preview.XXXXXXXX)"
app_pid=""
cleanup() {
  if [[ -n "$app_pid" ]]; then
    kill "$app_pid" 2>/dev/null || true
    for ((attempt = 0; attempt < 150; attempt++)); do
      kill -0 "$app_pid" 2>/dev/null || break
      sleep 0.1
    done
    if kill -0 "$app_pid" 2>/dev/null; then
      kill -KILL "$app_pid" 2>/dev/null || true
    fi
    wait "$app_pid" 2>/dev/null || true
  fi
  rm -rf -- "$runtime_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$repository_dir"
wails3 task build EXTRA_TAGS=server OUTPUT="$runtime_dir/sshbrowse"

mkdir -p "$runtime_dir/home" "$runtime_dir/config" "$runtime_dir/data" "$runtime_dir/cache"
HOME="$runtime_dir/home" XDG_CONFIG_HOME="$runtime_dir/config" \
  XDG_DATA_HOME="$runtime_dir/data" XDG_CACHE_HOME="$runtime_dir/cache" \
  XDG_RUNTIME_DIR="$runtime_dir" SSHBROWSE_HEADLESS_PREVIEW=1 \
  WAILS_SERVER_HOST=127.0.0.1 WAILS_SERVER_PORT="$port" \
  "$runtime_dir/sshbrowse" >"$runtime_dir/app.log" 2>&1 &
app_pid=$!

url="http://127.0.0.1:$port/"
deadline=$((SECONDS + 30))
ready=false
while ((SECONDS < deadline)); do
  if ! kill -0 "$app_pid" 2>/dev/null; then
    echo "Browser preview exited before becoming ready:" >&2
    tail -30 "$runtime_dir/app.log" >&2
    exit 1
  fi
  # Wails logs this after binding. An occupied port must not pass readiness
  # just because another process responds to the HTTP probe.
  if grep -q 'msg="Server mode starting"' "$runtime_dir/app.log" && \
    curl --noproxy '*' --fail --silent --max-time 1 --output /dev/null "$url"; then
    ready=true
    break
  fi
  sleep 0.1
done
if [[ "$ready" != true ]]; then
  echo "Browser preview did not become ready within 30 seconds:" >&2
  tail -30 "$runtime_dir/app.log" >&2
  exit 1
fi

echo "Preview directory: $runtime_dir"
echo "Browser preview: $url"
echo "Use a private browser session for temporary browser preferences."
echo "Press Ctrl-C to stop the server and remove its temporary profile."
wait "$app_pid"

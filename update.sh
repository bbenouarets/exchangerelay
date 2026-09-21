#!/usr/bin/env bash
# Updates a running ExchangeRelay installation:
#   1. pulls the latest changes from /opt/exchangerelay
#   2. rebuilds /usr/local/bin/exchangerelay
#   3. restarts the systemd service
# Requires root.
set -euo pipefail

REPO_URL="${EXCHANGERELAY_REPO:-https://github.com/bbenouarets/exchangerelay.git}"
REPO_BRANCH="${EXCHANGERELAY_BRANCH:-main}"
INSTALL_DIR="/opt/exchangerelay"
BINARY_PATH="/usr/local/bin/exchangerelay"
SERVICE_NAME="exchangerelay"

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

# Never run from a directory that might be removed below.
cwd="$(pwd 2>/dev/null)" || cwd=""
[ -d "$cwd" ] || cd /

[ "$(id -u)" -eq 0 ] || fail "run this script as root (sudo ./update.sh)"

if [ -d "$INSTALL_DIR/.git" ]; then
    log "Pulling latest changes for $REPO_BRANCH"
    git -C "$INSTALL_DIR" fetch --all -q
    git -C "$INSTALL_DIR" checkout -q "$REPO_BRANCH"
    git -C "$INSTALL_DIR" pull -q --ff-only origin "$REPO_BRANCH"
else
    log "No checkout found in $INSTALL_DIR - fresh clone"
    git clone -q --branch "$REPO_BRANCH" --depth 1 "$REPO_URL" "$INSTALL_DIR"
fi

log "Rebuilding $BINARY_PATH"
(cd "$INSTALL_DIR" && go build -o "$BINARY_PATH" .)

log "Restarting $SERVICE_NAME"
systemctl restart "$SERVICE_NAME.service"

systemctl is-active --quiet "$SERVICE_NAME.service" \
    && log "Update finished, service is running" \
    || fail "service failed to start; see 'journalctl -u $SERVICE_NAME'"

#!/usr/bin/env bash
# Installs the ExchangeRelay as a systemd service.
#
# - Downloads the source (git clone, or updates an existing checkout)
# - Installs Go and builds the binary to /usr/local/bin/exchangerelay
# - Prompts for the M365 credentials (Tenant ID, Client ID, Client Secret)
#   at the end and creates /etc/exchangerelay/config.ini
# - Creates and starts the exchangerelay systemd service
#
# Credentials can also be supplied as environment variables:
#   EXCHANGERELAY_TENANT_ID, EXCHANGERELAY_CLIENT_ID, EXCHANGERELAY_CLIENT_SECRET
# or run non-interactively with --yes (empty credentials).
set -euo pipefail

# ---------------------------------------------------------------- paths / settings
REPO_URL="${EXCHANGERELAY_REPO:-https://github.com/bbenouarets/exchangerelay.git}"
REPO_BRANCH="${EXCHANGERELAY_BRANCH:-main}"
INSTALL_DIR="/opt/exchangerelay"
BINARY_PATH="/usr/local/bin/exchangerelay"
CONFIG_DIR="/etc/exchangerelay"
CONFIG_PATH="${CONFIG_DIR}/config.ini"
SERVICE_USER="exchangerelay"
SERVICE_NAME="exchangerelay"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"

GO_MIN_VERSION="1.21"
GO_MAJOR="$(cut -d. -f1 <<< "$GO_MIN_VERSION")"

NON_INTERACTIVE=false
[ "${1:-}" = "--yes" ] && NON_INTERACTIVE=true

# ---------------------------------------------------------------- helpers
log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

running_as_root() { [ "$(id -u)" -eq 0 ]; }

running_as_root || fail "run this script as root (sudo ./install.sh)"

# ---------------------------------------------------------------- previous install?
cleanup_existing() {
    log "Removing previous installation"
    # Name the paths explicitly so bad variable values cannot nuke $HOME.
    if command -v systemctl >/dev/null 2>&1; then
        systemctl stop "$SERVICE_NAME.service" 2>/dev/null || true
        systemctl disable "$SERVICE_NAME.service" 2>/dev/null || true
        rm -f "$UNIT_PATH"
        systemctl daemon-reload 2>/dev/null || true
        systemctl reset-failed "$SERVICE_NAME.service" 2>/dev/null || true
    fi
    [ -d "$INSTALL_DIR" ] && rm -rf "$INSTALL_DIR"
    [ -e "$BINARY_PATH" ] && rm -f "$BINARY_PATH"
    [ -d "$CONFIG_DIR" ] && rm -rf "$CONFIG_DIR"
    log "Old installation removed"
    # The working directory may have been inside one of the deleted paths
    # (e.g. /etc/exchangerelay). cd somewhere valid so getwd() works.
    cd /
}

if [ -d "$INSTALL_DIR" ] || [ -e "$BINARY_PATH" ] || [ -e "$CONFIG_PATH" ]; then
    if [ "$NON_INTERACTIVE" = true ]; then
        log "Existing installation detected - keeping files (use without --yes to be asked)"
    elif [ -t 0 ] || [ -e /dev/tty ]; then
        printf 'Existing installation found:\n'
        [ -d "$INSTALL_DIR" ] && printf '  - source:  %s\n' "$INSTALL_DIR"
        [ -e "$BINARY_PATH" ] && printf '  - binary:  %s\n' "$BINARY_PATH"
        [ -e "$CONFIG_PATH" ] && printf '  - config:  %s\n' "$CONFIG_PATH"
        printf 'Remove these files and reinstall from scratch? [y/N] '
        read -r answer </dev/tty
        case "$answer" in
            [yY]|yes|Yes) cleanup_existing ;;
            *)            log "Keeping existing files; they will be updated/reused" ;;
        esac
    else
        log "Existing installation detected and no TTY - keeping files"
    fi
fi

# ---------------------------------------------------------------- go install
go_available() {
    command -v go >/dev/null 2>&1 || return 1
    ver="$(go version | awk '{print $3}' | tr -d 'go')"
    [ "${ver%%.*}" -ge "$GO_MAJOR" ] 2>/dev/null
}

install_go() {
    if go_available; then
        log "Go $(go version | awk '{print $3}') already installed"
        return
    fi

    log "Installing Go ${GO_MIN_VERSION}+"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq golang-go git
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y golang git
    elif command -v yum >/dev/null 2>&1; then
        yum install -y golang git
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm go git
    elif command -v zypper >/dev/null 2>&1; then
        zypper --non-interactive install go git
    fi

    log "Installing Go ${GO_MIN_VERSION}+ via the system package manager"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq
        apt-get install -y -qq golang-go git
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y golang git
    elif command -v yum >/dev/null 2>&1; then
        yum install -y golang git
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm go git
    elif command -v zypper >/dev/null 2>&1; then
        zypper --non-interactive install go git
    else
        fail "unsupported distribution: install Go ${GO_MIN_VERSION}+ and git manually"
    fi

    go_available || fail "Go could not be installed via the package manager; install Go ${GO_MIN_VERSION}+ manually"
}

# ---------------------------------------------------------------- download & build
install_go
command -v git >/dev/null 2>&1 || fail "git is required but not installed"

if [ -d "$INSTALL_DIR/.git" ]; then
    log "Updating existing checkout in $INSTALL_DIR"
    git -C "$INSTALL_DIR" fetch --all -q
    git -C "$INSTALL_DIR" checkout -q "$REPO_BRANCH"
    git -C "$INSTALL_DIR" pull -q --ff-only origin "$REPO_BRANCH"
else
    log "Cloning $REPO_URL ($REPO_BRANCH) to $INSTALL_DIR"
    git clone -q --branch "$REPO_BRANCH" --depth 1 "$REPO_URL" "$INSTALL_DIR"
fi

log "Building with $(go version)"
(cd "$INSTALL_DIR" && go build -o "$BINARY_PATH" .)
log "Built $BINARY_PATH"

# ---------------------------------------------------------------- service user
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    log "Creating system user $SERVICE_USER"
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER" \
        || useradd --system -s /sbin/nologin "$SERVICE_USER"
fi

mkdir -p "$CONFIG_DIR"

# ---------------------------------------------------------------- systemd unit
log "Writing unit file $UNIT_PATH"
cat > "$UNIT_PATH" <<EOF
[Unit]
Description=ExchangeRelay - IMAP/SMTP to Microsoft Graph proxy
Documentation=https://github.com/bbenouarets/exchangerelay
After=network-online.target
Wants=network-online.target
Requires=network.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
WorkingDirectory=${CONFIG_DIR}
ExecStart=${BINARY_PATH}
Restart=on-failure
RestartSec=5s
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=${CONFIG_DIR}
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF

# ---------------------------------------------------------------- credentials + config
log "Configuring the M365 credentials for $CONFIG_PATH"
TENANT_ID="${EXCHANGERELAY_TENANT_ID:-}"
CLIENT_ID="${EXCHANGERELAY_CLIENT_ID:-}"
CLIENT_SECRET="${EXCHANGERELAY_CLIENT_SECRET:-}"

if [ "$NON_INTERACTIVE" = true ]; then
    log "Non-interactive mode: using credentials from environment variables"
elif [ -n "$TENANT_ID$CLIENT_ID$CLIENT_SECRET" ]; then
    log "Using credentials from environment variables"
elif [ -t 0 ] || [ -e /dev/tty ]; then
    # Read from the terminal, NOT from stdin: this script may have been
    # piped in via curl, and read from stdin would swallow the rest of it.
    printf 'Microsoft M365 TenantId: '
    read -r TENANT_ID </dev/tty
    printf 'Application (client) ClientId: '
    read -r CLIENT_ID </dev/tty
    printf 'ClientSecret: '
    read -rs CLIENT_SECRET </dev/tty
    printf '\n'
else
    log "No TTY and no environment variables set — writing empty credentials"
    log "Edit $CONFIG_PATH afterwards and run 'systemctl restart $SERVICE_NAME'"
fi

log "Writing config to $CONFIG_PATH"
cat > "$CONFIG_PATH" <<EOF
[exchange]
tenant_id = "${TENANT_ID}"
client_id = "${CLIENT_ID}"
client_secret = "${CLIENT_SECRET}"

[server]
# Relax ports to 1025/1143 if you don't want to bind privileged ports
smtp_addr = "0.0.0.0:25"
imap_addr = "0.0.0.0:143"

# Wildcard sender: every host may send from every e-mail address.
# Restrict afterwards with dedicated [host:IP] sections if needed, e.g.:
#   [host:10.0.0.43]
#   emails = mail@host.tld
[host:*]
emails = *
EOF

chown -R "$SERVICE_USER":"$SERVICE_USER" "$CONFIG_DIR"
chmod 640 "$CONFIG_PATH"

# ---------------------------------------------------------------- start service
log "Configuring systemd"
systemctl daemon-reload
systemctl enable "$SERVICE_NAME.service"
systemctl restart "$SERVICE_NAME.service"

systemctl is-active --quiet "$SERVICE_NAME.service" \
    && log "Service is running" \
    || fail "service failed to start; see 'journalctl -u $SERVICE_NAME'"

log "Done."

cat <<EOF

Exchangerelay installed:
  - binary:   $BINARY_PATH
  - config:   $CONFIG_PATH
  - unit:     $UNIT_PATH
  - status:   systemctl status $SERVICE_NAME
  - logs:     journalctl -u $SERVICE_NAME -f

Next steps:
  1. Point your mail clients / devices at this server (SMTP :25, IMAP :143)
  2. Later, restrict senders: replace the [host:*] wildcard in $CONFIG_PATH
EOF

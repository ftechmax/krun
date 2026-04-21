#!/usr/bin/env bash
# Installs krun + krun-helper on Linux.
# Binaries -> /usr/local/bin
# Helper   -> systemd unit at /etc/systemd/system/krun-helper.service (runs as root)
set -euo pipefail

REPO="ftechmax/krun"
ASSET="krun_linux_amd64.zip"
TRAFFIC_MANAGER_MANIFEST="krun-traffic-manager.yaml"
SERVICE_NAME="krun-helper"
INSTALL_DIR="/usr/local/bin"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$SCRIPT_DIR"

VERSION="latest"
KUBECONFIG_PATH=""
SKIP_TRAFFIC_MANAGER=0
UNINSTALL=0
ORIGINAL_ARGS=("$@")
KRUN_CONFIG_DIR="${KRUN_CONFIG_DIR:-${HOME}/.krun}"
KRUN_CONFIG_PATH="${KRUN_CONFIG_PATH:-${KRUN_CONFIG_DIR}/config.json}"
KRUN_TOKEN_PATH="${KRUN_TOKEN_PATH:-${KRUN_CONFIG_DIR}/token.bin}"
DEFAULT_KUBECONFIG="${KRUN_INSTALL_DEFAULT_KUBECONFIG:-${HOME}/.kube/config}"
DEBUG_BUILD_DONE="${KRUN_DEBUG_BUILD_DONE:-0}"

usage() {
    cat <<EOF
Usage: $0 [--version vX.Y.Z|debug] [--kubeconfig <path>] [--uninstall]

Options:
  --version <tag>          Install a specific release tag (default: latest)
                           Use "debug" to build local binaries and apply the local runtime overlay
  --kubeconfig <path>      Kubeconfig to use for traffic-manager deploy
                           (default: ${DEFAULT_KUBECONFIG})
  --skip-traffic-manager   Skip in-cluster traffic-manager deploy/upgrade
  --uninstall              Remove service, binaries
  -h, --help               Show this help
EOF
}

require_value() {
    local flag="$1"
    local value="${2:-}"
    if [[ -z "$value" || "$value" == --* ]]; then
        echo "Missing value for $flag" >&2
        usage
        exit 1
    fi
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --version)              require_value "$1" "${2:-}"; VERSION="$2"; shift 2 ;;
        --kubeconfig)           require_value "$1" "${2:-}"; KUBECONFIG_PATH="$2"; shift 2 ;;
        --skip-traffic-manager) SKIP_TRAFFIC_MANAGER=1; shift ;;
        --uninstall)            UNINSTALL=1; shift ;;
        -h|--help)              usage; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; usage; exit 1 ;;
    esac
done

need_cmd() {
    command -v "$1" >/dev/null 2>&1 || { echo "Required command not found: $1" >&2; exit 1; }
}

ensure_default_config() {
    mkdir -p "$KRUN_CONFIG_DIR"
    if [[ -f "$KRUN_CONFIG_PATH" ]]; then
        echo "Existing config left unchanged: $KRUN_CONFIG_PATH"
        return
    fi

    cat > "$KRUN_CONFIG_PATH" <<'EOF'
{
  "source": {
    "path": "~/git/",
    "search_depth": 2
  },
  "local_registry": "registry:5000",
  "remote_registry": "docker.io/ftechmax"
}
EOF
    echo "Created default config: $KRUN_CONFIG_PATH"
}

write_random_secret() {
    if command -v openssl >/dev/null 2>&1; then
        (umask 077; openssl rand 32 > "$KRUN_TOKEN_PATH")
        return
    fi

    if command -v dd >/dev/null 2>&1; then
        (umask 077; dd if=/dev/urandom of="$KRUN_TOKEN_PATH" bs=32 count=1 2>/dev/null)
        return
    fi

    echo "Required command not found: openssl or dd" >&2
    exit 1
}

ensure_auth_token() {
    mkdir -p "$KRUN_CONFIG_DIR"
    if [[ -f "$KRUN_TOKEN_PATH" ]]; then
        echo "Existing auth token left unchanged: $KRUN_TOKEN_PATH"
        return
    fi

    write_random_secret
    echo "Created auth token: $KRUN_TOKEN_PATH"
}

build_debug_binaries() {
    if [[ "$DEBUG_BUILD_DONE" == "1" ]]; then
        return
    fi

    [[ -f "$REPO_ROOT/Makefile" ]] || { echo "--version debug requires a source checkout with Makefile at $REPO_ROOT" >&2; exit 1; }
    need_cmd make

    echo "Building debug binaries with make build-linux..."
    (cd "$REPO_ROOT" && make build-linux)
}

if [[ $UNINSTALL -eq 0 && "${KRUN_CONFIG_BOOTSTRAPPED:-0}" != "1" ]]; then
    ensure_default_config
    ensure_auth_token
fi

if [[ $UNINSTALL -eq 0 && "$VERSION" == "debug" ]]; then
    build_debug_binaries
fi

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
    echo "Elevation required, relaunching with sudo..."
    exec sudo -E env \
        KRUN_CONFIG_BOOTSTRAPPED=1 \
        KRUN_CONFIG_DIR="$KRUN_CONFIG_DIR" \
        KRUN_CONFIG_PATH="$KRUN_CONFIG_PATH" \
        KRUN_TOKEN_PATH="$KRUN_TOKEN_PATH" \
        KRUN_INSTALL_DEFAULT_KUBECONFIG="$DEFAULT_KUBECONFIG" \
        KRUN_DEBUG_BUILD_DONE=1 \
        "$0" "${ORIGINAL_ARGS[@]}"
fi

stop_service() {
    if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
        systemctl stop "$SERVICE_NAME"
    fi
}

remove_service() {
    stop_service
    if systemctl is-enabled --quiet "$SERVICE_NAME" 2>/dev/null; then
        systemctl disable "$SERVICE_NAME" >/dev/null 2>&1 || true
    fi
    if [[ -f "$UNIT_PATH" ]]; then
        rm -f "$UNIT_PATH"
        systemctl daemon-reload
    fi
}

kubectl_base_args=()
effective_kubeconfig="${KUBECONFIG_PATH:-$DEFAULT_KUBECONFIG}"
if [[ -n "$effective_kubeconfig" ]]; then
    kubectl_base_args+=(--kubeconfig "$effective_kubeconfig")
fi

deploy_traffic_manager() {
    if [[ "$SKIP_TRAFFIC_MANAGER" -eq 1 ]]; then
        echo "Skipping traffic-manager deploy."
        return
    fi

    need_cmd kubectl
    if [[ -n "$effective_kubeconfig" && ! -f "$effective_kubeconfig" ]]; then
        echo "Kubeconfig not found: $effective_kubeconfig" >&2
        echo "Use --kubeconfig <path> or --skip-traffic-manager." >&2
        exit 1
    fi

    if [[ "$VERSION" == "debug" ]]; then
        local overlay="$REPO_ROOT/deploy/runtime/overlays/local"
        [[ -d "$overlay" ]] || { echo "Local traffic-manager overlay not found: $overlay" >&2; exit 1; }

        echo "Applying local traffic-manager overlay: $overlay"
        kubectl "${kubectl_base_args[@]}" apply -k "$overlay"
        return
    fi

    local manifest_url
    if [[ "$VERSION" == "latest" ]]; then
        manifest_url="https://github.com/$REPO/releases/latest/download/$TRAFFIC_MANAGER_MANIFEST"
    else
        manifest_url="https://github.com/$REPO/releases/download/$VERSION/$TRAFFIC_MANAGER_MANIFEST"
    fi

    echo "Applying traffic-manager manifest: $manifest_url"
    kubectl "${kubectl_base_args[@]}" apply -f "$manifest_url"
}

if [[ $UNINSTALL -eq 1 ]]; then
    echo "Uninstalling krun..."
    remove_service
    rm -f "$INSTALL_DIR/krun" "$INSTALL_DIR/krun-helper"
    echo "Uninstall complete."
    exit 0
fi

need_cmd install
need_cmd systemctl

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

stop_service

if [[ "$VERSION" == "debug" ]]; then
    [[ -f "$REPO_ROOT/krun" ]]        || { echo "Missing built binary: $REPO_ROOT/krun" >&2; exit 1; }
    [[ -f "$REPO_ROOT/krun-helper" ]] || { echo "Missing built binary: $REPO_ROOT/krun-helper" >&2; exit 1; }
    cp "$REPO_ROOT/krun" "$REPO_ROOT/krun-helper" "$TMP/"
else
    need_cmd curl
    need_cmd unzip
    if [[ "$VERSION" == "latest" ]]; then
        URL="https://github.com/$REPO/releases/latest/download/$ASSET"
    else
        URL="https://github.com/$REPO/releases/download/$VERSION/$ASSET"
    fi
    echo "Downloading $URL"
    curl -fsSL --retry 3 -o "$TMP/$ASSET" "$URL"
    unzip -q "$TMP/$ASSET" -d "$TMP"
fi

[[ -f "$TMP/krun" ]]        || { echo "krun binary not found in archive" >&2; exit 1; }
[[ -f "$TMP/krun-helper" ]] || { echo "krun-helper binary not found in archive" >&2; exit 1; }

install -m 0755 "$TMP/krun"        "$INSTALL_DIR/krun"
install -m 0755 "$TMP/krun-helper" "$INSTALL_DIR/krun-helper"

cat > "$UNIT_PATH" <<EOF
[Unit]
Description=krun helper daemon
After=network.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/krun-helper --service --config-path "$KRUN_CONFIG_DIR"
Restart=on-failure
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"

deploy_traffic_manager

echo ""
echo "Done. Try: krun version"
echo "Service status: systemctl status $SERVICE_NAME"

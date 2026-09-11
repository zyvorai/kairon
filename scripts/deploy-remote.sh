#!/usr/bin/env bash
# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

# Adapted from ../fabric/scripts/deploy-remote.sh for kairon's architecture:
# kairon-node/kairon-controller are not self-contained daemons (they require
# a reachable Kubernetes API, see internal/kube/client.go) and kairon is Go
# with zero third-party deps, so unlike fabric this script cross-compiles
# locally and ships static binaries instead of syncing source and building
# on the remote host.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT_NAME="$(basename "$0")"
DEPLOY_LAST_FILE="$REPO_ROOT/.deploy-last"

if [[ -t 1 && "${NO_COLOR:-}" != "1" ]]; then
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BLUE=$'\033[34m'; C_RESET=$'\033[0m'
else
  C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''; C_RESET=''
fi
info() { printf '%s[*]%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
ok()   { printf '%s[+]%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '%s[!]%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
err()  { printf '%s[x]%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; }
die()  { err "$*"; exit 1; }
tip()  { printf '%s    tip:%s %s\n' "$C_YELLOW" "$C_RESET" "$*"; }

usage() {
  cat <<EOF
Usage:
  $SCRIPT_NAME USER@HOST [flags]
  $SCRIPT_NAME USER HOST [flags]
  $SCRIPT_NAME HOST USER [flags]        (auto-detected & swapped)
  $SCRIPT_NAME status [USER@HOST]
  $SCRIPT_NAME -h | --help

Deploys kairon-node (+ kaironctl) to a remote Linux host over SSH as a
systemd service. Unlike deploying a self-contained daemon, kairon-node
requires a reachable Kubernetes API (in-cluster, or via --kube-url) to
become ready -- see --kube-url below. A service that starts but reports
"not ready" because no cluster is configured yet is expected, not a bug.

Everything is cross-compiled on your machine with
  CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build
and shipped as a static binary. No Go toolchain and no source tree is
ever copied to or required on the remote host.

Target:
  USER@HOST, or USER HOST, or HOST USER (IPv4-vs-non-IPv4 auto swap).
  An optional trailing bare token after the target is treated as an SSH
  password (exported as SSHPASS; requires the 'sshpass' tool).

Flags:
  --with-controller       Also build/install/enable kairon-controller
                          (systemd unit kairon-controller.service).
  --no-start              Install files but do not enable/start the service(s).
  --sync-only             Copy binaries + unit files to the remote host only;
                          skip user/dir/config/unit install and service start.
  --dry-run               Print the resolved plan; no SSH, no build, no writes.
  --arch=amd64|arm64      Skip the remote 'uname -m' probe.
  --uninstall             Stop/disable/remove the service(s) and binaries.
                          Keeps /etc/kairon and the 'kairon' user.
  --purge                 With --uninstall, also remove /etc/kairon and
                          the 'kairon' system user.
  --kube-url=URL          Seed KAIRON_KUBE_URL (only if env file absent).
  --kube-token=TOKEN      Seed KAIRON_KUBE_TOKEN (only if env file absent).
  --kube-ca=PATH          Seed KAIRON_KUBE_CA (a path on the REMOTE host).
  --kube-insecure         Seed KAIRON_KUBE_INSECURE=true.
  --fluxvm-url=URL        Seed FLUXVM_URL (default http://127.0.0.1:7788).
  --backend=NAME          Seed KAIRON_DEFAULT_BACKEND (default qemu).
  --image-root=PATH       Seed KAIRON_IMAGE_ROOT (default /var/lib/fluxvm/images).
  --node-name=NAME        Seed NODE_NAME (default: the remote's own hostname).
                          Must match the Node object name in your cluster.
  --interval=DURATION     Reconciliation interval baked into the unit (default 3s).
  --node-port=N           kairon-node health port (default 8081). If busy on
                          the remote host and not explicitly set, a random
                          free port is chosen automatically and reported.
  --controller-port=N     kairon-controller health port (default 8080), same
                          auto-fallback-if-busy behavior as --node-port.
  --migration-ca=PATH     Local CA PEM file for the live-migration mTLS peer
                          control plane. All three of --migration-ca/-cert/-key
                          must be given together (fail-closed, matches
                          kairon-node's own validation) -- omit all three to
                          leave live migration disabled (cold migration still
                          works). Copied to /etc/kairon/migration/ on the host.
  --migration-cert=PATH   Local node certificate PEM (needs ExtKeyUsage
                          serverAuth + clientAuth -- this node acts as both
                          migration server and client).
  --migration-key=PATH    Local node private key PEM matching --migration-cert.
  --migration-server-name=NAME  Expected TLS ServerName from migration peers
                          (default kairon-node).
  --migration-port=N      Migration mTLS peer port (default 9443), same
                          auto-fallback-if-busy behavior as --node-port.
  --migration-adapter-socket=PATH  Unix socket path for the local migration
                          adapter (default /run/kairon/migration-adapter.sock).
                          Only meaningful once migration mTLS is configured.
  --with-migration-adapter-stub  Build and install kairon-migration-adapter-stub,
                          a test double that simulates transfers without moving
                          real VM memory -- see docs/migration-adapter.md. Use
                          only for testing the migration control plane; a real
                          deployment needs a real hypervisor-level adapter.
  --version=STRING        Version stamped into the binary (default: git describe,
                          or 'dev' if this checkout isn't a git repository).
  --ssh-port=N            SSH port (default 22, env SSH_PORT).
  -h, --help              Show this help.

Not present here (and why): fabric's --quick / remote-build modes --
kairon has no remote toolchain step to skip, everything is built locally
and shipped as a static binary. fabric's --bind / --open-firewall --
kairon-node's health port is a private liveness/readiness probe, not a
public API to expose.

Examples:
  $SCRIPT_NAME sus@80.79.5.173 --dry-run
  $SCRIPT_NAME 80.79.5.173 sus
  $SCRIPT_NAME sus@80.79.5.173 --kube-url=https://10.0.0.5:6443 --kube-insecure
  $SCRIPT_NAME status sus@80.79.5.173
  $SCRIPT_NAME --uninstall sus@80.79.5.173
EOF
}

MODE="deploy"
WITH_CONTROLLER=0
NO_START=0
SYNC_ONLY=0
DRY_RUN=0
ARCH_OVERRIDE=""
UNINSTALL=0
PURGE=0
KUBE_URL=""
KUBE_TOKEN=""
KUBE_CA=""
KUBE_INSECURE=0
FLUXVM_URL_OVERRIDE=""
BACKEND_OVERRIDE=""
IMAGE_ROOT_OVERRIDE=""
NODE_NAME_OVERRIDE=""
INTERVAL="3s"
NODE_PORT="8081"
NODE_PORT_EXPLICIT=0
CONTROLLER_PORT="8080"
CONTROLLER_PORT_EXPLICIT=0
MIGRATION_CA=""
MIGRATION_CERT=""
MIGRATION_KEY=""
MIGRATION_SERVER_NAME="kairon-node"
MIGRATION_PORT="9443"
MIGRATION_PORT_EXPLICIT=0
MIGRATION_ADAPTER_SOCKET="/run/kairon/migration-adapter.sock"
WITH_MIGRATION_ADAPTER_STUB=0
VERSION_OVERRIDE="${KAIRON_VERSION:-}"
SSH_PORT="${SSH_PORT:-22}"
USER_ARG=""
HOST_ARG=""

if [[ $# -eq 0 ]]; then
  usage
  die "missing target"
fi
if [[ "$1" == "-h" || "$1" == "--help" ]]; then
  usage
  exit 0
fi
if [[ "$1" == "status" ]]; then
  MODE="status"
  shift
fi

# Single order-independent pass: flags may appear before, after, or around
# the target (e.g. both "--uninstall sus@host" and "sus@host --uninstall"
# must work), so bucket non-flag tokens as positionals and handle them once
# flag parsing is done, rather than requiring the target to come first.
POSITIONAL=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --with-controller) WITH_CONTROLLER=1 ;;
    --no-start) NO_START=1 ;;
    --sync-only) SYNC_ONLY=1 ;;
    --dry-run) DRY_RUN=1 ;;
    --arch=*) ARCH_OVERRIDE="${1#*=}" ;;
    --uninstall) UNINSTALL=1 ;;
    --purge) PURGE=1 ;;
    --kube-url=*) KUBE_URL="${1#*=}" ;;
    --kube-token=*) KUBE_TOKEN="${1#*=}" ;;
    --kube-ca=*) KUBE_CA="${1#*=}" ;;
    --kube-insecure) KUBE_INSECURE=1 ;;
    --fluxvm-url=*) FLUXVM_URL_OVERRIDE="${1#*=}" ;;
    --backend=*) BACKEND_OVERRIDE="${1#*=}" ;;
    --image-root=*) IMAGE_ROOT_OVERRIDE="${1#*=}" ;;
    --node-name=*) NODE_NAME_OVERRIDE="${1#*=}" ;;
    --interval=*) INTERVAL="${1#*=}" ;;
    --node-port=*) NODE_PORT="${1#*=}"; NODE_PORT_EXPLICIT=1 ;;
    --controller-port=*) CONTROLLER_PORT="${1#*=}"; CONTROLLER_PORT_EXPLICIT=1 ;;
    --migration-ca=*) MIGRATION_CA="${1#*=}" ;;
    --migration-cert=*) MIGRATION_CERT="${1#*=}" ;;
    --migration-key=*) MIGRATION_KEY="${1#*=}" ;;
    --migration-server-name=*) MIGRATION_SERVER_NAME="${1#*=}" ;;
    --migration-port=*) MIGRATION_PORT="${1#*=}"; MIGRATION_PORT_EXPLICIT=1 ;;
    --migration-adapter-socket=*) MIGRATION_ADAPTER_SOCKET="${1#*=}" ;;
    --with-migration-adapter-stub) WITH_MIGRATION_ADAPTER_STUB=1 ;;
    --version=*) VERSION_OVERRIDE="${1#*=}" ;;
    --ssh-port=*) SSH_PORT="${1#*=}" ;;
    -h|--help) usage; exit 0 ;;
    --) shift; while [[ $# -gt 0 ]]; do POSITIONAL+=("$1"); shift; done ;;
    -*) die "unknown flag: $1 (see --help)" ;;
    *) POSITIONAL+=("$1") ;;
  esac
  shift
done

MIGRATION_CONFIGURED=0
migration_paths_given=0
for p in "$MIGRATION_CA" "$MIGRATION_CERT" "$MIGRATION_KEY"; do
  [[ -n "$p" ]] && migration_paths_given=$((migration_paths_given + 1))
done
if [[ "$migration_paths_given" -gt 0 && "$migration_paths_given" -lt 3 ]]; then
  die "--migration-ca, --migration-cert and --migration-key must be given together (fail-closed, matches kairon-node's own validation)"
fi
if [[ "$migration_paths_given" -eq 3 ]]; then
  MIGRATION_CONFIGURED=1
  for p in "$MIGRATION_CA" "$MIGRATION_CERT" "$MIGRATION_KEY"; do
    [[ -f "$p" ]] || die "migration cert file not found: $p"
  done
fi
if [[ "$WITH_MIGRATION_ADAPTER_STUB" == "1" && "$MIGRATION_CONFIGURED" != "1" ]]; then
  die "--with-migration-adapter-stub requires --migration-ca/-cert/-key (the stub is only useful once migration mTLS is configured)"
fi

if [[ ${#POSITIONAL[@]} -ge 1 ]]; then
  if [[ "${POSITIONAL[0]}" == *@* ]]; then
    USER_ARG="${POSITIONAL[0]%%@*}"
    HOST_ARG="${POSITIONAL[0]#*@}"
    if [[ ${#POSITIONAL[@]} -ge 2 ]]; then
      command -v sshpass >/dev/null 2>&1 || die "a password argument was given but 'sshpass' is not installed"
      export SSHPASS="${POSITIONAL[1]}"
    fi
  elif [[ ${#POSITIONAL[@]} -ge 2 ]]; then
    if [[ "${POSITIONAL[0]}" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ && ! "${POSITIONAL[1]}" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
      HOST_ARG="${POSITIONAL[0]}"
      USER_ARG="${POSITIONAL[1]}"
      tip "detected HOST then USER order -- using ${USER_ARG}@${HOST_ARG} (equivalent: $SCRIPT_NAME ${USER_ARG} ${HOST_ARG})"
    else
      USER_ARG="${POSITIONAL[0]}"
      HOST_ARG="${POSITIONAL[1]}"
    fi
    if [[ ${#POSITIONAL[@]} -ge 3 ]]; then
      command -v sshpass >/dev/null 2>&1 || die "a password argument was given but 'sshpass' is not installed"
      export SSHPASS="${POSITIONAL[2]}"
    fi
  else
    die "a single target argument must be in USER@HOST form (got: ${POSITIONAL[0]})"
  fi
fi

if [[ -z "$HOST_ARG" || -z "$USER_ARG" ]]; then
  if [[ -n "${DEPLOY_HOST:-}" && -n "${DEPLOY_USER:-}" ]]; then
    HOST_ARG="$DEPLOY_HOST"
    USER_ARG="$DEPLOY_USER"
  elif [[ -f "$DEPLOY_LAST_FILE" ]]; then
    HOST_ARG="$(grep -E '^HOST=' "$DEPLOY_LAST_FILE" | cut -d= -f2-)"
    USER_ARG="$(grep -E '^DEPLOY_USER=' "$DEPLOY_LAST_FILE" | cut -d= -f2-)"
    [[ -n "$HOST_ARG" && -n "$USER_ARG" ]] && tip "no target given -- reusing last deploy target ${USER_ARG}@${HOST_ARG} from .deploy-last"
  fi
fi
[[ -n "$HOST_ARG" && -n "$USER_ARG" ]] || { usage; die "no target host/user given (and no .deploy-last found)"; }

REMOTE="${USER_ARG}@${HOST_ARG}"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=30 -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -p "$SSH_PORT")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=30 -P "$SSH_PORT")
NEEDS_TTY=0
SUDO=""

ssh_cmd() {
  if [[ -n "${SSHPASS:-}" ]] && command -v sshpass >/dev/null 2>&1; then
    sshpass -e ssh "${SSH_OPTS[@]}" "$@"
  else
    ssh "${SSH_OPTS[@]}" "$@"
  fi
}
ssh_exec_privileged() {
  local extra=()
  [[ "$NEEDS_TTY" == "1" ]] && extra=(-tt)
  if [[ -n "${SSHPASS:-}" ]] && command -v sshpass >/dev/null 2>&1; then
    sshpass -e ssh "${extra[@]}" "${SSH_OPTS[@]}" "$@"
  else
    ssh "${extra[@]}" "${SSH_OPTS[@]}" "$@"
  fi
}
scp_cmd() {
  if [[ -n "${SSHPASS:-}" ]] && command -v sshpass >/dev/null 2>&1; then
    sshpass -e scp "${SCP_OPTS[@]}" "$@"
  else
    scp "${SCP_OPTS[@]}" "$@"
  fi
}

detect_sudo() {
  if [[ "$USER_ARG" == "root" ]]; then
    SUDO=""
    NEEDS_TTY=0
    return
  fi
  SUDO="sudo"
  if ssh_cmd "$REMOTE" 'sudo -n true' >/dev/null 2>&1; then
    NEEDS_TTY=0
    ok "passwordless sudo available for $USER_ARG"
  else
    NEEDS_TTY=1
    warn "passwordless sudo not detected for $USER_ARG -- allocating a TTY so sudo can prompt for a password"
  fi
}

detect_arch() {
  if [[ -n "$ARCH_OVERRIDE" ]]; then
    RESOLVED_GOARCH="$ARCH_OVERRIDE"
    return
  fi
  local uname_m
  uname_m="$(ssh_cmd "$REMOTE" uname -m)"
  case "$uname_m" in
    x86_64) RESOLVED_GOARCH="amd64" ;;
    aarch64|arm64) RESOLVED_GOARCH="arm64" ;;
    *) die "unrecognized remote architecture '$uname_m' -- pass --arch=amd64|arm64 explicitly" ;;
  esac
}

resolve_version() {
  if [[ -n "$VERSION_OVERRIDE" ]]; then
    RESOLVED_VERSION="$VERSION_OVERRIDE"
  elif git -C "$REPO_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    RESOLVED_VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
  else
    RESOLVED_VERSION="dev"
  fi
  if git -C "$REPO_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    RESOLVED_COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  else
    RESOLVED_COMMIT="unknown"
  fi
}

print_plan() {
  cat <<EOF

Dry run -- no SSH connections, no build, no changes.

Target:                 ${USER_ARG}@${HOST_ARG} (port $SSH_PORT)
Components:              kairon-node, kaironctl$([[ "$WITH_CONTROLLER" == "1" ]] && echo ", kairon-controller")
Arch:                    ${RESOLVED_GOARCH}
Version:                 ${RESOLVED_VERSION}
Node name:                ${RESOLVED_NODE_NAME}
FLUXVM_URL:               ${RESOLVED_FLUXVM_URL}
KAIRON_DEFAULT_BACKEND:   ${RESOLVED_BACKEND}
KAIRON_IMAGE_ROOT:        ${RESOLVED_IMAGE_ROOT}
KAIRON_KUBE_URL:          ${KUBE_URL:-<not set -- service will start but stay not-ready until configured>}
Node health port:         ${NODE_PORT}$([[ "$NODE_PORT_EXPLICIT" != "1" ]] && echo " (default; auto-replaced with a random free port if busy)")
$([[ "$WITH_CONTROLLER" == "1" ]] && echo "Controller health port:  ${CONTROLLER_PORT}$([[ "$CONTROLLER_PORT_EXPLICIT" != "1" ]] && echo " (default; auto-replaced with a random free port if busy)")")
Start after install:      $([[ "$NO_START" == "1" ]] && echo "no (--no-start)" || echo "yes")

Remote paths:
  /usr/bin/kairon-node, /usr/bin/kaironctl$([[ "$WITH_CONTROLLER" == "1" ]] && echo ", /usr/bin/kairon-controller")
  /etc/kairon/kairon-node.env   (seeded only if absent)
  /etc/systemd/system/kairon-node.service$([[ "$WITH_CONTROLLER" == "1" ]] && echo ", kairon-controller.service")
EOF
}

run_status() {
  info "checking kairon-node on ${USER_ARG}@${HOST_ARG}..."
  ssh_cmd "$REMOTE" '
    port_of() { grep -oE -- "--health-addr=:[0-9]+" "$1" 2>/dev/null | cut -d: -f2; }
    if systemctl list-unit-files kairon-node.service >/dev/null 2>&1; then
      systemctl status kairon-node.service --no-pager -l || true
      p=$(port_of /etc/systemd/system/kairon-node.service)
      echo "---"
      curl -s -o /dev/null -w "healthz=%{http_code}\n" "http://127.0.0.1:${p:-8081}/healthz" 2>/dev/null
      curl -s -o /dev/null -w "readyz=%{http_code}\n" "http://127.0.0.1:${p:-8081}/readyz" 2>/dev/null
    else
      echo "kairon-node.service is not installed on this host"
    fi
    if systemctl list-unit-files kairon-controller.service >/dev/null 2>&1; then
      echo "---"
      systemctl status kairon-controller.service --no-pager -l || true
    fi
  ' || true
}

run_uninstall() {
  if [[ "$DRY_RUN" == "1" ]]; then
    cat <<EOF

Dry run -- no SSH connections, no changes.

Would uninstall from ${USER_ARG}@${HOST_ARG}:
  stop+disable kairon-node.service, kairon-controller.service
  remove /etc/systemd/system/{kairon-node,kairon-controller}.service
  remove /usr/bin/{kairon-node,kairon-controller,kaironctl}
  $([[ "$PURGE" == "1" ]] && echo "remove /etc/kairon and the 'kairon' user (--purge)" || echo "keep /etc/kairon and the 'kairon' user (pass --purge to remove them)")
EOF
    return 0
  fi
  info "connecting to ${USER_ARG}@${HOST_ARG}..."
  ssh_cmd "$REMOTE" true || die "could not SSH to ${REMOTE}"
  detect_sudo
  info "uninstalling kairon-node / kairon-controller / kaironctl from ${USER_ARG}@${HOST_ARG}..."
  local remote_script
  remote_script="$(cat <<'UNINSTALL_EOF'
set -euo pipefail
PURGE="$1"
systemctl stop kairon-node.service 2>/dev/null || true
systemctl stop kairon-controller.service 2>/dev/null || true
systemctl stop kairon-migration-adapter-stub.service 2>/dev/null || true
systemctl disable kairon-node.service 2>/dev/null || true
systemctl disable kairon-controller.service 2>/dev/null || true
systemctl disable kairon-migration-adapter-stub.service 2>/dev/null || true
rm -f /etc/systemd/system/kairon-node.service /etc/systemd/system/kairon-controller.service /etc/systemd/system/kairon-migration-adapter-stub.service
systemctl daemon-reload
rm -f /usr/bin/kairon-node /usr/bin/kairon-controller /usr/bin/kaironctl /usr/bin/kairon-migration-adapter-stub
if [[ "$PURGE" == "1" ]]; then
  rm -rf /etc/kairon
  userdel kairon 2>/dev/null || true
  echo "purged /etc/kairon and removed the 'kairon' user"
else
  echo "kept /etc/kairon and the 'kairon' user (pass --purge to remove them)"
fi
echo "uninstall complete"
UNINSTALL_EOF
)"
  ssh_exec_privileged "$REMOTE" "${SUDO} bash -s -- $PURGE" <<< "$remote_script"
}

verify_remote() {
  local unit="$1" port="$2"
  ssh_cmd "$REMOTE" "
    systemctl is-active $unit 2>/dev/null || true
    curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:$port/healthz 2>/dev/null
    curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:$port/readyz 2>/dev/null
  " || true
}

save_deploy_last() {
  {
    echo "HOST=$HOST_ARG"
    echo "DEPLOY_USER=$USER_ARG"
    echo "MODE=$([[ "$WITH_CONTROLLER" == "1" ]] && echo node+controller || echo node)"
    echo "VERSION=$RESOLVED_VERSION"
    echo "COMMIT=$RESOLVED_COMMIT"
    echo "UPDATED=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } > "$DEPLOY_LAST_FILE"
  chmod 600 "$DEPLOY_LAST_FILE"
}

run_deploy() {
  resolve_version
  RESOLVED_FLUXVM_URL="${FLUXVM_URL_OVERRIDE:-http://127.0.0.1:7788}"
  RESOLVED_BACKEND="${BACKEND_OVERRIDE:-qemu}"
  RESOLVED_IMAGE_ROOT="${IMAGE_ROOT_OVERRIDE:-/var/lib/fluxvm/images}"

  if [[ "$DRY_RUN" == "1" ]]; then
    RESOLVED_NODE_NAME="${NODE_NAME_OVERRIDE:-<remote hostname, detected at run time>}"
    RESOLVED_GOARCH="${ARCH_OVERRIDE:-<auto-detected at run time>}"
    print_plan
    return 0
  fi

  info "connecting to ${USER_ARG}@${HOST_ARG}..."
  local remote_hostname
  remote_hostname="$(ssh_cmd "$REMOTE" hostname)" || die "could not SSH to ${REMOTE}"
  ok "connected (remote hostname: $remote_hostname)"
  # Kubernetes Node names are always lowercase DNS-1123 labels; 'hostname'
  # commonly returns mixed case, which would silently mismatch spec.nodeName
  # and cause kairon-node to skip every machine assigned to it with no error.
  RESOLVED_NODE_NAME="${NODE_NAME_OVERRIDE:-$(tr '[:upper:]' '[:lower:]' <<< "$remote_hostname")}"

  detect_sudo
  detect_arch
  info "target arch: linux/$RESOLVED_GOARCH"

  local build_dir="$REPO_ROOT/dist/deploy-remote/$RESOLVED_GOARCH"
  mkdir -p "$build_dir"
  info "cross-compiling for linux/$RESOLVED_GOARCH (version $RESOLVED_VERSION)..."
  ( cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$RESOLVED_GOARCH" go build -trimpath -ldflags="-s -w -X main.version=$RESOLVED_VERSION" -o "$build_dir/kairon-node" ./cmd/kairon-node )
  ( cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$RESOLVED_GOARCH" go build -trimpath -ldflags="-s -w -X main.version=$RESOLVED_VERSION" -o "$build_dir/kaironctl" ./cmd/kaironctl )
  if [[ "$WITH_CONTROLLER" == "1" ]]; then
    ( cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$RESOLVED_GOARCH" go build -trimpath -ldflags="-s -w -X main.version=$RESOLVED_VERSION" -o "$build_dir/kairon-controller" ./cmd/kairon-controller )
  fi
  if [[ "$WITH_MIGRATION_ADAPTER_STUB" == "1" ]]; then
    ( cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$RESOLVED_GOARCH" go build -trimpath -o "$build_dir/kairon-migration-adapter-stub" ./cmd/kairon-migration-adapter-stub )
  fi
  ok "build complete: $build_dir"

  local local_stage
  local_stage="$(mktemp -d)"
  trap 'rm -rf "$local_stage"' RETURN

  {
    printf 'WITH_CONTROLLER=%q\n' "$WITH_CONTROLLER"
    printf 'NO_START=%q\n' "$NO_START"
    printf 'NODE_NAME=%q\n' "$RESOLVED_NODE_NAME"
    printf 'FLUXVM_URL=%q\n' "$RESOLVED_FLUXVM_URL"
    printf 'KAIRON_DEFAULT_BACKEND=%q\n' "$RESOLVED_BACKEND"
    printf 'KAIRON_IMAGE_ROOT=%q\n' "$RESOLVED_IMAGE_ROOT"
    printf 'KUBE_URL=%q\n' "$KUBE_URL"
    printf 'KUBE_TOKEN=%q\n' "$KUBE_TOKEN"
    printf 'KUBE_CA=%q\n' "$KUBE_CA"
    printf 'KUBE_INSECURE=%q\n' "$KUBE_INSECURE"
    printf 'INTERVAL=%q\n' "$INTERVAL"
    printf 'NODE_PORT=%q\n' "$NODE_PORT"
    printf 'NODE_PORT_EXPLICIT=%q\n' "$NODE_PORT_EXPLICIT"
    printf 'CONTROLLER_PORT=%q\n' "$CONTROLLER_PORT"
    printf 'CONTROLLER_PORT_EXPLICIT=%q\n' "$CONTROLLER_PORT_EXPLICIT"
    printf 'MIGRATION_CONFIGURED=%q\n' "$MIGRATION_CONFIGURED"
    printf 'MIGRATION_SERVER_NAME=%q\n' "$MIGRATION_SERVER_NAME"
    printf 'MIGRATION_PORT=%q\n' "$MIGRATION_PORT"
    printf 'MIGRATION_PORT_EXPLICIT=%q\n' "$MIGRATION_PORT_EXPLICIT"
    printf 'MIGRATION_ADAPTER_SOCKET=%q\n' "$MIGRATION_ADAPTER_SOCKET"
    printf 'WITH_MIGRATION_ADAPTER_STUB=%q\n' "$WITH_MIGRATION_ADAPTER_STUB"
  } > "$local_stage/params.env"

  cat > "$local_stage/install.sh" <<'INSTALL_EOF'
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./params.env

info() { printf '[*] %s\n' "$*"; }
ok()   { printf '[+] %s\n' "$*"; }
warn() { printf '[!] %s\n' "$*" >&2; }

if ! getent group kairon >/dev/null 2>&1; then
  groupadd --system kairon
fi
if ! id kairon >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin -g kairon kairon
  ok "created system user 'kairon'"
fi

install -d -m 0750 -o root -g kairon /etc/kairon

install -m 0755 -o root -g root ./kairon-node /usr/bin/kairon-node
install -m 0755 -o root -g root ./kaironctl /usr/bin/kaironctl
ok "installed kairon-node, kaironctl to /usr/bin"

if [[ "$WITH_CONTROLLER" == "1" ]]; then
  install -m 0755 -o root -g root ./kairon-controller /usr/bin/kairon-controller
  ok "installed kairon-controller to /usr/bin"
fi

if [[ ! -f /etc/kairon/kairon-node.env ]]; then
  {
    echo "NODE_NAME=$NODE_NAME"
    echo "FLUXVM_URL=$FLUXVM_URL"
    echo "KAIRON_DEFAULT_BACKEND=$KAIRON_DEFAULT_BACKEND"
    echo "KAIRON_IMAGE_ROOT=$KAIRON_IMAGE_ROOT"
    if [[ -n "$KUBE_URL" ]]; then echo "KAIRON_KUBE_URL=$KUBE_URL"; else echo "#KAIRON_KUBE_URL="; fi
    if [[ -n "$KUBE_TOKEN" ]]; then echo "KAIRON_KUBE_TOKEN=$KUBE_TOKEN"; else echo "#KAIRON_KUBE_TOKEN="; fi
    if [[ -n "$KUBE_CA" ]]; then echo "KAIRON_KUBE_CA=$KUBE_CA"; else echo "#KAIRON_KUBE_CA="; fi
    if [[ "$KUBE_INSECURE" == "1" ]]; then echo "KAIRON_KUBE_INSECURE=true"; else echo "#KAIRON_KUBE_INSECURE=false"; fi
    echo "#FLUXVM_TOKEN="
    if [[ "$MIGRATION_CONFIGURED" == "1" ]]; then
      echo "KAIRON_MIGRATION_CA=/etc/kairon/migration/ca.pem"
      echo "KAIRON_MIGRATION_CERT=/etc/kairon/migration/cert.pem"
      echo "KAIRON_MIGRATION_KEY=/etc/kairon/migration/key.pem"
      echo "KAIRON_MIGRATION_SERVER_NAME=$MIGRATION_SERVER_NAME"
      echo "KAIRON_MIGRATION_ADAPTER_SOCKET=$MIGRATION_ADAPTER_SOCKET"
    else
      echo "#KAIRON_MIGRATION_CA="
      echo "#KAIRON_MIGRATION_CERT="
      echo "#KAIRON_MIGRATION_KEY="
    fi
  } > /etc/kairon/kairon-node.env
  chmod 0640 /etc/kairon/kairon-node.env
  chown root:kairon /etc/kairon/kairon-node.env
  ok "seeded /etc/kairon/kairon-node.env with defaults"
else
  warn "/etc/kairon/kairon-node.env already exists -- left untouched"
  if [[ -n "$KUBE_URL$KUBE_TOKEN$KUBE_CA" || "$KUBE_INSECURE" == "1" ]]; then
    warn "--kube-* flags were given but ignored because the env file already exists"
    warn "edit /etc/kairon/kairon-node.env by hand, then: systemctl restart kairon-node"
  fi
  if [[ "$MIGRATION_CONFIGURED" == "1" ]]; then
    warn "--migration-* flags were given but ignored because the env file already exists"
  fi
fi

if [[ "$MIGRATION_CONFIGURED" == "1" ]]; then
  install -d -m 0750 -o root -g kairon /etc/kairon/migration
  install -m 0640 -o root -g kairon ./migration-ca.pem /etc/kairon/migration/ca.pem
  install -m 0640 -o root -g kairon ./migration-cert.pem /etc/kairon/migration/cert.pem
  install -m 0640 -o root -g kairon ./migration-key.pem /etc/kairon/migration/key.pem
  ok "installed migration mTLS materials to /etc/kairon/migration/"
fi

if [[ "$WITH_MIGRATION_ADAPTER_STUB" == "1" ]]; then
  install -m 0755 -o root -g root ./kairon-migration-adapter-stub /usr/bin/kairon-migration-adapter-stub
  install -m 0644 -o root -g root ./kairon-migration-adapter-stub.service /etc/systemd/system/kairon-migration-adapter-stub.service
  sed -i "s#^ExecStart=.*#ExecStart=/usr/bin/kairon-migration-adapter-stub --socket=$MIGRATION_ADAPTER_SOCKET#" /etc/systemd/system/kairon-migration-adapter-stub.service
  ok "installed kairon-migration-adapter-stub (TEST DOUBLE -- simulates transfers, does not move real VM memory)"
fi

port_in_use() {
  ss -ltn 2>/dev/null | awk '{print $4}' | grep -qE ":$1\$"
}

# Resolves the port to actually use for a health server: if the caller
# explicitly asked for a port, that port is used as-is (a busy explicit
# port is a hard failure, not silently overridden). Otherwise, if the
# default is busy (common: some unrelated service already on 8080/8081),
# a random free port in the ephemeral range is picked automatically.
resolve_port() {
  local desired="$1" explicit="$2" name="$3"
  if ! port_in_use "$desired"; then
    echo "$desired"
    return 0
  fi
  if [[ "$explicit" == "1" ]]; then
    echo "ERROR: --$name-port=$desired is already in use on this host" >&2
    return 1
  fi
  warn "default port $desired for $name is already in use on this host -- picking a random free port"
  local tries=0 candidate
  while [[ $tries -lt 50 ]]; do
    candidate=$(( (RANDOM % 20000) + 20000 ))
    if ! port_in_use "$candidate"; then
      warn "$name health port auto-selected: $candidate (pass --$name-port=$candidate to pin it on future deploys)"
      echo "$candidate"
      return 0
    fi
    tries=$((tries + 1))
  done
  echo "ERROR: could not find a free port for $name after 50 attempts" >&2
  return 1
}

RESOLVED_NODE_PORT="$(resolve_port "$NODE_PORT" "$NODE_PORT_EXPLICIT" "node")" || exit 1
NODE_EXEC_ARGS="--interval=$INTERVAL --health-addr=:$RESOLVED_NODE_PORT"
if [[ "$MIGRATION_CONFIGURED" == "1" ]]; then
  RESOLVED_MIGRATION_PORT="$(resolve_port "$MIGRATION_PORT" "$MIGRATION_PORT_EXPLICIT" "migration")" || exit 1
  NODE_EXEC_ARGS="$NODE_EXEC_ARGS --migration-addr=:$RESOLVED_MIGRATION_PORT"
fi
install -m 0644 -o root -g root ./kairon-node.service /etc/systemd/system/kairon-node.service
sed -i "s#^ExecStart=.*#ExecStart=/usr/bin/kairon-node $NODE_EXEC_ARGS#" /etc/systemd/system/kairon-node.service
if [[ "$WITH_CONTROLLER" == "1" ]]; then
  RESOLVED_CONTROLLER_PORT="$(resolve_port "$CONTROLLER_PORT" "$CONTROLLER_PORT_EXPLICIT" "controller")" || exit 1
  install -m 0644 -o root -g root ./kairon-controller.service /etc/systemd/system/kairon-controller.service
  sed -i "s#^ExecStart=.*#ExecStart=/usr/bin/kairon-controller --interval=5s --health-addr=:$RESOLVED_CONTROLLER_PORT#" /etc/systemd/system/kairon-controller.service
  if [[ ! -f /etc/kairon/kairon-controller.env ]]; then
    : > /etc/kairon/kairon-controller.env
    chmod 0640 /etc/kairon/kairon-controller.env
    chown root:kairon /etc/kairon/kairon-controller.env
    [[ -n "$KUBE_URL" ]] && echo "KAIRON_KUBE_URL=$KUBE_URL" >> /etc/kairon/kairon-controller.env
    [[ -n "$KUBE_TOKEN" ]] && echo "KAIRON_KUBE_TOKEN=$KUBE_TOKEN" >> /etc/kairon/kairon-controller.env
    [[ -n "$KUBE_CA" ]] && echo "KAIRON_KUBE_CA=$KUBE_CA" >> /etc/kairon/kairon-controller.env
    [[ "$KUBE_INSECURE" == "1" ]] && echo "KAIRON_KUBE_INSECURE=true" >> /etc/kairon/kairon-controller.env
    ok "seeded /etc/kairon/kairon-controller.env"
  fi
fi

systemctl daemon-reload

if [[ "$NO_START" != "1" ]]; then
  if [[ "$WITH_MIGRATION_ADAPTER_STUB" == "1" ]]; then
    systemctl enable --now kairon-migration-adapter-stub.service
    ok "enabled + started kairon-migration-adapter-stub.service"
  fi
  systemctl enable --now kairon-node.service
  ok "enabled + started kairon-node.service"
  if [[ "$WITH_CONTROLLER" == "1" ]]; then
    systemctl enable --now kairon-controller.service
    ok "enabled + started kairon-controller.service"
  fi
  sleep 2
else
  systemctl enable kairon-node.service
  [[ "$WITH_CONTROLLER" == "1" ]] && systemctl enable kairon-controller.service
  [[ "$WITH_MIGRATION_ADAPTER_STUB" == "1" ]] && systemctl enable kairon-migration-adapter-stub.service
  warn "--no-start given: service(s) installed and enabled but not started"
fi

systemctl status kairon-node.service --no-pager -l 2>&1 | tail -n 15 || true
INSTALL_EOF

  local scp_files=("$build_dir/kairon-node" "$build_dir/kaironctl" "$REPO_ROOT/systemd/kairon-node.service")
  if [[ "$WITH_CONTROLLER" == "1" ]]; then
    scp_files+=("$build_dir/kairon-controller" "$REPO_ROOT/systemd/kairon-controller.service")
  fi
  if [[ "$MIGRATION_CONFIGURED" == "1" ]]; then
    cp "$MIGRATION_CA" "$local_stage/migration-ca.pem"
    cp "$MIGRATION_CERT" "$local_stage/migration-cert.pem"
    cp "$MIGRATION_KEY" "$local_stage/migration-key.pem"
    scp_files+=("$local_stage/migration-ca.pem" "$local_stage/migration-cert.pem" "$local_stage/migration-key.pem")
  fi
  if [[ "$WITH_MIGRATION_ADAPTER_STUB" == "1" ]]; then
    scp_files+=("$build_dir/kairon-migration-adapter-stub" "$REPO_ROOT/systemd/kairon-migration-adapter-stub.service")
  fi
  scp_files+=("$local_stage/params.env" "$local_stage/install.sh")

  local remote_tmp
  remote_tmp="$(ssh_cmd "$REMOTE" mktemp -d)"
  info "staging files in ${REMOTE}:${remote_tmp}"
  scp_cmd "${scp_files[@]}" "${REMOTE}:${remote_tmp}/"

  if [[ "$SYNC_ONLY" == "1" ]]; then
    ok "--sync-only: files copied to ${remote_tmp} on the remote host, nothing installed or started"
    tip "run manually: ssh ${REMOTE} 'cd ${remote_tmp} && sudo bash ./install.sh'"
    return 0
  fi

  info "running install on ${REMOTE}..."
  ssh_exec_privileged "$REMOTE" "cd ${remote_tmp} && ${SUDO} bash ./install.sh"
  ssh_cmd "$REMOTE" "rm -rf ${remote_tmp}" || true

  save_deploy_last

  if [[ "$NO_START" == "1" ]]; then
    ok "deploy complete (service not started, per --no-start)"
    return 0
  fi

  info "verifying kairon-node..."
  local node_port
  node_port="$(ssh_cmd "$REMOTE" "grep -oE -- '--health-addr=:[0-9]+' /etc/systemd/system/kairon-node.service | cut -d: -f2" || echo 8081)"
  local node_report node_active node_healthz node_readyz
  node_report="$(verify_remote kairon-node.service "${node_port:-8081}")"
  node_active="$(sed -n '1p' <<< "$node_report")"
  node_healthz="$(sed -n '2p' <<< "$node_report")"
  node_readyz="$(sed -n '3p' <<< "$node_report")"

  echo
  if [[ "$node_active" == "active" ]]; then
    ok "kairon-node.service is active"
  else
    err "kairon-node.service is '$node_active' (expected active)"
  fi
  [[ "$node_healthz" == "200" ]] && ok "healthz: $node_healthz" || warn "healthz: $node_healthz"
  if [[ "$node_readyz" == "200" ]]; then
    ok "readyz: $node_readyz (Kubernetes API reachable)"
  else
    warn "readyz: $node_readyz (not ready -- expected if KAIRON_KUBE_URL isn't configured yet)"
    tip "fix: ssh ${REMOTE}, edit /etc/kairon/kairon-node.env, then: sudo systemctl restart kairon-node"
  fi

  if [[ "$WITH_CONTROLLER" == "1" ]]; then
    local ctrl_port
    ctrl_port="$(ssh_cmd "$REMOTE" "grep -oE -- '--health-addr=:[0-9]+' /etc/systemd/system/kairon-controller.service | cut -d: -f2" || echo 8080)"
    local ctrl_report ctrl_active ctrl_healthz ctrl_readyz
    ctrl_report="$(verify_remote kairon-controller.service "${ctrl_port:-8080}")"
    ctrl_active="$(sed -n '1p' <<< "$ctrl_report")"
    ctrl_healthz="$(sed -n '2p' <<< "$ctrl_report")"
    ctrl_readyz="$(sed -n '3p' <<< "$ctrl_report")"
    [[ "$ctrl_active" == "active" ]] && ok "kairon-controller.service is active" || err "kairon-controller.service is '$ctrl_active'"
    [[ "$ctrl_healthz" == "200" ]] && ok "controller healthz: $ctrl_healthz" || warn "controller healthz: $ctrl_healthz"
    [[ "$ctrl_readyz" == "200" ]] && ok "controller readyz: $ctrl_readyz" || warn "controller readyz: $ctrl_readyz"
  fi

  echo
  ok "deploy to ${REMOTE} complete"
  tip "ssh ${REMOTE}"
  tip "ssh ${REMOTE} 'systemctl status kairon-node --no-pager -l'"
  tip "ssh ${REMOTE} 'journalctl -u kairon-node -n 30 --no-pager'"
  tip "ssh ${REMOTE} 'sudo cat /etc/kairon/kairon-node.env'"
  tip "use kaironctl on that host: export KAIRON_KUBE_URL=... ; kaironctl get"
}

case "$MODE" in
  status)
    run_status
    ;;
  deploy)
    if [[ "$UNINSTALL" == "1" ]]; then
      run_uninstall
    else
      run_deploy
    fi
    ;;
esac

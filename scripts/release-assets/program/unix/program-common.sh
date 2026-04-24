#!/usr/bin/env bash
set -euo pipefail

PROGRAM_COMMON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUNDLE_ROOT="$(cd "$PROGRAM_COMMON_DIR/.." && pwd)"
APP_NAME="agent-platform-runner"
MANIFEST_FILE="$BUNDLE_ROOT/manifest.json"
ENV_EXAMPLE_FILE="$BUNDLE_ROOT/.env.example"
ENV_FILE="$BUNDLE_ROOT/.env"
BACKEND_BIN="$BUNDLE_ROOT/backend/$APP_NAME"
CONFIG_DIR="$BUNDLE_ROOT/configs"
RUNTIME_ROOT="$BUNDLE_ROOT/runtime"
RUN_DIR="$BUNDLE_ROOT/run"
LOG_FILE="$RUN_DIR/$APP_NAME.log"
PID_FILE="$RUN_DIR/$APP_NAME.pid"
RELAY_DIR="$BUNDLE_ROOT/local-cli-acp-relay"
RELAY_ENTRY="$RELAY_DIR/relay.mjs"
RELAY_PID_FILE="$RUN_DIR/local-cli-acp-relay.pid"
NODE_CMD=""
PROGRAM_USE_ELECTRON_NODE=0

program_die() {
  echo "[program] $*" >&2
  exit 1
}

program_require_file() {
  local path="$1"
  [[ -f "$path" ]] || program_die "required file not found: $path"
}

program_require_dir() {
  local path="$1"
  [[ -d "$path" ]] || program_die "required directory not found: $path"
}

program_validate_bundle() {
  program_require_file "$MANIFEST_FILE"
  program_require_file "$ENV_EXAMPLE_FILE"
  program_require_dir "$CONFIG_DIR"
  program_require_dir "$RUNTIME_ROOT"
  program_require_dir "$RELAY_DIR"
  program_require_file "$RELAY_ENTRY"
  [[ -x "$BACKEND_BIN" ]] || program_die "backend binary is not executable: $BACKEND_BIN"
}

program_load_env() {
  [[ -f "$ENV_FILE" ]] || program_die "missing .env (copy from .env.example first)"
  set -a
  # shellcheck disable=SC1091
  . "$ENV_FILE"
  set +a

  LOCAL_CLI_ACP_RELAY_ENABLED="${LOCAL_CLI_ACP_RELAY_ENABLED:-true}"
  LOCAL_CLI_ACP_RELAY_PORT="${LOCAL_CLI_ACP_RELAY_PORT:-3220}"
  LOCAL_CLI_ACP_HANDSHAKE_TIMEOUT_MS="${LOCAL_CLI_ACP_HANDSHAKE_TIMEOUT_MS:-60000}"
  LOCAL_CLI_ACP_RUN_TIMEOUT_MS="${LOCAL_CLI_ACP_RUN_TIMEOUT_MS:-600000}"
  if [[ -z "${LOCAL_CLI_ACP_DEFAULT_CWD:-}" ]]; then
    if [[ -d "$HOME/Desktop" ]]; then
      LOCAL_CLI_ACP_DEFAULT_CWD="$HOME/Desktop"
    else
      LOCAL_CLI_ACP_DEFAULT_CWD="$HOME"
    fi
  fi
  if [[ -z "${LOCAL_CLI_ACP_ALLOWED_CWD_ROOTS:-}" ]]; then
    LOCAL_CLI_ACP_ALLOWED_CWD_ROOTS="$LOCAL_CLI_ACP_DEFAULT_CWD"
  fi

  export \
    LOCAL_CLI_ACP_RELAY_ENABLED \
    LOCAL_CLI_ACP_RELAY_PORT \
    LOCAL_CLI_ACP_HANDSHAKE_TIMEOUT_MS \
    LOCAL_CLI_ACP_RUN_TIMEOUT_MS \
    LOCAL_CLI_ACP_DEFAULT_CWD \
    LOCAL_CLI_ACP_ALLOWED_CWD_ROOTS
}

resolve_node_bin() {
  if [[ -n "${NODE_BIN:-}" ]]; then
    [[ -x "$NODE_BIN" ]] || program_die "NODE_BIN is not executable: $NODE_BIN"
    NODE_CMD="$NODE_BIN"
    PROGRAM_USE_ELECTRON_NODE=1
    return
  fi

  NODE_CMD="$(command -v node 2>/dev/null || true)"
  [[ -n "$NODE_CMD" ]] || program_die "node runtime not found; install Node.js 18+ or set NODE_BIN in .env"
  PROGRAM_USE_ELECTRON_NODE=0
}

program_prepare_node_command() {
  resolve_node_bin
  if [[ "$PROGRAM_USE_ELECTRON_NODE" == "1" ]]; then
    export ELECTRON_RUN_AS_NODE=1
    return
  fi
  unset ELECTRON_RUN_AS_NODE || true
}

program_prepare_runtime_dirs() {
  mkdir -p \
    "$RUN_DIR" \
    "$RUNTIME_ROOT/registries/providers" \
    "$RUNTIME_ROOT/registries/models" \
    "$RUNTIME_ROOT/registries/tools" \
    "$RUNTIME_ROOT/registries/mcp-servers" \
    "$RUNTIME_ROOT/registries/viewport-servers" \
    "$RUNTIME_ROOT/owner" \
    "$RUNTIME_ROOT/agents" \
    "$RUNTIME_ROOT/teams" \
    "$RUNTIME_ROOT/root" \
    "$RUNTIME_ROOT/schedules" \
    "$RUNTIME_ROOT/chats" \
    "$RUNTIME_ROOT/memory" \
    "$RUNTIME_ROOT/pan" \
    "$RUNTIME_ROOT/skills-market"
}

program_prepare_log_file() {
  mkdir -p "$RUN_DIR"
  : >"$LOG_FILE"
}

program_read_pid_file() {
  local pid_file="$1"
  [[ -f "$pid_file" ]] || return 1
  local pid
  pid="$(cat "$pid_file")"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  printf '%s\n' "$pid"
}

program_clear_stale_pid_file() {
  local pid_file="$1"
  local label="$2"

  if [[ ! -f "$pid_file" ]]; then
    return
  fi

  local pid
  pid="$(program_read_pid_file "$pid_file" || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" >/dev/null 2>&1; then
    program_die "$label is already running with pid $pid"
  fi

  rm -f "$pid_file"
}

program_stop_pid_file() {
  local pid_file="$1"
  local label="$2"

  if [[ ! -f "$pid_file" ]]; then
    echo "[program-stop] pid file not found for $label: $pid_file"
    return
  fi

  local pid
  pid="$(program_read_pid_file "$pid_file" || true)"
  [[ -n "$pid" ]] || program_die "pid file must contain a numeric pid: $pid_file"

  if ! kill -0 "$pid" >/dev/null 2>&1; then
    rm -f "$pid_file"
    echo "[program-stop] process $pid for $label is not running; removed stale pid file"
    return
  fi

  kill "$pid"
  for _ in $(seq 1 30); do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      rm -f "$pid_file"
      echo "[program-stop] stopped $label (pid=$pid)"
      return
    fi
    sleep 1
  done

  program_die "process $pid for $label did not stop within 30s"
}

program_relay_enabled() {
  case "$(printf '%s' "${LOCAL_CLI_ACP_RELAY_ENABLED:-true}" | tr '[:upper:]' '[:lower:]')" in
    false|0|no|off) return 1 ;;
    *) return 0 ;;
  esac
}

program_start_relay_daemon() {
  if ! program_relay_enabled; then
    echo "[program-start] local relay disabled by LOCAL_CLI_ACP_RELAY_ENABLED=false"
    return
  fi

  local -a relay_env=(
    "PORT=$LOCAL_CLI_ACP_RELAY_PORT"
    "DEFAULT_CWD=$LOCAL_CLI_ACP_DEFAULT_CWD"
    "ALLOWED_CWD_ROOTS=$LOCAL_CLI_ACP_ALLOWED_CWD_ROOTS"
    "HANDSHAKE_TIMEOUT_MS=$LOCAL_CLI_ACP_HANDSHAKE_TIMEOUT_MS"
    "RUN_TIMEOUT_MS=$LOCAL_CLI_ACP_RUN_TIMEOUT_MS"
  )

  if [[ -n "${LOCAL_CLI_ACP_RELAY_AUTH_TOKEN:-}" ]]; then
    relay_env+=("AUTH_TOKEN=$LOCAL_CLI_ACP_RELAY_AUTH_TOKEN")
  fi
  if [[ -n "${CLAUDE_CODE_ACP_COMMAND:-}" ]]; then
    relay_env+=("CLAUDE_CODE_ACP_COMMAND=$CLAUDE_CODE_ACP_COMMAND")
  fi
  if [[ -n "${CLAUDE_CODE_ACP_ARGS:-}" ]]; then
    relay_env+=("CLAUDE_CODE_ACP_ARGS=$CLAUDE_CODE_ACP_ARGS")
  fi

  program_prepare_node_command
  program_clear_stale_pid_file "$RELAY_PID_FILE" "local-cli-acp-relay"

  nohup env "${relay_env[@]}" "$NODE_CMD" "$RELAY_ENTRY" >>"$LOG_FILE" 2>&1 &
  local pid=$!
  printf '%s\n' "$pid" >"$RELAY_PID_FILE"
  sleep 1
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    rm -f "$RELAY_PID_FILE"
    program_die "local relay failed to start; see $LOG_FILE"
  fi

  echo "[program-start] started local-cli-acp-relay in daemon mode (pid=$pid, port=$LOCAL_CLI_ACP_RELAY_PORT)"
}

program_stop_relay() {
  if ! program_relay_enabled && [[ ! -f "$RELAY_PID_FILE" ]]; then
    return
  fi
  program_stop_pid_file "$RELAY_PID_FILE" "local-cli-acp-relay"
}

program_start_backend_daemon() {
  local pid

  program_clear_stale_pid_file "$PID_FILE" "$APP_NAME"
  nohup "$BACKEND_BIN" >>"$LOG_FILE" 2>&1 &
  pid=$!
  printf '%s\n' "$pid" >"$PID_FILE"
  sleep 1
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    rm -f "$PID_FILE"
    program_stop_relay
    program_die "backend failed to start; see $LOG_FILE"
  fi

  echo "[program-start] started $APP_NAME in daemon mode (pid=$pid)"
  echo "[program-start] log file: $LOG_FILE"
}

program_exec_backend() {
  exec "$BACKEND_BIN"
}

program_stop_backend() {
  program_stop_pid_file "$PID_FILE" "$APP_NAME"
}

#!/usr/bin/env bash
# ==============================================================================
# Antigravity ACP Gateway — Unified Cross-Platform Deployment Script
# Supports: macOS (darwin), Linux (x86_64 / arm64, systemd / direct), Docker Compose, Cross-Build
# Supports Automatic ACP Backend Download & Installation (fail-closed hash verification):
#   - cli (default) : Antigravity CLI (agy acp / antigravity acp), SHA-512 from the official manifest
#   - zed / zen     : Zed/Zen standalone ACP backend package (agy_acp_server.par), pinned SHA-256 (ZED_ACP_SHA256)
#   - auto          : Auto-discover CLI (agy) or Zed/Zen ACP package, auto-installing CLI if missing
# ==============================================================================
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ORIG_ARGS=("$@")
SCRIPT_NAME="$(basename "${BASH_SOURCE[0]}")"
ACTION="install"
if [[ "$SCRIPT_NAME" == update* ]]; then
  ACTION="update"
fi
MODE="local"          # local | service | docker
MODE_SET="0"
BACKEND=""            # cli | zed | zen | auto | both | all (empty = keep config.yaml value)
BACKEND_SET="0"
BACKEND_NORM=""
UPDATE_BOTH_ACP="0"
ACP_BIN=""
PORT=""
PORT_SET="0"
ADMIN_PASSWORD=""
ADMIN_PASSWORD_SOURCE=""   # "" | flag | stdin
LANG_CHOICE="zh"      # zh | en
AUTO_DOWNLOAD="1"
INSECURE_SKIP_VERIFY="0"
ALLOW_INSTALL_SCRIPT="0"
SKIP_GIT="0"
ZED_ACP_VERSION_SET="0"
if [[ -n "${ZED_ACP_VERSION:-}" ]]; then
  ZED_ACP_VERSION_SET="1"
fi
ZED_ACP_VERSION="${ZED_ACP_VERSION:-1.4.5}"
ZED_ACP_SHA256="${ZED_ACP_SHA256:-}"

CFG_PATH="$ROOT_DIR/config/config.yaml"
GATEWAY_BIN="$ROOT_DIR/bin/antigravity-gateway"
PROBE_BIN="$ROOT_DIR/bin/antigravity-probe"
COMPOSE_FILE="$ROOT_DIR/deploy/docker-compose.yml"
DOCKER_ENV_FILE="$ROOT_DIR/deploy/gateway.env"
CLI_MANIFEST_BASE="https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests"
CLI_INSTALL_SCRIPT_URL="https://antigravity.google/cli/install.sh"
ZED_ACP_REGISTRY_URL="https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

SERVICE_NAME="antigravity-gateway"
SERVICE_USER="antigravity"
SERVICE_STATE_DIR="/var/lib/antigravity-gateway"
SERVICE_DATA_DIR="$SERVICE_STATE_DIR/data"
SERVICE_WORKSPACE_DIR="$SERVICE_STATE_DIR/workspace"
SERVICE_CONFIG_DIR="/etc/antigravity-gateway"
SERVICE_ENV_FILE="$SERVICE_CONFIG_DIR/env"
SERVICE_BIN_DIR="/usr/local/bin"
LAUNCHD_LABEL="com.antigravity.gateway"
LAUNCHD_PLIST="$HOME/Library/LaunchAgents/${LAUNCHD_LABEL}.plist"

SCRIPT_TMP="$(mktemp -d "${TMPDIR:-/tmp}/ag-deploy.XXXXXX")"
trap 'rm -rf "$SCRIPT_TMP"' EXIT

msg() {
  local zh="$1"
  local en="$2"
  if [[ "$LANG_CHOICE" == "en" ]]; then
    echo -e "$en"
  else
    echo -e "$zh"
  fi
}

warn() {
  msg "$1" "$2" >&2
}

die() {
  msg ">>> [错误] $1" ">>> [ERROR] $2" >&2
  exit 1
}

lower() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

normalize_version() {
  printf '%s\n' "$1" | awk '
    match($0, /[vV]?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?/) {
      v = substr($0, RSTART, RLENGTH)
      sub(/^[vV]/, "", v)
      print v
      exit
    }
    {
      s = $0
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", s)
      sub(/^[vV]/, "", s)
      if (length(s) > 0) print s
      exit
    }
  '
}

json_string_field() {
  local key="$1" file="$2"
  awk -v k="$key" '
    match($0, "\"" k "\"[[:space:]]*:[[:space:]]*\"[^\"]*\"") {
      s = substr($0, RSTART, RLENGTH)
      sub("^.*:[[:space:]]*\"", "", s)
      sub("\"$", "", s)
      print s
      exit
    }
  ' "$file"
}

detect_local_zed_version() {
  local bin_path="$1"
  [[ -n "$bin_path" && -f "$bin_path" ]] || return 0
  local dir_path ver_file raw=""
  dir_path="$(dirname "$bin_path")"
  ver_file="$dir_path/.zed-acp-version"
  if [[ -r "$ver_file" ]]; then
    raw="$(awk 'NR==1{print $1; exit}' "$ver_file" 2>/dev/null || true)"
    if [[ -n "$raw" ]]; then
      normalize_version "$raw"
      return 0
    fi
  fi
  raw="$(printf '%s\n' "$bin_path" | awk -F'/' '{for (i=1; i<NF; i++) if ($i == "antigravity-acp") { print $(i+1); exit }}')"
  if [[ -n "$raw" ]]; then
    normalize_version "$raw"
  fi
}

usage() {
  if [[ "$LANG_CHOICE" == "en" ]]; then
    cat <<EOF
Usage: ./deploy/deploy.sh [options]
       ./deploy/update.sh [options]

Options:
  --action <install|update|install-acp|update-acp|start|stop|restart|status|build|build-all>
                                                Action to execute (default: install; update.sh defaults to update)
  --mode <local|service|docker>                 Deployment target; lifecycle actions follow it too (default: local, or auto-detected on update)
  --backend <cli|zed|zen|auto|both>             ACP backend engine (default: keep acp_pool.backend_type from config.yaml; 'both' updates CLI + Zed)
  --acp-bin <path>                              Explicit path to agy CLI or agy_acp_server.par
  --no-download                                 Disable automatic ACP backend download/update
  --no-git, --skip-git                          Skip 'git pull' during --action update
  --port <port>                                 Listen port (local/service) or published host port (docker).
                                                Default: AG_PORT, else server.port from config.yaml (docker: 8080)
  --admin-password-stdin                        Read the initial admin password from stdin / an interactive prompt
  --admin-password <pwd>                        Same, but on the command line (visible in shell history and ps; discouraged)
  --zed-version <ver>                           Zed ACP package version override (default: latest from ACP Registry, or ZED_ACP_VERSION)
  --zed-sha256 <hex>                            Expected SHA-256 of the Zed ACP zip (default: auto-resolved from ACP Registry, or ZED_ACP_SHA256)
  --insecure-skip-verify                        Allow installs when no expected hash or hashing tool is available
                                                (a hash MISMATCH is always fatal)
  --allow-install-script                        If the CLI manifest is unavailable, download and run the official
                                                install script (shown first; never piped into bash)
  --lang <zh|en>                                Output language: zh or en (default: zh)
  -h, --help                                    Show this help message

The admin password is never written to config/config.yaml. It is passed as AG_ADMIN_PASSWORD
(local: process environment; service: /etc/antigravity-gateway/env, mode 0600; docker: deploy/gateway.env,
mode 0600, gitignored). It only applies until the password is changed in the Web Console.

Examples:
  ./deploy/deploy.sh                                              # Build, auto-install CLI (agy) & start locally
  ./deploy/update.sh                                              # Pull latest code, update CLI/Zed ACP package & restart active mode
  ./deploy/deploy.sh --action update --backend zed                # One-click update code + Zed ACP package
  ./deploy/deploy.sh --mode local --backend cli --port 8080
  ./deploy/deploy.sh --mode service --admin-password-stdin        # systemd (dedicated user) / launchd service
  ./deploy/deploy.sh --mode service --action restart              # systemctl restart antigravity-gateway
  AG_BIND=0.0.0.0 ./deploy/deploy.sh --mode docker --port 8080    # Docker; publish on all interfaces explicitly
  ./deploy/deploy.sh --action update-acp --backend cli            # Download/update ACP backend binary only
  ./deploy/deploy.sh --action build-all                           # Cross-compile for macOS/Linux/Windows (amd64 & arm64)
EOF
  else
    cat <<EOF
用法: ./deploy/deploy.sh [选项]
      ./deploy/update.sh [选项]

选项:
  --action <install|update|install-acp|update-acp|start|stop|restart|status|build|build-all>
                                                执行动作 (默认: install；update.sh 默认为 update)
  --mode <local|service|docker>                 部署模式；start/stop/restart/status 同样按模式路由 (默认: local，update 时自动识别当前运行模式)
  --backend <cli|zed|zen|auto|both>             ACP 后端类型 (默认: 沿用 config.yaml 中的 acp_pool.backend_type；both 同时更新 CLI 与 Zed 包)
  --acp-bin <path>                              显式指定 agy 或 agy_acp_server.par 可执行文件路径
  --no-download                                 禁用自动下载/更新 ACP 后端
  --no-git, --skip-git                          在 --action update 时跳过 git pull 代码拉取
  --port <port>                                 监听端口 (local/service) 或宿主机映射端口 (docker)
                                                默认: 环境变量 AG_PORT，否则读取 config.yaml 的 server.port (docker 为 8080)
  --admin-password-stdin                        从标准输入 / 交互提示读取初始管理员密码 (推荐)
  --admin-password <pwd>                        直接在命令行传入密码 (会留在 shell 历史与 ps 中，不推荐)
  --zed-version <ver>                           指定 Zed ACP 压缩包版本 (默认从官方 ACP Registry 自动解析最新版本，或读取 ZED_ACP_VERSION)
  --zed-sha256 <hex>                            Zed ACP 压缩包的预期 SHA-256 (默认从官方 ACP Registry 自动获取，或设置 ZED_ACP_SHA256)
  --insecure-skip-verify                        缺少预期哈希或哈希工具时仍允许安装 (哈希不匹配始终报错)
  --allow-install-script                        CLI 清单不可用时，下载并展示官方安装脚本后再执行 (不会 curl | bash)
  --lang <zh|en>                                脚本输出语言: zh 或 en (默认: zh)
  -h, --help                                    显示帮助信息

管理员密码不会写入 config/config.yaml，而是通过 AG_ADMIN_PASSWORD 传递
(local: 进程环境变量; service: /etc/antigravity-gateway/env，权限 0600; docker: deploy/gateway.env，权限 0600 且已被 git 忽略)。
该密码仅在 Web 控制台修改密码之前生效。

示例:
  ./deploy/deploy.sh                                              # 一键编译、自动下载安装 agy CLI 并在本地后台启动
  ./deploy/update.sh                                              # 一键拉取最新代码、同步更新 CLI/Zed ACP 包并重启当前服务
  ./deploy/deploy.sh --action update --backend zed                # 一键拉取最新代码并同步更新 Zed ACP 独立后端包
  ./deploy/deploy.sh --mode local --backend cli --port 8080
  ./deploy/deploy.sh --mode service --admin-password-stdin        # 注册为 systemd（专用低权限用户）/ launchd 服务
  ./deploy/deploy.sh --mode service --action restart              # 等价于 systemctl restart antigravity-gateway
  AG_BIND=0.0.0.0 ./deploy/deploy.sh --mode docker --port 8080    # Docker 部署；显式对所有网卡开放端口
  ./deploy/deploy.sh --action update-acp --backend cli            # 仅下载/更新最新 ACP 后端二进制到 ./bin
  ./deploy/deploy.sh --action build-all                           # 交叉编译全平台发行包 (macOS/Linux/Windows, amd64 & arm64)
EOF
  fi
}

require_value() {
  if [[ $# -lt 2 || -z "$2" || "$2" == --* ]]; then
    echo "Missing value for $1" >&2
    usage
    exit 1
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --action) require_value "$1" "${2-}"; ACTION="$2"; shift 2 ;;
    --mode) require_value "$1" "${2-}"; MODE="$2"; MODE_SET="1"; shift 2 ;;
    --backend) require_value "$1" "${2-}"; BACKEND="$2"; BACKEND_SET="1"; shift 2 ;;
    --acp-bin) require_value "$1" "${2-}"; ACP_BIN="$2"; shift 2 ;;
    --no-download) AUTO_DOWNLOAD="0"; shift 1 ;;
    --no-git|--skip-git) SKIP_GIT="1"; shift 1 ;;
    --port) require_value "$1" "${2-}"; PORT="$2"; PORT_SET="1"; shift 2 ;;
    --admin-password) require_value "$1" "${2-}"; ADMIN_PASSWORD="$2"; ADMIN_PASSWORD_SOURCE="flag"; shift 2 ;;
    --admin-password-stdin) ADMIN_PASSWORD_SOURCE="stdin"; shift 1 ;;
    --zed-version) require_value "$1" "${2-}"; ZED_ACP_VERSION="$2"; ZED_ACP_VERSION_SET="1"; shift 2 ;;
    --zed-sha256) require_value "$1" "${2-}"; ZED_ACP_SHA256="$2"; shift 2 ;;
    --insecure-skip-verify) INSECURE_SKIP_VERIFY="1"; shift 1 ;;
    --allow-install-script) ALLOW_INSTALL_SCRIPT="1"; shift 1 ;;
    --lang) require_value "$1" "${2-}"; LANG_CHOICE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage; exit 1 ;;
  esac
done

OS_NAME="$(uname -s)"
ARCH_NAME="$(uname -m)"

detect_active_mode() {
  if command -v docker >/dev/null 2>&1 && docker inspect antigravity-gateway >/dev/null 2>&1; then
    echo "docker"
    return 0
  fi
  if [[ "$OS_NAME" == "Linux" ]] && [[ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]]; then
    echo "service"
    return 0
  fi
  if [[ "$OS_NAME" == "Darwin" ]] && [[ -f "$LAUNCHD_PLIST" ]]; then
    echo "service"
    return 0
  fi
  echo "local"
}

if [[ "$ACTION" == "update" ]]; then
  # Pull latest code before resolving config/build steps so any updated deploy.sh can re-exec cleanly.
  if [[ "$SKIP_GIT" != "1" && "${AG_UPDATE_REEXEC:-0}" != "1" ]]; then
    command -v git >/dev/null 2>&1 || die "未检测到 git，无法拉取最新代码 (如需跳过代码拉取请传入 --no-git)" \
                                          "git not found; cannot pull latest code (pass --no-git to skip git pull)"
    if ! git -C "$ROOT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
      git_repo_url="${AG_GIT_REPO:-https://github.com/Mio888888/Antigravity-ACP.git}"
      git_branch="${AG_GIT_BRANCH:-main}"
      msg ">>> 当前目录 ($ROOT_DIR) 尚未初始化 Git 仓库，正在关联远程仓库 (${git_repo_url})..." \
          ">>> Current directory ($ROOT_DIR) is not a git repository yet; linking to remote (${git_repo_url})..."
      cfg_backup=""
      if [[ -f "$CFG_PATH" ]]; then
        cfg_backup="$SCRIPT_TMP/config.yaml.preserve"
        cp -p "$CFG_PATH" "$cfg_backup"
      fi
      git -C "$ROOT_DIR" init -b "$git_branch" >/dev/null 2>&1 || git -C "$ROOT_DIR" init >/dev/null 2>&1
      if git -C "$ROOT_DIR" remote get-url origin >/dev/null 2>&1; then
        git -C "$ROOT_DIR" remote set-url origin "$git_repo_url"
      else
        git -C "$ROOT_DIR" remote add origin "$git_repo_url"
      fi
      git -C "$ROOT_DIR" fetch origin "$git_branch"
      git -C "$ROOT_DIR" checkout -f -B "$git_branch" "origin/$git_branch"
      if [[ -n "$cfg_backup" && -f "$cfg_backup" ]]; then
        cp -p "$cfg_backup" "$CFG_PATH"
      fi
      new_head="$(git -C "$ROOT_DIR" rev-parse --short HEAD)"
      msg ">>> 已同步至最新远程仓库提交 (${new_head})，重新加载最新部署脚本..." \
          ">>> Synced to latest remote commit (${new_head}); reloading updated deploy script..."
      export AG_UPDATE_REEXEC=1
      exec "$ROOT_DIR/deploy/deploy.sh" --action "$ACTION" "${ORIG_ARGS[@]}"
    fi
    stashed_BeforePull="0"
    if ! git -C "$ROOT_DIR" diff --quiet || ! git -C "$ROOT_DIR" diff --cached --quiet; then
      msg ">>> 检测到工作区存在本地修改，正在自动暂存 (git stash)..." \
          ">>> Local modifications detected; stashing before git pull..."
      git -C "$ROOT_DIR" stash push -m "antigravity-gateway-auto-update" >/dev/null
      stashed_BeforePull="1"
    fi
    old_head="$(git -C "$ROOT_DIR" rev-parse --short HEAD)"
    msg ">>> [更新] 正在从远程仓库拉取最新代码 (当前提交: ${old_head})..." \
        ">>> [Update] Pulling latest source code from remote (current commit: ${old_head})..."
    if ! git -C "$ROOT_DIR" pull --ff-only; then
      if [[ "$stashed_BeforePull" == "1" ]]; then
        git -C "$ROOT_DIR" stash pop >/dev/null 2>&1 || true
      fi
      die "git pull --ff-only 失败，请检查网络连接或本地分支状态" \
          "git pull --ff-only failed; please check network connection or local branch state"
    fi
    new_head="$(git -C "$ROOT_DIR" rev-parse --short HEAD)"
    if [[ "$stashed_BeforePull" == "1" ]]; then
      if ! git -C "$ROOT_DIR" stash pop >/dev/null 2>&1; then
        die "恢复本地修改 (git stash pop) 时发生冲突，请手动解决冲突后重试" \
            "Conflict while restoring local changes (git stash pop); please resolve manually"
      fi
    fi
    if [[ "$old_head" == "$new_head" ]]; then
      msg ">>> 代码已是最新版本 (${new_head})" ">>> Source code is already at the latest commit (${new_head})"
    else
      msg ">>> 代码已更新: ${old_head} -> ${new_head}，重新加载最新部署脚本..." \
          ">>> Source code updated: ${old_head} -> ${new_head}; reloading updated deploy script..."
      export AG_UPDATE_REEXEC=1
      exec "$ROOT_DIR/deploy/deploy.sh" --action "$ACTION" "${ORIG_ARGS[@]}"
    fi
  fi
  if [[ "$MODE_SET" != "1" ]]; then
    MODE="$(detect_active_mode)"
    msg ">>> 自动识别当前运行模式: ${MODE}" ">>> Auto-detected active deployment mode: ${MODE}"
  fi
fi

case "$MODE" in
  local|service|docker) ;;
  *) die "无效的 --mode: $MODE" "Invalid --mode: $MODE" ;;
esac

# ------------------------------------------------------------------------------
# config.yaml helpers (non-secret keys only)
# ------------------------------------------------------------------------------

# read_config_value <key> [file]: first scalar value of <key>, unquoted, comments stripped.
read_config_value() {
  local key="$1"
  local file="${2:-$CFG_PATH}"
  [[ -r "$file" ]] || return 0
  awk -v k="$key" '
    $0 ~ ("^[[:space:]]*" k "[[:space:]]*:") {
      sub("^[[:space:]]*" k "[[:space:]]*:[[:space:]]*", "")
      sub(/[[:space:]]+#.*$/, "")
      sub(/[[:space:]]+$/, "")
      if ($0 ~ /^".*"$/ || $0 ~ /^\047.*\047$/) { $0 = substr($0, 2, length($0) - 2) }
      print
      exit
    }' "$file" 2>/dev/null || true
}

yaml_quote() {
  printf '"%s"' "$(printf '%s' "$1" | sed -e 's/[\\"]/\\&/g')"
}

# set_yaml_value <file> <section> <key> <rendered-value>: replace <key> inside top-level <section>,
# inserting it at the end of the section if missing. Values are passed via ENVIRON, so no awk
# escape processing or "&" substitution happens.
set_yaml_value() {
  local file="$1" section="$2" key="$3" value="$4"
  local tmp="$SCRIPT_TMP/yaml.$$"
  YAML_SECTION="$section" YAML_KEY="$key" YAML_VAL="$value" awk '
    BEGIN { s = ENVIRON["YAML_SECTION"]; k = ENVIRON["YAML_KEY"]; v = ENVIRON["YAML_VAL"]; insec = 0; done = 0 }
    /^[^[:space:]#][^:]*:/ {
      if (insec && !done) { print "  " k ": " v; done = 1 }
      insec = ($0 ~ ("^" s "[[:space:]]*:"))
      print
      next
    }
    insec && !done && $0 ~ ("^[[:space:]]+" k "[[:space:]]*:") {
      indent = $0
      sub(/[^[:space:]].*$/, "", indent)
      print indent k ": " v
      done = 1
      next
    }
    { print }
    END { if (insec && !done) print "  " k ": " v }
  ' "$file" >"$tmp"
  cat "$tmp" >"$file"
  rm -f "$tmp"
}

# Resolve a path from config.yaml relative to the repo root (the gateway is launched from there).
resolve_root_path() {
  local p="$1"
  case "$p" in
    /*) printf '%s' "$p" ;;
    ./*) printf '%s/%s' "$ROOT_DIR" "${p#./}" ;;
    *) printf '%s/%s' "$ROOT_DIR" "$p" ;;
  esac
}

local_data_dir() {
  local d
  d="$(read_config_value data_dir)"
  resolve_root_path "${d:-./data}"
}

health_host() {
  local file="${1:-$CFG_PATH}"
  local h
  h="$(read_config_value host "$file")"
  case "$h" in
    ""|0.0.0.0|"::"|"[::]") echo "127.0.0.1" ;;
    *:*) echo "[${h#[}" | sed 's/]*$/]/' ;;
    *) echo "$h" ;;
  esac
}

# ------------------------------------------------------------------------------
# Resolve backend / port / admin password
# ------------------------------------------------------------------------------

if [[ "$BACKEND_SET" == "1" ]]; then
  BACKEND_NORM="$(lower "$BACKEND")"
else
  if [[ "$MODE" == "docker" ]] && command -v docker >/dev/null 2>&1 && docker inspect antigravity-gateway >/dev/null 2>&1; then
    BACKEND_NORM="$(lower "$(docker inspect antigravity-gateway --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | awk -F= '$1=="AG_ACP_BACKEND"{print $2; exit}' || true)")"
  elif [[ "$MODE" == "service" && "$OS_NAME" == "Linux" && -r "$SERVICE_CONFIG_DIR/config.yaml" ]]; then
    BACKEND_NORM="$(lower "$(read_config_value backend_type "$SERVICE_CONFIG_DIR/config.yaml")")"
  fi
  [[ -n "${BACKEND_NORM:-}" ]] || BACKEND_NORM="$(lower "$(read_config_value backend_type)")"
  [[ -n "${BACKEND_NORM:-}" ]] || BACKEND_NORM="cli"
fi
[[ "$BACKEND_NORM" == "zen" ]] && BACKEND_NORM="zed"
if [[ "$BACKEND_NORM" == "both" || "$BACKEND_NORM" == "all" ]]; then
  UPDATE_BOTH_ACP="1"
  BACKEND_NORM="auto"
fi
case "$BACKEND_NORM" in
  cli|zed|auto) ;;
  *) die "无效的 --backend: $BACKEND_NORM" "Invalid --backend: $BACKEND_NORM" ;;
esac

if [[ "$PORT_SET" != "1" ]]; then
  if [[ -n "${AG_PORT:-}" ]]; then
    PORT="$AG_PORT"
  elif [[ "$MODE" == "docker" ]]; then
    if command -v docker >/dev/null 2>&1 && docker inspect antigravity-gateway >/dev/null 2>&1; then
      PORT="$(docker inspect antigravity-gateway --format '{{with index .HostConfig.PortBindings "8080/tcp"}}{{with index . 0}}{{.HostPort}}{{end}}{{end}}' 2>/dev/null || true)"
    fi
    [[ -n "$PORT" ]] || PORT="8080"
  elif [[ "$MODE" == "service" && "$OS_NAME" == "Linux" && -r "$SERVICE_CONFIG_DIR/config.yaml" ]]; then
    PORT="$(read_config_value port "$SERVICE_CONFIG_DIR/config.yaml")"
    [[ -n "$PORT" ]] || PORT="8080"
  else
    PORT="$(read_config_value port)"
    [[ -n "$PORT" ]] || PORT="8080"
  fi
fi
if ! [[ "$PORT" =~ ^[0-9]+$ ]] || (( PORT < 1 || PORT > 65535 )); then
  die "无效的端口: $PORT" "Invalid port: $PORT"
fi

for secret_key in admin_password admin_token; do
  secret_val="$(read_config_value "$secret_key")"
  if [[ -n "$secret_val" && "$(lower "$secret_val")" != "auto" ]]; then
    warn ">>> [警告] config/config.yaml 中的 ${secret_key} 不是 \"auto\"。该文件受 git 跟踪，请改回 \"auto\" 并改用 --admin-password-stdin / AG_ADMIN_PASSWORD。" \
         ">>> [WARN] ${secret_key} in config/config.yaml is not \"auto\". The file is tracked by git; reset it to \"auto\" and use --admin-password-stdin / AG_ADMIN_PASSWORD instead."
  fi
done

if [[ -n "$ACP_BIN" && ! -f "$ACP_BIN" ]]; then
  die "--acp-bin 指定的文件不存在: $ACP_BIN" "--acp-bin file does not exist: $ACP_BIN"
fi

if [[ "$ADMIN_PASSWORD_SOURCE" == "stdin" ]]; then
  if [[ -t 0 ]]; then
    read -r -s -p "Admin password / 管理员密码: " ADMIN_PASSWORD
    echo >&2
  else
    IFS= read -r ADMIN_PASSWORD || true
  fi
elif [[ "$ADMIN_PASSWORD_SOURCE" == "flag" ]]; then
  warn ">>> [警告] --admin-password 会出现在 shell 历史与进程列表中，建议改用 --admin-password-stdin。" \
       ">>> [WARN] --admin-password is visible in shell history and the process list; prefer --admin-password-stdin."
fi
if [[ -n "$ADMIN_PASSWORD_SOURCE" ]]; then
  if [[ ${#ADMIN_PASSWORD} -lt 8 ]]; then
    die "管理员密码至少需要 8 个字符" "Admin password must be at least 8 characters"
  fi
  case "$ADMIN_PASSWORD" in
    *$'\n'*|*$'\r'*) die "管理员密码不能包含换行符" "Admin password must not contain newlines" ;;
  esac
  msg ">>> 提示: 该密码仅在 Web 控制台「修改密码」之前生效；修改后以数据库中的哈希为准。" \
      ">>> Note: this password only applies until it is changed in the Web Console; afterwards the DB hash is authoritative."
fi

# ------------------------------------------------------------------------------
# ACP backend discovery
# ------------------------------------------------------------------------------

find_existing_zed_binary() {
  for candidate in \
    "$ROOT_DIR/bin/agy_acp_server.par" \
    "$ROOT_DIR/bin/agy_acp_server" \
    "/usr/local/bin/agy_acp_server.par" \
    "/opt/antigravity-acp/agy_acp_server.par"; do
    if [[ -f "$candidate" ]]; then
      echo "$candidate"
      return 0
    fi
  done

  if [[ "$OS_NAME" == "Darwin" ]]; then
    local zed_dir="$HOME/Library/Application Support/Zed/external_agents/registry/antigravity-acp"
    if [[ -d "$zed_dir" ]]; then
      local found
      found="$(find "$zed_dir" -type f -name "agy_acp_server*" 2>/dev/null | sort | tail -n 1 || true)"
      if [[ -n "$found" ]]; then
        echo "$found"
        return 0
      fi
    fi
  else
    for zed_dir in \
      "$HOME/.local/share/zed/external_agents/registry/antigravity-acp" \
      "$HOME/.config/zed/external_agents/registry/antigravity-acp"; do
      if [[ -d "$zed_dir" ]]; then
        local found
        found="$(find "$zed_dir" -type f -name "agy_acp_server*" 2>/dev/null | sort | tail -n 1 || true)"
        if [[ -n "$found" ]]; then
          echo "$found"
          return 0
        fi
      fi
    done
  fi
  return 1
}

find_existing_cli_binary() {
  for candidate in \
    "$ROOT_DIR/bin/agy" \
    "$ROOT_DIR/bin/antigravity" \
    "/usr/local/bin/agy" \
    "/opt/antigravity-acp/agy" \
    "/opt/homebrew/bin/agy" \
    "$HOME/.local/bin/agy" \
    "$HOME/.antigravity/bin/agy"; do
    if [[ -f "$candidate" ]]; then
      echo "$candidate"
      return 0
    fi
  done
  if command -v agy >/dev/null 2>&1; then
    command -v agy
    return 0
  elif command -v antigravity >/dev/null 2>&1; then
    command -v antigravity
    return 0
  fi
  return 1
}

# ------------------------------------------------------------------------------
# Download integrity (fail closed)
# ------------------------------------------------------------------------------

file_digest() {
  local bits="$1" file="$2"
  if command -v "sha${bits}sum" >/dev/null 2>&1; then
    "sha${bits}sum" "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a "$bits" "$file" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst "-sha${bits}" -r "$file" | awk '{print $1}'
  else
    return 1
  fi
}

# verify_digest <256|512> <file> <expected-hex> <label>
# Missing expected hash or hashing tool: error unless --insecure-skip-verify. Mismatch: always fatal.
verify_digest() {
  local bits="$1" file="$2" label="$4"
  local expected
  expected="$(lower "$(printf '%s' "$3" | tr -d '[:space:]')")"
  if [[ -z "$expected" ]]; then
    if [[ "$INSECURE_SKIP_VERIFY" == "1" ]]; then
      warn ">>> [警告] ${label} 缺少预期 SHA-${bits}，因 --insecure-skip-verify 跳过校验" \
           ">>> [WARN] No expected SHA-${bits} for ${label}; skipping verification because of --insecure-skip-verify"
      return 0
    fi
    die "${label} 缺少预期 SHA-${bits}，拒绝安装 (如确需跳过请显式传入 --insecure-skip-verify)" \
        "No expected SHA-${bits} for ${label}; refusing to install (pass --insecure-skip-verify to override)"
  fi
  local want_len=$(( bits / 4 ))
  if ! [[ "$expected" =~ ^[0-9a-f]+$ ]] || [[ ${#expected} -ne $want_len ]]; then
    die "${label} 的预期 SHA-${bits} 格式无效: $expected" "Invalid expected SHA-${bits} for ${label}: $expected"
  fi
  local actual=""
  if ! actual="$(file_digest "$bits" "$file")" || [[ -z "$actual" ]]; then
    if [[ "$INSECURE_SKIP_VERIFY" == "1" ]]; then
      warn ">>> [警告] 未找到 sha${bits}sum/shasum/openssl，因 --insecure-skip-verify 跳过 ${label} 校验" \
           ">>> [WARN] No sha${bits}sum/shasum/openssl found; skipping ${label} verification because of --insecure-skip-verify"
      return 0
    fi
    die "未找到 sha${bits}sum/shasum/openssl，无法校验 ${label}" "No sha${bits}sum/shasum/openssl available to verify ${label}"
  fi
  if [[ "$(lower "$actual")" != "$expected" ]]; then
    die "${label} SHA-${bits} 校验失败: 预期 ${expected}，实际 ${actual}" \
        "${label} SHA-${bits} mismatch: expected $expected, got $actual"
  fi
  msg "    -> SHA-${bits} 校验通过" "    -> SHA-${bits} verified"
}

run_official_install_script() {
  local dest_dir="$1"
  local script="$SCRIPT_TMP/agy-install.sh"
  msg ">>> 正在下载官方安装脚本 ${CLI_INSTALL_SCRIPT_URL} (未执行，先展示内容)..." \
      ">>> Downloading official installer ${CLI_INSTALL_SCRIPT_URL} (not executed yet; showing it first)..."
  curl -fsSL --retry 3 -o "$script" "$CLI_INSTALL_SCRIPT_URL"
  echo "------------------------------------------------------------------------------" >&2
  sed 's/^/    | /' "$script" >&2
  echo "------------------------------------------------------------------------------" >&2
  msg "    -> 脚本 SHA-256: $(file_digest 256 "$script" || echo unknown)" \
      "    -> Script SHA-256: $(file_digest 256 "$script" || echo unknown)"
  if [[ -t 0 ]]; then
    local answer=""
    read -r -p "Run this script? / 是否执行该脚本? [y/N] " answer
    [[ "$answer" == "y" || "$answer" == "Y" ]] || die "已取消" "Aborted"
  fi
  bash "$script" --dir "$dest_dir"
}

cli_manifest_unavailable() {
  local dest_dir="$1" reason="$2"
  if [[ "$ALLOW_INSTALL_SCRIPT" == "1" ]]; then
    warn ">>> [警告] ${reason}；因 --allow-install-script 回退到官方安装脚本" \
         ">>> [WARN] ${reason}; falling back to the official install script because of --allow-install-script"
    run_official_install_script "$dest_dir"
    return 0
  fi
  die "${reason}。可使用 --acp-bin 指定已安装的 agy，或显式传入 --allow-install-script 改用官方安装脚本" \
      "${reason}. Use --acp-bin to point at an installed agy, or pass --allow-install-script to use the official installer"
}

install_cli_backend() {
  local dest_dir="${1:-$ROOT_DIR/bin}"
  mkdir -p "$dest_dir"
  local target_bin="$dest_dir/agy"

  local os_key=""
  case "$OS_NAME" in
    Darwin) os_key="darwin" ;;
    Linux)  os_key="linux" ;;
    *) die "当前系统 ($OS_NAME) 不支持自动下载 CLI，请使用 --acp-bin 手动指定" \
           "OS ($OS_NAME) not supported for auto CLI download; specify --acp-bin manually" ;;
  esac

  local arch_key=""
  case "$ARCH_NAME" in
    x86_64|amd64)  arch_key="amd64" ;;
    arm64|aarch64) arch_key="arm64" ;;
    *) die "当前架构 ($ARCH_NAME) 不支持自动下载 CLI，请使用 --acp-bin 手动指定" \
           "Architecture ($ARCH_NAME) not supported for auto CLI download; specify --acp-bin manually" ;;
  esac

  local platform="${os_key}_${arch_key}"
  if [[ "$os_key" == "linux" ]]; then
    if [[ -f /lib/libc.musl-x86_64.so.1 ]] || [[ -f /lib/libc.musl-aarch64.so.1 ]] || { ldd /bin/ls 2>&1 || true; } | grep -q musl; then
      platform="linux_${arch_key}_musl"
    fi
  fi

  msg ">>> 正在检查 Antigravity CLI 后端版本 (platform=${platform}) ..." \
      ">>> Checking Antigravity CLI backend version (platform=${platform}) ..."

  local work="$SCRIPT_TMP/cli"
  rm -rf "$work"
  mkdir -p "$work"
  local manifest_file="$work/manifest.json"
  if ! curl -fsSL --retry 3 -A "Antigravity-Gateway-Deploy/1.6" -o "$manifest_file" "${CLI_MANIFEST_BASE}/${platform}.json" || [[ ! -s "$manifest_file" ]]; then
    cli_manifest_unavailable "$dest_dir" "无法获取 CLI 发布清单 / CLI release manifest unavailable"
    return 0
  fi

  local version pkg_url expected_sha512 remote_ver
  version="$(json_string_field "version" "$manifest_file")"
  pkg_url="$(json_string_field "url" "$manifest_file")"
  expected_sha512="$(json_string_field "sha512" "$manifest_file")"
  remote_ver="$(normalize_version "$version")"
  if [[ -z "$pkg_url" ]]; then
    cli_manifest_unavailable "$dest_dir" "CLI 发布清单中没有下载地址 / CLI manifest has no package URL"
    return 0
  fi
  if [[ "$pkg_url" != https://* ]]; then
    die "CLI 下载地址不是 HTTPS: $pkg_url" "CLI package URL is not HTTPS: $pkg_url"
  fi

  local check_bin="" current_ver="" current_ver_norm=""
  if [[ -x "$target_bin" ]]; then
    check_bin="$target_bin"
  else
    check_bin="$(find_existing_cli_binary || true)"
  fi
  if [[ -n "$check_bin" && -x "$check_bin" ]]; then
    current_ver="$("$check_bin" --version 2>/dev/null | awk 'NR==1{gsub(/\r/,""); print; exit}' || true)"
    current_ver_norm="$(normalize_version "$current_ver")"
  fi
  if [[ -n "$current_ver_norm" && -n "$remote_ver" && "$current_ver_norm" == "$remote_ver" ]]; then
    rm -rf "$work"
    msg ">>> Antigravity CLI 已是相同版本 (v${remote_ver})，跳过下载更新: ${check_bin}" \
        ">>> Antigravity CLI is already at version v${remote_ver}; skipping download: ${check_bin}"
    return 0
  fi

  if [[ -n "$current_ver_norm" ]]; then
    echo "    -> Updating Antigravity CLI (v${current_ver_norm} -> v${remote_ver:-${version:-unknown}}): ${pkg_url}"
  elif [[ -n "$current_ver" ]]; then
    echo "    -> Updating Antigravity CLI (${current_ver} -> v${version:-unknown}): ${pkg_url}"
  else
    echo "    -> Downloading Antigravity CLI v${remote_ver:-${version:-unknown}}: ${pkg_url}"
  fi
  local pkg_path="$work/agy_pkg"
  curl -fL --retry 3 -o "$pkg_path" "$pkg_url"
  verify_digest 512 "$pkg_path" "$expected_sha512" "Antigravity CLI ${version:-}"

  if [[ "$pkg_url" == *.tar.gz* ]]; then
    tar -xzf "$pkg_path" -C "$work" antigravity
    install -m 0755 "$work/antigravity" "$target_bin"
  else
    install -m 0755 "$pkg_path" "$target_bin"
  fi
  if [[ "$os_key" == "darwin" ]]; then
    xattr -d com.apple.quarantine "$target_bin" 2>/dev/null || true
  fi
  rm -rf "$work"
  msg ">>> Antigravity CLI v${version:-unknown} 安装/更新完成: ${target_bin}" \
      ">>> Antigravity CLI v${version:-unknown} installed/updated at: ${target_bin}"
}

install_zed_backend() {
  local dest_dir="${1:-$ROOT_DIR/bin}"
  mkdir -p "$dest_dir"

  local os_key="" release_os=""
  case "$OS_NAME" in
    Darwin) os_key="darwin"; release_os="macos" ;;
    Linux)  os_key="linux";  release_os="linux" ;;
    *) die "当前系统 ($OS_NAME) 不支持自动下载 Zed ACP 包" "OS ($OS_NAME) not supported for auto Zed ACP download" ;;
  esac

  local release_arch="" reg_arch=""
  case "$ARCH_NAME" in
    x86_64|amd64)  release_arch="x86_64"; reg_arch="x86_64" ;;
    arm64|aarch64) release_arch="arm64";  reg_arch="aarch64" ;;
    *) die "当前架构 ($ARCH_NAME) 不支持自动下载 Zed ACP 包" "Architecture ($ARCH_NAME) not supported for auto Zed ACP download" ;;
  esac

  local reg_target="${os_key}-${reg_arch}"
  local archive_url=""
  local reg_file="$SCRIPT_TMP/acp-registry.json"
  if [[ -z "$ZED_ACP_SHA256" ]]; then
    msg ">>> 正在从官方 ACP Registry 查询最新 Zed ACP 包信息 (target=${reg_target}) ..." \
        ">>> Querying official ACP Registry for latest Zed ACP package (target=${reg_target}) ..."
    if curl -fsSL --retry 3 -A "Antigravity-Gateway-Deploy/1.6" -o "$reg_file" "$ZED_ACP_REGISTRY_URL" 2>/dev/null && [[ -s "$reg_file" ]]; then
      local reg_ver reg_archive reg_sha
      reg_ver="$(awk '/"id"[ \t]*:[ \t]*"antigravity-acp"/ { in_a=1; next } in_a && /"id"[ \t]*:/ { in_a=0 } in_a && !v && /"version"[ \t]*:/ { l=$0; sub(/^.*"version"[ \t]*:[ \t]*"/, "", l); sub(/".*$/, "", l); v=l } END { print v }' "$reg_file")"
      if [[ "$ZED_ACP_VERSION_SET" != "1" || "$ZED_ACP_VERSION" == "latest" || "$ZED_ACP_VERSION" == "$reg_ver" ]]; then
        reg_archive="$(awk -v target="$reg_target" '/"id"[ \t]*:[ \t]*"antigravity-acp"/ { in_a=1; next } in_a && /"id"[ \t]*:/ { in_a=0 } in_a && $0 ~ ("\"" target "\"[ \t]*:") { in_t=1; next } in_t && /}/ { in_t=0 } in_t && /"archive"[ \t]*:/ { l=$0; sub(/^.*"archive"[ \t]*:[ \t]*"/, "", l); sub(/".*$/, "", l); print l; exit }' "$reg_file")"
        reg_sha="$(awk -v target="$reg_target" '/"id"[ \t]*:[ \t]*"antigravity-acp"/ { in_a=1; next } in_a && /"id"[ \t]*:/ { in_a=0 } in_a && $0 ~ ("\"" target "\"[ \t]*:") { in_t=1; next } in_t && /}/ { in_t=0 } in_t && /"sha256"[ \t]*:/ { l=$0; sub(/^.*"sha256"[ \t]*:[ \t]*"/, "", l); sub(/".*$/, "", l); print l; exit }' "$reg_file")"
        if [[ -n "$reg_archive" && -n "$reg_sha" ]]; then
          ZED_ACP_VERSION="$reg_ver"
          archive_url="$reg_archive"
          ZED_ACP_SHA256="$reg_sha"
          echo "    -> Matched Zed ACP v${ZED_ACP_VERSION} (${reg_target}): ${archive_url}"
        fi
      fi
    fi
  fi

  local remote_zed_ver existing_zed="" local_zed_ver=""
  remote_zed_ver="$(normalize_version "$ZED_ACP_VERSION")"
  if [[ -f "$dest_dir/agy_acp_server.par" ]]; then
    existing_zed="$dest_dir/agy_acp_server.par"
  elif [[ -f "$dest_dir/agy_acp_server" ]]; then
    existing_zed="$dest_dir/agy_acp_server"
  else
    existing_zed="$(find_existing_zed_binary || true)"
  fi
  if [[ -n "$existing_zed" ]]; then
    local_zed_ver="$(detect_local_zed_version "$existing_zed")"
  fi
  if [[ -n "$local_zed_ver" && -n "$remote_zed_ver" && "$local_zed_ver" == "$remote_zed_ver" ]]; then
    msg ">>> Zed ACP 独立后端包已是相同版本 (v${remote_zed_ver})，跳过下载更新: ${existing_zed}" \
        ">>> Zed ACP standalone package is already at version v${remote_zed_ver}; skipping download: ${existing_zed}"
    return 0
  fi

  local archive_name="agy-acp-server-${ZED_ACP_VERSION}-${os_key}-${release_arch}.zip"
  if [[ -z "$archive_url" ]]; then
    archive_url="https://dl.google.com/agy-extensions/releases/${release_os}/${archive_name}"
  fi
  if [[ "$archive_url" != https://* ]]; then
    die "Zed ACP 下载地址不是 HTTPS: $archive_url" "Zed ACP package URL is not HTTPS: $archive_url"
  fi

  if [[ -z "$ZED_ACP_SHA256" && "$INSECURE_SKIP_VERIFY" != "1" ]]; then
    die "无法从官方 ACP Registry 获取 ${archive_name} 的 SHA-256，且未手动指定。请设置 ZED_ACP_SHA256=<sha256> 或传入 --zed-sha256 <hex>。\n    哈希应来自可信来源，例如 ${ZED_ACP_REGISTRY_URL} 中 antigravity-acp 条目的 distribution.binary.${reg_target}.sha256。\n    也可以用 --acp-bin 指定已安装的 agy_acp_server.par。" \
        "Could not resolve SHA-256 for ${archive_name} from the ACP Registry and none was supplied. Set ZED_ACP_SHA256=<sha256> or pass --zed-sha256 <hex>.\n    Take the hash from a trusted source, e.g. distribution.binary.${reg_target}.sha256 of the antigravity-acp entry in ${ZED_ACP_REGISTRY_URL}.\n    Alternatively pass --acp-bin with an already installed agy_acp_server.par."
  fi
  if ! command -v unzip >/dev/null 2>&1 && ! command -v python3 >/dev/null 2>&1 && ! command -v bsdtar >/dev/null 2>&1; then
    if [[ "$OS_NAME" == "Linux" ]] && command -v apt-get >/dev/null 2>&1; then
      msg ">>> 正在自动安装 unzip 以解压 Zed ACP 包..." ">>> Auto-installing unzip to extract Zed ACP package..."
      if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
        apt-get update -qq && apt-get install -y -qq unzip >/dev/null
      elif command -v sudo >/dev/null 2>&1; then
        sudo apt-get update -qq && sudo apt-get install -y -qq unzip >/dev/null
      fi
    fi
  fi
  if ! command -v unzip >/dev/null 2>&1 && ! command -v python3 >/dev/null 2>&1 && ! command -v bsdtar >/dev/null 2>&1; then
    die "需要 unzip、python3 或 bsdtar 来解压 Zed ACP 包" "unzip, python3, or bsdtar is required to extract the Zed ACP package"
  fi

  msg ">>> 正在下载并更新 Zed ACP 独立后端包 (v${ZED_ACP_VERSION}) ${archive_url} -> ${dest_dir} ..." \
      ">>> Downloading & updating Zed ACP standalone package (v${ZED_ACP_VERSION}) ${archive_url} -> ${dest_dir} ..."
  local work="$SCRIPT_TMP/zed"
  rm -rf "$work"
  mkdir -p "$work/extract"
  local zip_path="$work/agy_acp.zip"
  curl -fL --retry 3 -o "$zip_path" "$archive_url"
  verify_digest 256 "$zip_path" "$ZED_ACP_SHA256" "$archive_name"
  if command -v unzip >/dev/null 2>&1; then
    unzip -q -o "$zip_path" -d "$work/extract"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -m zipfile -e "$zip_path" "$work/extract"
  else
    bsdtar -xf "$zip_path" -C "$work/extract"
  fi

  local installed="0"
  for fn in agy_acp_server.par agy_acp_server localharness_external; do
    if [[ -f "$work/extract/$fn" ]]; then
      install -m 0755 "$work/extract/$fn" "$dest_dir/$fn"
      installed="1"
      if [[ "$os_key" == "darwin" ]]; then
        xattr -d com.apple.quarantine "$dest_dir/$fn" 2>/dev/null || true
      fi
    fi
  done
  rm -rf "$work"
  [[ "$installed" == "1" ]] || die "压缩包中未找到 agy_acp_server" "agy_acp_server not found in the archive"
  printf '%s %s\n' "${remote_zed_ver:-$ZED_ACP_VERSION}" "$(lower "$ZED_ACP_SHA256")" >"$dest_dir/.zed-acp-version"
  msg ">>> Zed ACP v${ZED_ACP_VERSION} 独立后端包安装/更新完成: ${dest_dir}" \
      ">>> Zed ACP v${ZED_ACP_VERSION} standalone package installed/updated in: ${dest_dir}"
}

ensure_acp_backend() {
  if [[ -n "$ACP_BIN" ]]; then
    return 0
  fi
  if [[ "$AUTO_DOWNLOAD" != "1" ]]; then
    return 0
  fi

  case "$BACKEND_NORM" in
    zed)
      if ! find_existing_zed_binary >/dev/null 2>&1; then
        install_zed_backend "$ROOT_DIR/bin"
      fi
      ;;
    cli)
      if ! find_existing_cli_binary >/dev/null 2>&1; then
        install_cli_backend "$ROOT_DIR/bin"
      fi
      ;;
    *)
      if ! find_existing_cli_binary >/dev/null 2>&1 && ! find_existing_zed_binary >/dev/null 2>&1; then
        install_cli_backend "$ROOT_DIR/bin"
      fi
      ;;
  esac
}

update_acp_backend() {
  if [[ -n "$ACP_BIN" ]]; then
    msg ">>> 已显式指定 --acp-bin ($ACP_BIN)，跳过自动下载更新 ACP 后端" \
        ">>> --acp-bin ($ACP_BIN) explicitly set; skipping automatic ACP backend update"
    return 0
  fi
  if [[ "$AUTO_DOWNLOAD" != "1" ]]; then
    msg ">>> 已指定 --no-download，跳过自动更新 ACP 后端" \
        ">>> --no-download set; skipping automatic ACP backend update"
    return 0
  fi

  if [[ "$UPDATE_BOTH_ACP" == "1" ]]; then
    install_cli_backend "$ROOT_DIR/bin"
    install_zed_backend "$ROOT_DIR/bin"
    return 0
  fi

  case "$BACKEND_NORM" in
    zed)
      install_zed_backend "$ROOT_DIR/bin"
      if [[ "$BACKEND_SET" != "1" && ( -f "$ROOT_DIR/bin/agy" || -f "$ROOT_DIR/bin/antigravity" ) ]]; then
        install_cli_backend "$ROOT_DIR/bin"
      fi
      ;;
    cli)
      install_cli_backend "$ROOT_DIR/bin"
      if [[ "$BACKEND_SET" != "1" && ( -f "$ROOT_DIR/bin/agy_acp_server.par" || -f "$ROOT_DIR/bin/agy_acp_server" ) ]]; then
        install_zed_backend "$ROOT_DIR/bin"
      fi
      ;;
    *)
      local has_cli="0" has_zed="0"
      find_existing_cli_binary >/dev/null 2>&1 && has_cli="1"
      find_existing_zed_binary >/dev/null 2>&1 && has_zed="1"
      if [[ "$has_zed" == "1" && "$has_cli" == "0" ]]; then
        install_zed_backend "$ROOT_DIR/bin"
      elif [[ "$has_zed" == "1" && "$has_cli" == "1" ]]; then
        install_cli_backend "$ROOT_DIR/bin"
        install_zed_backend "$ROOT_DIR/bin"
      else
        install_cli_backend "$ROOT_DIR/bin"
      fi
      ;;
  esac
}

discover_acp_binary() {
  if [[ -n "$ACP_BIN" ]]; then
    echo "$ACP_BIN"
    return 0
  fi

  local found_cli=""
  local found_zed=""
  found_cli="$(find_existing_cli_binary || true)"
  found_zed="$(find_existing_zed_binary || true)"

  case "$BACKEND_NORM" in
    zed)
      echo "${found_zed:-agy_acp_server.par}"
      ;;
    cli)
      echo "${found_cli:-agy}"
      ;;
    *)
      if [[ -n "$found_cli" ]]; then
        echo "$found_cli"
      elif [[ -n "$found_zed" ]]; then
        echo "$found_zed"
      else
        echo "auto"
      fi
      ;;
  esac
}

# ------------------------------------------------------------------------------
# Build
# ------------------------------------------------------------------------------

build_frontend() {
  msg ">>> [1/3] 构建中英双语前端控制台 (web/dist)..." ">>> [1/3] Building bilingual ReUI frontend (web/dist)..."
  if [[ ! -d "$ROOT_DIR/web/node_modules" || "$ROOT_DIR/web/package-lock.json" -nt "$ROOT_DIR/web/node_modules/.package-lock.json" || "$ROOT_DIR/web/package.json" -nt "$ROOT_DIR/web/node_modules/.package-lock.json" ]]; then
    npm --prefix "$ROOT_DIR/web" ci
  fi
  npm --prefix "$ROOT_DIR/web" run build
}

build_local_binaries() {
  build_frontend
  mkdir -p "$ROOT_DIR/bin"
  msg ">>> [2/3] 编译 Go 网关与探测工具 ($OS_NAME/$ARCH_NAME)..." ">>> [2/3] Building Go gateway & probe binaries ($OS_NAME/$ARCH_NAME)..."
  go build -tags webui -trimpath -ldflags="-s -w" -o "$GATEWAY_BIN" ./cmd/gateway
  go build -trimpath -ldflags="-s -w" -o "$PROBE_BIN" ./cmd/probe
}

# Copy the tracked (pristine) config so local edits never end up in release archives.
copy_pristine_config() {
  local dest="$1"
  if command -v git >/dev/null 2>&1 && git -C "$ROOT_DIR" cat-file -e HEAD:config/config.yaml 2>/dev/null; then
    git -C "$ROOT_DIR" show HEAD:config/config.yaml >"$dest"
  else
    warn ">>> [警告] 无法从 git 读取原始 config.yaml，改为复制工作区版本，请确认其中不含敏感信息" \
         ">>> [WARN] Could not read the tracked config.yaml from git; copying the working copy — make sure it holds no secrets"
    cp "$CFG_PATH" "$dest"
  fi
}

build_all_platforms() {
  build_frontend
  mkdir -p "$ROOT_DIR/dist"
  local targets=(
    "darwin/arm64"
    "darwin/amd64"
    "linux/amd64"
    "linux/arm64"
    "windows/amd64"
    "windows/arm64"
  )
  for target in "${targets[@]}"; do
    local goos="${target%/*}"
    local goarch="${target#*/}"
    local ext=""
    [[ "$goos" == "windows" ]] && ext=".exe"
    local out_dir="$ROOT_DIR/dist/antigravity-gateway-${goos}-${goarch}"
    mkdir -p "$out_dir/config" "$out_dir/deploy"
    msg ">>> 交叉编译 ${goos}/${goarch}..." ">>> Cross-compiling ${goos}/${goarch}..."
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -tags webui -trimpath -ldflags="-s -w" \
      -o "$out_dir/antigravity-gateway${ext}" ./cmd/gateway
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" \
      -o "$out_dir/antigravity-probe${ext}" ./cmd/probe
    copy_pristine_config "$out_dir/config/config.yaml"
    cp "$ROOT_DIR/deploy/deploy.sh" "$ROOT_DIR/deploy/update.sh" "$ROOT_DIR/deploy/deploy.ps1" "$ROOT_DIR/deploy/update.ps1" "$ROOT_DIR/deploy/Dockerfile" "$ROOT_DIR/deploy/docker-compose.yml" "$ROOT_DIR/deploy/antigravity-gateway.service" "$ROOT_DIR/deploy/gateway.env.example" "$out_dir/deploy/" 2>/dev/null || true
    cp "$ROOT_DIR/README.md" "$ROOT_DIR/README_EN.md" "$out_dir/" 2>/dev/null || true
  done
  msg ">>> 全平台编译完成，产物位于 ./dist 目录。" ">>> Cross-platform build complete. Artifacts in ./dist."
}

# Persist explicitly requested, non-secret settings into config/config.yaml (local / launchd modes).
apply_local_config() {
  local resolved_bin current_bin
  resolved_bin="$(discover_acp_binary)"
  if [[ "$resolved_bin" == "$ROOT_DIR/"* ]]; then
    resolved_bin="./${resolved_bin#"$ROOT_DIR/"}"
  fi
  current_bin="$(read_config_value binary_path)"

  msg ">>> [3/3] 同步网关配置 (backend_type=${BACKEND_NORM}, binary_path=${resolved_bin}, port=${PORT})..." \
      ">>> [3/3] Updating gateway config (backend_type=${BACKEND_NORM}, binary_path=${resolved_bin}, port=${PORT})..."
  if [[ "$BACKEND_SET" == "1" ]]; then
    set_yaml_value "$CFG_PATH" acp_pool backend_type "$(yaml_quote "$BACKEND_NORM")"
  fi
  if [[ -n "$resolved_bin" && "$resolved_bin" != "$current_bin" ]]; then
    set_yaml_value "$CFG_PATH" acp_pool binary_path "$(yaml_quote "$resolved_bin")"
  fi
  if [[ "$PORT_SET" == "1" ]]; then
    set_yaml_value "$CFG_PATH" server port "$PORT"
    if [[ "$(read_config_value public_base_url)" =~ ^http://localhost:[0-9]+$ ]]; then
      set_yaml_value "$CFG_PATH" server public_base_url "$(yaml_quote "http://localhost:${PORT}")"
    fi
  fi
}

# ------------------------------------------------------------------------------
# Process helpers (local mode)
# ------------------------------------------------------------------------------

process_command() {
  ps -ww -p "$1" -o command= 2>/dev/null || true
}

# Only treat a PID as ours if its command line is the gateway binary from this checkout.
pid_is_gateway() {
  local pid="$1"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  local cmd
  cmd="$(process_command "$pid")"
  [[ "$cmd" == "$GATEWAY_BIN" || "$cmd" == "$GATEWAY_BIN "* ]]
}

pid_alive() {
  kill -0 "$1" 2>/dev/null
}

wait_for_exit() {
  local pid="$1" timeout="$2" i
  for (( i = 0; i < timeout; i++ )); do
    pid_alive "$pid" || return 0
    sleep 1
  done
  ! pid_alive "$pid"
}

port_listeners() {
  if command -v lsof >/dev/null 2>&1; then
    lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | sort -u | tr '\n' ' ' | sed 's/ *$//' || true
  fi
}

report_port_owner() {
  local ours="${1:-}"
  local p
  for p in $(port_listeners); do
    [[ "$p" == "$ours" ]] && continue
    warn ">>> 端口 ${PORT} 被 PID ${p} 占用 (非本网关进程): $(process_command "$p")" \
         ">>> Port ${PORT} is busy by PID ${p} (not ours): $(process_command "$p")"
  done
}

# wait_for_healthz <url> <timeout-seconds> [pid]: 0 = healthy, 1 = timeout, 2 = process exited
wait_for_healthz() {
  local url="$1" timeout="$2" pid="${3:-}" i
  if ! command -v curl >/dev/null 2>&1; then
    sleep 2
    [[ -z "$pid" ]] || pid_alive "$pid" || return 2
    return 0
  fi
  for (( i = 0; i < timeout; i++ )); do
    if [[ -n "$pid" ]] && ! pid_alive "$pid"; then
      return 2
    fi
    if curl -fsS -m 2 -o /dev/null "$url" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# ------------------------------------------------------------------------------
# Credentials banner
# ------------------------------------------------------------------------------

print_admin_credentials_banner() {
  local cred_content=""
  local hint_cmd=""
  if [[ "$MODE" == "docker" ]]; then
    hint_cmd="docker exec antigravity-gateway cat /var/lib/antigravity-gateway/admin_credentials.txt"
    cred_content="$(docker exec antigravity-gateway cat /var/lib/antigravity-gateway/admin_credentials.txt 2>/dev/null || true)"
  elif [[ "$MODE" == "service" && "$OS_NAME" == "Linux" ]]; then
    local cred_file="$SERVICE_DATA_DIR/admin_credentials.txt"
    hint_cmd="sudo cat ${cred_file}"
    cred_content="$(sudo -n cat "$cred_file" 2>/dev/null || true)"
  else
    local cred_file
    cred_file="$(local_data_dir)/admin_credentials.txt"
    hint_cmd="cat ${cred_file}"
    if [[ -f "$cred_file" ]]; then
      cred_content="$(cat "$cred_file" 2>/dev/null || true)"
    fi
  fi

  echo "=============================================================================="
  msg ">>> [管理后台凭证 / Admin Credentials]" ">>> [Admin Console Credentials]"
  if [[ -n "$cred_content" ]]; then
    echo "$cred_content"
  fi
  msg ">>> 随时查看凭证命令: ${hint_cmd}" ">>> View credentials anytime via: ${hint_cmd}"
  msg ">>> 说明: 凭证不会写入日志。未主动修改前，每次启动会动态生成随机密码与 Token；登录 Web 控制台点击右上角「修改密码」修改后即永久固定。" \
      ">>> Note: credentials are never logged. Password & token rotate on every start until changed via Web Console -> Change Password."
  msg ">>> 北向 API (/v1/*) 仅允许本机回环地址免密访问；远程客户端请先在控制台创建 API Key。" \
      ">>> Northbound API (/v1/*) is only open without a key from loopback; create an API key in the console for remote clients."
  echo "=============================================================================="
}

# ------------------------------------------------------------------------------
# Local mode lifecycle
# ------------------------------------------------------------------------------

read_pid_file() {
  local pid_file
  pid_file="$(local_data_dir)/gateway.pid"
  if [[ -f "$pid_file" ]]; then
    tr -d '[:space:]' <"$pid_file" || true
  fi
}

stop_local() {
  local data_dir pid_file pid
  data_dir="$(local_data_dir)"
  pid_file="$data_dir/gateway.pid"
  pid="$(read_pid_file)"

  if [[ -z "$pid" ]] || ! pid_alive "$pid"; then
    rm -f "$pid_file"
    msg ">>> 未发现运行中的网关进程 (PID 文件: $pid_file)" ">>> No running gateway process found (pid file: $pid_file)"
    report_port_owner
    return 0
  fi
  if ! pid_is_gateway "$pid"; then
    warn ">>> [警告] PID 文件中的进程 $pid 不是 $GATEWAY_BIN ($(process_command "$pid"))，不会终止它；已删除过期的 PID 文件" \
         ">>> [WARN] PID $pid from the pid file is not $GATEWAY_BIN ($(process_command "$pid")); not killing it, removed stale pid file"
    rm -f "$pid_file"
    return 0
  fi

  kill "$pid" 2>/dev/null || true
  if ! wait_for_exit "$pid" 15; then
    warn ">>> [警告] 网关进程 $pid 15 秒内未退出，发送 SIGKILL" ">>> [WARN] Gateway PID $pid did not exit within 15s; sending SIGKILL"
    kill -9 "$pid" 2>/dev/null || true
    wait_for_exit "$pid" 5 || true
  fi
  rm -f "$pid_file"
  msg ">>> 已停止网关进程 (PID: $pid)" ">>> Stopped gateway process (PID: $pid)"
}

start_local() {
  [[ -x "$GATEWAY_BIN" ]] || die "未找到 ${GATEWAY_BIN}，请先执行 --action build" "$GATEWAY_BIN not found; run --action build first"
  umask 077
  local data_dir pid_file log_file
  data_dir="$(local_data_dir)"
  pid_file="$data_dir/gateway.pid"
  log_file="$data_dir/gateway.log"
  mkdir -p "$data_dir"
  chmod 700 "$data_dir"
  touch "$log_file"
  chmod 600 "$log_file"

  local old_pid
  old_pid="$(read_pid_file)"
  if [[ -n "$old_pid" ]] && pid_is_gateway "$old_pid"; then
    msg ">>> 网关已在运行 (PID: $old_pid)，正在平滑重启..." ">>> Gateway already running (PID: $old_pid), restarting..."
    stop_local
  fi

  local busy
  busy="$(port_listeners)"
  if [[ -n "$busy" ]]; then
    report_port_owner
    die "端口 ${PORT} 已被占用，无法启动" "Port ${PORT} is already in use; cannot start"
  fi

  local gw_args=(-config "$CFG_PATH")
  [[ "$BACKEND_SET" == "1" ]] && gw_args+=(-backend "$BACKEND_NORM")
  [[ -n "$ACP_BIN" ]] && gw_args+=(-acp-bin "$ACP_BIN")

  if [[ -n "$ADMIN_PASSWORD" ]]; then
    AG_ADMIN_PASSWORD="$ADMIN_PASSWORD" nohup "$GATEWAY_BIN" "${gw_args[@]}" >>"$log_file" 2>&1 &
  else
    nohup "$GATEWAY_BIN" "${gw_args[@]}" >>"$log_file" 2>&1 &
  fi
  local new_pid=$!
  echo "$new_pid" >"$pid_file"

  local rc=0
  wait_for_healthz "http://$(health_host):${PORT}/healthz" 20 "$new_pid" || rc=$?
  if [[ "$rc" == "0" ]]; then
    msg ">>> Antigravity ACP Gateway 已启动! PID: $new_pid | 地址: http://localhost:${PORT}" \
        ">>> Antigravity ACP Gateway started! PID: $new_pid | URL: http://localhost:${PORT}"
    print_admin_credentials_banner
    return 0
  fi
  if [[ "$rc" == "1" ]]; then
    msg ">>> 网关进程 (PID: $new_pid) 20 秒内未通过 /healthz 检查，请检查日志: $log_file" \
        ">>> Gateway (PID: $new_pid) did not pass /healthz within 20s. Check log: $log_file"
  else
    rm -f "$pid_file"
    msg ">>> 启动失败，进程已退出，请检查日志: $log_file" ">>> Startup failed; the process exited. Check log: $log_file"
  fi
  tail -n 20 "$log_file" || true
  exit 1
}

status_local() {
  local pid
  pid="$(read_pid_file)"
  if [[ -n "$pid" ]] && pid_is_gateway "$pid"; then
    msg ">>> 运行中 (PID: $pid) | http://localhost:${PORT}" ">>> Running (PID: $pid) | http://localhost:${PORT}"
    report_port_owner "$pid"
    return 0
  fi
  msg ">>> 未运行" ">>> Stopped"
  report_port_owner
  return 3
}

# ------------------------------------------------------------------------------
# Service mode (systemd on Linux, launchd on macOS)
# ------------------------------------------------------------------------------

systemd_env_quote() {
  printf '"%s"' "$(printf '%s' "$1" | sed -e 's/[\\"$`]/\\&/g')"
}

xml_escape() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g"
}

write_service_env_file() {
  [[ -n "$ADMIN_PASSWORD" ]] || return 0
  local tmp="$SCRIPT_TMP/service.env"
  (
    umask 077
    printf '# Managed by deploy/deploy.sh. Applies only until the password is changed in the Web Console.\nAG_ADMIN_PASSWORD=%s\n' \
      "$(systemd_env_quote "$ADMIN_PASSWORD")" >"$tmp"
  )
  sudo install -d -o root -g root -m 0755 "$SERVICE_CONFIG_DIR"
  sudo install -o root -g root -m 0600 "$tmp" "$SERVICE_ENV_FILE"
  rm -f "$tmp"
  msg ">>> 管理员密码已写入 $SERVICE_ENV_FILE (root:root 0600)" ">>> Admin password written to $SERVICE_ENV_FILE (root:root 0600)"
}

ensure_service_user() {
  if id -u "$SERVICE_USER" >/dev/null 2>&1; then
    return 0
  fi
  local nologin="/usr/sbin/nologin"
  [[ -x "$nologin" ]] || nologin="/sbin/nologin"
  msg ">>> 创建系统用户 $SERVICE_USER ..." ">>> Creating system user $SERVICE_USER ..."
  sudo useradd --system --user-group --home-dir "$SERVICE_STATE_DIR" --no-create-home --shell "$nologin" "$SERVICE_USER"
}

# Install the ACP backend as a root-owned copy in /usr/local/bin; never run a binary from the checkout.
SERVICE_ACP_PATH=""
install_service_acp_binary() {
  local src
  src="$(discover_acp_binary)"
  if [[ ! -f "$src" ]]; then
    if [[ "$src" != */* ]] && command -v "$src" >/dev/null 2>&1; then
      src="$(command -v "$src")"
    else
      die "未找到 ACP 后端二进制 ($src)，请使用 --acp-bin 指定或去掉 --no-download" \
          "ACP backend binary not found ($src); pass --acp-bin or drop --no-download"
    fi
  fi

  local base dest
  base="$(basename "$src")"
  case "$base" in
    agy_acp_server*) dest="$SERVICE_BIN_DIR/$base" ;;
    *) dest="$SERVICE_BIN_DIR/agy" ;;
  esac
  if [[ ! "$src" -ef "$dest" ]]; then
    sudo install -o root -g root -m 0755 "$src" "$dest"
  fi
  if [[ "$base" == agy_acp_server* ]]; then
    local helper
    helper="$(dirname "$src")/localharness_external"
    if [[ -f "$helper" && ! "$helper" -ef "$SERVICE_BIN_DIR/localharness_external" ]]; then
      sudo install -o root -g root -m 0755 "$helper" "$SERVICE_BIN_DIR/localharness_external"
    fi
  fi
  SERVICE_ACP_PATH="$dest"
}

wait_for_service_health() {
  local rc=0
  wait_for_healthz "http://$(health_host "$SERVICE_CONFIG_DIR/config.yaml"):${PORT}/healthz" 20 || rc=$?
  if [[ "$rc" != "0" ]]; then
    msg ">>> 服务 20 秒内未通过 /healthz 检查，请执行: sudo systemctl status $SERVICE_NAME; sudo journalctl -u $SERVICE_NAME -n 50" \
        ">>> Service did not pass /healthz within 20s. Run: sudo systemctl status $SERVICE_NAME; sudo journalctl -u $SERVICE_NAME -n 50"
    exit 1
  fi
}

install_service_linux() {
  command -v systemctl >/dev/null 2>&1 || die "未检测到 systemd (systemctl)" "systemd (systemctl) not found"

  sudo install -o root -g root -m 0755 "$GATEWAY_BIN" "$SERVICE_BIN_DIR/antigravity-gateway"
  sudo install -o root -g root -m 0755 "$PROBE_BIN" "$SERVICE_BIN_DIR/antigravity-probe"
  install_service_acp_binary

  ensure_service_user
  sudo install -d -o root -g root -m 0755 "$SERVICE_CONFIG_DIR"
  sudo install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0700 "$SERVICE_STATE_DIR" "$SERVICE_DATA_DIR" "$SERVICE_WORKSPACE_DIR"
  sudo chown -R "$SERVICE_USER:$SERVICE_USER" "$SERVICE_STATE_DIR"

  local cfg_tmp="$SCRIPT_TMP/service-config.yaml"
  if [[ "$ACTION" == "update" && -r "$SERVICE_CONFIG_DIR/config.yaml" ]]; then
    cp "$SERVICE_CONFIG_DIR/config.yaml" "$cfg_tmp"
  else
    cp "$CFG_PATH" "$cfg_tmp"
  fi
  set_yaml_value "$cfg_tmp" acp_pool backend_type "$(yaml_quote "$BACKEND_NORM")"
  set_yaml_value "$cfg_tmp" acp_pool binary_path "$(yaml_quote "$SERVICE_ACP_PATH")"
  set_yaml_value "$cfg_tmp" acp_pool workspace_dir "$(yaml_quote "$SERVICE_WORKSPACE_DIR")"
  set_yaml_value "$cfg_tmp" server data_dir "$(yaml_quote "$SERVICE_DATA_DIR")"
  set_yaml_value "$cfg_tmp" server port "$PORT"
  set_yaml_value "$cfg_tmp" server public_base_url "$(yaml_quote "http://localhost:${PORT}")"
  if [[ -f "$SERVICE_CONFIG_DIR/config.yaml" ]] && ! sudo cmp -s "$cfg_tmp" "$SERVICE_CONFIG_DIR/config.yaml"; then
    local backup
    backup="$SERVICE_CONFIG_DIR/config.yaml.bak.$(date +%Y%m%d%H%M%S)"
    sudo cp -p "$SERVICE_CONFIG_DIR/config.yaml" "$backup"
    msg ">>> 已备份旧配置: $backup" ">>> Backed up previous config: $backup"
  fi
  sudo install -o root -g root -m 0644 "$cfg_tmp" "$SERVICE_CONFIG_DIR/config.yaml"
  write_service_env_file

  sudo install -o root -g root -m 0644 "$ROOT_DIR/deploy/antigravity-gateway.service" "/etc/systemd/system/${SERVICE_NAME}.service"
  sudo systemctl daemon-reload
  sudo systemctl enable "${SERVICE_NAME}.service"
  sudo systemctl restart "${SERVICE_NAME}.service"
  wait_for_service_health
  msg ">>> Linux systemd 服务 (${SERVICE_NAME}.service，用户 ${SERVICE_USER}) 已安装并启动" \
      ">>> Linux systemd service (${SERVICE_NAME}.service, user ${SERVICE_USER}) installed and started"
  print_admin_credentials_banner
}

write_launchd_plist() {
  local data_dir log_file
  data_dir="$(local_data_dir)"
  log_file="$data_dir/gateway.log"
  (
    umask 077
    mkdir -p "$(dirname "$LAUNCHD_PLIST")" "$data_dir"
    chmod 700 "$data_dir"
    touch "$log_file"
    chmod 600 "$log_file"
  )
  local env_block=""
  if [[ -n "$ADMIN_PASSWORD" ]]; then
    env_block="  <key>EnvironmentVariables</key>
  <dict>
    <key>AG_ADMIN_PASSWORD</key>
    <string>$(xml_escape "$ADMIN_PASSWORD")</string>
  </dict>"
  fi
  (
    umask 077
    cat >"$LAUNCHD_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LAUNCHD_LABEL}</string>
  <key>WorkingDirectory</key>
  <string>$(xml_escape "$ROOT_DIR")</string>
  <key>ProgramArguments</key>
  <array>
    <string>$(xml_escape "$GATEWAY_BIN")</string>
    <string>-config</string>
    <string>$(xml_escape "$CFG_PATH")</string>
  </array>
${env_block}
  <key>Umask</key>
  <integer>63</integer>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>$(xml_escape "$log_file")</string>
  <key>StandardErrorPath</key>
  <string>$(xml_escape "$log_file")</string>
</dict>
</plist>
EOF
  )
  chmod 600 "$LAUNCHD_PLIST"
}

install_service_macos() {
  apply_local_config
  write_launchd_plist
  launchctl unload "$LAUNCHD_PLIST" 2>/dev/null || true
  launchctl load "$LAUNCHD_PLIST"
  local rc=0
  wait_for_healthz "http://$(health_host):${PORT}/healthz" 20 || rc=$?
  if [[ "$rc" != "0" ]]; then
    msg ">>> launchd 服务 20 秒内未通过 /healthz 检查，请检查日志: $(local_data_dir)/gateway.log" \
        ">>> launchd service did not pass /healthz within 20s. Check log: $(local_data_dir)/gateway.log"
    exit 1
  fi
  msg ">>> macOS launchd 服务已安装并启动: $LAUNCHD_PLIST" ">>> macOS launchd service installed & loaded: $LAUNCHD_PLIST"
  print_admin_credentials_banner
}

install_service() {
  case "$OS_NAME" in
    Linux) install_service_linux ;;
    Darwin) install_service_macos ;;
    *) die "当前系统 ($OS_NAME) 不支持 service 模式" "Service mode is not supported on $OS_NAME" ;;
  esac
}

service_ctl() {
  local verb="$1"
  if [[ "$OS_NAME" == "Linux" ]]; then
    case "$verb" in
      start|restart)
        write_service_env_file
        sudo systemctl "$verb" "${SERVICE_NAME}.service"
        wait_for_service_health
        msg ">>> 已执行 ${verb}: ${SERVICE_NAME}.service" ">>> ${SERVICE_NAME}.service: ${verb} done"
        print_admin_credentials_banner
        ;;
      stop) sudo systemctl stop "${SERVICE_NAME}.service" ;;
      status) systemctl status --no-pager "${SERVICE_NAME}.service" ;;
    esac
  elif [[ "$OS_NAME" == "Darwin" ]]; then
    [[ -f "$LAUNCHD_PLIST" ]] || die "未安装 launchd 服务，请先执行 --mode service --action install" \
                                    "launchd service not installed; run --mode service --action install first"
    case "$verb" in
      start) launchctl load "$LAUNCHD_PLIST" ;;
      stop) launchctl unload "$LAUNCHD_PLIST" ;;
      restart)
        if [[ -n "$ADMIN_PASSWORD" ]]; then
          write_launchd_plist
        fi
        launchctl unload "$LAUNCHD_PLIST" 2>/dev/null || true
        launchctl load "$LAUNCHD_PLIST"
        ;;
      status) launchctl list | grep -F "$LAUNCHD_LABEL" || { msg ">>> 未运行" ">>> Stopped"; return 3; } ;;
    esac
  else
    die "当前系统 ($OS_NAME) 不支持 service 模式" "Service mode is not supported on $OS_NAME"
  fi
}

# ------------------------------------------------------------------------------
# Docker mode
# ------------------------------------------------------------------------------

write_docker_env_file() {
  [[ -n "$ADMIN_PASSWORD" ]] || return 0
  case "$ADMIN_PASSWORD" in
    *"'"*) die "Docker 模式下管理员密码不能包含单引号 (')" "In docker mode the admin password must not contain a single quote (')" ;;
  esac
  (
    umask 077
    printf "# Managed by deploy/deploy.sh (gitignored). Applies only until the password is changed in the Web Console.\nAG_ADMIN_PASSWORD='%s'\n" \
      "$ADMIN_PASSWORD" >"$DOCKER_ENV_FILE"
  )
  chmod 600 "$DOCKER_ENV_FILE"
  msg ">>> 管理员密码已写入 $DOCKER_ENV_FILE (0600，已被 git 忽略)" ">>> Admin password written to $DOCKER_ENV_FILE (0600, gitignored)"
}

docker_env_setup() {
  export AG_ACP_BACKEND="$BACKEND_NORM"
  export AG_PORT="$PORT"
  [[ -n "$ZED_ACP_SHA256" ]] && export ZED_ACP_SHA256
  if [[ "$ZED_ACP_VERSION_SET" == "1" ]]; then
    export ZED_ACP_VERSION
  fi
  [[ "$INSECURE_SKIP_VERIFY" == "1" ]] && export AG_INSECURE_SKIP_VERIFY=1
  if [[ -z "${AG_BIND:-}" ]]; then
    if [[ -f "$ROOT_DIR/deploy/.env" ]]; then
      AG_BIND="$(awk -F= '$1=="AG_BIND"{print $2; exit}' "$ROOT_DIR/deploy/.env" | tr -d "\"'[:space:]" || true)"
    fi
    if [[ -z "${AG_BIND:-}" ]] && command -v docker >/dev/null 2>&1 && docker inspect antigravity-gateway >/dev/null 2>&1; then
      AG_BIND="$(docker inspect antigravity-gateway --format '{{with index .HostConfig.PortBindings "8080/tcp"}}{{with index . 0}}{{.HostIp}}{{end}}{{end}}' 2>/dev/null || true)"
    fi
    [[ -n "${AG_BIND:-}" ]] && export AG_BIND
  fi
  if [[ -z "${AG_BIND:-}" ]]; then
    msg ">>> 端口仅绑定 127.0.0.1:${PORT}；如需对外开放请显式设置 AG_BIND=0.0.0.0" \
        ">>> Port is published on 127.0.0.1:${PORT} only; set AG_BIND=0.0.0.0 explicitly to expose it"
  fi
  write_docker_env_file
}

docker_health_url() {
  local bind="${AG_BIND:-127.0.0.1}"
  case "$bind" in
    0.0.0.0|"::"|"") bind="127.0.0.1" ;;
  esac
  echo "http://${bind}:${PORT}/healthz"
}

docker_wait_and_banner() {
  local rc=0
  wait_for_healthz "$(docker_health_url)" 30 || rc=$?
  if [[ "$rc" != "0" ]]; then
    msg ">>> 容器 30 秒内未通过 /healthz 检查，请执行: docker compose -f deploy/docker-compose.yml logs" \
        ">>> Container did not pass /healthz within 30s. Run: docker compose -f deploy/docker-compose.yml logs"
    exit 1
  fi
  msg ">>> Docker 容器已启动! 访问地址: http://localhost:${PORT}" ">>> Docker container is up! Access URL: http://localhost:${PORT}"
  print_admin_credentials_banner
}

deploy_docker() {
  mkdir -p "$ROOT_DIR/bin"
  if [[ -n "$ACP_BIN" ]]; then
    local base_name
    base_name="$(basename "$ACP_BIN")"
    cp -f "$ACP_BIN" "$ROOT_DIR/bin/$base_name"
    chmod 0755 "$ROOT_DIR/bin/$base_name"
    export AG_ACP_BINARY_PATH="/opt/antigravity-acp/$base_name"
  elif [[ "$ACTION" == "update" && "$OS_NAME" == "Linux" ]]; then
    if [[ -f "$ROOT_DIR/bin/agy" || -f "$ROOT_DIR/bin/agy_acp_server.par" || -f "$ROOT_DIR/bin/agy_acp_server" ]]; then
      update_acp_backend
    fi
  fi
  if [[ "$ACTION" == "update" ]]; then
    export AG_ACP_CACHE_BUST
    local docker_cli_arch="amd64" remote_acp_key=""
    case "$ARCH_NAME" in
      arm64|aarch64) docker_cli_arch="arm64" ;;
    esac
    if [[ "$BACKEND_NORM" != "zed" ]]; then
      local cli_ver_probe
      cli_ver_probe="$(curl -fsSL --retry 2 -A "Antigravity-Gateway-Deploy/1.6" "${CLI_MANIFEST_BASE}/linux_${docker_cli_arch}.json" 2>/dev/null | awk -F'"' '/"version"[ \t]*:/ {print $4; exit}' || true)"
      [[ -n "$cli_ver_probe" ]] && remote_acp_key="cli-$(normalize_version "$cli_ver_probe")"
    fi
    if [[ "$BACKEND_NORM" == "zed" || "$BACKEND_NORM" == "auto" ]]; then
      local zed_ver_probe
      zed_ver_probe="$(curl -fsSL --retry 2 -A "Antigravity-Gateway-Deploy/1.6" "$ZED_ACP_REGISTRY_URL" 2>/dev/null | awk '/"id"[ \t]*:[ \t]*"antigravity-acp"/ { in_a=1; next } in_a && /"id"[ \t]*:/ { in_a=0 } in_a && !v && /"version"[ \t]*:/ { l=$0; sub(/^.*"version"[ \t]*:[ \t]*"/, "", l); sub(/".*$/, "", l); v=l } END { print v }' || true)"
      [[ -n "$zed_ver_probe" ]] && remote_acp_key="${remote_acp_key:+$remote_acp_key+}zed-$(normalize_version "$zed_ver_probe")"
    fi
    AG_ACP_CACHE_BUST="${remote_acp_key:-$(date +%s)}"
    msg ">>> [Docker 更新] 目标 ACP 后端版本标识: ${AG_ACP_CACHE_BUST} (相同版本将直接复用镜像缓存)..." \
        ">>> [Docker Update] Target ACP backend version key: ${AG_ACP_CACHE_BUST} (reuses Docker layer cache if unchanged)..."
  fi
  docker_env_setup

  msg ">>> 使用 Docker Compose 构建并启动容器 (AG_ACP_BACKEND=${AG_ACP_BACKEND}, PORT=${AG_PORT})..." \
      ">>> Building and starting container via Docker Compose (AG_ACP_BACKEND=${AG_ACP_BACKEND}, PORT=${AG_PORT})..."
  docker compose -f "$COMPOSE_FILE" up -d --build --force-recreate
  docker_wait_and_banner
}

docker_ctl() {
  local verb="$1"
  mkdir -p "$ROOT_DIR/bin"
  case "$verb" in
    start)
      docker_env_setup
      docker compose -f "$COMPOSE_FILE" up -d
      docker_wait_and_banner
      ;;
    restart)
      docker_env_setup
      docker compose -f "$COMPOSE_FILE" up -d --force-recreate
      docker_wait_and_banner
      ;;
    stop) docker compose -f "$COMPOSE_FILE" down ;;
    status) docker compose -f "$COMPOSE_FILE" ps ;;
  esac
}

# ------------------------------------------------------------------------------
# Main
# ------------------------------------------------------------------------------

lifecycle() {
  local verb="$1"
  case "$MODE" in
    service) service_ctl "$verb" ;;
    docker) docker_ctl "$verb" ;;
    *)
      case "$verb" in
        start|restart)
          ensure_acp_backend
          apply_local_config
          [[ "$verb" == "restart" ]] && stop_local
          start_local
          ;;
        stop) stop_local ;;
        status) status_local ;;
      esac
      ;;
  esac
}

case "$ACTION" in
  install-acp|update-acp)
    update_acp_backend
    if [[ "$MODE" == "service" && "$OS_NAME" == "Linux" ]]; then
      install_service_acp_binary
      msg ">>> 已同步更新系统服务 ACP 二进制: $SERVICE_ACP_PATH" \
          ">>> Updated systemd service ACP binary: $SERVICE_ACP_PATH"
    fi
    ;;
  build-all)
    build_all_platforms
    ;;
  build)
    build_local_binaries
    ensure_acp_backend
    apply_local_config
    ;;
  update)
    if [[ "$MODE" == "docker" ]]; then
      deploy_docker
    elif [[ "$MODE" == "service" ]]; then
      build_local_binaries
      update_acp_backend
      install_service
    else
      build_local_binaries
      update_acp_backend
      apply_local_config
      start_local
    fi
    ;;
  install)
    if [[ "$MODE" == "docker" ]]; then
      deploy_docker
    elif [[ "$MODE" == "service" ]]; then
      build_local_binaries
      ensure_acp_backend
      install_service
    else
      build_local_binaries
      ensure_acp_backend
      apply_local_config
      start_local
    fi
    ;;
  start|stop|restart|status)
    lifecycle "$ACTION"
    ;;
  *)
    usage
    exit 1
    ;;
esac

#!/usr/bin/env bash
# ==============================================================================
# Antigravity ACP Gateway — Unified Cross-Platform Deployment Script
# Supports: macOS (darwin), Linux (x86_64 / arm64, systemd / direct), Docker Compose, Cross-Build
# Supports Automatic ACP Backend Download & Installation:
#   - cli (default) : Antigravity CLI (agy acp / antigravity acp)
#   - zed / zen     : Zed/Zen standalone ACP backend package (agy_acp_server.par)
#   - auto          : Auto-discover CLI (agy) or Zed/Zen ACP package, auto-installing CLI if missing
# ==============================================================================
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ACTION="install"
MODE="local"          # local | service | docker | build-all
BACKEND="cli"         # cli (default) | zed | zen | auto
ACP_BIN=""
PORT="8080"
ADMIN_PASSWORD=""
LANG_CHOICE="zh"      # zh | en
AUTO_DOWNLOAD="1"

msg() {
  local zh="$1"
  local en="$2"
  if [[ "$LANG_CHOICE" == "en" ]]; then
    echo -e "$en"
  else
    echo -e "$zh"
  fi
}

usage() {
  if [[ "$LANG_CHOICE" == "en" ]]; then
    cat <<EOF
Usage: ./deploy/deploy.sh [options]

Options:
  --action <install|install-acp|start|stop|restart|status|build|build-all>  Action to execute (default: install)
  --mode <local|service|docker>                                             Deployment target (default: local)
  --backend <cli|zed|zen|auto>                                              ACP backend engine (default: cli)
  --acp-bin <path>                                                          Explicit path to agy CLI or agy_acp_server.par
  --no-download                                                             Disable automatic ACP backend download
  --port <port>                                                             Gateway listen port (default: 8080)
  --admin-password <pwd>                                                    Admin Console login password
  --lang <zh|en>                                                            Output language: zh or en (default: zh)
  -h, --help                                                                Show this help message

Examples:
  ./deploy/deploy.sh                                              # One-click build, auto-install CLI (agy) & start locally
  ./deploy/deploy.sh --mode local --backend cli --port 8080
  ./deploy/deploy.sh --mode local --backend zed                   # Auto-download Zed ACP package if missing
  ./deploy/deploy.sh --mode service --admin-password "your-pwd"   # Install systemd / launchd service (default: cli)
  ./deploy/deploy.sh --mode docker --port 8080                    # Build & run Docker container (auto-installs Linux ACP)
  ./deploy/deploy.sh --action install-acp --backend cli           # Download/update ACP backend binary only
  ./deploy/deploy.sh --action build-all                           # Cross-compile for macOS/Linux/Windows (amd64 & arm64)
EOF
  else
    cat <<EOF
用法: ./deploy/deploy.sh [选项]

选项:
  --action <install|install-acp|start|stop|restart|status|build|build-all>  执行动作 (默认: install)
  --mode <local|service|docker>                                             部署模式: local(本地后台) | service(系统服务) | docker(容器)
  --backend <cli|zed|zen|auto>                                              ACP 后端类型 (默认: cli，缺失时自动下载安装)
  --acp-bin <path>                                                          显式指定 agy 或 agy_acp_server.par 可执行文件路径
  --no-download                                                             禁用缺失 ACP 后端时的自动下载安装
  --port <port>                                                             网关监听端口 (默认: 8080)
  --admin-password <pwd>                                                    设置 Web 管理控制台登录密码
  --lang <zh|en>                                                            脚本输出语言: zh 或 en (默认: zh)
  -h, --help                                                                显示帮助信息

示例:
  ./deploy/deploy.sh                                              # 一键编译、自动下载安装 agy CLI 并在本地后台启动
  ./deploy/deploy.sh --mode local --backend cli --port 8080
  ./deploy/deploy.sh --mode local --backend zed                   # 使用 Zed ACP 独立包（缺失时自动从官方源下载）
  ./deploy/deploy.sh --mode service --admin-password "your-pwd"   # 注册为 systemd / launchd 系统服务（默认 cli）
  ./deploy/deploy.sh --mode docker --port 8080                    # 构建并启动 Docker 容器（镜像内自动安装对应架构 ACP）
  ./deploy/deploy.sh --action install-acp --backend cli           # 仅自动下载/更新 ACP 后端二进制到 ./bin
  ./deploy/deploy.sh --action build-all                           # 交叉编译全平台发行包 (macOS/Linux/Windows, amd64 & arm64)
EOF
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --action) ACTION="$2"; shift 2 ;;
    --mode) MODE="$2"; shift 2 ;;
    --backend) BACKEND="$2"; shift 2 ;;
    --acp-bin) ACP_BIN="$2"; shift 2 ;;
    --no-download) AUTO_DOWNLOAD="0"; shift 1 ;;
    --port) PORT="$2"; shift 2 ;;
    --admin-password) ADMIN_PASSWORD="$2"; shift 2 ;;
    --lang) LANG_CHOICE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1"; usage; exit 1 ;;
  esac
done

# Normalize backend alias ("zen" -> "zed")
BACKEND_NORM="$(echo "$BACKEND" | tr '[:upper:]' '[:lower:]')"
if [[ "$BACKEND_NORM" == "zen" ]]; then
  BACKEND_NORM="zed"
fi

OS_NAME="$(uname -s)"
ARCH_NAME="$(uname -m)"

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

verify_sha512() {
  local file_path="$1"
  local expected="$2"
  [[ -z "$expected" ]] && return 0
  local actual=""
  if command -v sha512sum >/dev/null 2>&1; then
    actual="$(sha512sum "$file_path" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 512 "$file_path" | awk '{print $1}')"
  else
    return 0
  fi
  if [[ "$(echo "$actual" | tr '[:upper:]' '[:lower:]')" != "$(echo "$expected" | tr '[:upper:]' '[:lower:]')" ]]; then
    echo "SHA-512 verification failed: expected $expected, got $actual" >&2
    return 1
  fi
  return 0
}

verify_sha256() {
  local file_path="$1"
  local expected="$2"
  [[ -z "$expected" ]] && return 0
  local actual=""
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file_path" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file_path" | awk '{print $1}')"
  else
    return 0
  fi
  if [[ "$(echo "$actual" | tr '[:upper:]' '[:lower:]')" != "$(echo "$expected" | tr '[:upper:]' '[:lower:]')" ]]; then
    echo "SHA-256 verification failed: expected $expected, got $actual" >&2
    return 1
  fi
  return 0
}

install_cli_backend() {
  local dest_dir="${1:-$ROOT_DIR/bin}"
  mkdir -p "$dest_dir"
  local target_bin="$dest_dir/agy"

  local os_key=""
  case "$OS_NAME" in
    Darwin) os_key="darwin" ;;
    Linux)  os_key="linux" ;;
    *)
      msg ">>> [警告] 当前系统 ($OS_NAME) 不支持自动下载 CLI，请使用 --acp-bin 手动指定" \
          ">>> [WARN] OS ($OS_NAME) not supported for auto CLI download; specify --acp-bin manually"
      return 1
      ;;
  esac

  local arch_key=""
  case "$ARCH_NAME" in
    x86_64|amd64)  arch_key="amd64" ;;
    arm64|aarch64) arch_key="arm64" ;;
    *)
      msg ">>> [警告] 当前架构 ($ARCH_NAME) 不支持自动下载 CLI，请使用 --acp-bin 手动指定" \
          ">>> [WARN] Architecture ($ARCH_NAME) not supported for auto CLI download"
      return 1
      ;;
  esac

  local platform="${os_key}_${arch_key}"
  if [[ "$os_key" == "linux" ]]; then
    if [[ -f /lib/libc.musl-x86_64.so.1 ]] || [[ -f /lib/libc.musl-aarch64.so.1 ]] || ldd /bin/ls 2>&1 | grep -q musl; then
      platform="linux_${arch_key}_musl"
    fi
  fi

  msg ">>> 正在自动下载并安装 Antigravity CLI 后端 (platform=${platform}) -> ${target_bin} ..." \
      ">>> Auto-downloading & installing Antigravity CLI backend (platform=${platform}) -> ${target_bin} ..."

  local manifest_url="https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/${platform}.json"
  local manifest_json=""
  manifest_json="$(curl -fsSL --retry 3 -A "Antigravity-Gateway-Deploy/1.6" "$manifest_url" 2>/dev/null || true)"
  if [[ -n "$manifest_json" ]]; then
    local version pkg_url expected_sha512
    version="$(printf '%s\n' "$manifest_json" | awk -F'"' '/"version"[[:space:]]*:/ {print $4; exit}')"
    pkg_url="$(printf '%s\n' "$manifest_json" | awk -F'"' '/"url"[[:space:]]*:/ {print $4; exit}')"
    expected_sha512="$(printf '%s\n' "$manifest_json" | awk -F'"' '/"sha512"[[:space:]]*:/ {print $4; exit}')"
    if [[ -n "$pkg_url" ]]; then
      echo "    -> Matched Antigravity CLI v${version:-latest}: ${pkg_url}"
      local tmpdir
      tmpdir="$(mktemp -d)"
      local pkg_path="$tmpdir/agy_pkg"
      if curl -fL --retry 3 -o "$pkg_path" "$pkg_url" && verify_sha512 "$pkg_path" "$expected_sha512"; then
        if [[ "$pkg_url" == *.tar.gz* ]]; then
          tar -xzf "$pkg_path" -C "$tmpdir" antigravity
          mv -f "$tmpdir/antigravity" "$target_bin"
        else
          mv -f "$pkg_path" "$target_bin"
        fi
        chmod 0755 "$target_bin"
        if [[ "$os_key" == "darwin" ]]; then
          xattr -d com.apple.quarantine "$target_bin" 2>/dev/null || true
        fi
        rm -rf "$tmpdir"
        msg ">>> Antigravity CLI 安装完成: ${target_bin}" ">>> Antigravity CLI installed at: ${target_bin}"
        return 0
      fi
      rm -rf "$tmpdir"
    fi
  fi

  msg ">>> 清单直连下载未成功，正在回退使用官方安装脚本 https://antigravity.google/cli/install.sh ..." \
      ">>> Falling back to official installer script https://antigravity.google/cli/install.sh ..."
  curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir "$dest_dir"
}

install_zed_backend() {
  local dest_dir="${1:-$ROOT_DIR/bin}"
  mkdir -p "$dest_dir"

  local os_key=""
  local fallback_os=""
  case "$OS_NAME" in
    Darwin) os_key="darwin"; fallback_os="macos" ;;
    Linux)  os_key="linux";  fallback_os="linux" ;;
    *)
      msg ">>> [警告] 当前系统 ($OS_NAME) 不支持自动下载 Zed ACP 包" \
          ">>> [WARN] OS ($OS_NAME) not supported for auto Zed ACP download"
      return 1
      ;;
  esac

  local zed_arch=""
  local fallback_arch=""
  case "$ARCH_NAME" in
    x86_64|amd64)  zed_arch="x86_64";  fallback_arch="x86_64" ;;
    arm64|aarch64) zed_arch="aarch64"; fallback_arch="arm64" ;;
    *)
      msg ">>> [警告] 当前架构 ($ARCH_NAME) 不支持自动下载 Zed ACP 包" \
          ">>> [WARN] Architecture ($ARCH_NAME) not supported for auto Zed ACP download"
      return 1
      ;;
  esac

  local target_key="${os_key}-${zed_arch}"
  msg ">>> 正在自动下载并安装 Zed ACP 独立后端包 (target=${target_key}) -> ${dest_dir}/agy_acp_server.par ..." \
      ">>> Auto-downloading & installing Zed ACP standalone package (target=${target_key}) -> ${dest_dir}/agy_acp_server.par ..."

  local archive_url="https://dl.google.com/agy-extensions/releases/${fallback_os}/agy-acp-server-1.4.5-${os_key}-${fallback_arch}.zip"
  echo "    -> Downloading Zed ACP package: ${archive_url}"
  local tmpdir
  tmpdir="$(mktemp -d)"
  local zip_path="$tmpdir/agy_acp.zip"
  curl -fL --retry 3 -o "$zip_path" "$archive_url"
  unzip -q -o "$zip_path" -d "$dest_dir"
  for fn in agy_acp_server.par agy_acp_server localharness_external; do
    if [[ -f "$dest_dir/$fn" ]]; then
      chmod 0755 "$dest_dir/$fn"
      if [[ "$os_key" == "darwin" ]]; then
        xattr -d com.apple.quarantine "$dest_dir/$fn" 2>/dev/null || true
      fi
    fi
  done
  rm -rf "$tmpdir"
  msg ">>> Zed ACP 独立后端包安装完成: ${dest_dir}/agy_acp_server.par" \
      ">>> Zed ACP standalone package installed at: ${dest_dir}/agy_acp_server.par"
}

ensure_acp_backend() {
  if [[ -n "$ACP_BIN" && -f "$ACP_BIN" ]]; then
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

discover_acp_binary() {
  if [[ -n "$ACP_BIN" && -f "$ACP_BIN" ]]; then
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

build_frontend() {
  msg ">>> [1/3] 构建中英双语前端控制台 (web/dist)..." ">>> [1/3] Building bilingual ReUI frontend (web/dist)..."
  if [[ ! -d "$ROOT_DIR/web/node_modules" ]]; then
    npm --prefix "$ROOT_DIR/web" install
  fi
  npm --prefix "$ROOT_DIR/web" run build
}

build_local_binaries() {
  build_frontend
  mkdir -p "$ROOT_DIR/bin" "$ROOT_DIR/data"
  msg ">>> [2/3] 编译 Go 网关与探测工具 ($OS_NAME/$ARCH_NAME)..." ">>> [2/3] Building Go gateway & probe binaries ($OS_NAME/$ARCH_NAME)..."
  go build -trimpath -ldflags="-s -w" -o "$ROOT_DIR/bin/antigravity-gateway" ./cmd/gateway
  go build -trimpath -ldflags="-s -w" -o "$ROOT_DIR/bin/antigravity-probe" ./cmd/probe
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
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" \
      -o "$out_dir/antigravity-gateway${ext}" ./cmd/gateway
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" \
      -o "$out_dir/antigravity-probe${ext}" ./cmd/probe
    cp "$ROOT_DIR/config/config.yaml" "$out_dir/config/config.yaml"
    cp "$ROOT_DIR/deploy/deploy.sh" "$ROOT_DIR/deploy/deploy.ps1" "$ROOT_DIR/deploy/Dockerfile" "$ROOT_DIR/deploy/docker-compose.yml" "$ROOT_DIR/deploy/antigravity-gateway.service" "$out_dir/deploy/" 2>/dev/null || true
    cp "$ROOT_DIR/README.md" "$ROOT_DIR/README_EN.md" "$out_dir/" 2>/dev/null || true
  done
  msg ">>> 全平台编译完成，产物位于 ./dist 目录。" ">>> Cross-platform build complete. Artifacts in ./dist."
}

update_config_yaml() {
  local resolved_bin="auto"
  if [[ "$MODE" != "docker" ]]; then
    resolved_bin="$(discover_acp_binary)"
  elif [[ -n "$ACP_BIN" ]]; then
    resolved_bin="/opt/antigravity-acp/$(basename "$ACP_BIN")"
  fi
  msg ">>> [3/3] 同步网关配置 (backend_type=${BACKEND_NORM}, binary_path=${resolved_bin}, port=${PORT})..." \
      ">>> [3/3] Updating gateway config (backend_type=${BACKEND_NORM}, binary_path=${resolved_bin}, port=${PORT})..."

  local cfg_path="$ROOT_DIR/config/config.yaml"
  local tmp_cfg
  tmp_cfg="$(mktemp)"
  awk -v backend="$BACKEND_NORM" -v bin_path="$resolved_bin" -v port="$PORT" -v admin_pwd="$ADMIN_PASSWORD" '
    BEGIN { has_backend = 0 }
    /^[[:space:]]*backend_type[[:space:]]*:/ { has_backend = 1 }
    { lines[++n] = $0 }
    END {
      for (i = 1; i <= n; i++) {
        line = lines[i]
        if (!has_backend && line ~ /^acp_pool[[:space:]]*:[[:space:]]*$/) {
          print line
          print "  backend_type: \"" backend "\""
          continue
        }
        if (line ~ /^[[:space:]]*backend_type[[:space:]]*:/) {
          sub(/:.*/, ": \"" backend "\"", line)
        } else if (bin_path != "" && line ~ /^[[:space:]]*binary_path[[:space:]]*:/) {
          sub(/:.*/, ": \"" bin_path "\"", line)
        } else if (port != "" && line ~ /^[[:space:]]*port[[:space:]]*:[[:space:]]*[0-9]+/) {
          sub(/:[[:space:]]*[0-9]+/, ": " port, line)
        } else if (port != "" && line ~ /^[[:space:]]*public_base_url[[:space:]]*:[[:space:]]*"http:\/\/localhost:[0-9]+"/) {
          sub(/http:\/\/localhost:[0-9]+/, "http://localhost:" port, line)
        } else if (admin_pwd != "" && line ~ /^[[:space:]]*admin_password[[:space:]]*:/) {
          sub(/:.*/, ": \"" admin_pwd "\"", line)
        } else if (admin_pwd == "" && line ~ /^[[:space:]]*(admin_password|admin_token)[[:space:]]*:[[:space:]]*"ag-admin-secret-key"/) {
          sub(/:.*/, ": \"auto\"", line)
        }
        print line
      }
    }
  ' "$cfg_path" > "$tmp_cfg"
  mv -f "$tmp_cfg" "$cfg_path"
}

print_admin_credentials_banner() {
  local cred_content=""
  local hint_cmd=""
  if [[ "$MODE" == "docker" ]]; then
    hint_cmd="docker exec antigravity-gateway cat /var/lib/antigravity-gateway/admin_credentials.txt  (或 docker logs antigravity-gateway)"
    for _ in 1 2 3 4 5; do
      cred_content="$(docker exec antigravity-gateway cat /var/lib/antigravity-gateway/admin_credentials.txt 2>/dev/null || true)"
      [[ -n "$cred_content" ]] && break
      sleep 1
    done
  else
    local cred_file="$ROOT_DIR/data/admin_credentials.txt"
    [[ "$MODE" == "service" && "$OS_NAME" == "Linux" ]] && cred_file="/var/lib/antigravity-gateway/admin_credentials.txt"
    hint_cmd="cat ${cred_file}  (或查看日志 ${ROOT_DIR}/data/gateway.log)"
    for _ in 1 2 3 4; do
      if [[ -f "$cred_file" ]]; then
        cred_content="$(cat "$cred_file" 2>/dev/null || true)"
        [[ -n "$cred_content" ]] && break
      fi
      sleep 1
    done
  fi

  echo "=============================================================================="
  msg ">>> [管理后台凭证 / Admin Credentials]" ">>> [Admin Console Credentials]"
  if [[ -n "$cred_content" ]]; then
    echo "$cred_content"
  fi
  msg ">>> 随时查看凭证命令: ${hint_cmd}" ">>> View credentials anytime via: ${hint_cmd}"
  msg ">>> 说明: 未主动修改前，每次部署将动态生成随机密码与 Token；登录 Web 控制台点击右上角「修改密码」修改后即永久固定。" \
      ">>> Note: Password & Token rotate dynamically on each deploy until modified via Web Console -> Change Password."
  echo "=============================================================================="
}

find_running_gateway_pids() {
  local pid_file="$ROOT_DIR/data/gateway.pid"
  local pids=()
  if [[ -f "$pid_file" ]]; then
    local file_pid
    file_pid="$(cat "$pid_file" 2>/dev/null || true)"
    if [[ -n "$file_pid" ]] && kill -0 "$file_pid" 2>/dev/null; then
      pids+=("$file_pid")
    fi
  fi
  if command -v lsof >/dev/null 2>&1; then
    local port_pids
    port_pids="$(lsof -tiTCP:"${PORT}" -sTCP:LISTEN 2>/dev/null || true)"
    for p in $port_pids; do
      if [[ -n "$p" ]] && kill -0 "$p" 2>/dev/null; then
        local seen=0
        for ep in "${pids[@]:-}"; do
          [[ "$ep" == "$p" ]] && seen=1
        done
        [[ "$seen" == "0" ]] && pids+=("$p")
      fi
    done
  fi
  if command -v pgrep >/dev/null 2>&1; then
    local pg_pids
    pg_pids="$(pgrep -f "$ROOT_DIR/bin/antigravity-gateway" 2>/dev/null || true)"
    for p in $pg_pids; do
      if [[ -n "$p" ]] && kill -0 "$p" 2>/dev/null; then
        local seen=0
        for ep in "${pids[@]:-}"; do
          [[ "$ep" == "$p" ]] && seen=1
        done
        [[ "$seen" == "0" ]] && pids+=("$p")
      fi
    done
  fi
  echo "${pids[*]:-}"
}

start_local() {
  mkdir -p "$ROOT_DIR/data"
  local pid_file="$ROOT_DIR/data/gateway.pid"
  local log_file="$ROOT_DIR/data/gateway.log"
  local existing_pids
  existing_pids="$(find_running_gateway_pids)"
  if [[ -n "$existing_pids" ]]; then
    msg ">>> 网关已在运行 (PID: $existing_pids)，正在平滑重启..." ">>> Gateway already running (PID: $existing_pids), restarting..."
    for opid in $existing_pids; do
      kill "$opid" 2>/dev/null || true
    done
    sleep 1
  fi

  nohup "$ROOT_DIR/bin/antigravity-gateway" \
    -config "$ROOT_DIR/config/config.yaml" \
    -backend "$BACKEND_NORM" \
    ${ACP_BIN:+-acp-bin "$ACP_BIN"} \
    >>"$log_file" 2>&1 &
  local new_pid=$!
  echo "$new_pid" >"$pid_file"
  sleep 1
  if kill -0 "$new_pid" 2>/dev/null; then
    msg ">>> Antigravity ACP Gateway 已启动! PID: $new_pid | 地址: http://localhost:${PORT}" \
        ">>> Antigravity ACP Gateway started! PID: $new_pid | URL: http://localhost:${PORT}"
    print_admin_credentials_banner
  else
    msg ">>> 启动失败，请检查日志: $log_file" ">>> Startup failed. Check log: $log_file"
    tail -n 20 "$log_file" || true
    exit 1
  fi
}

stop_local() {
  local pid_file="$ROOT_DIR/data/gateway.pid"
  local existing_pids
  existing_pids="$(find_running_gateway_pids)"
  if [[ -n "$existing_pids" ]]; then
    for pid in $existing_pids; do
      kill "$pid" 2>/dev/null || true
    done
    rm -f "$pid_file"
    msg ">>> 已停止网关进程 (PID: $existing_pids)" ">>> Stopped gateway process (PID: $existing_pids)"
    return 0
  fi
  rm -f "$pid_file"
  msg ">>> 未发现运行中的网关进程" ">>> No running gateway process found"
}

status_local() {
  local existing_pids
  existing_pids="$(find_running_gateway_pids)"
  if [[ -n "$existing_pids" ]]; then
    msg ">>> 运行中 (PID: $existing_pids) | http://localhost:${PORT}" ">>> Running (PID: $existing_pids) | http://localhost:${PORT}"
    return 0
  fi
  msg ">>> 未运行" ">>> Stopped"
}

install_service() {
  if [[ "$OS_NAME" == "Darwin" ]]; then
    local plist_dir="$HOME/Library/LaunchAgents"
    local plist_path="$plist_dir/com.antigravity.gateway.plist"
    mkdir -p "$plist_dir" "$ROOT_DIR/data"
    cat >"$plist_path" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.antigravity.gateway</string>
  <key>WorkingDirectory</key>
  <string>${ROOT_DIR}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${ROOT_DIR}/bin/antigravity-gateway</string>
    <string>-config</string>
    <string>${ROOT_DIR}/config/config.yaml</string>
    <string>-backend</string>
    <string>${BACKEND_NORM}</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${ROOT_DIR}/data/gateway.log</string>
  <key>StandardErrorPath</key>
  <string>${ROOT_DIR}/data/gateway.log</string>
</dict>
</plist>
EOF
    launchctl unload "$plist_path" 2>/dev/null || true
    launchctl load "$plist_path"
    msg ">>> macOS launchd 服务已安装并启动: $plist_path" ">>> macOS launchd service installed & loaded: $plist_path"
    print_admin_credentials_banner
  elif [[ "$OS_NAME" == "Linux" ]]; then
    sudo install -m 0755 "$ROOT_DIR/bin/antigravity-gateway" /usr/local/bin/antigravity-gateway
    sudo install -m 0755 "$ROOT_DIR/bin/antigravity-probe" /usr/local/bin/antigravity-probe
    if [[ -f "$ROOT_DIR/bin/agy" ]]; then
      sudo install -m 0755 "$ROOT_DIR/bin/agy" /usr/local/bin/agy
    fi
    if [[ -f "$ROOT_DIR/bin/agy_acp_server.par" ]]; then
      sudo install -m 0755 "$ROOT_DIR/bin/agy_acp_server.par" /usr/local/bin/agy_acp_server.par
    fi
    if [[ -f "$ROOT_DIR/bin/localharness_external" ]]; then
      sudo install -m 0755 "$ROOT_DIR/bin/localharness_external" /usr/local/bin/localharness_external
    fi
    sudo mkdir -p /etc/antigravity-gateway /var/lib/antigravity-gateway
    sudo cp "$ROOT_DIR/config/config.yaml" /etc/antigravity-gateway/config.yaml
    sudo cp "$ROOT_DIR/deploy/antigravity-gateway.service" /etc/systemd/system/antigravity-gateway.service
    sudo systemctl daemon-reload
    sudo systemctl enable --now antigravity-gateway.service
    msg ">>> Linux systemd 服务 (antigravity-gateway.service) 已安装并启动" \
        ">>> Linux systemd service (antigravity-gateway.service) installed and started"
    print_admin_credentials_banner
  fi
}

deploy_docker() {
  mkdir -p "$ROOT_DIR/bin"
  if [[ -n "$ACP_BIN" && -f "$ACP_BIN" ]]; then
    local base_name
    base_name="$(basename "$ACP_BIN")"
    cp -f "$ACP_BIN" "$ROOT_DIR/bin/$base_name"
    chmod +x "$ROOT_DIR/bin/$base_name"
  fi
  export AG_ACP_BACKEND="$BACKEND_NORM"
  export AG_PORT="$PORT"
  [[ -n "$ADMIN_PASSWORD" ]] && export AG_ADMIN_PASSWORD="$ADMIN_PASSWORD"

  msg ">>> 使用 Docker Compose 构建并启动容器 (AG_ACP_BACKEND=${AG_ACP_BACKEND}, PORT=${AG_PORT})..." \
      ">>> Building and starting container via Docker Compose (AG_ACP_BACKEND=${AG_ACP_BACKEND}, PORT=${AG_PORT})..."
  docker compose -f "$ROOT_DIR/deploy/docker-compose.yml" up -d --build
  msg ">>> Docker 容器已启动! 访问地址: http://localhost:${PORT}" \
      ">>> Docker container is up! Access URL: http://localhost:${PORT}"
  print_admin_credentials_banner
}

case "$ACTION" in
  install-acp)
    if [[ "$BACKEND_NORM" == "zed" ]]; then
      install_zed_backend "$ROOT_DIR/bin"
    else
      install_cli_backend "$ROOT_DIR/bin"
    fi
    ;;
  build-all)
    build_all_platforms
    ;;
  build)
    build_local_binaries
    ensure_acp_backend
    update_config_yaml
    ;;
  install)
    if [[ "$MODE" == "docker" ]]; then
      update_config_yaml
      deploy_docker
    elif [[ "$MODE" == "service" ]]; then
      build_local_binaries
      ensure_acp_backend
      update_config_yaml
      install_service
    else
      build_local_binaries
      ensure_acp_backend
      update_config_yaml
      start_local
    fi
    ;;
  start)
    if [[ "$MODE" == "docker" ]]; then
      docker compose -f "$ROOT_DIR/deploy/docker-compose.yml" up -d
    else
      ensure_acp_backend
      start_local
    fi
    ;;
  stop)
    if [[ "$MODE" == "docker" ]]; then
      docker compose -f "$ROOT_DIR/deploy/docker-compose.yml" down
    else
      stop_local
    fi
    ;;
  restart)
    if [[ "$MODE" == "docker" ]]; then
      docker compose -f "$ROOT_DIR/deploy/docker-compose.yml" restart
    else
      stop_local
      ensure_acp_backend
      start_local
    fi
    ;;
  status)
    status_local
    ;;
  *)
    usage
    exit 1
    ;;
esac

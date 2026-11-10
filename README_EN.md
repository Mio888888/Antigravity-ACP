# Antigravity ACP Gateway

**English** | [简体中文](./README.md) · [License (GPL-3.0)](./LICENSE) · [Contributing](./CONTRIBUTING.md) · [Security](./SECURITY.md) · [Support](./SUPPORT.md) · [Changelog](./CHANGELOG.md)

**Antigravity ACP Gateway** is a production-grade multi-account isolation pool, real-time context & prompt-cache observability platform, and unified multi-protocol AI gateway. It bridges the **Antigravity ACP protocol**—defaulting to the official **Antigravity CLI (`agy acp`)** while also supporting **Zed/Zen's standalone ACP backend package (`agy_acp_server.par`)**—into standard northbound OpenAI and Anthropic endpoints, complete with a **bilingual (Chinese / English)** ReUI web administration console.

---

## Key Features

1. **Unified Multi-Protocol Northbound Endpoints (Single Port)**
   - **OpenAI Official Responses API**: `POST /v1/responses` (native compatibility with Codex CLI `wire_api="responses"`, `previous_response_id` multi-turn continuation, reasoning effort control, and tool call parking/resumption).
   - **OpenAI-Compatible Chat Completions API**: `POST /v1/chat/completions` (works out of the box with Cursor, Cline, Roo Code, Continue, Cherry Studio, and OpenAI SDKs).
   - **Claude Native Messages API**: `POST /v1/messages` (compatible with Claude Code CLI via `ANTHROPIC_BASE_URL`, extended thinking blocks, and multi-turn tool use).
   - **Real-Time Model Discovery & Capability Contract**: `GET /v1/models` and `GET /v1beta/models` (sub-millisecond in-memory cache with `?refresh=1` live bypass, advertising accurate context window capacities: 1M for Gemini, 200K for Claude, 272K for GPT-5/Codex/o3, 128K for GPT-OSS).
   - **Native ACP WebSocket Bridge**: `WS /v1/acp/ws`.

2. **Selectable Dual ACP Backend Engine with Auto-Download (Default: `cli`)**
   - **`cli` Mode (Default)**: Runs the official Antigravity CLI (`agy acp` / `antigravity acp`) with full CLI flag support (`--process-per-session`, `--sandbox`, etc.). One-click deployment automatically downloads and **SHA-512 verifies** the native `agy` binary for your OS/Arch (`darwin/linux/windows`, `amd64/arm64`) if not already installed.
   - **`zed` / `zen` Mode**: Runs Zed's standalone Antigravity ACP server package (`agy_acp_server.par` / `agy_acp_server` / `agy_acp_server.exe`), also supporting automatic download and **SHA-256 verification** from the ACP Registry when missing.
   - **`auto` Discovery Mode**: Automatically locates the installed `agy` CLI or Zed ACP extension package across macOS, Windows, Linux, and Docker containers, with built-in binary header (ELF / Mach-O / PE) validation to prevent cross-OS mount conflicts.

3. **4-Layer Sticky Session & Worker Process Affinity**
   - **Parked Tool Call Continuation**: When the model invokes a client tool, the underlying ACP Worker enters an exclusive `Parked` state; returning `tool_result` resumes the exact same Worker process and in-memory ACP session while folding consecutive streaming tool continuations cleanly.
   - **Explicit Session ID & Client Metadata Extraction**: Automatically extracts client session IDs and conversation titles from HTTP headers (`X-ACP-Session-ID`, `X-Conversation-ID`), request payloads (`prompt_cache_key`, `previous_response_id`, `metadata.session_id`), and system prompts—preserving sticky session routing even across client Context Compaction.
   - **History Hash Prefix Affinity**: Stateless clients that omit session headers are automatically routed to their existing `ManagedSession` via SHA-256 conversation history fingerprinting.
   - **Preemption-Safe Worker Allocation**: New conversations prioritize unbound pre-warmed idle workers (`BoundSessionCount() == 0`), protecting workers that hold active sessions in memory and sustaining **94%–100% prompt cache hit rates**.

4. **Multi-Account Isolation Pool & Egress Routing**
   - Per-account isolated `GEMINI_CLI_HOME` directories (`data/accounts/<id>/.gemini`) and dedicated worker sub-pools.
   - Supports **Direct**, **SOCKS5 / HTTP Proxy**, **Source IP Binding (`bind_ip`)**, and **Linux Network Namespace (`netns`)** egress modes, plus headless OAuth Terminal Login and per-model quota window tracking.

5. **Bilingual (Chinese / English) ReUI Observability Console**
   - Instant **`中文 / EN`** toggle in the top bar.
   - **Overview Dashboard**: Per-request routing trace, 4-stage latency breakdown (Gateway / Network / Provider TTFB / Streaming), pure output TPS calculation, 2D proportional Context Window Treemap & Waffle Grid, bottom request/session selector, and a live **Worker Processes & Bound Sessions** panel.

---

## Comprehensive Deployment Guide

### 1. Supported Platforms & Prerequisites

The gateway uses a pure-Go SQLite engine (`CGO_ENABLED=0`) and embeds the compiled frontend SPA into a single self-contained binary—requiring no external database or web server.

| Target OS | Supported Architectures | Default ACP Auto-Download Source | Deployment Modes |
| :--- | :--- | :--- | :--- |
| **Linux Server** | `x86_64 (amd64)`, `ARM64 (aarch64)` (glibc & musl) | Official `linux_amd64` / `linux_arm64` manifest (SHA-512 verified) | Local background, `systemd` service, Docker Compose, prebuilt binary |
| **macOS** | `Apple Silicon (arm64)`, `Intel (x86_64)` | Official `darwin_arm64` / `darwin_amd64` manifest (SHA-512 verified) | Local background, `launchd` LaunchAgent, Docker Desktop |
| **Windows** | `x64 (amd64)`, `ARM64` | Official `windows_amd64` / `windows_arm64` manifest (SHA-512 verified) | Local background, Scheduled Task service, Docker Desktop |
| **Docker** | `linux/amd64`, `linux/arm64` | Auto-downloaded & verified during `docker build` per container arch | `deploy.sh --mode docker` or `docker compose` |

- **Source Build Prerequisites**: `Go >= 1.23`, `Node.js >= 20` (with `npm`), `python3`, `curl`.
- **Docker Prerequisites**: Only `Docker` and `Docker Compose` (no Go or Node.js needed on the host).
- **Prebuilt Binary Prerequisites**: Only `curl` and `python3` (for auto-downloading the ACP backend, or zero dependencies if copying the binary directly).

---

### 2. macOS & Linux (`x86_64` / `ARM64`) One-Click Deployment (`deploy/deploy.sh`)

Make the deployment script executable:

```bash
chmod +x deploy/deploy.sh
```

#### A. One-Click Build & Local Background Start (Quickest)
Defaults to `cli` mode (`agy acp`). If `agy` is not found on the machine, the script automatically detects your OS and CPU architecture (`linux_arm64`, `linux_amd64`, `darwin_arm64`, etc.), downloads the official release package, verifies its SHA-512 checksum, and installs it to `./bin/agy`:

```bash
./deploy/deploy.sh --mode local --port 8080 --admin-password "your-strong-password" --lang en
```

#### B. Install as a System Service (Linux `systemd` / macOS `launchd` — Recommended for Production)
On Linux servers (both `x86_64` and `ARM64`), this builds the frontend and Go binaries, auto-downloads the ACP backend, installs binaries to `/usr/local/bin` and config to `/etc/antigravity-gateway/config.yaml`, and enables + starts `antigravity-gateway.service`:

```bash
./deploy/deploy.sh --mode service --port 8080 --admin-password "your-strong-password" --lang en
```

Common Linux `systemd` management commands:
```bash
sudo systemctl status antigravity-gateway.service     # Check service status
sudo journalctl -u antigravity-gateway.service -f     # Tail live gateway logs
sudo systemctl restart antigravity-gateway.service    # Restart service
sudo systemctl stop antigravity-gateway.service       # Stop service
```

#### C. Switch or Standalone Download/Update ACP Backend Engine
```bash
# Download or update the default CLI backend (./bin/agy) only
./deploy/deploy.sh --action install-acp --backend cli --lang en

# Use Zed/Zen's standalone ACP backend package (auto-downloads agy_acp_server.par with SHA-256 verification if missing)
./deploy/deploy.sh --mode local --backend zed --lang en

# Offline deployment (use an existing binary path and skip auto-download)
./deploy/deploy.sh --mode local --backend cli --acp-bin "/path/to/agy" --no-download --lang en
```

#### D. Local Background Process Management
```bash
./deploy/deploy.sh --action status --lang en    # Check running status & PID
./deploy/deploy.sh --action restart --lang en   # Graceful restart
./deploy/deploy.sh --action stop --lang en      # Stop background gateway
```

#### E. `deploy/deploy.sh` Options Reference

| Flag | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--action` | `install` \| `install-acp` \| `start` \| `stop` \| `restart` \| `status` \| `build` \| `build-all` | `install` | Action to run (`install-acp` downloads ACP backend only; `build-all` cross-compiles all platforms) |
| `--mode` | `local` \| `service` \| `docker` | `local` | Deployment target: local background daemon, OS service, or Docker Compose |
| `--backend` | `cli` \| `zed` \| `zen` \| `auto` | `cli` | ACP backend engine (defaults to `cli`) |
| `--acp-bin` | File path | Empty (auto) | Explicit path to `agy` or `agy_acp_server.par` |
| `--no-download` | Flag | Off (auto-download on) | Disable automatic ACP backend download when missing |
| `--port` | Port number | `8080` | Gateway HTTP listen port |
| `--admin-password` | String | Keeps config value | Set Web Admin Console login password |
| `--lang` | `zh` \| `en` | `zh` | Script terminal output language |

---

### 3. Docker & Docker Compose Deployment (`linux/amd64` & `linux/arm64`)

`deploy/Dockerfile` uses a multi-stage build and automatically detects the container architecture (`x86_64` or `aarch64`) to download and verify the matching Linux ACP backend (`/usr/local/bin/agy` or `agy_acp_server.par`) during image build—no manual binary preparation required.

#### A. One-Command Docker Launch via `deploy.sh`
```bash
# Build and start container with default `cli` backend
./deploy/deploy.sh --mode docker --port 8080 --admin-password "your-strong-password" --lang en

# Or build and start container with Zed standalone ACP package
./deploy/deploy.sh --mode docker --backend zed --port 8080 --admin-password "your-strong-password" --lang en
```

#### B. Direct `docker compose` Launch & Management
```bash
# Build and start in background (defaults to AG_ACP_BACKEND=cli, works on both x86_64 and ARM64 Docker hosts)
AG_ACP_BACKEND=cli AG_PORT=8080 AG_ADMIN_PASSWORD="your-strong-password" \
  docker compose -f deploy/docker-compose.yml up -d --build

# Follow container logs
docker compose -f deploy/docker-compose.yml logs -f

# Stop and remove container (persistent volumes are preserved)
docker compose -f deploy/docker-compose.yml down
```

#### C. Persistent Volumes
- `gateway-data` (mounted at `/var/lib/antigravity-gateway`): Persists the SQLite database (`gateway.db`) and isolated per-account credential directories (`accounts/<id>/.gemini`).
- `gemini-home` (mounted at `/root/.gemini`): Default root credential directory.
- `../bin` (mounted read-only at `/opt/antigravity-acp`): Optional custom ACP binary directory. Built-in ELF/Mach-O/PE magic-byte checking automatically skips incompatible host binaries (e.g., macOS binaries in `./bin`) and uses the container's native Linux `/usr/local/bin/agy`.

---

### 4. Zero-Toolchain Server Deployment (Cross-Compile & Upload to ARM64 / x86_64 Linux)

If your production server does not have Go or Node.js installed, you can cross-compile standalone release bundles on your workstation:

```bash
# 1. Cross-compile release packages for all platforms on your workstation
./deploy/deploy.sh --action build-all --lang en
```

This produces self-contained release directories (with the frontend SPA embedded) under `./dist/`:
- `dist/antigravity-gateway-linux-arm64/` (for ARM64 / aarch64 Linux servers such as AWS Graviton, Oracle Ampere, Raspberry Pi)
- `dist/antigravity-gateway-linux-amd64/` (for x86_64 Linux servers)
- `dist/antigravity-gateway-darwin-arm64/`, `dist/antigravity-gateway-darwin-amd64/`
- `dist/antigravity-gateway-windows-amd64/`, `dist/antigravity-gateway-windows-arm64/`

```bash
# 2. Upload the target bundle to your Linux server
scp -r ./dist/antigravity-gateway-linux-arm64 root@your-server:/opt/antigravity-gateway

# 3. SSH into the server, auto-download the native ACP backend, and launch
ssh root@your-server
cd /opt/antigravity-gateway
chmod +x antigravity-gateway antigravity-probe deploy/deploy.sh
./deploy/deploy.sh --action install-acp --backend cli --lang en
nohup ./antigravity-gateway -config ./config/config.yaml -backend cli > gateway.log 2>&1 &
```

---

### 5. Windows One-Click Deployment (PowerShell `deploy/deploy.ps1`)

Run in PowerShell on Windows 10/11 or Windows Server (`x64` or `ARM64`):

```powershell
# Option A: Build & start locally in background (defaults to `cli` mode, auto-downloading `agy.exe` with SHA-512 verification if missing)
.\deploy\deploy.ps1 -Mode local -Port 8080 -AdminPassword "your-strong-password" -Lang en

# Option B: Explicitly use Zed/Zen ACP package (auto-downloads with SHA-256 verification if missing)
.\deploy\deploy.ps1 -Mode local -Backend zed -Lang en

# Option C: Download/update ACP backend binary into .\bin only
.\deploy\deploy.ps1 -Action install-acp -Backend cli -Lang en

# Option D: Register as a Windows background Scheduled Task service
.\deploy\deploy.ps1 -Mode service -Port 8080 -AdminPassword "your-strong-password" -Lang en

# Lifecycle management
.\deploy\deploy.ps1 -Action status -Lang en
.\deploy\deploy.ps1 -Action restart -Lang en
.\deploy\deploy.ps1 -Action stop -Lang en
```

---

### 6. Post-Deployment Initialization (Admin Console -> OAuth Sign-In -> API Key Issuance)

1. **Sign In to the Web Admin Console**
   - Open `http://<server-ip>:8080` in your browser and sign in with your admin password (defaults to `ag-admin-secret-key` in `config/config.yaml`).
2. **Add an Account & Complete Headless OAuth Sign-In**
   - Navigate to the **Account Pool** tab. Select the `default` account or click **New Isolated Account** (each account can be configured with Direct, SOCKS5/HTTP Proxy, Source IP Binding, or Linux NetNS egress).
   - Click **Terminal Login (OAuth)** on the account card. The gateway spawns an isolated login session and extracts the Google OAuth URL.
   - Open the OAuth URL in your local browser to authenticate (on headless remote servers, paste the resulting redirect URL or authorization code back into the console modal to complete sign-in).
   - Once authenticated, the account status transitions to `ready`. Click **Refresh Quota** to inspect real-time 5-hour and weekly quota percentages across Gemini and Claude/GPT model groups.
3. **Issue Northbound API Keys**
   - Open the **API Keys** tab to create or copy your `sk-ag-...` key (bound to a specific account or `auto` load-balancing).

---

## Configuration Reference (`config/config.yaml`)

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  public_base_url: "http://localhost:8080"
  data_dir: "./data"
  admin_token: "ag-admin-secret-key"
  admin_password: "ag-admin-secret-key"

acp_pool:
  # backend_type options:
  # - "cli" (default) : Uses Antigravity CLI (agy acp / antigravity acp)
  # - "zed" / "zen"   : Uses standalone Zed/Zen ACP backend package (agy_acp_server.par / agy_acp_server)
  # - "auto"          : Uses binary_path if present; otherwise auto-discovers agy CLI or Zed/Zen ACP package
  backend_type: "cli"
  binary_path: "auto"
  workspace_dir: "/tmp/antigravity-gateway-workspace"
  min_idle_workers: 1
  max_workers: 5
  max_idle_time_sec: 600
  tool_park_timeout_sec: 180

prompt_policy:
  mode: "pure_passthrough"      # pure_passthrough | prepend_global | native_hybrid
  disable_builtin_tools: true

sandbox_policy:
  session_mode: "yolo"          # yolo (full-auto sticky reuse) | sticky_conversation | stateless_single_turn
  internal_approval: "auto_approve"
```

Supported Environment Variable Overrides:
- `AG_ACP_BACKEND`: `cli` (default) | `zed` | `zen` | `auto`
- `AG_ACP_BINARY_PATH`: Explicit path to `agy` or `agy_acp_server.par` binary
- `AG_HOST` / `AG_PORT`: Listen host and port
- `AG_DATA_DIR` / `AG_WORKSPACE_DIR`: Persistent SQLite/account data directory and workspace directory
- `AG_ADMIN_PASSWORD` / `AG_ADMIN_TOKEN`: Admin console password and API token

---

## Quick Client Onboarding

Open `http://localhost:8080` in your browser to sign in to the Web Admin Console (and copy ready-to-use snippets from the **Client Onboarding** tab):

- **OpenAI Codex CLI (`~/.codex/config.toml`)**:
  ```toml
  model = "gemini-4-argon"
  model_provider = "antigravity"

  [model_providers.antigravity]
  name = "Antigravity ACP Gateway"
  base_url = "http://localhost:8080/v1"
  env_key = "ANTIGRAVITY_API_KEY"
  wire_api = "responses"
  ```
- **Claude Code CLI**:
  ```bash
  export ANTHROPIC_BASE_URL="http://localhost:8080"
  export ANTHROPIC_AUTH_TOKEN="sk-ag-your-issued-key"
  export ANTHROPIC_MODEL="gemini-4-argon"
  claude
  ```
- **Cursor / Cline / Roo Code / Cherry Studio**:
  - Base URL: `http://localhost:8080/v1`
  - API Key: `sk-ag-your-issued-key`
  - Optional Custom Headers: `X-ACP-Context-Window: 1048576`, `X-ACP-Account-ID: auto`

---

## Community & Governance Documentation

All repository maintenance and governance documents are provided in both **English and Simplified Chinese**:

- **[License (`LICENSE`)](./LICENSE)**: Full official GNU General Public License v3.0 (GPL-3.0) text alongside a Simplified Chinese reference translation.
- **[Contributing Guide (`CONTRIBUTING.md`)](./CONTRIBUTING.md)**: Architecture overview, local Go/Web build & test commands, engineering standards, and Pull Request workflow.
- **[Code of Conduct (`CODE_OF_CONDUCT.md`)](./CODE_OF_CONDUCT.md)**: Contributor Covenant v2.1 community standards.
- **[Security Policy (`SECURITY.md`)](./SECURITY.md)**: Production hardening guidelines and private vulnerability reporting procedures.
- **[Support & Troubleshooting (`SUPPORT.md`)](./SUPPORT.md)**: Checklist for ACP binary verification, remote headless OAuth sign-in, and sticky session diagnostics.
- **[Changelog (`CHANGELOG.md`)](./CHANGELOG.md)**: Release history, performance optimizations, and root-cause fixes.

---

## License

This project is licensed under the **[GNU General Public License v3.0 (GPL-3.0)](./LICENSE)**. You are free to run, study, share, and modify the software under the terms of the GPL-3.0, provided that any distributed copies or derivative works are also licensed under the GPL-3.0 with complete corresponding source code.


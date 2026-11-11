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
   - **`cli` Mode (Default)**: Runs the official Antigravity CLI (`agy acp` / `antigravity acp`) with full CLI flag support (`--process-per-session`, `--sandbox`, etc.). One-click deployment automatically downloads and **SHA-512 verifies** the native `agy` binary for your OS/Arch (`darwin/linux/windows`, `amd64/arm64`) from the official manifest if not already installed. Verification **fails closed**: a missing hash, missing hashing tool or unreachable manifest is an error, and a hash mismatch is always fatal.
   - **`zed` / `zen` Mode**: Runs Zed's standalone Antigravity ACP server package (`agy_acp_server.par` / `agy_acp_server` / `agy_acp_server.exe`). Automatic download resolves the latest version and **SHA-256** checksum from the official ACP Registry by default, or accepts an explicitly pinned `ZED_ACP_SHA256` (`--zed-sha256`).
   - **`auto` Discovery Mode**: Automatically locates the installed `agy` CLI or Zed ACP extension package across macOS, Windows, Linux, and Docker containers, with built-in binary header (ELF / Mach-O / PE) validation to prevent cross-OS mount conflicts.

3. **4-Layer Sticky Session & Worker Process Affinity**
   - **Parked Tool Call Continuation**: When the model invokes a client tool, the underlying ACP Worker enters an exclusive `Parked` state; returning `tool_result` resumes the exact same Worker process and in-memory ACP session while folding consecutive streaming tool continuations cleanly.
   - **Explicit Session ID & Client Metadata Extraction**: Automatically extracts client session IDs and conversation titles from HTTP headers (`X-ACP-Session-ID`, `X-Conversation-ID`), request payloads (`prompt_cache_key`, `previous_response_id`, `metadata.session_id`), and system prompts—preserving sticky session routing even across client Context Compaction.
   - **History Hash Prefix Affinity**: Stateless clients that omit session headers are automatically routed to their existing `ManagedSession` via SHA-256 conversation history fingerprinting.
   - **Preemption-Safe Worker Allocation**: New conversations prioritize unbound pre-warmed idle workers (`BoundSessionCount() == 0`), protecting workers that hold active sessions in memory and sustaining **94%–100% prompt cache hit rates**.

4. **Multi-Account Isolation Pool & Egress Routing**
   - Per-account isolated `GEMINI_CLI_HOME` directories (`data/accounts/<id>/.gemini`) and dedicated worker sub-pools.
   - Supports **Direct**, **SOCKS5 / HTTP Proxy**, **Source IP Binding (`bind_ip`)**, and **Linux Network Namespace (`netns`)** egress modes, plus headless OAuth Terminal Login and per-model quota window tracking.

5. **Bilingual (Chinese / English) ReUI Observability Console & In-Console Self-Update**
   - Instant **`中文 / EN`** toggle in the top bar.
   - **In-Console Online Updater (`Updates`)**: Built-in dual-target updater in the admin header—downloads prebuilt gateway archives directly from **GitHub Releases** (`SHA256SUMS` verification + ELF/Mach-O/PE binary header validation + graceful in-place restart & automatic page reload), and hot-updates the **Antigravity CLI / Zed ACP backend engine** without restarting the gateway or interrupting active requests (automatically detects Docker or read-only systemd sandboxes and provides the host update command).
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

- **Source Build Prerequisites**: `Go >= 1.27.3` (as pinned in `go.mod`), `Node.js >= 20` (with `npm`), `curl`, and one of `sha512sum` / `shasum` / `openssl` for hash verification; the Zed backend also needs `unzip`.
- **Docker Prerequisites**: `Docker` and `Docker Compose v2.24+` (no Go or Node.js needed on the host).
- **Prebuilt Binary Prerequisites**: Only `curl` and one of the hashing tools above (for auto-downloading the ACP backend, or zero dependencies if copying the binary directly).

> **Building from source**: build the console first (`cd web && npm ci && npm run build`), then `go build -tags webui ./cmd/gateway` to embed `web/dist`. A plain `go build ./...` (no `webui` tag) also works on a fresh clone but embeds only a placeholder page saying the console was not built. The deploy scripts, Dockerfile and CI pass the tag automatically.

---

### 2. macOS & Linux (`x86_64` / `ARM64`) One-Click Deployment (`deploy/deploy.sh`)

Make the deployment script executable:

```bash
chmod +x deploy/deploy.sh deploy/update.sh
```

#### A. One-Click Build & Local Background Start (Quickest)
Defaults to `cli` mode (`agy acp`). If `agy` is not found on the machine, the script automatically detects your OS and CPU architecture (`linux_arm64`, `linux_amd64`, `darwin_arm64`, etc.), downloads the official release package, verifies its SHA-512 checksum, and installs it to `./bin/agy`. After launching, the script polls `/healthz` for up to 20 seconds and only reports success once the gateway is healthy:

```bash
./deploy/deploy.sh --mode local --port 8080 --admin-password-stdin --lang en   # prompts for an initial admin password (optional; omit to auto-generate)
```

- The admin password is **never written** to `config/config.yaml`; it is passed to the gateway process only via the `AG_ADMIN_PASSWORD` environment variable. `--admin-password <pwd>` still works but is visible in shell history and `ps`, so the script warns about it.
- Local mode starts with `umask 077`; the data directory is `0700` and `data/gateway.log` is `0600`.

#### B. Install as a System Service (Linux `systemd` / macOS `launchd` — Recommended for Production)
On Linux servers (both `x86_64` and `ARM64`), this builds the frontend and Go binaries, auto-downloads the ACP backend, and then:
- creates a dedicated system user `antigravity` (`useradd --system`, home `/var/lib/antigravity-gateway`, `nologin` shell) — the service **no longer runs as root**;
- installs the gateway and ACP backend into `/usr/local/bin` as root-owned `0755` files (the service always uses `/usr/local/bin/agy`, never a binary from your checkout);
- installs the config to `/etc/antigravity-gateway/config.yaml` (the previous one is backed up) and writes the admin password to `/etc/antigravity-gateway/env` (`root:root 0600`, loaded via `EnvironmentFile=`);
- runs `systemctl daemon-reload`, `enable` and `restart` (so redeploys take effect) and waits for `/healthz`.

```bash
./deploy/deploy.sh --mode service --port 8080 --admin-password-stdin --lang en
```

`deploy/antigravity-gateway.service` is sandboxed: `NoNewPrivileges`, `ProtectSystem=strict` (only `/var/lib/antigravity-gateway` is writable), `ProtectHome=tmpfs`, `PrivateTmp`, `PrivateDevices`, `ProtectKernelTunables/Modules/Logs`, `ProtectControlGroups`, `RestrictSUIDSGID`, `LockPersonality`, an empty capability set and `UMask=0077`. Service layout:

| Path | Purpose |
| :--- | :--- |
| `/var/lib/antigravity-gateway/data` | `AG_DATA_DIR`: `gateway.db`, `admin_credentials.txt`, per-account directories |
| `/var/lib/antigravity-gateway/workspace` | `AG_WORKSPACE_DIR`: ACP session workspace (the agent CLI writes here) |
| `/var/lib/antigravity-gateway/.gemini` | Default account credentials of the service user (`HOME`) |

> Account network mode `netns` relies on `ip netns exec` (needs `CAP_SYS_ADMIN`) and is not available under this hardened unit; use `proxy` or `bind_ip` instead.

Common Linux `systemd` management commands (or `./deploy/deploy.sh --mode service --action start|stop|restart|status`):
```bash
sudo systemctl status antigravity-gateway.service     # Check service status
sudo journalctl -u antigravity-gateway.service -f     # Tail live gateway logs
sudo systemctl restart antigravity-gateway.service    # Restart service
sudo cat /var/lib/antigravity-gateway/data/admin_credentials.txt   # Show the generated admin credentials
```

On macOS, `--mode service` registers a per-user launchd agent at `~/Library/LaunchAgents/com.antigravity.gateway.plist` (mode `0600`).

#### C. One-Click Code Pull & CLI / Zed ACP Package Update (`deploy/update.sh`)
Run `./deploy/update.sh` (or `./deploy/deploy.sh --action update`) to perform an end-to-end update across `local`, `service` (systemd / launchd), or `docker` deployments:
1. Automatically stashes local modifications and runs `git pull --ff-only` (or links and syncs the directory to the official GitHub repository if deployed from a release archive without `.git`), then re-execs the updated script;
2. Auto-detects the active deployment mode (`docker` / `service` / `local`) and backend engine;
3. **Updates the ACP backend package(s)**: downloads and verifies the latest **Antigravity CLI (`agy`)** and/or **Zed standalone ACP package (`agy_acp_server.par`)** (in Docker mode, passes `AG_ACP_CACHE_BUST` to force a fresh ACP layer build);
4. Rebuilds the frontend + Go binaries, restarts the gateway, and verifies `/healthz`.

```bash
# One-click pull latest code + update active/installed ACP backend package + rebuild & restart
./deploy/update.sh --lang en

# One-click update code and explicitly update (or switch to) CLI, Zed ACP, or both
./deploy/update.sh --backend cli --lang en
./deploy/update.sh --backend zed --lang en
./deploy/update.sh --backend both --lang en

# Update ACP backend package(s) only without pulling code or restarting
./deploy/update.sh --action update-acp --backend cli --lang en
./deploy/update.sh --action update-acp --backend zed --lang en
./deploy/update.sh --action update-acp --backend both --lang en

# Offline deployment (use an existing binary path and skip auto-download)
./deploy/deploy.sh --mode local --backend cli --acp-bin "/path/to/agy" --no-download --lang en
```

Download integrity rules (identical in `deploy.sh`, `deploy.ps1` and the `Dockerfile`):
- **CLI**: uses the SHA-512 from the official manifest. An unreachable manifest, a missing hash or a missing hashing tool is an error; only an explicit `--insecure-skip-verify` lets the "missing hash/tool" cases continue.
- **Zed package**: automatically resolves the latest version and expected SHA-256 from the `antigravity-acp` entry in the official ACP Registry (`https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json`), or accepts an explicit override via `ZED_ACP_SHA256` / `--zed-sha256` and `ZED_ACP_VERSION` / `--zed-version`.
- **A hash mismatch is always fatal**; there is no fallback to another download method.
- The official installer `https://antigravity.google/cli/install.sh` is only used when the manifest is unavailable **and** `--allow-install-script` is passed. It is downloaded to a temp file and shown in full first (with a confirmation prompt on a terminal); it is never piped into `bash`.

#### D. Process Management (routed by `--mode`)
```bash
./deploy/deploy.sh --action status --lang en                    # Local: running status & PID
./deploy/deploy.sh --action restart --lang en                   # Local: graceful restart (waits up to 15s for the old process)
./deploy/deploy.sh --action stop --lang en                      # Local: stop background gateway
./deploy/deploy.sh --mode service --action restart --lang en    # systemctl restart / launchctl reload
./deploy/deploy.sh --mode docker --action status --lang en      # docker compose ps
```

In local mode, `stop` / `restart` only terminate the PID recorded in the pid file, and only after confirming its command line is this checkout's `bin/antigravity-gateway`. If another process holds the port, the script just reports "port busy by PID X (not ours)" and never kills it. When `--backend` / `--port` are omitted, `start` / `restart` keep the values from `config.yaml` instead of silently resetting them to `cli` / `8080`.

#### E. `deploy/deploy.sh` Options Reference

| Flag | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--action` | `install` \| `update` \| `install-acp` \| `update-acp` \| `start` \| `stop` \| `restart` \| `status` \| `build` \| `build-all` | `install` (`update` in `update.sh`) | Action to run (`update` pulls latest code + updates ACP package + restarts; `update-acp` updates ACP backend only) |
| `--mode` | `local` \| `service` \| `docker` | `local` (auto-detected on `update`) | Deployment target: local background daemon, OS service, or Docker Compose; lifecycle actions are routed accordingly |
| `--backend` | `cli` \| `zed` \| `zen` \| `auto` \| `both` | Keep `config.yaml` value | ACP backend engine (repository default `cli`; `both` updates both CLI and Zed ACP packages) |
| `--acp-bin` | File path | Empty (auto) | Explicit path to `agy` or `agy_acp_server.par` |
| `--no-download` | Flag | Off (auto-download on) | Disable automatic ACP backend download/update |
| `--no-git` / `--skip-git` | Flag | Off | Skip `git pull` during `--action update` |
| `--port` | Port number | `AG_PORT`, else `config.yaml` (docker: `8080`) | Listen port in local/service mode; host-side published port in docker mode (the container always listens on `8080`) |
| `--admin-password-stdin` | Flag | Off | Read the initial admin password (min. 8 chars) from stdin or an interactive prompt |
| `--admin-password` | String | Empty (auto-generated) | Same, but on the command line (discouraged); only applies until the password is changed in the console |
| `--zed-version` | Version string | Latest from ACP Registry | Override Zed ACP package version (or set `ZED_ACP_VERSION`) |
| `--zed-sha256` | 64 hex chars | Auto-resolved from ACP Registry | Expected SHA-256 of the Zed ACP archive (or set `ZED_ACP_SHA256`) |
| `--insecure-skip-verify` | Flag | Off | Continue when no expected hash or hashing tool is available (a mismatch still fails) |
| `--allow-install-script` | Flag | Off | Allow downloading and running the official installer when the manifest is unavailable (shown first) |
| `--lang` | `zh` \| `en` | `zh` | Script terminal output language |

---

### 3. Docker & Docker Compose Deployment (`linux/amd64` & `linux/arm64`)

`deploy/Dockerfile` uses a multi-stage build (base images pinned to exact version tags and tracked by Dependabot), installs frontend dependencies with `npm ci` from the lockfile, and automatically detects the container architecture (`x86_64` or `aarch64`) to download and verify the matching Linux ACP backend (`/usr/local/bin/agy` or `agy_acp_server.par`) during image build. The runtime image runs as the non-root user `agy` (UID `10001`) and ships a `HEALTHCHECK` against `/healthz`.

#### A. One-Command Docker Launch via `deploy.sh`
```bash
# Build and start container with default `cli` backend (published on 127.0.0.1 only by default)
./deploy/deploy.sh --mode docker --port 8080 --admin-password-stdin --lang en

# Explicitly publish on all interfaces
AG_BIND=0.0.0.0 ./deploy/deploy.sh --mode docker --port 8080 --lang en

# Or build with the Zed standalone ACP package (a pinned SHA-256 is required and passed as a build arg)
ZED_ACP_SHA256="<sha256 of agy-acp-server-1.4.5-linux-<arch>.zip>" ./deploy/deploy.sh --mode docker --backend zed --port 8080 --lang en
```

In docker mode `--port` only changes the host-side port (`AG_PORT`); the container always listens on `8080` and the mounted `config.yaml` is not rewritten.

#### B. Direct `docker compose` Launch & Management
```bash
# Optional: set an initial admin password (deploy/gateway.env is gitignored)
cp deploy/gateway.env.example deploy/gateway.env && chmod 600 deploy/gateway.env   # edit and uncomment AG_ADMIN_PASSWORD

# Build and start in background (defaults to AG_ACP_BACKEND=cli, works on both x86_64 and ARM64 Docker hosts)
AG_ACP_BACKEND=cli AG_PORT=8080 docker compose -f deploy/docker-compose.yml up -d --build

# Publish on all interfaces when other machines need access
AG_BIND=0.0.0.0 docker compose -f deploy/docker-compose.yml up -d

# Follow container logs / show the generated admin credentials
docker compose -f deploy/docker-compose.yml logs -f
docker exec antigravity-gateway cat /var/lib/antigravity-gateway/admin_credentials.txt

# Stop and remove container (persistent volumes are preserved)
docker compose -f deploy/docker-compose.yml down
```

Compose variables: `AG_BIND` (default `127.0.0.1`), `AG_PORT` (default `8080`), `AG_ACP_BACKEND`, `AG_ACP_BINARY_PATH` (default `auto`), and the build args `ZED_ACP_SHA256` / `ZED_ACP_VERSION` / `AG_INSECURE_SKIP_VERIFY` (set to `1` only to build without an expected hash). Requests reaching the container do not come from loopback, so the northbound API requires an API key created in the console first.

#### C. Persistent Volumes
- `gateway-data` (mounted at `/var/lib/antigravity-gateway`): Persists the SQLite database (`gateway.db`), `admin_credentials.txt`, and isolated per-account credential directories (`accounts/<id>/.gemini`).
- `gemini-home` (mounted at `/home/agy/.gemini`; previously `/root/.gemini`): Default account credential directory.
- `../bin` (mounted read-only at `/opt/antigravity-acp`): Optional custom ACP binary directory (`deploy.sh` creates `./bin`). Built-in ELF/Mach-O/PE magic-byte checking automatically skips incompatible host binaries (e.g., macOS binaries in `./bin`) and uses the container's native Linux `/usr/local/bin/agy`.

> **Upgrading from the old root container**: files in existing volumes are owned by root; fix ownership once:
> `docker compose -f deploy/docker-compose.yml run --rm --user root --entrypoint chown antigravity-gateway -R 10001:10001 /var/lib/antigravity-gateway /home/agy/.gemini`

---

### 4. Zero-Toolchain Server Deployment (Cross-Compile & Upload to ARM64 / x86_64 Linux)

If your production server does not have Go or Node.js installed, you can cross-compile standalone release bundles on your workstation:

```bash
# 1. Cross-compile release packages for all platforms on your workstation
./deploy/deploy.sh --action build-all --lang en
```

This produces self-contained release directories (with the frontend SPA embedded) under `./dist/` (gitignored; each bundle's `config/config.yaml` is the pristine tracked version, without local edits):
- `dist/antigravity-gateway-linux-arm64/` (for ARM64 / aarch64 Linux servers such as AWS Graviton, Oracle Ampere, Raspberry Pi)
- `dist/antigravity-gateway-linux-amd64/` (for x86_64 Linux servers)
- `dist/antigravity-gateway-darwin-arm64/`, `dist/antigravity-gateway-darwin-amd64/`
- `dist/antigravity-gateway-windows-amd64/`, `dist/antigravity-gateway-windows-arm64/`

```bash
# 2. Upload the target bundle to your Linux server
scp -r ./dist/antigravity-gateway-linux-arm64 deploy@your-server:/opt/antigravity-gateway

# 3. SSH into the server (preferably as a non-root user), auto-download the native ACP backend, and launch
ssh deploy@your-server
cd /opt/antigravity-gateway
chmod +x antigravity-gateway antigravity-probe deploy/deploy.sh
./deploy/deploy.sh --action install-acp --backend cli --lang en
mkdir -p -m 700 data && umask 077
nohup ./antigravity-gateway -config ./config/config.yaml >> data/gateway.log 2>&1 &
cat data/admin_credentials.txt   # generated admin credentials for this start
```

---

### 5. Windows One-Click Deployment (PowerShell `deploy/deploy.ps1`)

Run in PowerShell on Windows 10/11 or Windows Server (`x64` or `ARM64`):

```powershell
# Option A: Build & start locally in background (defaults to `cli` mode, auto-downloading `agy.exe` with SHA-512 verification if missing)
.\deploy\deploy.ps1 -Mode local -Port 8080 -AdminPasswordStdin -Lang en

# Option B: One-click pull latest code & update CLI / Zed ACP packages (.\deploy\update.ps1)
.\deploy\update.ps1 -Lang en
.\deploy\update.ps1 -Backend zed -Lang en
.\deploy\update.ps1 -Backend both -Lang en

# Option C: Download/update ACP backend binary into .\bin only
.\deploy\deploy.ps1 -Action update-acp -Backend cli -Lang en

# Option D: Register a Windows Scheduled Task that starts at boot (run from an elevated PowerShell)
.\deploy\deploy.ps1 -Mode service -Port 8080 -AdminPasswordStdin -Lang en

# Lifecycle management (also routed by -Mode)
.\deploy\deploy.ps1 -Action status -Lang en
.\deploy\deploy.ps1 -Action restart -Lang en
.\deploy\deploy.ps1 -Mode service -Action stop -Lang en
```

- Same rules as `deploy.sh`: a missing hash fails (`-InsecureSkipVerify` only allows the "missing hash" case; a mismatch always fails), the password is never written to `config.yaml`, `stop` only terminates a process whose path is verified to be `bin\antigravity-gateway.exe`, and `-Backend` / `-Port` default to the config file values.
- Service mode uses `Register-ScheduledTask` with an **at-startup** trigger (`AtStartup`, `S4U` for the current user, no execution time limit) for the task `AntigravityACPGateway`. A small `data\run-gateway.cmd` wrapper sets the working directory and writes output to `data\gateway.log`; the `data` ACL is restricted to the current user.

---

### 6. Post-Deployment Initialization (Admin Console -> OAuth Sign-In -> API Key Issuance)

1. **Sign In to the Web Admin Console**
   - Open `http://<server-ip>:8080` in your browser and sign in with the admin password. With the default `admin_password: "auto"` / `admin_token: "auto"`, a random password and token are generated on every start and written to `<data_dir>/admin_credentials.txt` (mode `0600`, **never printed to logs**); the deploy scripts show the file and the command to read it when they finish.
   - Click **Change Password** (top right; minimum 8 characters, custom token minimum 12) to make the credentials permanent. They are then stored as a salted PBKDF2-SHA256 hash in `gateway.db`, and passwords supplied via `AG_ADMIN_PASSWORD` / `--admin-password-stdin` no longer apply.
2. **Add an Account & Complete Headless OAuth Sign-In**
   - Navigate to the **Account Pool** tab. Select the `default` account or click **New Isolated Account** (each account can be configured with Direct, SOCKS5/HTTP Proxy, Source IP Binding, or Linux NetNS egress).
   - Click **Terminal Login (OAuth)** on the account card. The gateway spawns an isolated login session and extracts the Google OAuth URL.
   - Open the OAuth URL in your local browser to authenticate (on headless remote servers, paste the resulting redirect URL or authorization code back into the console modal to complete sign-in).
   - Once authenticated, the account status transitions to `ready`. Click **Refresh Quota** to inspect real-time 5-hour and weekly quota percentages across Gemini and Claude/GPT model groups.
3. **Issue Northbound API Keys**
   - Open the **API Keys** tab to create or copy your `sk-ag-...` key (bound to a specific account or `auto` load-balancing).
   - Until at least one API key exists, `/v1/*` only accepts unauthenticated requests from loopback (`127.0.0.1` / `::1`); remote clients and Docker hosts need an API key. A key bound to an account cannot be redirected to another account via `X-ACP-Account-ID` or `?account_id=`.

---

## Configuration Reference (`config/config.yaml`)

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  public_base_url: "http://localhost:8080"
  data_dir: "./data"            # created with mode 0700 at startup
  admin_token: "auto"           # "auto": random per start, written to <data_dir>/admin_credentials.txt
  admin_password: "auto"        # do not put a real password here (tracked by git); use AG_ADMIN_PASSWORD

acp_pool:
  # backend_type options:
  # - "cli" (default) : Uses Antigravity CLI (agy acp / antigravity acp)
  # - "zed" / "zen"   : Uses standalone Zed/Zen ACP backend package (agy_acp_server.par / agy_acp_server)
  # - "auto"          : Uses binary_path if present; otherwise auto-discovers agy CLI or Zed/Zen ACP package
  backend_type: "cli"
  binary_path: "./bin/agy"      # falls back to auto-discovery per backend_type when missing; "auto" also works
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
- `AG_ADMIN_PASSWORD` / `AG_ADMIN_TOKEN`: Initial admin password (min. 8 chars) and admin token (min. 12 chars); they only apply until the password is changed in the console
- `AG_WEB_DIR`: Optional; serve static files from this directory first (for frontend development). Without it the embedded assets are always used

---

## Secure Defaults

- **Northbound API authentication**: while no API key exists, `/v1/*` only accepts unauthenticated requests from loopback (`127.0.0.1` / `::1`). Create an API key in the console first. The `?key=` query parameter is still accepted for the northbound API; `?token=` is no longer accepted anywhere.
- **Account & session isolation**: an API key bound to an account cannot be redirected to another account via `X-ACP-Account-ID` or `?account_id=`; sessions are isolated per API key.
- **ACP WebSocket workspace**: `/v1/acp/ws` sessions always run inside a gateway-managed workspace directory; a client-provided `cwd` outside it is rewritten.
- **Admin credentials**: the admin password and token are no longer printed to logs; read `<data_dir>/admin_credentials.txt`. The password is stored as salted PBKDF2-SHA256 (minimum 8 characters; custom admin token minimum 12). `Authorization: Bearer` on admin APIs accepts the admin token or a session token, not the password. Failed credential attempts are rate-limited per IP on all endpoints.
- **Admin API hardening**: state-changing admin endpoints require POST/PUT/DELETE, cross-origin state-changing requests are rejected, the session cookie is `SameSite=Strict`, and WebSocket upgrades check `Origin`. Admin API responses no longer include account `api_key`, and proxy passwords are masked as `********`.
- **Internal MCP bridge**: `/mcp/sessions/*` only accepts loopback connections.
- **Subprocess environment**: ACP agent subprocesses no longer inherit `AG_*` environment variables.
- **Account IDs**: restricted to `[A-Za-z0-9._-]`, 1–64 characters.

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
  - Optional Custom Headers: `X-ACP-Context-Window: 1048576`, `X-ACP-Account-ID: auto` (only honoured for API keys not bound to an account)

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


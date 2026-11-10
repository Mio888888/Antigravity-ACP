# Base images are pinned to exact version tags; Dependabot (docker ecosystem, /deploy) proposes bumps.

# Stage 1: Build ReUI Bilingual Frontend SPA
FROM node:22.23.4-bookworm-slim AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build Go Gateway & Probe Binaries (embeds web/dist via -tags webui)
FROM golang:1.27.3-bookworm AS go-builder
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -tags webui -trimpath -ldflags="-s -w" -o /out/antigravity-gateway ./cmd/gateway && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/antigravity-probe ./cmd/probe

# Stage 3: Runtime Image (installs Antigravity CLI `agy` or Zed `agy_acp_server.par` for linux/amd64 & linux/arm64)
FROM debian:bookworm-20261104-slim
# AG_ACP_BACKEND: cli (default) | zed | zen | auto
ARG AG_ACP_BACKEND=cli
# Zed package: the expected SHA-256 of agy-acp-server-${ZED_ACP_VERSION}-linux-<arch>.zip is required.
ARG ZED_ACP_VERSION=1.4.5
ARG ZED_ACP_SHA256=""
# Set to 1 only to allow a build when no expected hash is available. A hash mismatch always fails.
ARG AG_INSECURE_SKIP_VERIFY=0
ENV AG_ACP_BACKEND=${AG_ACP_BACKEND} \
    AG_HOST=0.0.0.0 \
    AG_PORT=8080 \
    AG_DATA_DIR=/var/lib/antigravity-gateway \
    AG_WORKSPACE_DIR=/tmp/antigravity-gateway-workspace \
    AGY_ACP_FORCE_FILE_STORAGE=1 \
    HOME=/home/agy

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    iproute2 \
    unzip \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --user-group --home-dir /home/agy --create-home --shell /usr/sbin/nologin agy \
    && mkdir -p /opt/antigravity-acp /var/lib/antigravity-gateway /etc/antigravity-gateway /home/agy/.gemini \
    && chown -R agy:agy /var/lib/antigravity-gateway /home/agy \
    && chmod 0700 /var/lib/antigravity-gateway /home/agy/.gemini

RUN set -eu; \
    arch_raw="$(uname -m)"; \
    case "$arch_raw" in \
      x86_64|amd64) cli_arch="amd64"; zed_arch="x86_64" ;; \
      aarch64|arm64) cli_arch="arm64"; zed_arch="arm64" ;; \
      *) echo "Unsupported architecture: $arch_raw" >&2; exit 1 ;; \
    esac; \
    backend_norm="$(echo "${AG_ACP_BACKEND}" | tr '[:upper:]' '[:lower:]')"; \
    tmpd="$(mktemp -d)"; \
    if [ "$backend_norm" = "zen" ] || [ "$backend_norm" = "zed" ]; then \
      expected="$(echo "${ZED_ACP_SHA256}" | tr '[:upper:]' '[:lower:]')"; \
      if [ -z "$expected" ] && [ "${AG_INSECURE_SKIP_VERIFY}" != "1" ]; then \
        echo "ERROR: build arg ZED_ACP_SHA256 (SHA-256 of agy-acp-server-${ZED_ACP_VERSION}-linux-${zed_arch}.zip) is required." >&2; \
        echo "       Take it from a trusted source (e.g. the antigravity-acp entry of the ACP registry), or set AG_INSECURE_SKIP_VERIFY=1 to build unverified." >&2; \
        exit 1; \
      fi; \
      archive_url="https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-${ZED_ACP_VERSION}-linux-${zed_arch}.zip"; \
      curl -fsSL --retry 3 -o "$tmpd/acp.zip" "$archive_url"; \
      if [ -n "$expected" ]; then \
        echo "${expected}  $tmpd/acp.zip" | sha256sum -c -; \
      else \
        echo "WARNING: installing $archive_url WITHOUT SHA-256 verification (AG_INSECURE_SKIP_VERIFY=1)" >&2; \
      fi; \
      mkdir -p "$tmpd/x"; \
      unzip -q -o "$tmpd/acp.zip" -d "$tmpd/x"; \
      for fn in agy_acp_server.par agy_acp_server localharness_external; do \
        if [ -f "$tmpd/x/$fn" ]; then install -m 0755 "$tmpd/x/$fn" "/usr/local/bin/$fn"; fi; \
      done; \
    else \
      manifest_url="https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_${cli_arch}.json"; \
      manifest_json="$(curl -fsSL --retry 3 "$manifest_url")"; \
      pkg_url="$(printf '%s\n' "$manifest_json" | awk -F'"' '/"url"[[:space:]]*:/ {print $4; exit}')"; \
      expected_sha512="$(printf '%s\n' "$manifest_json" | awk -F'"' '/"sha512"[[:space:]]*:/ {print $4; exit}' | tr '[:upper:]' '[:lower:]')"; \
      if [ -z "$pkg_url" ]; then echo "ERROR: CLI manifest $manifest_url has no package URL" >&2; exit 1; fi; \
      if [ -z "$expected_sha512" ] && [ "${AG_INSECURE_SKIP_VERIFY}" != "1" ]; then \
        echo "ERROR: CLI manifest $manifest_url has no sha512; refusing to install (AG_INSECURE_SKIP_VERIFY=1 overrides)" >&2; \
        exit 1; \
      fi; \
      curl -fsSL --retry 3 -o "$tmpd/agy.tar.gz" "$pkg_url"; \
      if [ -n "$expected_sha512" ]; then \
        echo "${expected_sha512}  $tmpd/agy.tar.gz" | sha512sum -c -; \
      else \
        echo "WARNING: installing $pkg_url WITHOUT SHA-512 verification (AG_INSECURE_SKIP_VERIFY=1)" >&2; \
      fi; \
      tar -xzf "$tmpd/agy.tar.gz" -C "$tmpd" antigravity; \
      install -m 0755 "$tmpd/antigravity" /usr/local/bin/agy; \
    fi; \
    rm -rf "$tmpd"

WORKDIR /app
COPY --from=go-builder /out/antigravity-gateway /usr/local/bin/antigravity-gateway
COPY --from=go-builder /out/antigravity-probe /usr/local/bin/antigravity-probe
COPY config/config.yaml /etc/antigravity-gateway/config.yaml

USER agy
VOLUME ["/var/lib/antigravity-gateway", "/home/agy/.gemini", "/opt/antigravity-acp"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD curl -fsS -o /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["antigravity-gateway", "-config", "/etc/antigravity-gateway/config.yaml"]

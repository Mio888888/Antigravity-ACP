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
# Zed package: automatically resolved from the official ACP registry when empty, or pin version + SHA-256 explicitly.
ARG ZED_ACP_VERSION=""
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

# Set AG_ACP_CACHE_BUST to a unique value (e.g. timestamp) during updates to force re-downloading the latest ACP backend package.
ARG AG_ACP_CACHE_BUST=""
RUN set -eu; \
    arch_raw="$(uname -m)"; \
    case "$arch_raw" in \
      x86_64|amd64) cli_arch="amd64"; zed_arch="x86_64"; zed_reg_arch="x86_64" ;; \
      aarch64|arm64) cli_arch="arm64"; zed_arch="arm64"; zed_reg_arch="aarch64" ;; \
      *) echo "Unsupported architecture: $arch_raw" >&2; exit 1 ;; \
    esac; \
    backend_norm="$(echo "${AG_ACP_BACKEND}" | tr '[:upper:]' '[:lower:]')"; \
    install_cli="0"; install_zed="0"; \
    case "$backend_norm" in \
      zed|zen) install_zed="1" ;; \
      auto|both|all) install_cli="1"; install_zed="1" ;; \
      *) install_cli="1" ;; \
    esac; \
    tmpd="$(mktemp -d)"; \
    if [ "$install_cli" = "1" ]; then \
      manifest_url="https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_${cli_arch}.json"; \
      curl -fsSL --retry 3 -o "$tmpd/manifest.json" "$manifest_url"; \
      pkg_url="$(awk -F'"' '/"url"[ \t]*:/ {print $4; exit}' "$tmpd/manifest.json")"; \
      expected_sha512="$(awk -F'"' '/"sha512"[ \t]*:/ {print tolower($4); exit}' "$tmpd/manifest.json")"; \
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
    if [ "$install_zed" = "1" ]; then \
      zed_ver="${ZED_ACP_VERSION}"; \
      expected="$(echo "${ZED_ACP_SHA256}" | tr '[:upper:]' '[:lower:]')"; \
      archive_url=""; \
      if [ -z "$expected" ]; then \
        reg_url="https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"; \
        if curl -fsSL --retry 3 -o "$tmpd/registry.json" "$reg_url" 2>/dev/null && [ -s "$tmpd/registry.json" ]; then \
          reg_target="linux-${zed_reg_arch}"; \
          reg_ver="$(awk '/"id"[ \t]*:[ \t]*"antigravity-acp"/ { in_a=1; next } in_a && /"id"[ \t]*:/ { in_a=0 } in_a && !v && /"version"[ \t]*:/ { l=$0; sub(/^.*"version"[ \t]*:[ \t]*"/, "", l); sub(/".*$/, "", l); v=l } END { print v }' "$tmpd/registry.json")"; \
          if [ -z "$zed_ver" ] || [ "$zed_ver" = "latest" ] || [ "$zed_ver" = "$reg_ver" ]; then \
            reg_archive="$(awk -v target="$reg_target" '/"id"[ \t]*:[ \t]*"antigravity-acp"/ { in_a=1; next } in_a && /"id"[ \t]*:/ { in_a=0 } in_a && $0 ~ ("\"" target "\"[ \t]*:") { in_t=1; next } in_t && /}/ { in_t=0 } in_t && /"archive"[ \t]*:/ { l=$0; sub(/^.*"archive"[ \t]*:[ \t]*"/, "", l); sub(/".*$/, "", l); print l; exit }' "$tmpd/registry.json")"; \
            reg_sha="$(awk -v target="$reg_target" '/"id"[ \t]*:[ \t]*"antigravity-acp"/ { in_a=1; next } in_a && /"id"[ \t]*:/ { in_a=0 } in_a && $0 ~ ("\"" target "\"[ \t]*:") { in_t=1; next } in_t && /}/ { in_t=0 } in_t && /"sha256"[ \t]*:/ { l=$0; sub(/^.*"sha256"[ \t]*:[ \t]*"/, "", l); sub(/".*$/, "", l); print tolower(l); exit }' "$tmpd/registry.json")"; \
            if [ -n "$reg_archive" ] && [ -n "$reg_sha" ]; then \
              zed_ver="$reg_ver"; \
              archive_url="$reg_archive"; \
              expected="$reg_sha"; \
            fi; \
          fi; \
        fi; \
      fi; \
      [ -n "$zed_ver" ] || zed_ver="1.4.5"; \
      [ -n "$archive_url" ] || archive_url="https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-${zed_ver}-linux-${zed_arch}.zip"; \
      if [ -z "$expected" ] && [ "${AG_INSECURE_SKIP_VERIFY}" != "1" ]; then \
        echo "ERROR: could not resolve SHA-256 from ACP registry and build arg ZED_ACP_SHA256 is empty (agy-acp-server-${zed_ver}-linux-${zed_arch}.zip)." >&2; \
        echo "       Pass ZED_ACP_SHA256 from a trusted source, or set AG_INSECURE_SKIP_VERIFY=1 to build unverified." >&2; \
        exit 1; \
      fi; \
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

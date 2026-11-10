# Stage 1: Build ReUI Bilingual Frontend SPA
FROM node:22-bookworm-slim AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
RUN npm run build

# Stage 2: Build Go Gateway & Probe Binaries
FROM golang:bookworm AS go-builder
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/antigravity-gateway ./cmd/gateway && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/antigravity-probe ./cmd/probe

# Stage 3: Runtime Image (auto-installs Antigravity CLI `agy` or Zed `agy_acp_server.par` for linux/amd64 & linux/arm64)
FROM debian:bookworm-slim
ARG AG_ACP_BACKEND=cli
ENV AG_ACP_BACKEND=${AG_ACP_BACKEND} \
    AG_DATA_DIR=/var/lib/antigravity-gateway \
    AG_WORKSPACE_DIR=/tmp/antigravity-gateway-workspace \
    AGY_ACP_FORCE_FILE_STORAGE=1

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    iproute2 \
    unzip \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /opt/antigravity-acp /var/lib/antigravity-gateway /etc/antigravity-gateway

RUN set -eux; \
    arch_raw="$(uname -m)"; \
    case "$arch_raw" in \
      x86_64|amd64) cli_arch="amd64"; zed_arch="x86_64"; fallback_arch="x86_64" ;; \
      aarch64|arm64) cli_arch="arm64"; zed_arch="aarch64"; fallback_arch="arm64" ;; \
      *) echo "Unsupported architecture: $arch_raw" >&2; exit 1 ;; \
    esac; \
    backend_norm="$(echo "${AG_ACP_BACKEND}" | tr '[:upper:]' '[:lower:]')"; \
    tmpd="$(mktemp -d)"; \
    if [ "$backend_norm" = "zen" ] || [ "$backend_norm" = "zed" ]; then \
      archive_url="https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-1.4.5-linux-${fallback_arch}.zip"; \
      curl -fsSL --retry 3 -o "$tmpd/acp.zip" "$archive_url"; \
      unzip -q -o "$tmpd/acp.zip" -d /usr/local/bin; \
      chmod 0755 /usr/local/bin/agy_acp_server.par /usr/local/bin/localharness_external 2>/dev/null || true; \
    else \
      manifest_url="https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_${cli_arch}.json"; \
      manifest_json="$(curl -fsSL --retry 3 "$manifest_url")"; \
      pkg_url="$(printf '%s\n' "$manifest_json" | awk -F'"' '/"url"[[:space:]]*:/ {print $4; exit}')"; \
      expected_sha512="$(printf '%s\n' "$manifest_json" | awk -F'"' '/"sha512"[[:space:]]*:/ {print $4; exit}')"; \
      curl -fsSL --retry 3 -o "$tmpd/agy.tar.gz" "$pkg_url"; \
      if [ -n "$expected_sha512" ]; then \
        echo "${expected_sha512}  $tmpd/agy.tar.gz" | sha512sum -c -; \
      fi; \
      tar -xzf "$tmpd/agy.tar.gz" -C "$tmpd" antigravity; \
      install -m 0755 "$tmpd/antigravity" /usr/local/bin/agy; \
    fi; \
    rm -rf "$tmpd"

WORKDIR /app
COPY --from=go-builder /out/antigravity-gateway /usr/local/bin/antigravity-gateway
COPY --from=go-builder /out/antigravity-probe /usr/local/bin/antigravity-probe
COPY config/config.yaml /etc/antigravity-gateway/config.yaml

VOLUME ["/var/lib/antigravity-gateway", "/root/.gemini", "/opt/antigravity-acp"]
EXPOSE 8080
ENTRYPOINT ["antigravity-gateway", "-config", "/etc/antigravity-gateway/config.yaml"]

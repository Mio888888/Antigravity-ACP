#!/usr/bin/env bash
# ==============================================================================
# Antigravity ACP Gateway — One-Click Update Script (macOS / Linux / Docker)
#
# Pulls the latest repository code (git pull), updates the ACP backend package
# (Antigravity CLI `agy` and/or Zed standalone `agy_acp_server.par`), rebuilds
# the frontend + Go binaries (or Docker image), and restarts the active service.
#
# Usage:
#   ./deploy/update.sh                         # Auto-detect mode & backend, update all
#   ./deploy/update.sh --backend cli           # Update code + Antigravity CLI (agy)
#   ./deploy/update.sh --backend zed           # Update code + Zed ACP (agy_acp_server.par)
#   ./deploy/update.sh --backend both          # Update code + both CLI and Zed ACP packages
#   ./deploy/update.sh --action update-acp     # Update ACP backend package(s) only
#   ./deploy/update.sh --no-git                # Update ACP & rebuild without git pull
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

HAS_ACTION="0"
for arg in "$@"; do
  if [[ "$arg" == "--action" || "$arg" == "--action="* ]]; then
    HAS_ACTION="1"
    break
  fi
done

if [[ "$HAS_ACTION" == "1" ]]; then
  exec "$SCRIPT_DIR/deploy.sh" "$@"
else
  exec "$SCRIPT_DIR/deploy.sh" --action update "$@"
fi

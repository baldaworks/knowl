#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

echo "=== Knowl Showcase: Source -> Wiki ==="
echo "Building knowl CLI..."
(cd "${REPO_ROOT}" && go build -o "${SCRIPT_DIR}/knowl" ./cmd/knowl)

echo "Running one-shot knowledge processing cycle..."
echo "Configuration: ${SCRIPT_DIR}/.config/knowl/config.yaml"
echo "Input sources: ${SCRIPT_DIR}/sources"
echo "Output wiki:   ${SCRIPT_DIR}/wiki"
echo ""

(cd "${SCRIPT_DIR}" && ./knowl run)

echo ""
echo "=== Knowledge Processing Completed Successfully ==="
echo "Inspect generated wiki files in: ${SCRIPT_DIR}/wiki"

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
KNOWL_BIN="${SCRIPT_DIR}/knowledge/.knowl/bin/knowl"

echo "=== Knowl Showcase: Source -> Wiki ==="
echo "Building knowl CLI..."
mkdir -p "${SCRIPT_DIR}/knowledge/.knowl/bin"
(cd "${REPO_ROOT}" && go build -o "${KNOWL_BIN}" ./cmd/knowl)

echo "Running one-shot knowledge processing cycle..."
echo "Configuration: ${SCRIPT_DIR}/.config/knowl/config.yaml"
echo "Input sources: ${SCRIPT_DIR}/sources"
echo "Schema policy: ${SCRIPT_DIR}/knowledge/schema.md"
echo "Output wiki:   ${SCRIPT_DIR}/knowledge/wiki"
echo ""

(cd "${SCRIPT_DIR}" && "${KNOWL_BIN}" run)

echo ""
echo "=== Knowledge Processing Completed Successfully ==="
echo "Inspect generated wiki files in: ${SCRIPT_DIR}/knowledge/wiki"

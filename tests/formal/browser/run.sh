#!/usr/bin/env bash
set -Eeuo pipefail

# Playwright browser tier -- TESTPLAN.md's 7-case contract. Deliberately
# separate from ../run.sh's Docker gate (see README.md's "Browser tier"
# note): call this script directly, it is never invoked by the parent suite.

BROWSER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$BROWSER_DIR"

export CHRONICLE_IMAGE="${CHRONICLE_IMAGE:-chronicler:latest}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-chronicle-formal-browser}"
ARTIFACTS="$BROWSER_DIR/artifacts"

if ! docker image inspect "$CHRONICLE_IMAGE" >/dev/null 2>&1; then
  docker pull "$CHRONICLE_IMAGE"
fi

find "$ARTIFACTS" -depth -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
mkdir -p "$ARTIFACTS"
chmod 0777 "$ARTIFACTS"

cleanup() {
  local status=$?
  docker compose logs --no-color --timestamps > "$ARTIFACTS/compose.log" 2>&1 || true
  docker compose ps --all > "$ARTIFACTS/compose-ps.txt" 2>&1 || true
  docker compose down --volumes --remove-orphans --timeout 10 || true
  exit "$status"
}
trap cleanup EXIT INT TERM

docker compose down --volumes --remove-orphans --timeout 5 2>/dev/null || true
docker compose up --detach --wait chronicle

docker compose run --rm playwright sh -c "npm ci && npx playwright test"

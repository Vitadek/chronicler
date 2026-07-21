#!/usr/bin/env bash
set -Eeuo pipefail

FORMAL_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$FORMAL_DIR/../.." && pwd)
COMPOSE_FILE="$FORMAL_DIR/compose.yml"
ARTIFACTS="$FORMAL_DIR/artifacts"

export CHRONICLE_IMAGE=${CHRONICLE_IMAGE:-chronicler:latest}
# How to invoke the maintenance CLI inside the chronicle container. The Go
# build exposes it as subcommands of the server binary; the retired Node build
# needed `node dist/cli.cjs`. Overridable so this suite can still be pointed at
# a Node image for comparison:
#   CHRONICLE_CLI="node dist/cli.cjs" ./run.sh
export CHRONICLE_CLI=${CHRONICLE_CLI:-/app/chronicle-server}
export COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-chronicle-formal}
export COMPOSE_ANSI=${COMPOSE_ANSI:-never}
export BUILDKIT_PROGRESS=${BUILDKIT_PROGRESS:-plain}

compose() {
  docker compose --file "$COMPOSE_FILE" "$@"
}

wait_for_chronicle_health() {
  local container status
  container=$(compose ps --quiet chronicle)
  if [ -z "$container" ]; then
    echo "Chronicler container is not running" >&2
    return 1
  fi
  for _ in $(seq 1 90); do
    status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container")
    if [ "$status" = healthy ]; then
      return 0
    fi
    if [ "$status" = exited ] || [ "$status" = dead ]; then
      compose logs --no-color chronicle >&2
      return 1
    fi
    sleep 1
  done
  echo "Chronicler did not become healthy" >&2
  compose logs --no-color chronicle >&2
  return 1
}

capture_and_clean() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  compose ps --all >"$ARTIFACTS/compose-ps.txt" 2>&1
  compose logs --no-color --timestamps >"$ARTIFACTS/compose.log" 2>&1
  docker image inspect "$CHRONICLE_IMAGE" >"$ARTIFACTS/chronicle-image-inspect.json" 2>&1
  compose config >"$ARTIFACTS/compose-resolved.yml" 2>&1
  compose down --volumes --remove-orphans --timeout 10 >/dev/null 2>&1
  exit "$status"
}

trap capture_and_clean EXIT INT TERM

mkdir -p "$ARTIFACTS"
find "$ARTIFACTS" -depth -mindepth 1 ! -name .gitkeep -delete
chmod 0777 "$ARTIFACTS"

cd "$ROOT_DIR"
compose down --volumes --remove-orphans --timeout 5 >/dev/null 2>&1 || true

if ! docker image inspect "$CHRONICLE_IMAGE" >/dev/null 2>&1; then
  docker pull "$CHRONICLE_IMAGE"
fi

docker image inspect --format 'Testing Chronicler image {{.Id}} ({{join .RepoTags ", "}})' "$CHRONICLE_IMAGE" \
  | tee "$ARTIFACTS/image-under-test.txt"

REPORT_DIR="$ARTIFACTS" node "$FORMAL_DIR/orchestrator/preflight.mjs" \
  | tee "$ARTIFACTS/preflight.tap"

compose build runner
compose up --detach --wait chronicle

compose run --rm --no-deps runner node specs/run.mjs foundation \
  | tee "$ARTIFACTS/foundation.tap"

compose exec -T chronicle $CHRONICLE_CLI verify \
  | tee "$ARTIFACTS/verify-before-outage.json"

compose run --rm --no-deps runner node specs/run.mjs outage \
  | tee "$ARTIFACTS/outage.tap"

set +e
compose exec -T chronicle $CHRONICLE_CLI status >"$ARTIFACTS/status-during-outage.json" 2>&1
degraded_status=$?
set -e
if [ "$degraded_status" -ne 2 ]; then
  echo "Expected degraded storage CLI status 2, got $degraded_status" >&2
  cat "$ARTIFACTS/status-during-outage.json" >&2
  exit 1
fi

compose run --rm --no-deps runner node orchestrator/toxiproxy.mjs enable \
  | tee "$ARTIFACTS/toxiproxy-recovery.txt"

compose exec -T chronicle $CHRONICLE_CLI retry \
  | tee "$ARTIFACTS/retry.json"

compose exec -T chronicle $CHRONICLE_CLI verify \
  | tee "$ARTIFACTS/verify-after-recovery.json"

compose run --rm --no-deps runner node specs/run.mjs recovery \
  | tee "$ARTIFACTS/recovery.tap"

compose exec -T chronicle $CHRONICLE_CLI backup --output /data/formal-backup.db \
  | tee "$ARTIFACTS/backup.json"
compose exec -T chronicle test -s /data/formal-backup.db
compose cp chronicle:/data/formal-backup.db "$ARTIFACTS/formal-backup.db" >/dev/null
sha256sum "$ARTIFACTS/formal-backup.db" | tee "$ARTIFACTS/formal-backup.sha256"

compose restart chronicle
wait_for_chronicle_health

compose run --rm --no-deps runner node specs/run.mjs durability \
  | tee "$ARTIFACTS/durability.tap"

compose exec -T chronicle $CHRONICLE_CLI verify \
  | tee "$ARTIFACTS/verify-after-restart.json"

compose run --rm --no-deps runner node specs/run.mjs pre_restore \
  | tee "$ARTIFACTS/pre-restore.tap"

alice_id=$(node -e \
  'const fs=require("fs");process.stdout.write(JSON.parse(fs.readFileSync(process.argv[1],"utf8")).aliceId)' \
  "$ARTIFACTS/restore-baseline.json")

# The recovery snapshot is already sealed in MinIO and every later local write
# is a dead letter. Stop the sole SQLite writer before reconnecting the remote.
compose stop --timeout 15 chronicle
if [ -n "$(compose ps --status running --quiet chronicle)" ]; then
  echo "Chronicler must be stopped before restore apply" >&2
  exit 1
fi

compose run --rm --no-deps runner node orchestrator/toxiproxy.mjs enable \
  | tee "$ARTIFACTS/toxiproxy-restore-enable.txt"

compose run --rm --no-deps chronicle $CHRONICLE_CLI restore \
  | tee "$ARTIFACTS/restore-dry-run-all.json"
compose run --rm --no-deps chronicle $CHRONICLE_CLI restore --user "$alice_id" \
  | tee "$ARTIFACTS/restore-dry-run-user.json"
compose run --rm --no-deps runner node orchestrator/assert-restore-artifacts.mjs dry-runs \
  | tee "$ARTIFACTS/restore-dry-run-assertion.txt"

set +e
compose run --rm --no-deps chronicle $CHRONICLE_CLI restore --apply \
  >"$ARTIFACTS/restore-apply-without-force.txt" 2>&1
refusal_status=$?
set -e
if [ "$refusal_status" -ne 1 ] || \
   ! grep -F -- '--apply --force' "$ARTIFACTS/restore-apply-without-force.txt" >/dev/null; then
  echo "Restore --apply did not refuse existing records as expected" >&2
  cat "$ARTIFACTS/restore-apply-without-force.txt" >&2
  exit 1
fi

compose run --rm --no-deps chronicle $CHRONICLE_CLI restore --apply --force \
  | tee "$ARTIFACTS/restore-apply-force.json"
compose run --rm --no-deps runner node orchestrator/assert-restore-artifacts.mjs apply \
  | tee "$ARTIFACTS/restore-apply-assertion.txt"

backup_path=$(node -e \
  'const fs=require("fs");const p=JSON.parse(fs.readFileSync(process.argv[1],"utf8")).backupPath;if(!/^\/data\/chronicle-before-restore-[A-Za-z0-9-]+\.db$/.test(p))process.exit(1);process.stdout.write(p)' \
  "$ARTIFACTS/restore-apply-force.json")

# Copy the automatic pre-restore backup out first, then inspect it from the
# runner. This used to run an inline `node -` heredoc with better-sqlite3
# INSIDE the chronicle container; the Go image has neither Node nor that
# module. orchestrator/inspect-backup.mjs runs the same SQL and emits the same
# JSON, so the assertions below are unchanged.
compose cp "chronicle:$backup_path" "$ARTIFACTS/automatic-pre-restore.db" >/dev/null
test -s "$ARTIFACTS/automatic-pre-restore.db"
sha256sum "$ARTIFACTS/automatic-pre-restore.db" \
  | tee "$ARTIFACTS/automatic-pre-restore.sha256"

compose run --rm --no-deps --no-TTY \
  -e BACKUP_PATH=/artifacts/automatic-pre-restore.db -e ALICE_ID="$alice_id" \
  runner node orchestrator/inspect-backup.mjs \
  >"$ARTIFACTS/automatic-backup-verification.json"

compose run --rm --no-deps runner node orchestrator/assert-restore-artifacts.mjs backup \
  | tee "$ARTIFACTS/automatic-backup-assertion.txt"

if [ -n "$(compose ps --status running --quiet chronicle)" ]; then
  echo "Canonical Chronicler unexpectedly started during offline restore" >&2
  exit 1
fi
compose start chronicle
wait_for_chronicle_health

compose run --rm --no-deps runner node specs/run.mjs post_restore \
  | tee "$ARTIFACTS/post-restore.tap"

compose exec -T chronicle $CHRONICLE_CLI verify \
  | tee "$ARTIFACTS/verify-after-offline-restore.json"
compose run --rm --no-deps runner node orchestrator/assert-restore-artifacts.mjs verify \
  | tee "$ARTIFACTS/verify-after-offline-restore-assertion.txt"

echo "Formal Chronicler suite passed. Artifacts: $ARTIFACTS"

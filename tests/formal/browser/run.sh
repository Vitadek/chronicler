#!/usr/bin/env bash
set -eu

cat >&2 <<'EOF'
The Chronicler Playwright formal tier is scaffolded but not enabled yet.
Pin a Playwright image, add accessibility-stable editor selectors, and implement
the cases in tests/formal/browser/TESTPLAN.md before making this a release gate.
The executable API/S3/recovery gate is: ./tests/formal/run.sh
EOF
exit 2

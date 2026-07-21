# Browser formal tier test plan

The eventual `./tests/formal/browser/run.sh` tier will reuse the formal Compose
stack and add a digest-pinned Playwright container. Its first contract cases
are: stale library request suppression, coalesced autosave under latency,
offline draft journal/reload recovery, explicit chapter deletion, a visible
draft-storage failure state, checker chunk lazy loading, and two browser-context
collaboration convergence. Implementations must use role/label selectors and
save traces, screenshots, console output, and videos under `artifacts/browser`.

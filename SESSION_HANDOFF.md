# Chronicle Go session handoff

Last validated: 2026-07-20.

## Current state

- `main` is the Go migration target; the Node repository is not modified by
  this session.
- The pinned Docker build succeeds: `docker build -t chronicle-go:local .`.
- Smoke container passed `/healthz` and `/readyz` on host port 13000.
- Uncommitted changes implement Hocuspocus first-frame document negotiation,
  collaboration auth acknowledgements, strict room validation, and shared
  forward-proxy authentication in `pkg/collab/collab.go` and `pkg/auth/auth.go`.

## Next commands

```sh
docker build -t chronicle-go:local .
cd tests/formal && ./run.sh
```

The formal suite previously stopped at collaboration convergence (47/48
foundation specs). Verify that fix first, then run outage, recovery,
durability, and restore phases. Next validate S3 against the isolated MinIO
compose deployment at `/opt/chronicle/docker/agent-2` using the credentials in
the workspace `CREDS.md`.

## Commit protocol

Commit coherent changes locally, then push to `https://forgejo.lan/protoman/chronicle-go.git`.
Do not overwrite remote history. Update
`/opt/chronicle/workspace/codex_optimizations.md` as measurements and
performance findings are made.

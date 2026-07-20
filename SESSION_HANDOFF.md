# Chronicle Go session handoff

Last validated: 2026-07-20 at commit `28435b8` (release image built from this
commit; deployment documentation is the next commit).

## Current state

- `main` is the Go migration target; the Node repository is not modified by
  this session.
- The full 97-case formal suite passes, including collaboration, real MinIO,
  outage/retry, backup, restart durability, offline restore, and 19/19 deep
  replica verification.
- The tested image is published as `forgejo.lan/protoman/chronicle-go:latest`
  and `:release-20260720`, digest `sha256:9f80ee946a37badb8c97db3e15395af10aa70159b1c72b7ca16ff160c2da5bb5`.
- `/opt/chronicle/docker/agent-2` is running that registry image on
  `127.0.0.1:13000` against persistent MinIO. Its data survived restart and
  image recreation; `verify` reports 2/2 exact matches.

## Next commands

```sh
docker build -t chronicle-go:release .
CHRONICLE_IMAGE=chronicle-go:release ./tests/formal/run.sh
```

For the persistent validation deployment, use `docker compose` from
`/opt/chronicle/docker/agent-2`; credentials are in the workspace `CREDS.md`.
The Node deployment on port 3000 has deliberately not been replaced.

## Commit protocol

Commit coherent changes locally, then push to `https://forgejo.lan/protoman/chronicle-go.git`.
Do not overwrite remote history. Update
`/opt/chronicle/workspace/codex_optimizations.md` as measurements and
performance findings are made.

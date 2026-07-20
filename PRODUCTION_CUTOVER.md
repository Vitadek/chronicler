# Node-to-Go production cutover

This runbook covers the trusted-LAN deployment at
`/opt/chronicle/docker/agent-1`. It preserves the existing
`chronicle_chronicle-data` volume and public port 3000.

## Validated artifacts

- Go release: `forgejo.lan/protoman/chronicle-go:release-20260720`
- Go digest: `sha256:9f80ee946a37badb8c97db3e15395af10aa70159b1c72b7ca16ff160c2da5bb5`
- Node rollback: `forgejo.lan/protoman/chronicle:node-pre-go-cutover-20260720`
- Node digest: `sha256:e79afb4a3fdaad4918007bbfaec3755ef12e8a0752ac5221ad8426aaac4d8149`

Before the live cutover, Chronicle's Node CLI created a transaction-consistent
SQLite backup. The exact Go release booted successfully against a cloned copy,
completed its migrations, passed readiness, and returned the same active
manuscript count. The Go release also passed the repository's complete 97-case
formal Docker suite and persistent MinIO validation.

## Cut over

The production Compose file should retain the `chronicle-data` volume, port
3000, and the existing authentication policy while replacing the service image
with the validated Go tag. A Node-created volume is owned by UID:GID 1000:1000,
while the non-root Go image runs as UID:GID 100:101. Stop the service and
change that existing volume's ownership once before starting Go:

```sh
cd /opt/chronicle/docker/agent-1
docker compose pull chronicle
docker compose stop chronicle
docker run --rm -v chronicle_chronicle-data:/data alpine:3.22 \
  chown -R 100:101 /data
docker compose up -d --wait --remove-orphans
curl --fail --silent --show-error http://127.0.0.1:3000/readyz
docker compose exec -T chronicle /app/chronicle-server status
```

The Go binary has an embedded grammar engine, so the Java LanguageTool sidecar
is not part of the new topology.

## Roll back

Do not delete the named volume. Replace the Chronicle service image with the
Node rollback tag and restore the pre-cutover Compose settings from the backup
directory named by `/opt/chronicle/workspace/.last-cutover-backup`. Because the
Node image runs as UID:GID 1000:1000, reverse the ownership change while the
service is stopped, then start it:

```sh
cd /opt/chronicle/docker/agent-1
docker compose stop chronicle
docker run --rm -v chronicle_chronicle-data:/data alpine:3.22 \
  chown -R 1000:1000 /data
docker compose up -d --wait
curl --fail --silent --show-error http://127.0.0.1:3000/readyz
```

If a database rollback is required, stop Chronicle first and restore
`chronicle.db` from the recorded pre-cutover directory. Never copy a database
over the live file while SQLite is running.

## 2026-07-20 result

The live cutover completed successfully after the one-time ownership change.
The production container is healthy on port 3000 and runs the validated Go
digest. One active manuscript with five chapters remained readable, its full
API response was byte-identical across a graceful restart, the installed
proofreader remained visible, and the embedded grammar endpoint returned both
misspelling and style results. A post-cutover hot backup passes SQLite
`quick_check`. Warm readiness and manuscript-list requests measured about
0.6 ms locally; runtime memory measured about 115 MiB.

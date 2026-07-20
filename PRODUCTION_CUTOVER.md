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
with the validated Go tag. Then run:

```sh
cd /opt/chronicle/docker/agent-1
docker compose pull chronicle
docker compose up -d --wait --remove-orphans
curl --fail --silent --show-error http://127.0.0.1:3000/readyz
docker compose exec -T chronicle /app/chronicle-server status
```

The Go binary has an embedded grammar engine, so the Java LanguageTool sidecar
is not part of the new topology.

## Roll back

Do not delete the named volume. Replace the Chronicle service image with the
Node rollback tag, restore the pre-cutover Compose settings from the backup
directory named by `/opt/chronicle/workspace/.last-cutover-backup`, and run:

```sh
cd /opt/chronicle/docker/agent-1
docker compose up -d --wait
curl --fail --silent --show-error http://127.0.0.1:3000/readyz
```

If a database rollback is required, stop Chronicle first and restore
`chronicle.db` from the recorded pre-cutover directory. Never copy a database
over the live file while SQLite is running.

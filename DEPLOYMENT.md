# Chronicle Go deployment

The release image is `forgejo.lan/protoman/chronicle-go:latest`. The immutable
validated tag from 2026-07-20 is `release-20260720`, digest
`sha256:9f80ee946a37badb8c97db3e15395af10aa70159b1c72b7ca16ff160c2da5bb5`.

## Quick start

```sh
cd deploy
cp .env.example .env
sed -i "s/replace-with-a-random-token/$(openssl rand -hex 32)/" .env
docker compose up -d --wait
```

Open `http://localhost:3000`. The `/data` volume contains SQLite and must be
backed up even when S3 replication is enabled: SQLite remains authoritative and
S3 is the asynchronous recovery replica.

For S3-compatible storage, set `STORAGE_REPLICA=s3`, the bucket, region,
endpoint, credentials, and path-style options in `.env`. Plain HTTP endpoints
are rejected unless `S3_ALLOW_INSECURE_HTTP=true`; use that override only on a
trusted local network.

## Maintenance

```sh
docker compose exec chronicle /app/chronicle-server status
docker compose exec chronicle /app/chronicle-server verify
docker compose exec chronicle /app/chronicle-server retry
docker compose exec chronicle /app/chronicle-server backup
```

Restore is offline. Stop the service and review a dry run before applying:

```sh
docker compose stop chronicle
docker compose run --rm --no-deps chronicle /app/chronicle-server restore
docker compose run --rm --no-deps chronicle /app/chronicle-server restore --apply --force
docker compose start chronicle
```

## Validated release characteristics

- OCI image: 16,542,363 bytes; static Go binary: 29,745,336 bytes.
- Direct container restart to ready: about 693 ms on the validation host.
- Warm local readiness and manuscript-list requests: about 0.7 ms average.
- Complete 97-case destructive formal gate passed, including collaboration,
  S3 outage/retry, backup, restart durability, offline restore, and deep remote
  verification.
- Persistent external MinIO validation passed before and after both restart
  and image recreation, with 2/2 objects matching by content, checksum, and
  generation.

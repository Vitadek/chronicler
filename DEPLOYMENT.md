# Chronicler deployment

The public release image is `ghcr.io/vitadek/chronicler:latest`. The immutable
validated application tag from 2026-07-20 is `255af2d`, digest
`sha256:dcde4edd74e82a22796ccc50e11d341768b651727cd06401cf77d9252cb16e93`.
Pin the digest or immutable tag for production instead of allowing `latest` to
change during an unattended restart.

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

See [`deploy/ENVIRONMENT.md`](deploy/ENVIRONMENT.md) for the complete variable
reference, including forward-auth, OIDC, and Nextcloud replica configuration
— `deploy/compose.yml` and `deploy/.env.example` wire through every variable
it documents, so switching modes is a pure `.env` edit.

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

- OCI image: 16,620,070 bytes; production runtime memory was about 115 MiB on
  the validation host.
- Complete 98-case destructive formal gate passed, including collaboration,
  S3 outage/retry, backup, restart durability, offline restore, and deep remote
  verification.
- Production MinIO validation passed before and after restart, with 32/32
  objects matching by content, checksum, and generation and no pending or
  dead-letter jobs.

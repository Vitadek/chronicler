# Environment variables

Complete reference for every variable `pkg/config/config.go` reads. Matches
`deploy/.env.example` and `deploy/compose.yml` section-for-section.

`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and `AWS_REGION` are **not**
read by `pkg/config` — the S3 replica (`pkg/replica/s3.go`) constructs its
client with the AWS SDK v2's own `LoadDefaultConfig`, which reads those
(and the rest of the SDK's standard credential chain: shared config files,
IAM roles, etc.) itself. They're listed here anyway because every deploy
template in this repo sets them explicitly.

## Core server

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `PORT` | `3000` | — | no | Listen port. |
| `HOST` | `0.0.0.0` | — | no | Bind address. |
| `DATA_DIR` | `<cwd>/data` | — | no | SQLite database, plugin, and data storage path. |
| `NODE_ENV` | unset | — | no | Set to `production` to enable stricter boot validation (e.g. the `AUTH_MODE=none` non-loopback guard below). |
| `LOCAL_ADMIN` | `false` | — | no | Grants the local-admin capability (whole-database backup/restore). Only enable on a trusted single-operator deployment. |

## Auth — `AUTH_MODE`

`AUTH_MODE` selects one of four modes; unset or unrecognized values fall
back to `none`.

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `AUTH_MODE` | `none` | — | no | `none` \| `token` \| `forward` \| `oidc`. |
| `ALLOW_INSECURE_NO_AUTH` | `false` | — | no | Required to bind `AUTH_MODE=none` to a non-loopback host in production (`NODE_ENV=production`). Only for an explicitly trusted private network. |

### `AUTH_MODE=token`

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `AUTH_TOKEN` | `""` | `AUTH_MODE=token` | **yes** | Shared bearer token. Generate with `openssl rand -hex 32`. |

### `AUTH_MODE=forward`

For a reverse proxy that authenticates the request and forwards identity via headers.

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `AUTH_FORWARD_HEADER_USER` | `Remote-User` | — | no | Header carrying the username. |
| `AUTH_FORWARD_HEADER_EMAIL` | `Remote-Email` | — | no | Header carrying the email. |
| `AUTH_FORWARD_HEADER_NAME` | `Remote-Name` | — | no | Header carrying the display name. |
| `AUTH_FORWARD_HEADER_GROUPS` | `Remote-Groups` | — | no | Header carrying group membership. |
| `AUTH_FORWARD_TRUSTED_PROXIES` | `loopback,linklocal,uniquelocal` | `AUTH_MODE=forward` (must be non-empty; the default already satisfies this) | no | Comma-separated proxy allowlist (CIDR or the `loopback`/`linklocal`/`uniquelocal` keywords) — only these peers' forwarded headers are trusted. |
| `AUTH_FORWARD_SECRET_HEADER` | `""` | — | no | Name of a shared-secret header the proxy sends, if used. |
| `AUTH_FORWARD_SECRET` | `""` | — | **yes** | Value the proxy must send in `AUTH_FORWARD_SECRET_HEADER`. |
| `AUTH_FORWARD_ADMIN_GROUP` | `""` | — | no | Group name (from the groups header) granted admin. |

### `AUTH_MODE=oidc`

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `AUTH_OIDC_ISSUER_URL` | `""` | `AUTH_MODE=oidc` | no | OIDC provider issuer URL. |
| `AUTH_OIDC_CLIENT_ID` | `""` | `AUTH_MODE=oidc` | no | OAuth client ID. |
| `AUTH_OIDC_CLIENT_SECRET` | `""` | — | **yes** | OAuth client secret (required by most providers even though the server doesn't hard-require it). |
| `AUTH_OIDC_REDIRECT_URI` | `""` | `AUTH_MODE=oidc` | no | Callback URL registered with the provider. |
| `AUTH_OIDC_SCOPES` | `openid profile email` | — | no | Space-separated OIDC scopes. |
| `AUTH_OIDC_POST_LOGOUT_REDIRECT_URI` | `""` | — | no | Where to send the browser after logout. |
| `AUTH_OIDC_TOKEN_AUTH_METHOD` | `auto` | — | no | Token endpoint auth method. |

## Storage replica — `STORAGE_REPLICA`

SQLite is always authoritative. A replica is an asynchronous copy for
disaster recovery, not the primary store.

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `STORAGE_REPLICA` | `none` | — | no | `none` \| `nextcloud` \| `s3`. |
| `STORAGE_RETRY_INTERVAL_MS` | `30000` | — | no | Backoff between replica retry attempts. |
| `STORAGE_MAX_ATTEMPTS` | `10` | — | no | Max retry attempts before an object is dead-lettered. |
| `STORAGE_PROVIDER` | — | — | no | **Legacy alias**, still read if `STORAGE_REPLICA` is unset (`sqlite`→`none`, `hybrid`→`nextcloud`). Prefer `STORAGE_REPLICA` directly; don't set both. |

### `STORAGE_REPLICA=nextcloud`

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `NEXTCLOUD_URL` | `""` | `STORAGE_REPLICA=nextcloud` | no | Nextcloud instance URL. Must be HTTPS unless `NEXTCLOUD_ALLOW_INSECURE_HTTP=true`. Setting this alone also flips `Nextcloud.Enabled` on regardless of `STORAGE_REPLICA`. |
| `NEXTCLOUD_ALLOW_INSECURE_HTTP` | `false` | — | no | Allow a plain-HTTP `NEXTCLOUD_URL`. Trusted LAN only. |
| `NC_USER` | `""` | `STORAGE_REPLICA=nextcloud` | no | Nextcloud username. |
| `NC_PASS` | `""` | `STORAGE_REPLICA=nextcloud` | **yes** | Nextcloud **App Password**, not the account password. |
| `NC_DIR` | `Chronicle_Storage` | — | no | Remote storage directory. |
| `NEXTCLOUD_CLIENT_ID` | `""` | — | no | OAuth client ID, if using OAuth instead of an App Password. |
| `NEXTCLOUD_CLIENT_SECRET` | `""` | — | **yes** | OAuth client secret. |
| `NEXTCLOUD_REDIRECT_URI` | `""` | — | no | OAuth redirect URI. |
| `NEXTCLOUD_MIRROR` | — | — | — | **Retired.** Boot fails with an explicit error if set. Migrate to `STORAGE_REPLICA=nextcloud`. |
| `NEXTCLOUD_MIRROR_ROOT` | — | — | — | **Retired.** Same as above. |

## Grammar providers

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `LANGUAGETOOL_URL` | `""` | — | no | Legacy single-provider LanguageTool base URL. Prefer `GRAMMAR_PROVIDERS_FILE` for new deployments. |
| `LANGUAGETOOL_LANG` | `en-US` | — | no | Language used by the legacy LanguageTool connection. |
| `GRAMMAR_PROVIDERS_FILE` | `""` | — | no | Versioned YAML registry for LanguageTool and Chronicle-v1 adapters. |
| `GRAMMAR_BACKGROUND_SWEEP` | `false` | — | no | Warm native and explicitly permitted local-provider caches while Chronicle is idle. Cloud providers remain off unless their registry entry opts in. |
| `GRAMMAR_SWEEP_IDLE_THRESHOLD_MS` | `180000` | — | no | Required period without real traffic before a cache sweep starts. |

### `STORAGE_REPLICA=s3`

| Var | Default | Required when | Secret | Description |
|---|---|---|---|---|
| `S3_BUCKET` | `""` | `STORAGE_REPLICA=s3` | no | Target bucket name. |
| `S3_REGION` | `us-east-1` | — | no | AWS region. |
| `S3_ENDPOINT` | `""` | — | no | Custom/S3-compatible endpoint (e.g. MinIO). Must be HTTPS unless `S3_ALLOW_INSECURE_HTTP=true`. |
| `S3_PREFIX` | `chronicle` | — | no | Key prefix under the bucket. |
| `S3_FORCE_PATH_STYLE` | `false` | — | no | Path-style addressing — needed for MinIO and most S3-compatible endpoints. |
| `S3_ALLOW_INSECURE_HTTP` | `false` | — | no | Allow a plain-HTTP `S3_ENDPOINT`. Trusted LAN only. |
| `S3_SERVER_SIDE_ENCRYPTION` | `""` | — | no | `""` \| `AES256` \| `aws:kms`. |
| `S3_KMS_KEY_ID` | `""` | `S3_SERVER_SIDE_ENCRYPTION=aws:kms` | no | KMS key identifying which key encrypts objects. |
| `AWS_ACCESS_KEY_ID` | — | effectively required for `s3` (unless using an IAM role / shared credentials file) | **yes** | Read by the AWS SDK's default credential chain, not by `pkg/config`. |
| `AWS_SECRET_ACCESS_KEY` | — | same as above | **yes** | Same. |
| `AWS_REGION` | — | optional, `S3_REGION` is passed to the client explicitly | no | SDK-level region override; `S3_REGION` is what `pkg/config` actually uses. |

## Verification

`deploy/ENVIRONMENT.md` should be re-diffed against `pkg/config/config.go` whenever that file changes:

```sh
grep -oE '(env(String|Boolean|Int|PositiveInt)\("[A-Z0-9_]+"|os\.Getenv\("[A-Z0-9_]+"\))' pkg/config/config.go | grep -oE '"[A-Z0-9_]+"' | sort -u
```

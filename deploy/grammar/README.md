# Optional grammar providers

Chronicle Native is always available. This override adds deployment-managed
LanguageTool, Harper, and proselint analyzers without publishing their ports.
Writers can select healthy providers in Proofreader; they cannot edit server
URLs or credentials.

Start one adapter:

```sh
docker compose -f deploy/compose.yml -f deploy/grammar/compose.yml \
  --profile grammar-harper up -d --wait --build
```

Start the complete local comparison set:

```sh
docker compose -f deploy/compose.yml -f deploy/grammar/compose.yml \
  --profile grammar-all up -d --wait --build
```

Validate before restarting:

```sh
docker compose -f deploy/compose.yml -f deploy/grammar/compose.yml \
  run --rm chronicle providers validate
```

Copy `providers.example.yml` for real deployments. Provider IDs are stable UI
and cache identities. Raw secrets are rejected: use `env:VARIABLE` or
`file:/run/secrets/name`. Cloud providers should declare
`data_boundary: cloud`; Proofreader then requires writer consent before sending
text to them.

## LanguageTool n-grams

Set `LT_NGRAMS_DIR` to a host directory containing LanguageTool's `en/1grams`,
`en/2grams`, and `en/3grams` indexes, and set `LT_LANGUAGE_MODEL=/ngrams`.
The directory is mounted read-only. The model is large, so Chronicle does not
download it implicitly during ordinary startup or enable an empty model path.

## Custom adapters

Set `adapter: chronicle-v1` and point `endpoint` at a service implementing:

- `GET /healthz`
- `GET /v1/capabilities`
- `POST /v1/check` with `{ "text", "language", "mode" }`

The check response is `{ "findings": [...] }`. Each finding uses UTF-16
`start`/`end` offsets and Chronicle's existing `kind`, `message`,
`replacements`, `ruleId`, and `category` fields. Remote adapters may use a
`bearer_token` secret reference.

ProWritingAid is intentionally not included because it no longer offers a
public integration API. A private enterprise endpoint can still be exposed
through a `chronicle-v1` adapter.

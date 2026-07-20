# Chronicler

[Chronicler](https://chronicler.ink) is a focused, self-hosted manuscript
workstation. It combines a distraction-free TipTap editor with a
multi-manuscript library, revision-aware sync, collaboration, exports, plugins,
and an authoritative SQLite store in a small Go container.

This repository is the maintained successor to Chronicle. The application is
currently completing its naming transition, so some internal paths, environment
variables, and image names still use `chronicle`.

## Screenshots

**The Library** — manuscripts in light and dark mode:

<p align="center">
  <img src="screenshots/landing_page.webp" alt="The manuscript library in light mode" width="49%">
  <img src="screenshots/landing_page_dark_mode.webp" alt="The manuscript library in dark mode" width="49%">
</p>

**The writing space** — a distraction-free editor with focus dimming:

<p align="center">
  <img src="screenshots/writing_space.webp" alt="The distraction-free manuscript editor" width="90%">
</p>

These screenshots remain representative of the current interface. The archived
Export screenshot was intentionally not carried forward because it showed the
removed built-in Outline tab.

## What it does

- Manages multiple manuscripts, chapters, metadata, author profiles, and cover
  art.
- Provides a responsive manuscript editor with smart typography, comments,
  focus mode, and a formatting-only Bold/Italic/Underline selection toolbar.
- Imports existing work and exports DOCX, Markdown, HTML, and EPUB3.
- Supports revision-aware synchronization and opt-in real-time collaboration.
- Includes local spelling and grammar infrastructure, a custom dictionary, and
  a guided proofreading workflow.
- Supports API v4 plugins installed from Git repositories, with dependency,
  conflict, update, and pinning controls.
- Offers `none`, shared-token, forward-auth, and OIDC authentication modes.
- Keeps SQLite authoritative while optionally replicating portable recovery
  records asynchronously to S3-compatible storage.
- Ships as a static Go server with the React application embedded in the
  binary. The accepted production container uses roughly 114–120 MiB on the
  validation host.

## Lean-core boundary

Chronicler deliberately does **not** ship Outliner, Issues, Thesaurus, Tense
Check, Autocomplete, Autocorrect, or live inline Grammar Check features in the
normal editor. Their final Chronicle implementations are preserved in separate
plugin repositories for future ports; several are archive placeholders and are
not yet installable.

The grammar service and `/api/grammar/check` remain available for the
Proofreader and compatible plugins. Existing documents retain legacy schema
support, and obsolete settings are ignored without destructively rewriting user
data.

## Quick start with Docker

Until the container registry completes the Chronicler naming migration, the
most portable public workflow is to build the image from this repository:

```sh
git clone https://github.com/Vitadek/chronicler.git
cd chronicler
docker build -t chronicler:local .
docker volume create chronicler-data
export CHRONICLER_TOKEN="$(openssl rand -hex 32)"
printf 'Chronicler token: %s\n' "$CHRONICLER_TOKEN"
docker run -d \
  --name chronicler \
  --restart unless-stopped \
  -p 3000:3000 \
  -v chronicler-data:/data \
  -e AUTH_MODE=token \
  -e AUTH_TOKEN="$CHRONICLER_TOKEN" \
  chronicler:local
```

Open <http://localhost:3000>. Save the generated token before running the
container if you need to enter it on another device. The `/data` volume contains
the authoritative SQLite database and must be backed up.

For a Compose template, S3 variables, health checks, and maintenance commands,
see [DEPLOYMENT.md](DEPLOYMENT.md) and [`deploy/`](deploy/). Production
deployments should use token, forward-auth, or OIDC mode. Anonymous non-loopback
binding fails closed unless `ALLOW_INSECURE_NO_AUTH=true` is explicitly set for
a trusted private network.

## Maintenance

The same image provides the storage and recovery CLI:

```sh
docker exec chronicler /app/chronicle-server status
docker exec chronicler /app/chronicle-server verify
docker exec chronicler /app/chronicle-server retry
docker exec chronicler /app/chronicle-server backup
```

Restore applies offline. Stop the application, review the dry run, and require
`--apply --force` before overwriting existing records. The CLI creates a hot
SQLite backup before applying a restore. See [DEPLOYMENT.md](DEPLOYMENT.md) for
the complete sequence.

## Development

The frontend uses Node 22; the server uses Go 1.25 with vendored dependencies.

```sh
cd frontend
npm ci
npm run lint
npm test
npm run build

cd ..
go test ./...
go run -tags headless .
```

The complete release gate treats the OCI image as the product boundary:

```sh
CHRONICLE_IMAGE=chronicler:local ./tests/formal/run.sh
```

It covers fail-closed configuration, manuscript and settings isolation, sync,
collaboration, S3 outage/recovery, backup, restart durability, offline restore,
and deep replica verification. The current release passed all 97 cases.

## Current release

- Source tag: `release-20260720-core-lean`
- Source commit: `04c23501d6d8b50e99235ee159b0a4e36220de2e`
- Accepted OCI digest:
  `sha256:b40df22c4ccffc047bd0cdfe7622038801f4becf17ce68cb79b5e53fc1f3014a`
- Canonical mirrors:
  [GitHub](https://github.com/Vitadek/chronicler) and
  [Forgejo](https://forgejo.lan/protoman/chronicler)

The original Chronicle repositories remain archived as read-only history and
rollback references.

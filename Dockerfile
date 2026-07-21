# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

# --- Frontend stage -----------------------------------------------------------
# Builds the React app into /src/web, which the Go stage then embeds. Generated
# web/ output is intentionally not committed; this stage is the release source
# of truth and must run before `//go:embed web/*` is compiled.
FROM node:22-alpine@sha256:16e22a550f3863206a3f701448c45f7912c6896a62de43add43bb9c86130c3e2 AS frontend

WORKDIR /src/frontend

# Deps first so they cache independently of source changes. `npm ci` (not
# `npm install`) is deliberate: it installs exactly what package-lock.json
# pins. Resolving fresh versions inside the same semver ranges has already been
# shown to change the bundle materially — it dropped the LazyMotion feature
# chunks and grew the entry chunk by 145K.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci

COPY frontend/ ./
RUN npm run build


# --- Go build stage -----------------------------------------------------------
FROM golang:1.25-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS builder

WORKDIR /src

# Vendored deps are committed, so the build needs no network.
COPY go.mod go.sum ./
COPY vendor/ ./vendor/

COPY . .
# The frontend stage supplies the generated directory omitted from the source
# tree, so the image always embeds a UI built from this commit's frontend.
COPY --from=frontend /src/web ./web

# CGO_ENABLED=0 gives a static binary and, with `headless`, excludes the
# webview GUI (gui.go is `//go:build !headless && cgo`). modernc.org/sqlite is
# pure Go, which is what makes a cgo-free server build possible at all.
ENV CGO_ENABLED=0
RUN go build -mod=vendor -tags headless -trimpath \
      -ldflags="-s -w" -o /out/chronicle-server .


# --- Runtime stage ------------------------------------------------------------
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc

# tini reaps zombies and forwards signals, so the server shuts down gracefully
# when the container stops. wget backs the healthcheck below.
RUN apk add --no-cache tini wget

WORKDIR /app
ENV DATA_DIR=/data \
    PORT=3000 \
    HOST=0.0.0.0

COPY --from=builder /out/chronicle-server /app/chronicle-server

# Bundled plugin sources. EMPTY in the official image — a base deployment ships
# no plugins; they're installed from git in the UI. Kept as a hook for building
# a customised (or air-gapped) image: drop a plugin's source in plugins-seed/
# and the server copies it into /data/plugins and compiles it on first boot
# (esbuild is linked into the binary for exactly this).
COPY plugins-seed/ /app/plugins-seed/

RUN addgroup -S chronicle && adduser -S -G chronicle chronicle \
    && mkdir -p /data && chown chronicle:chronicle /data /app
VOLUME ["/data"]

USER chronicle
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:3000/readyz >/dev/null || exit 1

LABEL org.opencontainers.image.source=https://github.com/Vitadek/chronicler.git
LABEL org.opencontainers.image.url=https://chronicler.ink
LABEL org.opencontainers.image.title="Chronicler"
LABEL org.opencontainers.image.description="A focused, self-hosted manuscript workstation."
LABEL org.opencontainers.image.licenses=MIT

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/app/chronicle-server"]

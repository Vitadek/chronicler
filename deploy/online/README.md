# Chronicler online single-user deployment

This bundle runs Chronicler behind Nginx and Authelia for one trusted user. It keeps SQLite as the authoritative local store, enables plugin installation through the normal authenticated UI, and publishes HTTPS over TCP (HTTP/2) and UDP (HTTP/3/QUIC). It is intentionally not a PostgreSQL or multi-user deployment; that topology will be documented separately.

## Before you start

Create A and AAAA records for `example.com` and `auth.example.com`, and allow inbound TCP 80/443 plus UDP 443. Docker must have IPv6 and ip6tables enabled; the Compose network has its own IPv4 and IPv6 subnets, but the host/router still needs to route IPv6. Install Docker Compose v2, `curl`, and `openssl`.

The installer asks for the server name, Authelia username, email, password, and ACME email. It stores only an Argon2 password hash and generated secrets. The default image is pinned by digest; override image variables only when deliberately testing another release.

## One-command bootstrap

Inspect or pin the script before piping it to a shell. The following downloads the installer and then runs the complete deployment interactively:

```sh
curl -fsSL https://raw.githubusercontent.com/Vitadek/chronicler/main/deploy/online/install.sh | bash
```

For reproducible installs, replace `main` in both the raw URL and `CHRONICLER_REF` with a reviewed commit, for example:

```sh
curl -fsSL https://raw.githubusercontent.com/Vitadek/chronicler/<reviewed-commit>/deploy/online/install.sh | CHRONICLER_REF=<reviewed-commit> bash
```

The generated project defaults to `./chronicler-online`; set `CHRONICLER_DEPLOY_DIR` before running to choose another directory. `CHRONICLER_SKIP_ACME=1` is for local validation only and creates a short-lived self-signed certificate.

After deployment, open `https://example.com` and sign in through `https://auth.example.com`. The prompted account belongs to `admins`, which is the default `AUTH_FORWARD_ADMIN_GROUP`; this allows that one user to install plugins. The proxy forwards the authenticated `Remote-*` headers to Chronicler and preserves WebSocket upgrades for collaboration. Keep the Chronicler container unexposed: plugin assets, API calls, and `/collab` should all pass through the same Authelia-protected origin.

## Operations

```sh
cd chronicler-online
docker compose ps
docker compose logs -f nginx authelia chronicler
docker compose exec nginx nginx -t
docker compose down                         # stop, keep data
```

The Certbot sidecar renews certificates through the shared ACME webroot and sends Nginx a reload signal. Back up `data/chronicler` and `data/authelia` together before upgrades. Never publish `.env`, Authelia configuration, or the data directory.

To check protocol negotiation, use an HTTP/3-capable client (for example `curl --http3-only https://example.com`); ordinary clients fall back to HTTPS over TCP. If the host cannot bind UDP 443, HTTP/3 is unavailable but HTTP/2 remains usable.

This configuration follows [Nginx HTTP/3/QUIC](https://nginx.org/en/docs/quic.html), [Nginx HTTP/3 module](https://nginx.org/en/docs/http/ngx_http_v3_module.html), [Authelia's Nginx auth-request integration](https://www.authelia.com/integration/proxies/nginx/), [Authelia's password guidance](https://www.authelia.com/reference/guides/passwords/), and [Docker IPv6 networking](https://docs.docker.com/engine/daemon/ipv6/).

## Scope and future work

This is a single-user SQLite deployment with forward authentication. It does not provide PostgreSQL, multiple independent users, external object storage, or automatic DNS/firewall configuration. A future deployment profile can add those without changing this one. Keep the image and raw script pinned to reviewed commits in production.

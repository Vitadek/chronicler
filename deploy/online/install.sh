#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }
escape_sed() { printf '%s' "$1" | sed 's/[\\&|]/\\&/g'; }
replace() { local file=$1 key=$2 value; value=$(escape_sed "$3"); sed -i "s|$key|$value|g" "$file"; }
need curl; need openssl; need docker
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2 is required'
if [[ -r /proc/sys/net/ipv6/conf/all/disable_ipv6 ]] && [[ $(< /proc/sys/net/ipv6/conf/all/disable_ipv6) == 1 ]]; then
  die 'host IPv6 is disabled; enable it before deploying (Docker IPv6 is required)'
fi
RAW_BASE=${CHRONICLER_RAW_BASE:-https://raw.githubusercontent.com/Vitadek/chronicler/${CHRONICLER_REF:-main}/deploy/online}
DEPLOY_DIR=${CHRONICLER_DEPLOY_DIR:-${PWD}/chronicler-online}
SERVER_NAME=${CHRONICLER_SERVER_NAME:-}
while [[ -z $SERVER_NAME ]]; do read -r -p 'Server name (DNS name, without https://): ' SERVER_NAME; done
SERVER_NAME=${SERVER_NAME,,}
[[ $SERVER_NAME =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || die 'server name must be a DNS name without a scheme or path'
AUTH_DOMAIN="auth.${SERVER_NAME}"
AUTHELIA_USER=${AUTHELIA_USER:-}
read -r -p "Authelia username [${AUTHELIA_USER:-writer}]: " answer; AUTHELIA_USER=${answer:-${AUTHELIA_USER:-writer}}
[[ $AUTHELIA_USER =~ ^[a-zA-Z0-9._-]+$ ]] || die 'invalid Authelia username'
AUTHELIA_EMAIL=${AUTHELIA_EMAIL:-}; read -r -p 'Authelia email: ' AUTHELIA_EMAIL
[[ $AUTHELIA_EMAIL == *@*.* ]] || die 'a valid email is required'
read -r -s -p 'Authelia password: ' AUTHELIA_PASSWORD; printf '\n'
read -r -s -p 'Repeat Authelia password: ' AUTHELIA_PASSWORD_CONFIRM; printf '\n'
[[ $AUTHELIA_PASSWORD == "$AUTHELIA_PASSWORD_CONFIRM" && -n $AUTHELIA_PASSWORD ]] || die 'passwords do not match or are empty'
ACME_EMAIL=${ACME_EMAIL:-$AUTHELIA_EMAIL}; read -r -p "ACME email [${ACME_EMAIL}]: " answer; ACME_EMAIL=${answer:-$ACME_EMAIL}
AUTHELIA_ADMIN_GROUP=${AUTHELIA_ADMIN_GROUP:-admins}
CHRONICLER_IMAGE=${CHRONICLER_IMAGE:-ghcr.io/vitadek/chronicler@sha256:dcde4edd74e82a22796ccc50e11d341768b651727cd06401cf77d9252cb16e93}
NGINX_IMAGE=${NGINX_IMAGE:-nginx:1.29-alpine}; AUTHELIA_IMAGE=${AUTHELIA_IMAGE:-authelia/authelia:4.39.20}; CERTBOT_IMAGE=${CERTBOT_IMAGE:-certbot/certbot:5.7.0}
mkdir -p "$DEPLOY_DIR"/{nginx,authelia/config,data/{chronicler,authelia,certs,acme}}
chmod 700 "$DEPLOY_DIR" "$DEPLOY_DIR"/data "$DEPLOY_DIR"/data/{chronicler,authelia,certs,acme}
fetch() { curl -fsSL --retry 3 "$RAW_BASE/$1" -o "$DEPLOY_DIR/$2"; }
fetch compose.yml compose.yml; fetch nginx/nginx.conf.template nginx/nginx.conf.template; fetch authelia/configuration.yml.template authelia/configuration.yml.template; fetch authelia/users_database.yml.template authelia/users_database.yml.template
hex() { openssl rand -hex 32; }
FORWARD_SECRET=$(hex); SESSION_SECRET=$(hex); STORAGE_KEY=$(hex); JWT_SECRET=$(hex)
docker pull "$AUTHELIA_IMAGE" >/dev/null
PASSWORD_HASH=$(docker run --rm "$AUTHELIA_IMAGE" authelia crypto hash generate argon2 --password "$AUTHELIA_PASSWORD" --no-confirm | sed -n 's/^Digest: //p')
unset AUTHELIA_PASSWORD AUTHELIA_PASSWORD_CONFIRM
[[ -n $PASSWORD_HASH ]] || die 'Authelia password hash generation failed'
cp "$DEPLOY_DIR/nginx/nginx.conf.template" "$DEPLOY_DIR/nginx/nginx.conf"; cp "$DEPLOY_DIR/authelia/configuration.yml.template" "$DEPLOY_DIR/authelia/configuration.yml"; cp "$DEPLOY_DIR/authelia/users_database.yml.template" "$DEPLOY_DIR/authelia/users_database.yml"
replace "$DEPLOY_DIR/nginx/nginx.conf" __SERVER_NAME__ "$SERVER_NAME"; replace "$DEPLOY_DIR/nginx/nginx.conf" __AUTH_DOMAIN__ "$AUTH_DOMAIN"; replace "$DEPLOY_DIR/nginx/nginx.conf" __FORWARD_SECRET__ "$FORWARD_SECRET"
replace "$DEPLOY_DIR/authelia/configuration.yml" __SERVER_NAME__ "$SERVER_NAME"; replace "$DEPLOY_DIR/authelia/configuration.yml" __AUTH_DOMAIN__ "$AUTH_DOMAIN"; replace "$DEPLOY_DIR/authelia/configuration.yml" __SESSION_SECRET__ "$SESSION_SECRET"; replace "$DEPLOY_DIR/authelia/configuration.yml" __STORAGE_KEY__ "$STORAGE_KEY"; replace "$DEPLOY_DIR/authelia/configuration.yml" __JWT_SECRET__ "$JWT_SECRET"
replace "$DEPLOY_DIR/authelia/users_database.yml" __AUTHELIA_USER__ "$AUTHELIA_USER"; replace "$DEPLOY_DIR/authelia/users_database.yml" __AUTHELIA_EMAIL__ "$AUTHELIA_EMAIL"; replace "$DEPLOY_DIR/authelia/users_database.yml" __PASSWORD_HASH__ "$PASSWORD_HASH"; replace "$DEPLOY_DIR/authelia/users_database.yml" __AUTHELIA_ADMIN_GROUP__ "$AUTHELIA_ADMIN_GROUP"
chmod 600 "$DEPLOY_DIR"/nginx/nginx.conf "$DEPLOY_DIR"/authelia/*.yml
cat >"$DEPLOY_DIR/.env" <<EOF
SERVER_NAME=$SERVER_NAME
AUTH_DOMAIN=$AUTH_DOMAIN
CHRONICLER_IMAGE=$CHRONICLER_IMAGE
NGINX_IMAGE=$NGINX_IMAGE
AUTHELIA_IMAGE=$AUTHELIA_IMAGE
CERTBOT_IMAGE=$CERTBOT_IMAGE
CHRONICLER_FORWARD_SECRET=$FORWARD_SECRET
CHRONICLER_TRUSTED_PROXY_CIDR=172.30.0.0/24
AUTHELIA_ADMIN_GROUP=$AUTHELIA_ADMIN_GROUP
EOF
chmod 600 "$DEPLOY_DIR/.env"
if [[ ${CHRONICLER_SKIP_ACME:-0} == 1 ]]; then
  info 'CHRONICLER_SKIP_ACME=1: generating a self-signed certificate for validation only.'
  mkdir -p "$DEPLOY_DIR/data/certs/live/$SERVER_NAME"
  openssl req -x509 -nodes -newkey rsa:2048 -days 7 -subj "/CN=$SERVER_NAME" -keyout "$DEPLOY_DIR/data/certs/live/$SERVER_NAME/privkey.pem" -out "$DEPLOY_DIR/data/certs/live/$SERVER_NAME/fullchain.pem" >/dev/null 2>&1
else
  info "Requesting certificates for $SERVER_NAME and $AUTH_DOMAIN (ports 80/443 must be reachable)."
  docker run --rm -p 80:80/tcp -v "$DEPLOY_DIR/data/certs:/etc/letsencrypt" "$CERTBOT_IMAGE" certonly --standalone --non-interactive --agree-tos --no-eff-email --email "$ACME_EMAIL" -d "$SERVER_NAME" -d "$AUTH_DOMAIN"
fi
docker compose --project-directory "$DEPLOY_DIR" --env-file "$DEPLOY_DIR/.env" -f "$DEPLOY_DIR/compose.yml" config >/dev/null
docker compose --project-directory "$DEPLOY_DIR" --env-file "$DEPLOY_DIR/.env" -f "$DEPLOY_DIR/compose.yml" up -d
info "Chronicler is deployed at https://$SERVER_NAME"; info "Authelia is at https://$AUTH_DOMAIN; your account is '$AUTHELIA_USER'."; info 'The account is in the configured admin group so single-user plugin installation is available.'; info "Deployment files and SQLite data are under $DEPLOY_DIR; back them up before upgrades."

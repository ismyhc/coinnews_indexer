#!/usr/bin/env bash
#
# Install Caddy and configure it as an HTTPS reverse proxy in front of the
# CoinNews indexer container (which publishes :8080 on the host).
#
# Caddy obtains and renews a Let's Encrypt certificate automatically, so you get
# HTTPS with no manual cert management. Requirements:
#   - A domain whose DNS A/AAAA record points at THIS host.
#   - Ports 80 and 443 reachable from the internet (Caddy needs 80 for the ACME
#     challenge and serves 443).
#   - The indexer running and published on the host, e.g.:
#       docker run -d --name coinnews -p 8080:8080 ... coinnews-indexer run ...
#
# Usage:
#   sudo ./scripts/install-caddy.sh <domain> [email] [upstream]
#
#   <domain>    e.g. coinnews.example.com   (or set $DOMAIN)
#   [email]     ACME contact email (optional but recommended; or set $EMAIL)
#   [upstream]  what Caddy proxies to       (default: localhost:8080, or $UPSTREAM)
#
# Examples:
#   sudo ./scripts/install-caddy.sh coinnews.example.com me@example.com
#   DOMAIN=coinnews.example.com EMAIL=me@example.com sudo -E ./scripts/install-caddy.sh

set -euo pipefail

DOMAIN="${1:-${DOMAIN:-}}"
EMAIL="${2:-${EMAIL:-}}"
UPSTREAM="${3:-${UPSTREAM:-localhost:8080}}"

if [[ -z "$DOMAIN" ]]; then
  echo "error: domain required" >&2
  echo "usage: sudo $0 <domain> [email] [upstream]" >&2
  exit 1
fi

if [[ "$(id -u)" -ne 0 ]]; then
  echo "error: run as root (use sudo)" >&2
  exit 1
fi

# --- install Caddy (Debian/Ubuntu via the official apt repo) ---
if ! command -v caddy >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    echo "==> installing Caddy via apt"
    apt-get update
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
      > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update
    apt-get install -y caddy
  elif command -v dnf >/dev/null 2>&1; then
    echo "==> installing Caddy via dnf"
    dnf install -y 'dnf-command(copr)'
    dnf copr enable -y @caddy/caddy
    dnf install -y caddy
  else
    echo "error: no supported package manager (apt-get/dnf) found." >&2
    echo "Install Caddy manually: https://caddyserver.com/docs/install" >&2
    exit 1
  fi
else
  echo "==> Caddy already installed: $(caddy version)"
fi

# --- write the Caddyfile ---
CADDYFILE=/etc/caddy/Caddyfile
echo "==> writing $CADDYFILE (proxying $DOMAIN -> $UPSTREAM)"

{
  if [[ -n "$EMAIL" ]]; then
    printf '{\n\temail %s\n}\n\n' "$EMAIL"
  fi
  cat <<EOF
$DOMAIN {
	encode gzip
	reverse_proxy $UPSTREAM
}
EOF
} > "$CADDYFILE"

# --- validate and (re)start ---
echo "==> validating config"
caddy validate --config "$CADDYFILE" --adapter caddyfile

echo "==> enabling and restarting caddy"
systemctl enable caddy >/dev/null 2>&1 || true
systemctl restart caddy

echo
echo "Done. Caddy is serving https://$DOMAIN -> $UPSTREAM"
echo "Once DNS for $DOMAIN points here and ports 80/443 are open, a certificate"
echo "is issued automatically on first request. Check logs with:"
echo "  journalctl -u caddy -f"

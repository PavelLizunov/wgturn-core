#!/bin/sh
# Provision a new wgturn VPN user.
#
# Generates a fresh WireGuard keypair locally (the private key never
# leaves the machine running this script), allocates the next free IP
# in TUN_NETWORK, appends a [Peer] section to the server's wg0.conf,
# applies it to the running interface via `wg syncconf` (no downtime),
# and prints the client config to stdout.
#
# Usage:
#   ./scripts/provision-user.sh <username> > ~/wgturn-handoff/users/<username>.conf
#
# Flags:
#   -n   dry-run: don't touch server state, just print what would happen
#        and emit the client conf with placeholder server pubkey.
#
# Env: see scripts/lib.sh for SERVER_HOST / SSH_PROXY / etc.
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=lib.sh
. "${DIR}/lib.sh"

DRY_RUN=0
while getopts "n" opt; do
    case "$opt" in
        n) DRY_RUN=1 ;;
        *) die "usage: $0 [-n] <username>" ;;
    esac
done
shift $((OPTIND - 1))

USER_NAME="${1:-}"
[ -n "$USER_NAME" ] || die "usage: $0 [-n] <username>"

# Sanity: alphanumerics + -_. only. Goes into a comment on the server, so
# we want to avoid embedded shell metachars and newlines.
case "$USER_NAME" in
    *[!A-Za-z0-9._-]*) die "username may only contain [A-Za-z0-9._-]" ;;
esac

require_local_tools wg

log "==> generating client keypair + PSK locally"
CLIENT_PRIV=$(wg genkey)
CLIENT_PUB=$(printf '%s' "$CLIENT_PRIV" | wg pubkey)
PSK=$(wg genpsk)

if [ "$DRY_RUN" -eq 1 ]; then
    log "==> dry-run: skipping server-side state"
    SERVER_PUB="<server-public-key-here>"
    OCTET=$(allocate_ip)
    log "    would assign 10.7.0.${OCTET}/32"
else
    log "==> querying server for pubkey + used IPs"
    SERVER_PUB=$(server_pubkey)
    [ -n "$SERVER_PUB" ] || die "could not read server pubkey via 'wg show ${SERVER_IFACE} public-key'"
    OCTET=$(allocate_ip)
    log "    allocated 10.7.0.${OCTET}/32"

    log "==> appending [Peer] block to ${SERVER_CONFIG} and applying via wg syncconf"
    # Build the peer block inline. We embed username + provisioning
    # timestamp as comments so revoke-user.sh can grep for them later.
    NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    PEER_BLOCK="
# wgturn-provisioned: user=${USER_NAME} ip=10.7.0.${OCTET}/32 at=${NOW}
[Peer]
PublicKey    = ${CLIENT_PUB}
PresharedKey = ${PSK}
AllowedIPs   = 10.7.0.${OCTET}/32
"

    # Stream the block to the server over stdin — avoids multi-layer
    # shell quoting hell (heredocs nested inside ssh single-quotes
    # nested inside our local double-quotes). ssh forwards stdin
    # transparently, including across the SSH_PROXY hop.
    printf '%s' "$PEER_BLOCK" | ssh_server "cat >> ${SERVER_CONFIG}" \
        || die "failed to append peer block on server"

    # Apply without bringing the interface down: wg syncconf reads a
    # *stripped* config (PostUp etc removed), comparing against the
    # live state and adding/removing peers as needed. wg-quick strip
    # writes to stdout; we stream that into wg syncconf via bash's
    # process substitution.
    ssh_server "bash -c 'wg syncconf ${SERVER_IFACE} <(wg-quick strip ${SERVER_IFACE})'" \
        || die "wg syncconf failed; the new peer is in the file but not active"

    log "==> verifying peer is live"
    if ssh_server "wg show ${SERVER_IFACE} | grep -q '${CLIENT_PUB}'"; then
        log "    peer ${CLIENT_PUB} active on ${SERVER_IFACE}"
    else
        die "peer not visible in 'wg show' output — investigate manually"
    fi
fi

log "==> emitting client config to stdout"
emit_client_conf "$CLIENT_PRIV" "$OCTET" "$SERVER_PUB" "$PSK" "$USER_NAME"

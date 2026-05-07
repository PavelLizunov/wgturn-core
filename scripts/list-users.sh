#!/bin/sh
# List wgturn VPN peers configured on the server.
#
# Usage:  ./scripts/list-users.sh
#
# Output (TSV):  <ip>  <username|"unknown">  <pubkey>  <provisioned>
#                                                       (last-handshake from `wg show`)
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=lib.sh
. "${DIR}/lib.sh"

# Pull live handshake info to enrich the list.
LIVE=$(ssh_server "wg show ${SERVER_IFACE} dump") || die "wg show failed"

# 'wg show <iface> dump' format (server line first, then one per peer):
#   <iface-priv>  <iface-pub>  <listen-port>  <fwmark>
#   <peer-pub>    <psk>        <endpoint>     <allowed-ips>  <last-handshake-sec>  <rx>  <tx>  <persistent-keepalive>
#
# We grab pubkey + last-handshake-sec for the peers, then cross-reference
# the wg0.conf for username comments.

CONF=$(ssh_server "cat ${SERVER_CONFIG}") || die "cat ${SERVER_CONFIG} failed"

printf 'IP\tUSERNAME\tPUBKEY\tLAST_HANDSHAKE\n'

# Skip the first line (interface) and walk the rest.
echo "$LIVE" | tail -n +2 | while IFS=$(printf '\t') read -r pub _psk _endpoint allowed last_hs _rx _tx _keepalive; do
    [ -n "$pub" ] || continue
    ip=$(printf '%s' "$allowed" | tr ',' '\n' | grep -oE '10\.7\.0\.[0-9]+' | head -1)
    # Find the username from the conf's "wgturn-provisioned: user=<name>" comment
    # placed right above the matching [Peer] block. We match the pubkey via
    # index() rather than regex because base64 contains '+' / '/' which would
    # be interpreted as regex meta-characters.
    uname=$(printf '%s' "$CONF" | awk -v pub="$pub" '
        /^# wgturn-provisioned: user=/ { saved = $0 }
        /^PublicKey/ && index($0, pub) > 0 {
            if (saved != "") {
                match(saved, /user=[^ ]+/)
                if (RLENGTH > 0) print substr(saved, RSTART+5, RLENGTH-5)
                exit
            }
        }
    ')
    [ -n "$uname" ] || uname="unknown"

    if [ -z "$last_hs" ] || [ "$last_hs" = "0" ]; then
        hs="never"
    else
        hs="$(date -u -d "@${last_hs}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "${last_hs}")"
    fi
    printf '%s\t%s\t%s\t%s\n' "${ip:-?}" "$uname" "$pub" "$hs"
done

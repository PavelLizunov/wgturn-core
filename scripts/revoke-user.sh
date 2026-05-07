#!/bin/sh
# Revoke (delete) a wgturn VPN peer by username.
#
# Looks for the "# wgturn-provisioned: user=<name>" marker followed by
# a [Peer] block, removes that block from the server's wg0.conf, then
# applies the change live via `wg syncconf`. Refuses to touch peers
# without the marker (those were added outside this tooling).
#
# Usage:  ./scripts/revoke-user.sh <username>
#         ./scripts/revoke-user.sh -p <pubkey>     # by public key instead
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=lib.sh
. "${DIR}/lib.sh"

BY_PUBKEY=""
while getopts "p:" opt; do
    case "$opt" in
        p) BY_PUBKEY="$OPTARG" ;;
        *) die "usage: $0 <username>  |  $0 -p <pubkey>" ;;
    esac
done
shift $((OPTIND - 1))

if [ -n "$BY_PUBKEY" ]; then
    SELECTOR="pubkey=${BY_PUBKEY}"
else
    USER_NAME="${1:-}"
    [ -n "$USER_NAME" ] || die "usage: $0 <username>  |  $0 -p <pubkey>"
    case "$USER_NAME" in
        *[!A-Za-z0-9._-]*) die "username may only contain [A-Za-z0-9._-]" ;;
    esac
    SELECTOR="user=${USER_NAME}"
fi

log "==> finding peer block matching ${SELECTOR}"

# 3-state machine. The trick is that the `[Peer]` line immediately
# below our marker IS our peer — naïvely treating it as a "section
# header that ends the skip" would leave the body intact.
#
#   normal       — passing lines through; on matching marker switch
#                  to after_marker.
#   after_marker — expects [Peer] right next. Always drops one line.
#                  If the line is [Peer], advance to in_peer to swallow
#                  the body. Otherwise the marker had no peer attached
#                  (corrupt file?) — return to normal and print.
#   in_peer      — dropping the peer body. Exits on blank line, on
#                  another `[Section]`, or on another marker; that
#                  terminator is preserved and we go back to normal.
AWK_PROG=$(cat <<'AWK'
BEGIN { mode = "normal" }
{
    if (mode == "normal") {
        if ($0 ~ /^# wgturn-provisioned:/ && index($0, SEL) > 0) {
            mode = "after_marker"
            removed = 1
            next
        }
        print
        next
    }
    if (mode == "after_marker") {
        if ($0 ~ /^\[Peer\]/) {
            mode = "in_peer"
            next
        }
        # Marker without a peer body — restore the line and resume.
        mode = "normal"
        print
        next
    }
    # mode == "in_peer"
    if ($0 ~ /^[[:space:]]*$/) {
        mode = "normal"
        print
        next
    }
    if ($0 ~ /^\[/ || $0 ~ /^# wgturn-provisioned:/) {
        mode = "normal"
        print
        next
    }
    # Still inside our peer block — drop.
    next
}
END {
    if (!removed) exit 1
}
AWK
)

# Stream the awk program to the server over stdin (avoids quoting hell).
# Tell awk to read its program from /dev/stdin via -f -.
# Output goes to a sibling tmp file we then atomically rename.
if ! printf '%s' "$AWK_PROG" \
        | ssh_server "awk -v SEL='${SELECTOR}' -f - ${SERVER_CONFIG} > ${SERVER_CONFIG}.new && mv ${SERVER_CONFIG}.new ${SERVER_CONFIG}"; then
    ssh_server "rm -f ${SERVER_CONFIG}.new" || true
    die "no peer matching ${SELECTOR} found in ${SERVER_CONFIG}"
fi

log "==> applying via wg syncconf"
ssh_server "bash -c 'wg syncconf ${SERVER_IFACE} <(wg-quick strip ${SERVER_IFACE})'" \
    || die "wg syncconf failed; conf file is updated but the live interface still has the peer"

log "==> revoked ${SELECTOR}"

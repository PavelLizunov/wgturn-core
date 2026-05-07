# Shared helpers for the provisioning scripts. POSIX sh, no bashisms.
#
# Source me, don't execute me directly:
#
#   . "$(dirname "$0")/lib.sh"
#
# Configuration (env vars; defaults are the homelab):
#   SERVER_HOST       — wgturn-server host or IP             (default 93.95.226.167)
#   SERVER_SSH_USER   — SSH login on the server              (default root)
#   SSH_PROXY         — intermediate hop, blank = direct     (default user@192.168.0.207)
#   SERVER_CONFIG     — wg-quick conf path on the server     (default /etc/wireguard/wg0.conf)
#   SERVER_IFACE      — wg interface name                    (default wg0)
#   TUN_NETWORK       — CIDR the tunnel lives in             (default 10.7.0.0/24)
#   VK_LINKS          — comma-separated VK call-link list    (default = handoff bundle's pool)

SERVER_HOST="${SERVER_HOST:-93.95.226.167}"
SERVER_SSH_USER="${SERVER_SSH_USER:-root}"
# Note: deliberately uses ${VAR-default} (no colon) so that explicitly
# setting SSH_PROXY="" disables the proxy hop without unsetting the
# variable. Useful when these scripts run directly on the homelab .207
# host that is normally the proxy itself.
SSH_PROXY="${SSH_PROXY-user@192.168.0.207}"
SERVER_CONFIG="${SERVER_CONFIG:-/etc/wireguard/wg0.conf}"
SERVER_IFACE="${SERVER_IFACE:-wg0}"
TUN_NETWORK="${TUN_NETWORK:-10.7.0.0/24}"
VK_LINKS="${VK_LINKS:-https://vk.ru/call/join/-M7zHqhgZako1jvZ9-Lckqzt6VCCWRTc-f4t2zd0fBI,https://vk.ru/call/join/8O4R9gKoke_KLMKWeUqp0UClRG3FD-xZMjmlEr9aaF8,https://vk.ru/call/join/BY6TVSshYjNKJt53v8WtmFzz7UrHIkJPMff3bL7sMoE,https://vk.ru/call/join/KN1mJNqvF2iajRe99gktXl2loBQAxk-WIjIPGSPPj0A}"

# log writes a status line to stderr so stdout stays a pure config blob
# the caller can redirect.
log() { printf '%s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# SSH options used everywhere we shell out:
#   - BatchMode: never prompt for password / key passphrase, fail fast.
#   - StrictHostKeyChecking=accept-new: trust on first use, reject on
#     subsequent mismatch (matches the homelab CLAUDE.md convention).
SSH_OPTS="-o BatchMode=yes -o StrictHostKeyChecking=accept-new"

# ssh_server runs <cmd> on the wgturn-server, hopping through SSH_PROXY
# when set. The server-side command is passed verbatim — caller must
# quote shell metacharacters appropriately.
ssh_server() {
    if [ -n "${SSH_PROXY:-}" ]; then
        # shellcheck disable=SC2029  # we deliberately expand SSH_OPTS locally
        ssh $SSH_OPTS "$SSH_PROXY" \
            "ssh $SSH_OPTS ${SERVER_SSH_USER}@${SERVER_HOST} '$1'"
    else
        # shellcheck disable=SC2029
        ssh $SSH_OPTS "${SERVER_SSH_USER}@${SERVER_HOST}" "$1"
    fi
}

# require_local_tools dies if any of the named binaries is missing on
# the local host. We need wg + wg pubkey to derive keys without ever
# sending a private key to the server.
require_local_tools() {
    for t in "$@"; do
        command -v "$t" >/dev/null 2>&1 || \
            die "missing local tool: $t (apt install wireguard-tools)"
    done
}

# server_pubkey echoes the server's runtime WireGuard public key.
# Reads it from `wg show <iface> public-key` rather than parsing the
# config — works even when PrivateKey lives in a separate file via
# PostUp hooks.
server_pubkey() {
    ssh_server "wg show ${SERVER_IFACE} public-key" | tr -d '\r\n'
}

# server_used_octets emits one integer per line: each /32 octet already
# claimed in the server's [Peer] sections under TUN_NETWORK. Used by
# allocate_ip to find the next free slot.
server_used_octets() {
    # Match `AllowedIPs = 10.7.0.42/32` (whitespace lenient).
    # Output the last octet of each match.
    ssh_server "grep -E '^[[:space:]]*AllowedIPs[[:space:]]*=' ${SERVER_CONFIG}" \
        | awk -F'=' '{print $2}' \
        | tr ',' '\n' \
        | grep -oE '10\.7\.0\.[0-9]+' \
        | awk -F. '{print $4}' \
        | sort -un
}

# allocate_ip prints the first free octet >= 2 (server keeps .1) within
# /24, refusing to roll over past 254. Idempotent: pure read; doesn't
# mutate server state.
allocate_ip() {
    used="$(server_used_octets || true)"
    next=2
    for n in $used; do
        if [ "$n" = "$next" ]; then
            next=$((next + 1))
        elif [ "$n" -gt "$next" ]; then
            break
        fi
    done
    if [ "$next" -gt 254 ]; then
        die "TUN_NETWORK ${TUN_NETWORK} is full (>= 253 peers)"
    fi
    printf '%d\n' "$next"
}

# emit_client_conf prints a client wg-quick config (with #@wgt: metadata)
# to stdout. Inputs are positional, all required.
#   $1 priv key (base64)
#   $2 client IP octet (just the last byte)
#   $3 server pubkey (base64)
#   $4 PSK (base64)
#   $5 username (free-form, embedded as a comment so revoke-user.sh can find it)
emit_client_conf() {
    priv="$1"; octet="$2"; spub="$3"; psk="$4"; uname="$5"
    cat <<EOF
# wgturn handoff config for user '${uname}'
# Generated $(date -u +%Y-%m-%dT%H:%M:%SZ)
# Server: ${SERVER_HOST}, peer slot: 10.7.0.${octet}/24
# Use:    sudo wgturn-cli connect ${uname}.conf -v

[Interface]
PrivateKey = ${priv}
Address    = 10.7.0.${octet}/24
DNS        = 1.1.1.1, 8.8.8.8
MTU        = 1280

#@wgt:EnableTURN = true
#@wgt:Mode       = vk_link
#@wgt:Peer       = ${SERVER_HOST}:56000
#@wgt:PeerType   = proxy_v2
#@wgt:UDP        = true
#@wgt:Streams    = 24
#@wgt:VkLink     = ${VK_LINKS}

[Peer]
PublicKey    = ${spub}
PresharedKey = ${psk}
Endpoint     = 127.0.0.1:9000
AllowedIPs   = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
EOF
}

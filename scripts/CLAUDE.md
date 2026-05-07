# scripts/ — server-side user provisioning

Operator tools for managing wgturn VPN peers on the wgturn-server
(`/etc/wireguard/wg0.conf` on is-01 by default). Run from any host
with SSH access to the server (directly or via the SSH_PROXY hop).

## Files

- `lib.sh`              shared helpers — `ssh_server`, IP allocator,
                       client-config templater. Sourced, not executed.
- `provision-user.sh`   create a new peer; emit client `.conf` to stdout.
- `list-users.sh`       TSV of currently-configured peers.
- `revoke-user.sh`      remove a peer by username (or `-p <pubkey>`).

## Usage

```sh
# From the homelab .207 (default proxy):
./scripts/provision-user.sh alice > ~/wgturn-handoff/users/alice.conf
./scripts/list-users.sh
./scripts/revoke-user.sh alice
```

```sh
# From the .207 host directly (no proxy hop needed):
SSH_PROXY="" ./scripts/provision-user.sh bob > /tmp/bob.conf
```

## Configuration (env vars; defaults are the homelab)

| Var | Default | Purpose |
|---|---|---|
| `SERVER_HOST` | `93.95.226.167` | wgturn-server host |
| `SERVER_SSH_USER` | `root` | SSH login on the server |
| `SSH_PROXY` | `user@192.168.0.207` | hop host; set to `""` (not unset!) to disable |
| `SERVER_CONFIG` | `/etc/wireguard/wg0.conf` | wg-quick conf path |
| `SERVER_IFACE` | `wg0` | WireGuard interface name |
| `TUN_NETWORK` | `10.7.0.0/24` | tunnel CIDR for IP allocation |
| `VK_LINKS` | handoff bundle pool | comma-separated VK call links |

`SSH_PROXY` uses `${VAR-default}` (no colon) so an explicit empty
string disables it. `unset SSH_PROXY` falls back to the default.

## How it works

1. Keys are generated **locally** with `wg genkey` / `wg pubkey` /
   `wg genpsk`. The private key never leaves the host running the
   script.
2. The next free /32 in `TUN_NETWORK` is found by parsing the
   server's existing `AllowedIPs` lines.
3. The new `[Peer]` block is appended to `wg0.conf`, prefixed with
   a `# wgturn-provisioned: user=<name> ip=<addr> at=<utc>` marker
   so list / revoke can find it again.
4. The change is applied to the running interface via
   `wg syncconf <iface> <(wg-quick strip <iface>)` — no downtime,
   existing peer connections are not interrupted.
5. The client `.conf` (with `#@wgt:` metadata for `wgturn-cli connect`)
   is printed to stdout; the operator redirects it to a file.

`revoke-user.sh` does the inverse: a 3-state awk machine drops the
marker line and the [Peer] block immediately following it, then
`wg syncconf` propagates the change live.

## Don't regress

- The `# wgturn-provisioned: …` marker format is part of the
  contract between provision-user.sh, list-users.sh, and
  revoke-user.sh. Don't reformat without updating all three.
- `awk` is invoked with the program piped over stdin (`-f -`)
  rather than as a `'…'` argument, because base64 keys contain
  characters the layered shell-quoting would mangle. Keep that
  pattern.
- `bash -c '<process substitution>'` is the wrapper for
  `wg syncconf wg0 <(wg-quick strip wg0)` because the server's
  default sh is dash on some installs and process substitution is
  a bash feature.
- IP allocation is sequential and starts at `.2` (server keeps
  `.1`). Refuse to roll over past `.254`.
- The keypair is generated locally; **never** ask the server for
  the private half. (We could in principle have the server `wg
  genkey` for us, but that would expose the key to anyone with
  log access on the server.)

## Testing

There is no automated test suite for these scripts (they're admin
tools, not library code). The verification path is:

1. Restore a known-clean `wg0.conf` on the server.
2. `./provision-user.sh smoketest > /tmp/smoke.conf`.
3. `./list-users.sh` should show `smoketest` on the next free IP.
4. `./revoke-user.sh smoketest`.
5. `./list-users.sh` should NOT show `smoketest` anymore.
6. `wg show wg0 | grep peer:` should reflect the same state.

This was the e2e Pavel ran when the scripts landed.

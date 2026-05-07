// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package wgadmin manages the wireguard-tools side of a wgturn server:
// generates client keypairs, allocates client IPs out of the server's
// subnet, edits wg0.conf, and syncs the running interface with
// `wg syncconf` so existing sessions don't drop.
//
// One Server value bundles the static config (interface name, conf
// path, subnet, listen endpoint) so the day-to-day API is just
// Provision / Revoke / List.
//
// Provision returns a wgshare.Profile, which the CLI Encodes into the
// wgturn:// URL the operator hands to the user. Revoke and List are
// straightforward analogues of the legacy provision-user.sh /
// revoke-user.sh / list-users.sh scripts.
//
// Persistence: this package treats wg0.conf as the single source of
// truth (matching the legacy scripts). Each peer block is tagged with
// a `# wgturn-name = <name>` comment so List can recover the friendly
// name set at provisioning time. There is no separate state.json.
//
// Thread safety: a single Server instance protects mutating calls
// with an internal mutex. Concurrent processes (multiple admin shells
// editing the same wg0.conf) need their own file lock — the legacy
// scripts use `flock` on /var/lock/wgturn-provision.lock, the same
// approach is recommended here.
package wgadmin

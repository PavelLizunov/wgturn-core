// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"runtime"
	"strings"

	"github.com/PavelLizunov/wgturn-core/pkg/wgkernel"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

const goosLinux = "linux"

// errHostSetupUnsupported signals that automatic host configuration
// (IP addresses, routes) is not yet implemented for the current OS.
// The CLI surfaces a helpful message including the manual commands
// the user can run themselves.
var errHostSetupUnsupported = errors.New("automatic host setup not implemented for this OS yet")

// setupHostIface configures the just-created TUN: brings it up,
// assigns each Address from the [Interface] section, and adds a route
// for every distinct AllowedIPs prefix across all peers.
//
// Returns a teardown function that reverses the state in LIFO order.
// Callers MUST defer the teardown after a successful return; on error
// the partial state is rolled back internally before returning.
//
// v0 supports Linux only. macOS / Windows return errHostSetupUnsupported
// so the CLI can print a clean "do this manually" message instead of
// silently leaving the tunnel half-configured. DNS is always the
// caller's job — wg-quick uses resolvconf / systemd-resolved / a
// /etc/resolv.conf rewrite, all of which are too platform-specific
// for v0.
func setupHostIface(ifaceName string, cfg wgkernel.Config, log wgturn.Logger) (func(), error) {
	if runtime.GOOS != goosLinux {
		return nil, fmt.Errorf("%w (GOOS=%s); after wgturn-cli connect, run manually:\n"+
			"  ip link set %s up                                # macOS: ifconfig %s up\n"+
			"  ip addr add <Address> dev %s                     # one per [Interface] Address\n"+
			"  ip route add <AllowedIPs> dev %s                 # one per peer AllowedIPs",
			errHostSetupUnsupported, runtime.GOOS,
			ifaceName, ifaceName, ifaceName, ifaceName)
	}
	return setupHostIfaceLinux(ifaceName, cfg, log)
}

// setupHostIfaceLinux is the Linux-specific host configuration. It
// shells out to /sbin/ip rather than using netlink directly so the
// implementation stays free of CGO and platform-specific Go libraries
// — wgturn-core deliberately keeps the dep tree small.
//
// Idempotency: we don't try to detect already-existing state. If the
// interface already has the address (e.g. from a previous crashed
// run), `ip addr add` returns "File exists" and we treat that as fatal
// — the user should clean up the stale interface (`ip link del`)
// rather than have us silently pick up half-state. This is rough but
// honest for v0.
func setupHostIfaceLinux(ifaceName string, cfg wgkernel.Config, log wgturn.Logger) (func(), error) {
	var rollbacks []func()
	rollback := func() {
		// LIFO so we tear down in reverse construction order.
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i]()
		}
	}

	// 1. Bring the interface up.
	if err := runIP(log, "link", "set", ifaceName, "up"); err != nil {
		rollback()
		return nil, fmt.Errorf("link set up: %w", err)
	}
	rollbacks = append(rollbacks, func() {
		_ = runIP(log, "link", "set", ifaceName, "down")
	})

	// 2. Assign each [Interface] Address.
	for _, addr := range cfg.Address {
		if err := runIP(log, "addr", "add", addr.String(), "dev", ifaceName); err != nil {
			rollback()
			return nil, fmt.Errorf("addr add %s: %w", addr, err)
		}
		a := addr
		rollbacks = append(rollbacks, func() {
			_ = runIP(log, "addr", "del", a.String(), "dev", ifaceName)
		})
	}

	// 3. Add a route for every distinct AllowedIPs prefix across all
	//    peers, skipping prefixes already covered by an interface
	//    address (the kernel adds those automatically as connected
	//    routes when we did `addr add`).
	seen := map[netip.Prefix]bool{}
	for _, p := range cfg.Peers {
		for _, aip := range p.AllowedIPs {
			if seen[aip] {
				continue
			}
			seen[aip] = true
			if isCoveredByConnectedRoute(aip, cfg.Address) {
				log.Debugf("hostsetup: skipping route %s — covered by interface address", aip)
				continue
			}
			if err := runIP(log, "route", "add", aip.String(), "dev", ifaceName); err != nil {
				rollback()
				return nil, fmt.Errorf("route add %s: %w", aip, err)
			}
			a := aip
			rollbacks = append(rollbacks, func() {
				_ = runIP(log, "route", "del", a.String(), "dev", ifaceName)
			})
		}
	}

	return rollback, nil
}

// runIP executes /sbin/ip with the given args and returns any error
// combined with the captured stderr — the user reads this when the
// CLI exits, so detail matters.
func runIP(log wgturn.Logger, args ...string) error {
	log.Debugf("hostsetup: $ ip %s", strings.Join(args, " "))
	cmd := exec.Command("ip", args...) //nolint:gosec // args are static + validated CIDRs
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %w (%s)", strings.Join(args, " "), err,
			strings.TrimSpace(string(out)))
	}
	return nil
}

// isCoveredByConnectedRoute reports whether routing to prefix is
// already implied by an interface address — `ip addr add 10.7.0.2/24
// dev wg0` automatically creates a 10.7.0.0/24 connected route, so
// adding it again would error with "RTNETLINK answers: File exists".
//
// We treat "covered" as "the connected route's prefix contains the
// peer's prefix and is at least as broad". Equality counts.
func isCoveredByConnectedRoute(prefix netip.Prefix, addrs []netip.Prefix) bool {
	want := prefix.Masked()
	for _, a := range addrs {
		ap := a.Masked()
		if ap == want {
			return true
		}
		// Connected route is the address's network masked by its prefix
		// length. It covers `prefix` iff the connected network is a
		// supernet of `prefix`.
		if ap.Bits() <= prefix.Bits() && ap.Contains(prefix.Addr()) {
			// Same address family check (Contains is liberal across families).
			if ap.Addr().Is4() == prefix.Addr().Is4() {
				return true
			}
		}
	}
	return false
}

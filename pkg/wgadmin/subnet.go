// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgadmin

import (
	"errors"
	"fmt"
	"net/netip"
)

// ErrSubnetExhausted is returned by AllocateClientIP when every
// host address in the subnet (excluding the server's reserved .1 and
// network/broadcast bookends) is already claimed by an existing peer.
var ErrSubnetExhausted = errors.New("wgadmin: subnet exhausted")

// AllocateClientIP returns the lowest unclaimed host address in
// subnet that:
//
//   - is not the network address (.0)
//   - is not the broadcast address (last)
//   - is not the serverAddr (typically .1, the gateway)
//   - is not present in `taken` (existing peers' AllowedIPs /32s)
//
// The returned netip.Addr can be promoted to a /32 prefix for the
// new peer's AllowedIPs entry, and to subnet-prefix-length for the
// client's tunnel address.
func AllocateClientIP(subnet netip.Prefix, serverAddr netip.Addr, taken []netip.Addr) (netip.Addr, error) {
	if !subnet.IsValid() {
		return netip.Addr{}, fmt.Errorf("wgadmin: invalid subnet %v", subnet)
	}
	used := make(map[netip.Addr]bool, len(taken)+1)
	if serverAddr.IsValid() {
		used[serverAddr] = true
	}
	for _, a := range taken {
		used[a] = true
	}

	addr := subnet.Masked().Addr()
	// Skip network address (the .0 in IPv4 / all-zeros in IPv6).
	addr = addr.Next()
	for subnet.Contains(addr) {
		// In IPv4, also skip the broadcast (last addr in the prefix).
		if addr.Is4() && isBroadcast(addr, subnet) {
			break
		}
		if !used[addr] {
			return addr, nil
		}
		next := addr.Next()
		if !next.IsValid() || next == addr {
			break
		}
		addr = next
	}
	return netip.Addr{}, ErrSubnetExhausted
}

// isBroadcast reports whether addr is the broadcast address of an
// IPv4 subnet (the last host: all host bits set).
func isBroadcast(addr netip.Addr, subnet netip.Prefix) bool {
	if !addr.Is4() {
		return false
	}
	mask := uint32(0xFFFFFFFF) >> uint32(subnet.Bits())
	a := addr.As4()
	host := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	return (host & mask) == mask
}

// existingPeerAddrs flattens the /32 host addresses recorded in each
// peer's AllowedIPs. Used as the "taken" input to AllocateClientIP.
// AllowedIPs entries that aren't single-host /32 (e.g. site-to-site
// peers with /24 ranges) are ignored — they might overlap the subnet
// but we don't try to fragment around them.
func existingPeerAddrs(peers []Peer) []netip.Addr {
	out := make([]netip.Addr, 0, len(peers))
	for _, p := range peers {
		for _, a := range p.AllowedIPs {
			if a.IsSingleIP() {
				out = append(out, a.Addr())
			}
		}
	}
	return out
}

// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgadmin

import (
	"errors"
	"net/netip"
	"testing"
)

// TestAllocateClientIP_PicksFirstFree walks through a /24 with the
// gateway and a couple of clients already in. Allocator must skip
// the network/.0, gateway/.1, taken /32s, and return .2 first.
func TestAllocateClientIP_PicksFirstFree(t *testing.T) {
	subnet := netip.MustParsePrefix("10.7.0.0/24")
	gw := netip.MustParseAddr("10.7.0.1")

	addr, err := AllocateClientIP(subnet, gw, nil)
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	if addr.String() != "10.7.0.2" {
		t.Errorf("first = %v, want 10.7.0.2", addr)
	}

	taken := []netip.Addr{netip.MustParseAddr("10.7.0.2"), netip.MustParseAddr("10.7.0.3")}
	addr, err = AllocateClientIP(subnet, gw, taken)
	if err != nil {
		t.Fatalf("with taken: %v", err)
	}
	if addr.String() != "10.7.0.4" {
		t.Errorf("second = %v, want 10.7.0.4", addr)
	}
}

// TestAllocateClientIP_SkipsBroadcast ensures we never hand out the
// last address in an IPv4 prefix — typically reserved for broadcast
// and ignored by wg's AllowedIPs matching.
func TestAllocateClientIP_SkipsBroadcast(t *testing.T) {
	subnet := netip.MustParsePrefix("10.7.0.0/30") // .0, .1, .2, .3
	gw := netip.MustParseAddr("10.7.0.1")

	// .2 is the only valid pick (.0 net, .1 gw, .3 bcast).
	addr, err := AllocateClientIP(subnet, gw, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if addr.String() != "10.7.0.2" {
		t.Errorf("first = %v, want 10.7.0.2", addr)
	}

	// Subsequent call exhausts.
	taken := []netip.Addr{netip.MustParseAddr("10.7.0.2")}
	_, err = AllocateClientIP(subnet, gw, taken)
	if !errors.Is(err, ErrSubnetExhausted) {
		t.Errorf("err = %v, want ErrSubnetExhausted", err)
	}
}

// TestAllocateClientIP_TinySubnet covers a /31 (point-to-point) where
// only .1 exists as a host and is consumed by the gateway: there is
// no room for any client. Allocation must error rather than hand
// back the gw or wrap around.
func TestAllocateClientIP_TinySubnet(t *testing.T) {
	subnet := netip.MustParsePrefix("10.7.0.0/31")
	gw := netip.MustParseAddr("10.7.0.1")
	_, err := AllocateClientIP(subnet, gw, nil)
	if !errors.Is(err, ErrSubnetExhausted) {
		t.Errorf("err = %v, want ErrSubnetExhausted", err)
	}
}

// TestExistingPeerAddrs surfaces only host (/32) entries from peers'
// AllowedIPs, since wider ranges aren't allocations to track.
func TestExistingPeerAddrs(t *testing.T) {
	peers := []Peer{
		{AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.7.0.5/32")}},
		{AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.7.0.0/24")}}, // wide, ignored
		{AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.7.0.7/32"), netip.MustParsePrefix("0.0.0.0/0")}},
	}
	got := existingPeerAddrs(peers)
	want := []string{"10.7.0.5", "10.7.0.7"}
	if len(got) != len(want) {
		t.Fatalf("got %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("got[%d] = %v, want %v", i, got[i], w)
		}
	}
}

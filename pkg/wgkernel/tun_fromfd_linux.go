// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || android

package wgkernel

import (
	"golang.zx2c4.com/wireguard/tun"
)

// NewTUNFromFD wraps an already-open OS file descriptor into a
// tun.Device. The FD must reference an open /dev/net/tun (Linux) or
// the equivalent /dev/tun handle on Android. Used by Android's
// VpnService.protect/establish flow which hands the app an FD.
//
// The mtu argument is currently unused — wireguard-go's
// CreateUnmonitoredTUNFromFD takes the MTU from the FD's existing
// configuration. Kept in the API for forwards compatibility.
//
// Returns the device and the resolved interface name.
//
// On macOS / Windows this function is not available (the
// CreateUnmonitoredTUNFromFD primitive is Linux-only in wireguard-go);
// see tun_fromfd_other.go for the stub implementation.
func NewTUNFromFD(fd int, _ int) (tun.Device, string, error) {
	return tun.CreateUnmonitoredTUNFromFD(fd)
}

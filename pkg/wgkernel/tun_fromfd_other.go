// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !android

package wgkernel

import (
	"errors"

	"golang.zx2c4.com/wireguard/tun"
)

// NewTUNFromFD is unavailable on this platform — wireguard-go's
// tun.CreateUnmonitoredTUNFromFD is implemented only on Linux/Android.
// Desktop macOS and Windows callers should use NewSystemTUN; iOS apps
// using NEPacketTunnelProvider drive the data plane outside this API.
//
// Returning a typed error rather than build-failing keeps cross-
// compilation working: a multi-platform CLI that compiles wgkernel
// can still link on macOS / Windows; only callers that actually
// invoke NewTUNFromFD on those platforms see the runtime failure.
func NewTUNFromFD(_ int, _ int) (tun.Device, string, error) {
	return nil, "", errors.New("wgkernel: NewTUNFromFD is not implemented on this platform " +
		"(Linux/Android only); use NewSystemTUN for desktop")
}

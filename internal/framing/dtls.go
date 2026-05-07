// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package framing

import (
	"crypto/tls"
	"fmt"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

// Role distinguishes the two sides of the DTLS connection so callers
// pick up the right defaults for the connection-ID generator and any
// future role-specific tweaks. Wire-format (cipher suite, master-secret
// requirement, certificate identity policy) stays identical regardless
// of role; this is just a builder convenience.
type Role int

const (
	// RoleClient is the dialing side: it speaks first via dtls.Client.
	RoleClient Role = iota
	// RoleServer is the listening side: it accepts via dtls.Listen.
	RoleServer
)

// CIDSize is the length, in bytes, of the DTLS Connection-ID the
// server advertises. The choice of 8 matches the legacy server so
// existing clients keep negotiating identical CIDs.
const CIDSize = 8

// GenerateCertificate produces a fresh self-signed ECDSA certificate
// suitable for either role. We do not authenticate the peer through
// the certificate — DTLS is used purely as opaque framing — so a new
// cert per process start is fine.
func GenerateCertificate() (tls.Certificate, error) {
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("framing: self-sign: %w", err)
	}
	return cert, nil
}

// NewDTLSConfig assembles the dtls.Config used by both sides of the
// proxy. The cipher-suite list is intentionally narrow (one suite, the
// one the legacy server negotiates) because anything else would force
// a server-side change to interoperate. The Connection-ID generator
// differs per role: clients only need to send a CID, servers must
// accept one and round-trip it.
//
// Callers wrap the returned config with dtls.Client (RoleClient) or
// dtls.Listen (RoleServer) and provide their own context / timeouts.
func NewDTLSConfig(role Role, cert tls.Certificate) *dtls.Config {
	cfg := &dtls.Config{
		Certificates:         []tls.Certificate{cert},
		InsecureSkipVerify:   true,
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		CipherSuites:         []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
	}
	switch role {
	case RoleClient:
		cfg.ConnectionIDGenerator = dtls.OnlySendCIDGenerator()
	case RoleServer:
		cfg.ConnectionIDGenerator = dtls.RandomCIDGenerator(CIDSize)
	}
	return cfg
}

// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package framing_test

import (
	"testing"

	"github.com/pion/dtls/v3"

	"github.com/PavelLizunov/wgturn-core/internal/framing"
)

// TestNewDTLSConfig_Common pins the wire-relevant invariants both
// roles share: the cipher list is exactly the one the legacy server
// negotiates, extended-master-secret is required, and a certificate
// is attached. Anything looser would change interop behaviour.
func TestNewDTLSConfig_Common(t *testing.T) {
	cert, err := framing.GenerateCertificate()
	if err != nil {
		t.Fatalf("GenerateCertificate: %v", err)
	}

	for name, role := range map[string]framing.Role{
		"client": framing.RoleClient,
		"server": framing.RoleServer,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := framing.NewDTLSConfig(role, cert)
			if cfg == nil {
				t.Fatal("NewDTLSConfig returned nil")
			}
			if !cfg.InsecureSkipVerify {
				t.Error("InsecureSkipVerify must be true; DTLS here is opaque framing, not auth")
			}
			if cfg.ExtendedMasterSecret != dtls.RequireExtendedMasterSecret {
				t.Errorf("ExtendedMasterSecret = %v, want RequireExtendedMasterSecret", cfg.ExtendedMasterSecret)
			}
			if len(cfg.CipherSuites) != 1 || cfg.CipherSuites[0] != dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 {
				t.Errorf("CipherSuites = %v, want [TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256]", cfg.CipherSuites)
			}
			if len(cfg.Certificates) != 1 {
				t.Errorf("Certificates len = %d, want 1", len(cfg.Certificates))
			}
			if cfg.ConnectionIDGenerator == nil {
				t.Error("ConnectionIDGenerator must be set; both roles use connection-IDs")
			}
		})
	}
}

// TestNewDTLSConfig_ServerCID checks the server's CID generator is in
// the size we advertise to clients (8 bytes). The check is that the
// generator returns CIDSize-length identifiers — generators internal
// state is otherwise opaque.
func TestNewDTLSConfig_ServerCID(t *testing.T) {
	cert, err := framing.GenerateCertificate()
	if err != nil {
		t.Fatalf("GenerateCertificate: %v", err)
	}
	cfg := framing.NewDTLSConfig(framing.RoleServer, cert)
	cid := cfg.ConnectionIDGenerator()
	if len(cid) != framing.CIDSize {
		t.Errorf("server CID length = %d, want %d", len(cid), framing.CIDSize)
	}
}

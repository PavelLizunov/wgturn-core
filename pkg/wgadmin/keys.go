// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgadmin

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// Keypair carries a WireGuard X25519 private/public key pair, both
// in the standard 44-character base64 form `wg-tools` produces.
type Keypair struct {
	Private string
	Public  string
}

// GenerateKeypair returns a fresh WG-compatible private/public key
// pair. Equivalent to `wg genkey | tee | wg pubkey` end-to-end:
//
//   - 32 random bytes from crypto/rand
//   - WG's curve25519 clamping (sk[0] &= 248; sk[31] &= 127; sk[31] |= 64)
//   - public key = X25519(sk, basepoint)
//   - both encoded as 44-char standard base64
//
// Embedders can call this without having `wireguard-tools` on the
// host, which matters when the server bin runs in a slim container.
func GenerateKeypair() (Keypair, error) {
	var sk [32]byte
	if _, err := rand.Read(sk[:]); err != nil {
		return Keypair{}, fmt.Errorf("wgadmin: rand: %w", err)
	}
	// WG's curve25519 clamping. Without these bits the public key
	// isn't on the right subgroup and existing wg implementations
	// reject the peer with an obscure protocol error.
	sk[0] &= 248
	sk[31] &= 127
	sk[31] |= 64

	pkBytes, err := curve25519.X25519(sk[:], curve25519.Basepoint)
	if err != nil {
		return Keypair{}, fmt.Errorf("wgadmin: derive public: %w", err)
	}
	return Keypair{
		Private: base64.StdEncoding.EncodeToString(sk[:]),
		Public:  base64.StdEncoding.EncodeToString(pkBytes),
	}, nil
}

// GeneratePresharedKey returns a fresh WG preshared key, 32 random
// bytes encoded base64. Equivalent to `wg genpsk`. PSKs are
// symmetric and have no public counterpart.
func GeneratePresharedKey() (string, error) {
	var psk [32]byte
	if _, err := rand.Read(psk[:]); err != nil {
		return "", fmt.Errorf("wgadmin: rand: %w", err)
	}
	return base64.StdEncoding.EncodeToString(psk[:]), nil
}

// PublicKeyFor derives the public key from a base64 WG private key.
// Useful when the server's PrivateKey is already in wg0.conf and
// only the public is needed (for emitting share URLs).
func PublicKeyFor(privBase64 string) (string, error) {
	sk, err := base64.StdEncoding.DecodeString(privBase64)
	if err != nil {
		return "", fmt.Errorf("wgadmin: decode private key: %w", err)
	}
	if len(sk) != 32 {
		return "", fmt.Errorf("wgadmin: private key must be 32 bytes, got %d", len(sk))
	}
	pkBytes, err := curve25519.X25519(sk, curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("wgadmin: derive public: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pkBytes), nil
}

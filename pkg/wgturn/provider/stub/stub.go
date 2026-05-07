// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package stub provides a no-network CredentialsProvider for tests and
// local smoke runs. It returns a fixed set of credentials immediately.
package stub

import (
	"context"
	"sync/atomic"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// Provider is a CredentialsProvider that returns a constant Credentials
// value. Useful for unit tests and CI smoke tests where no real VK / WB
// API is available.
//
// Concurrent calls are safe; each call increments Calls by 1.
type Provider struct {
	// Creds is the value returned from every Fetch call.
	Creds wgturn.Credentials

	// Err, if non-nil, is returned from Fetch instead of Creds.
	Err error

	// Calls counts the total Fetch invocations across all goroutines.
	Calls atomic.Int64
}

// Fetch satisfies wgturn.CredentialsProvider.
func (p *Provider) Fetch(ctx context.Context, _ string, _ int) (wgturn.Credentials, error) {
	p.Calls.Add(1)
	if p.Err != nil {
		return wgturn.Credentials{}, p.Err
	}
	if err := ctx.Err(); err != nil {
		return wgturn.Credentials{}, err
	}
	return p.Creds, nil
}

// New is a small constructor convenience for tests:
//
//	p := stub.New("user", "pass", "turn.example.com:3478")
func New(user, pass, server string) *Provider {
	return &Provider{
		Creds: wgturn.Credentials{
			Username:   user,
			Password:   pass,
			ServerAddr: server,
		},
	}
}

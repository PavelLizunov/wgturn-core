// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturn

import (
	"context"
	"errors"
	"time"
)

// Credentials is one set of TURN authentication parameters as returned
// by a CredentialsProvider.
type Credentials struct {
	// Username and Password are the TURN long-term credentials. They are
	// passed verbatim to the underlying TURN client (pion/turn).
	Username string
	Password string

	// ServerAddr is the host:port of the TURN server. Both host and
	// port must be present; the host may be a hostname or IP.
	ServerAddr string

	// ExpiresIn, if non-zero, hints how long the credentials remain
	// valid. The cache will refresh slightly before this deadline.
	// If zero, the cache uses its default lifetime.
	ExpiresIn time.Duration
}

// CredentialsProvider is the abstraction over "where do I get TURN
// credentials from". Concrete providers (VK Calls, WB stream, manual,
// stub) implement this interface; the Tunnel does not know about VK.
//
// Fetch is allowed to block on network I/O. Implementations must respect
// ctx cancellation.
//
// hint is the per-call discriminator: for VK Calls it is the call link;
// for stub providers it is ignored. streamID is the index of the stream
// requesting credentials (0..Streams-1) — useful if a provider wants to
// rotate credentials per stream.
type CredentialsProvider interface {
	Fetch(ctx context.Context, hint string, streamID int) (Credentials, error)
}

// ProviderFunc adapts a plain function to CredentialsProvider.
type ProviderFunc func(ctx context.Context, hint string, streamID int) (Credentials, error)

// Fetch satisfies the CredentialsProvider interface.
func (f ProviderFunc) Fetch(ctx context.Context, hint string, streamID int) (Credentials, error) {
	return f(ctx, hint, streamID)
}

// ErrAuthFailure should be returned by a CredentialsProvider when the
// credentials it has just produced (or that it knows about) are
// rejected by the upstream API in a way that retrying the SAME provider
// SAME hint will not fix. Used as a signal for the cache to invalidate
// without retrying.
var ErrAuthFailure = errors.New("wgturn: credentials rejected (auth failure)")

// ErrCaptchaRequired should be returned by a CredentialsProvider when it
// hit a captcha or similar interactive challenge it cannot resolve
// programmatically. Callers (typically a UI) can intercept this to
// prompt the user.
var ErrCaptchaRequired = errors.New("wgturn: captcha required")

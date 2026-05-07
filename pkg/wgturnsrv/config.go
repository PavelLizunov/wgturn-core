// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv

import (
	"errors"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// Defaults for Config.* timeouts. Public so tests and embedders can
// reach them; pinned to the legacy server's behaviour.
const (
	// DefaultHandshakeTimeout bounds how long we wait for a client to
	// send the 17-byte session+stream preamble after the DTLS handshake
	// completes. Longer reads beyond this are treated as a stalled or
	// half-broken session.
	DefaultHandshakeTimeout = 30 * time.Second

	// DefaultStreamReadTimeout is the per-Read deadline applied while
	// pumping payload between a DTLS stream and the backend. A read
	// that takes longer than this means the TURN allocation behind the
	// stream has likely expired; tear the stream down and let the
	// client reconnect.
	DefaultStreamReadTimeout = 5 * time.Minute

	// DefaultBackendWriteTimeout caps how long a single Write to the
	// backend (typically a UDP socket to wg0) may block. UDP writes
	// only block on socket-buffer pressure, but pinning the deadline
	// keeps a wedged backend from stalling the demuxer.
	DefaultBackendWriteTimeout = 5 * time.Second
)

// ErrInvalidConfig is returned by New when Config fails validation.
// Callers can errors.Is it for setup-time error handling.
var ErrInvalidConfig = errors.New("wgturnsrv: invalid config")

// Config controls a Server. ListenAddr and Backend are mandatory; the
// rest fall back to the Default* constants when zero. Validation
// happens inside New so a misconfigured Config never starts a listener.
type Config struct {
	// ListenAddr is the UDP address the DTLS listener binds to, in
	// "host:port" form. Use ":56000" to listen on every interface;
	// "127.0.0.1:0" picks an ephemeral port (useful for tests).
	ListenAddr string

	// Backend opens a per-session connection to whatever lives behind
	// the proxy. The Server reads peer→client packets from it and
	// writes client→peer packets into it.
	Backend Backend

	// Logger receives lifecycle and error messages. Nil means
	// wgturn.NoopLogger; everything is silenced.
	Logger wgturn.Logger

	// HandshakeTimeout overrides DefaultHandshakeTimeout when non-zero.
	HandshakeTimeout time.Duration

	// StreamReadTimeout overrides DefaultStreamReadTimeout when non-zero.
	StreamReadTimeout time.Duration

	// BackendWriteTimeout overrides DefaultBackendWriteTimeout when
	// non-zero.
	BackendWriteTimeout time.Duration
}

// validate returns ErrInvalidConfig wrapped with a context-specific
// message when c has missing or nonsensical fields. Defaults that have
// concrete fall-backs are NOT validated here — see withDefaults.
func (c Config) validate() error {
	if c.ListenAddr == "" {
		return errInvalid("ListenAddr is required")
	}
	if c.Backend == nil {
		return errInvalid("Backend is required")
	}
	return nil
}

// withDefaults returns a copy of c with zero-valued optional fields
// filled in from the Default* constants.
func (c Config) withDefaults() Config {
	if c.Logger == nil {
		c.Logger = wgturn.NoopLogger{}
	}
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if c.StreamReadTimeout <= 0 {
		c.StreamReadTimeout = DefaultStreamReadTimeout
	}
	if c.BackendWriteTimeout <= 0 {
		c.BackendWriteTimeout = DefaultBackendWriteTimeout
	}
	return c
}

// errInvalid wraps msg with ErrInvalidConfig so callers can use
// errors.Is(err, ErrInvalidConfig) for setup-time error handling.
func errInvalid(msg string) error {
	return wrappedErr{base: ErrInvalidConfig, detail: msg}
}

// wrappedErr is a small helper that chains ErrInvalidConfig with a
// human-readable detail. Keeping it private avoids cluttering the
// public API surface.
type wrappedErr struct {
	base   error
	detail string
}

func (w wrappedErr) Error() string { return w.base.Error() + ": " + w.detail }
func (w wrappedErr) Unwrap() error { return w.base }

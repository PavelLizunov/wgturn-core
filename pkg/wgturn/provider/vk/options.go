// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"net/http"

	"github.com/slovn/wgturn-core/pkg/wgturn"
)

// Option mutates a Provider during construction. Apply via vk.New(opts...).
type Option func(*Provider)

// WithHTTPClient overrides the default HTTP client. The client is
// reused across all Fetch calls. If you need socket protection on
// Android/iOS, use WithProtector instead — passing a custom client
// without a Control hook will leak credentials traffic into the host
// VPN.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.httpClient = c }
}

// WithProtector wires a SocketProtector into the default http.Client's
// dialer Control hook. Mutually exclusive with WithHTTPClient (the
// last option wins).
func WithProtector(sp wgturn.SocketProtector) Option {
	return func(p *Provider) { p.httpClient = newDefaultHTTPClient(sp) }
}

// WithLogger sets the logger. Default: wgturn.NoopLogger.
func WithLogger(l wgturn.Logger) Option {
	return func(p *Provider) {
		if l != nil {
			p.logger = l
		}
	}
}

// WithAppCredentials overrides the VK Web App id and client secret.
// Use only if VK rotates the public anonymous app credentials and
// the constants in api.go become stale.
func WithAppCredentials(appID, clientSecret string) Option {
	return func(p *Provider) {
		p.appID = appID
		p.clientSec = clientSecret
	}
}

// WithHostsForTest overrides the VK / OK endpoint hosts. Public and
// safe — but normally needed only by tests that point everything at
// a httptest server. Pass empty strings to keep the existing value
// for that host.
func WithHostsForTest(login, api, ok string) Option {
	return func(p *Provider) {
		if login != "" {
			p.hosts.login = login
		}
		if api != "" {
			p.hosts.api = api
		}
		if ok != "" {
			p.hosts.ok = ok
		}
	}
}

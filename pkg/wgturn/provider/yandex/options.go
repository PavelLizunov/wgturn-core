// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package yandex

import (
	"net/http"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// Option mutates a Provider during construction. Apply via yandex.New(opts...).
type Option func(*Provider)

// WithHTTPClient overrides the default HTTP client. The client is reused
// across all Fetch calls. If you need socket protection on Android/iOS,
// use WithProtector instead — passing a custom client without a Control
// hook will leak credentials traffic into the host VPN.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.httpClient = c }
}

// WithProtector wires a SocketProtector into the default client's
// dialer Control hook. Mutually exclusive with WithHTTPClient (last
// option wins).
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

// WithHostsForTest overrides the cloud-api host. Tests typically pass
// the URL of a httptest server here; in production leave as-is.
func WithHostsForTest(cloudAPI string) Option {
	return func(p *Provider) {
		if cloudAPI != "" {
			p.cloudAPI = cloudAPI
		}
	}
}

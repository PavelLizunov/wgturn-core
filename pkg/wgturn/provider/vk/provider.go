// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/slovn/wgturn-core/pkg/wgturn"
)

// Provider is a wgturn.CredentialsProvider that obtains TURN
// credentials from VK Calls' anonymous-token API. It's safe to share
// across goroutines; concurrent Fetch calls reuse the same
// *http.Client and pick a fresh browser profile each time.
type Provider struct {
	httpClient *http.Client
	logger     wgturn.Logger
	appID      string
	clientSec  string
	hosts      apiHosts
	captcha    CaptchaSolver
}

// New constructs a Provider with the given options applied. With no
// options it picks reasonable defaults (30 s HTTP timeout, NoopLogger,
// VK's public anonymous app credentials, no SocketProtector).
func New(opts ...Option) *Provider {
	p := &Provider{
		logger:    wgturn.NoopLogger{},
		appID:     defaultAppID,
		clientSec: defaultClientSecret,
		hosts:     defaultHosts(),
		captcha:   rejectingSolver{},
	}
	for _, o := range opts {
		o(p)
	}
	if p.httpClient == nil {
		p.httpClient = newDefaultHTTPClient(nil)
	}
	return p
}

// Fetch satisfies wgturn.CredentialsProvider.
//
// hint must be a VK call link of the form
// "https://vk.com/call/join/<id>" (or just the bare id). streamID is
// passed for diagnostic logging only — every Fetch call performs a
// fresh full handshake; the cache layer above us decides when to
// reuse the result across streams.
func (p *Provider) Fetch(ctx context.Context, hint string, streamID int) (wgturn.Credentials, error) {
	callID, err := extractCallID(hint)
	if err != nil {
		return wgturn.Credentials{}, fmt.Errorf("vk: parse hint %q: %w", hint, err)
	}
	p.logger.Debugf("[vk] stream=%d fetching creds for call_id=%s", streamID, callID)

	api := &apiClient{
		http:      p.httpClient,
		profile:   pickProfile(),
		logger:    p.logger,
		appID:     p.appID,
		clientSec: p.clientSec,
		hosts:     p.hosts,
		captcha:   p.captcha,
	}

	user, pass, addr, err := api.fetchTurn(ctx, callID)
	if err != nil {
		p.logger.Warnf("[vk] stream=%d fetch failed: %v", streamID, err)
		return wgturn.Credentials{}, err
	}
	p.logger.Infof("[vk] stream=%d fetched: turn=%s user=%s", streamID, addr, user)

	return wgturn.Credentials{
		Username:   user,
		Password:   pass,
		ServerAddr: addr,
		// VK's TURN credentials are typically valid ~10 minutes; the
		// outer creds cache will rotate before that anyway. Leave
		// ExpiresIn at zero to defer to cache defaults.
	}, nil
}

// newDefaultHTTPClient returns the *http.Client used when the embedder
// does not supply one. The Control hook is wired through the optional
// SocketProtector so that on a host VPN the credential traffic does
// not loop through the very tunnel we're trying to bring up.
//
// The Transport is a utls-backed RoundTripper that mimics a recent
// Chrome JA3 fingerprint. VK's anonymous-token API rejects requests
// from Go's default crypto/tls handshake with a captcha challenge
// regardless of UA / Sec-CH-UA headers; the JA3 forgery is mandatory
// for the flow to succeed without a captcha solver.
//
// Tests that point the provider at httptest.NewServer (HTTP, not
// HTTPS) transparently fall through to the stdlib transport.
func newDefaultHTTPClient(p wgturn.SocketProtector) *http.Client {
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	if p != nil {
		d.Control = wgturn.ControlFunc(p)
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: newUTLSTransport(d),
	}
}

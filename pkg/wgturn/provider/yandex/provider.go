// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package yandex

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/slovn/wgturn-core/pkg/wgturn"
)

// cloudAPIHost is the production Telemost API origin.
const cloudAPIHost = "https://cloud-api.yandex.ru"

// Provider is a wgturn.CredentialsProvider that obtains TURN
// credentials from Yandex Telemost's anonymous-conference API. Safe to
// share across goroutines.
type Provider struct {
	httpClient *http.Client
	logger     wgturn.Logger
	cloudAPI   string
}

// New constructs a Provider with the given options applied. With no
// options it picks reasonable defaults (30 s HTTP timeout, NoopLogger,
// the production cloud-api.yandex.ru host).
func New(opts ...Option) *Provider {
	p := &Provider{
		logger:   wgturn.NoopLogger{},
		cloudAPI: cloudAPIHost,
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
// hint must be a Telemost invite link of the form
// "https://telemost.yandex.ru/j/<id>" (or the bare id, or "telemost:<id>").
//
// NOTE: As of 2026-05 Yandex Telemost's TURN cluster is a walled
// garden — see the package doc-comment. Credentials come back fine,
// but the relay refuses to forward to non-Yandex peer IPs. A WARN log
// fires once on every successful fetch to remind the operator.
func (p *Provider) Fetch(ctx context.Context, hint string, streamID int) (wgturn.Credentials, error) {
	callID, err := extractCallID(hint)
	if err != nil {
		return wgturn.Credentials{}, fmt.Errorf("yandex: parse hint %q: %w", hint, err)
	}
	p.logger.Debugf("[yandex] stream=%d fetching creds for call_id=%s", streamID, callID)

	user, pass, addr, err := fetchTurn(ctx, p.httpClient, p.cloudAPI, callID, p.logger)
	if err != nil {
		p.logger.Warnf("[yandex] stream=%d fetch failed: %v", streamID, err)
		return wgturn.Credentials{}, err
	}
	p.logger.Infof("[yandex] stream=%d fetched: turn=%s user=%s", streamID, addr, user)
	p.logger.Warnf("[yandex] stream=%d note: Yandex Telemost TURN is peer-IP-restricted; "+
		"DTLS handshake to a non-Yandex server WILL fail. See pkg doc.", streamID)

	return wgturn.Credentials{
		Username:   user,
		Password:   pass,
		ServerAddr: addr,
		// Yandex Telemost TURN credentials typically expire after ~10
		// minutes; let the outer creds cache rotate as it sees fit.
	}, nil
}

// newDefaultHTTPClient returns the *http.Client used when the embedder
// does not supply one. Like the VK provider, we wire a SocketProtector
// into the dialer Control hook so credential traffic doesn't loop
// through the very tunnel we're bringing up. We do NOT use utls here —
// Yandex's API doesn't fingerprint TLS as aggressively as VK's.
func newDefaultHTTPClient(p wgturn.SocketProtector) *http.Client {
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	if p != nil {
		d.Control = wgturn.ControlFunc(p)
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:           d.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

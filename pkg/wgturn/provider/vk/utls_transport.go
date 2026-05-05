// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	utls "github.com/refraction-networking/utls"
)

// utlsRoundTripper performs TLS handshakes via refraction-networking/utls
// instead of crypto/tls, so the JA3 fingerprint matches a real Chrome
// instead of Go's static one.
//
// VK Calls' API gates the anonymous-token endpoints behind a JA3 check:
// any request from Go's default crypto/tls is challenged with a captcha
// (error_code 14, "Captcha needed") regardless of the User-Agent header.
// This RoundTripper makes the wire-level handshake look like a recent
// Chrome (133 by default), which lets the request through.
//
// Connections are NOT pooled — each RoundTrip opens a fresh TLS conn
// and closes it on Body.Close. Acceptable for our 5-step credentials
// flow which fires once every ~10 minutes per cache group; pooling
// adds complexity without measurable benefit at that rate.
//
// Plain-HTTP requests (e.g. httptest servers used in unit tests) are
// delegated to a wrapped http.Transport so we don't break tests that
// don't bother with TLS.
type utlsRoundTripper struct {
	dialer   *net.Dialer
	helloID  utls.ClientHelloID
	fallback http.RoundTripper
}

// newUTLSTransport returns a RoundTripper using utls for HTTPS and the
// stdlib transport for HTTP. dialer is reused for both paths; pass nil
// for sane defaults (20 s connect / 30 s keepalive).
func newUTLSTransport(d *net.Dialer) *utlsRoundTripper {
	if d == nil {
		d = &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	}
	fb := http.DefaultTransport.(*http.Transport).Clone()
	fb.DialContext = d.DialContext
	fb.MaxIdleConns = 16
	fb.IdleConnTimeout = 90 * time.Second
	return &utlsRoundTripper{
		dialer:   d,
		helloID:  utls.HelloChrome_Auto,
		fallback: fb,
	}
}

// RoundTrip satisfies http.RoundTripper.
func (rt *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return rt.fallback.RoundTrip(req)
	}

	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	rawConn, err := rt.dialer.DialContext(req.Context(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("utls dial %s: %w", addr, err)
	}

	uconn := utls.UClient(rawConn, &utls.Config{
		ServerName: host,
		// Stay on HTTP/1.1: VK's API endpoints don't benefit from h2,
		// and supporting h2 would require a much larger transport.
		NextProtos: []string{"http/1.1"},
	}, rt.helloID)

	// HelloChrome_Auto's ALPN extension advertises ["h2","http/1.1"];
	// even if our Config.NextProtos is "http/1.1", many servers still
	// pick h2 from the ClientHello list. Force the ALPN extension to
	// http/1.1-only by walking the spec after BuildHandshakeState.
	if err := uconn.BuildHandshakeState(); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("utls build state %s: %w", host, err)
	}
	for _, ext := range uconn.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}

	if err := uconn.HandshakeContext(req.Context()); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("utls handshake %s: %w", host, err)
	}

	// Tell the server we don't intend to reuse this connection. Cleaner
	// in logs and matches the per-request lifetime of our transport.
	reqCopy := *req
	reqCopy.Close = true

	if err := reqCopy.Write(uconn); err != nil {
		_ = uconn.Close()
		return nil, fmt.Errorf("utls write request: %w", err)
	}

	bufr := bufio.NewReader(uconn)
	resp, err := http.ReadResponse(bufr, &reqCopy)
	if err != nil {
		_ = uconn.Close()
		return nil, fmt.Errorf("utls read response: %w", err)
	}

	// http.Transport auto-decompresses gzip when DisableCompression is
	// false. We don't get that for free with manual ReadResponse, so
	// handle it here. We advertise gzip/deflate/br/zstd in Accept-Encoding
	// to look like Chrome; the server may pick any of them.
	if enc := strings.ToLower(resp.Header.Get("Content-Encoding")); enc != "" {
		decoded, err := wrapDecompressor(resp.Body, enc)
		if err != nil {
			_ = uconn.Close()
			return nil, fmt.Errorf("utls decompress %s: %w", enc, err)
		}
		resp.Body = decoded
		// Once we decompress, neither header nor length describe the
		// post-decode size. Strip the header and force chunked-style
		// "unknown length" so callers reading-to-EOF still work.
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
	}

	resp.Body = &utlsResponseBody{ReadCloser: resp.Body, conn: uconn}
	return resp, nil
}

// wrapDecompressor returns a ReadCloser that decompresses the given
// content-encoded body. Closing the wrapper closes the underlying body.
func wrapDecompressor(body io.ReadCloser, enc string) (io.ReadCloser, error) {
	switch strings.TrimSpace(enc) {
	case "gzip":
		gz, err := gzip.NewReader(body)
		if err != nil {
			return nil, err
		}
		return &decompReadCloser{Reader: gz, base: body, closer: gz}, nil
	case "deflate":
		fl := flate.NewReader(body)
		return &decompReadCloser{Reader: fl, base: body, closer: fl}, nil
	case "br":
		return &decompReadCloser{Reader: brotli.NewReader(body), base: body}, nil
	case "zstd":
		zr, err := zstd.NewReader(body)
		if err != nil {
			return nil, err
		}
		return &decompReadCloser{Reader: zr, base: body, closer: zstdCloser{zr}}, nil
	case "identity", "":
		return body, nil
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", enc)
	}
}

// decompReadCloser plumbs Read to the decompressor and Close to both
// the decompressor (when it has Close) and the underlying body.
type decompReadCloser struct {
	io.Reader
	base   io.Closer
	closer io.Closer
}

func (d *decompReadCloser) Close() error {
	var err error
	if d.closer != nil {
		err = d.closer.Close()
	}
	if e := d.base.Close(); e != nil && err == nil {
		err = e
	}
	return err
}

// zstdCloser adapts (*zstd.Decoder) to io.Closer (its Close has no return).
type zstdCloser struct{ z *zstd.Decoder }

func (c zstdCloser) Close() error { c.z.Close(); return nil }

// utlsResponseBody wraps the response body so closing it also closes
// the underlying utls connection — required because we don't pool.
type utlsResponseBody struct {
	io.ReadCloser
	conn net.Conn
	once sync.Once
	err  error
}

// Close drains and closes both the body and the underlying TLS conn.
func (b *utlsResponseBody) Close() error {
	b.once.Do(func() {
		if cerr := b.ReadCloser.Close(); cerr != nil {
			b.err = cerr
		}
		if cerr := b.conn.Close(); cerr != nil && b.err == nil {
			b.err = cerr
		}
	})
	return b.err
}

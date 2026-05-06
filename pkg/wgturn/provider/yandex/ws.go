// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package yandex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/slovn/wgturn-core/pkg/wgturn"
)

// wsFetchTurn opens a WebSocket to the conference's media_server_url,
// sends a hello frame, and reads back the ServerHello whose
// rtc_configuration.ice_servers carry the TURN credentials.
//
// The exact WS protocol is documented poorly by Yandex; what's
// implemented here mirrors the shape used by the public Telemost web
// client (telemost.yandex.ru SPA, observed 2026-04). The hello
// payload is intentionally minimal — Telemost is permissive about
// missing optional fields. The reply parser scans every frame for
// fields named ice_servers / iceServers anywhere in the JSON tree, so
// minor schema drift between deployments doesn't break us.
func wsFetchTurn(ctx context.Context, hc *http.Client, conf *conferenceResponse, log wgturn.Logger) (string, string, string, error) {
	wsURL := conf.MediaServerURL
	if wsURL == "" {
		return "", "", "", errors.New("media_server_url is empty")
	}

	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	hdr := http.Header{}
	hdr.Set("Origin", "https://telemost.yandex.ru")
	hdr.Set("User-Agent", defaultUA)
	conn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPClient: hc,
		HTTPHeader: hdr,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("ws dial: %w", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(2 * 1024 * 1024)

	hello := map[string]any{
		"uid": uuid.New().String(),
		"hello": map[string]any{
			"room_id":      conf.RoomID,
			"peer_id":      conf.PeerID,
			"capabilities": []string{"webrtc", "ice", "dtls"},
			"sdk": map[string]any{
				"name":     "telemost-web",
				"version":  "1.0.0",
				"platform": "web",
			},
			"token": "",
		},
	}
	if conf.Credentials != nil {
		hello["hello"].(map[string]any)["token"] = conf.Credentials.Token
	}

	body, err := json.Marshal(hello)
	if err != nil {
		return "", "", "", fmt.Errorf("ws marshal hello: %w", err)
	}
	log.Debugf("[yandex] ws hello (%d bytes)", len(body))
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		return "", "", "", fmt.Errorf("ws write hello: %w", err)
	}

	// Read frames for up to 15 s, scanning each for ice_servers.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, rcancel := context.WithDeadline(ctx, deadline)
		_, raw, err := conn.Read(readCtx)
		rcancel()
		if err != nil {
			return "", "", "", fmt.Errorf("ws read: %w", err)
		}
		if u, p, addr, ok := scanFrameForTurn(raw); ok {
			return u, p, addr, nil
		}
		// keep reading — the first frame is usually a non-hello ack.
	}
	return "", "", "", errors.New("ws: no ice_servers in any frame within deadline")
}

// scanFrameForTurn walks an arbitrary JSON object looking for ICE
// server entries. It tolerates schema drift: as long as somewhere in
// the tree there is an array of objects with a "urls" + "username" +
// "credential" trio (where at least one urls entry is turn:/turns:),
// it returns the first match.
func scanFrameForTurn(b []byte) (string, string, string, bool) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", "", "", false
	}
	return walkForTurn(v)
}

func walkForTurn(v any) (string, string, string, bool) {
	switch x := v.(type) {
	case []any:
		// Array of ice server objects?
		for _, e := range x {
			if u, p, addr, ok := tryICEEntry(e); ok {
				return u, p, addr, true
			}
		}
		// Recurse.
		for _, e := range x {
			if u, p, addr, ok := walkForTurn(e); ok {
				return u, p, addr, true
			}
		}
	case map[string]any:
		// First, look for a known ice_servers field at this level.
		for _, key := range []string{"ice_servers", "iceServers", "ice_server", "iceserver"} {
			if entries, ok := x[key]; ok {
				if u, p, addr, ok := walkForTurn(entries); ok {
					return u, p, addr, true
				}
			}
		}
		// If THIS object looks like an ICE server, take it.
		if u, p, addr, ok := tryICEEntry(x); ok {
			return u, p, addr, true
		}
		// Recurse into all children.
		for _, child := range x {
			if u, p, addr, ok := walkForTurn(child); ok {
				return u, p, addr, true
			}
		}
	}
	return "", "", "", false
}

func tryICEEntry(v any) (string, string, string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", "", "", false
	}
	urls, ok := m["urls"]
	if !ok {
		urls = m["url"]
	}
	if urls == nil {
		return "", "", "", false
	}
	username, _ := m["username"].(string)
	credential, _ := m["credential"].(string)
	if username == "" || credential == "" {
		return "", "", "", false
	}
	switch u := urls.(type) {
	case string:
		if !isTurnURL(u) {
			return "", "", "", false
		}
		addr, err := parseTurnURL(u)
		if err != nil {
			return "", "", "", false
		}
		return username, credential, addr, true
	case []any:
		for _, e := range u {
			s, ok := e.(string)
			if !ok || !isTurnURL(s) {
				continue
			}
			addr, err := parseTurnURL(s)
			if err != nil {
				continue
			}
			return username, credential, addr, true
		}
	}
	return "", "", "", false
}

// String-search fallback when the JSON parser can't recurse into a
// frame (e.g. compressed / binary content). Not currently wired but
// kept for potential use when probing a real Telemost session.
//
//nolint:unused // diagnostic helper
func grepTurnURL(buf []byte) string {
	s := string(buf)
	for _, prefix := range []string{"turn:", "turns:"} {
		if i := strings.Index(s, prefix); i >= 0 {
			rest := s[i:]
			end := strings.IndexAny(rest, "\" \t,]}")
			if end < 0 {
				end = len(rest)
			}
			return rest[:end]
		}
	}
	return ""
}

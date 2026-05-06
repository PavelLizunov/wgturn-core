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

// wsFetchTurn drives the GOLOOM media-server handshake on
// wss://goloom.strm.yandex.net/join (or whatever step 1 returned in
// client_configuration.media_server_url).
//
// Wire protocol — observed against telemost.yandex.ru's web SPA,
// 2026-Q2:
//
//   - Frames are JSON objects, each carrying a top-level "uid" plus a
//     payload field that names the message kind ("hello", "ack",
//     "serverHello", "subscriberSdpOffer", "webrtcIceCandidate", …).
//
//   - When the server sends a non-ack frame, the client must echo
//     back {"uid": <theirs>, "ack": {"status": {"code":"OK","description":""}}}.
//
//   - The ICE servers we want live in
//     serverHello.rtcConfiguration.iceServers — but the walker is
//     schema-tolerant: it scans the entire JSON tree for any object
//     with a urls/username/credential trio (where urls includes a
//     turn: or turns: entry), so minor field-name drift between
//     Telemost deployments doesn't break us.
func wsFetchTurn(ctx context.Context, hc *http.Client, conf *conferenceResponse, log wgturn.Logger) (string, string, string, error) {
	wsURL := conf.ClientConfiguration.MediaServerURL
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
	conn.SetReadLimit(4 * 1024 * 1024)

	// Build the hello payload. The shape mirrors what telemost's web
	// SPA dispatches on the WebSocket open event (extracted from
	// the live JS bundle, May 2026). capabilitiesOffer={} works in
	// practice — the server fills sensible defaults into
	// capabilitiesAnswer and proceeds. participantId / roomId /
	// credentials all come straight from step 1.
	helloUID := uuid.NewString()
	hello := map[string]any{
		"uid": helloUID,
		"hello": map[string]any{
			"participantMeta": map[string]any{
				"name":        "Guest",
				"role":        "SPEAKER",
				"description": "",
				"sendAudio":   false,
				"sendVideo":   false,
			},
			"participantAttributes": map[string]any{
				"name":        "Guest",
				"role":        "SPEAKER",
				"description": "",
			},
			"sendAudio":           false,
			"sendVideo":           false,
			"sendSharing":         false,
			"participantId":       conf.PeerID,
			"roomId":              conf.RoomID,
			"serviceName":         conf.ClientConfiguration.ServiceName,
			"credentials":         conf.Credentials,
			"capabilitiesOffer":   map[string]any{},
			"sdkInfo":             map[string]any{"implementation": "browser", "version": "1.0.0"},
			"sdkInitializationId": uuid.NewString(),
		},
	}
	if hello["hello"].(map[string]any)["serviceName"] == "" {
		hello["hello"].(map[string]any)["serviceName"] = "telemost"
	}
	body, err := json.Marshal(hello)
	if err != nil {
		return "", "", "", fmt.Errorf("ws marshal hello: %w", err)
	}
	log.Debugf("[yandex] ws hello (%d bytes, peer=%s room=%s)", len(body), conf.PeerID, conf.RoomID)
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		return "", "", "", fmt.Errorf("ws write hello: %w", err)
	}

	// Read frames for up to 15 s. Ack any non-ack frame the server
	// sends, scan each for ICE servers carrying a turn:/turns: URL.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, rcancel := context.WithDeadline(ctx, deadline)
		_, raw, err := conn.Read(readCtx)
		rcancel()
		if err != nil {
			return "", "", "", fmt.Errorf("ws read: %w", err)
		}
		var probe struct {
			UID string          `json:"uid"`
			Ack json.RawMessage `json:"ack,omitempty"`
		}
		_ = json.Unmarshal(raw, &probe)
		// Echo an ack for any frame the server sent that isn't itself
		// just an ack. Skip the server's ack of OUR hello (we sent
		// helloUID, server replies with the same uid).
		if probe.UID != "" && len(probe.Ack) == 0 && probe.UID != helloUID {
			ackBody := mustMarshal(map[string]any{
				"uid": probe.UID,
				"ack": map[string]any{"status": map[string]any{"code": "OK", "description": ""}},
			})
			if err := conn.Write(ctx, websocket.MessageText, ackBody); err != nil {
				log.Debugf("[yandex] ws ack write: %v (continuing)", err)
			}
		}
		if u, p, addr, ok := scanFrameForTurn(raw); ok {
			return u, p, addr, nil
		}
	}
	return "", "", "", errors.New("ws: no turn ice_servers within deadline")
}

// scanFrameForTurn walks an arbitrary JSON object looking for ICE
// server entries. As long as somewhere in the tree there is an array
// of objects with a "urls" + "username" + "credential" trio (with
// at least one urls entry being turn:/turns:), it returns the first
// match.
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
		for _, e := range x {
			if u, p, addr, ok := tryICEEntry(e); ok {
				return u, p, addr, true
			}
		}
		for _, e := range x {
			if u, p, addr, ok := walkForTurn(e); ok {
				return u, p, addr, true
			}
		}
	case map[string]any:
		for _, key := range []string{"iceServers", "ice_servers", "iceServer", "ice_server"} {
			if entries, ok := x[key]; ok {
				if u, p, addr, ok := walkForTurn(entries); ok {
					return u, p, addr, true
				}
			}
		}
		if u, p, addr, ok := tryICEEntry(x); ok {
			return u, p, addr, true
		}
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

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// grepTurnURL is a string-search fallback for diagnostic dumps. Not
// wired into the production path — kept because it's useful when
// probing future Telemost wire-format changes.
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

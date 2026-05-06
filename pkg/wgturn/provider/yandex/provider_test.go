// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package yandex_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/slovn/wgturn-core/pkg/wgturn"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/yandex"
)

// TestProvider_HappyPath_InlineCreds — older Telemost deployments
// shipped TURN ice_servers in the bootstrap response; we still
// honour that path as a fast escape hatch (no WS round-trip needed).
func TestProvider_HappyPath_InlineCreds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/telemost/conferences/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(w, map[string]any{
			"room_id":     "room-1",
			"peer_id":     "peer-1",
			"credentials": "boot-token",
			"client_configuration": map[string]any{
				"media_server_url": "ws://unused.example.invalid/",
				"service_name":     "telemost",
			},
			// ice_servers at root with usable TURN — short-circuits step 2.
			"ice_servers": []any{
				map[string]any{
					"urls":       "turn:5.255.211.241:3478?transport=udp",
					"username":   "user-A",
					"credential": "pass-A",
				},
			},
		})
	}))
	defer srv.Close()

	p := yandex.New(yandex.WithHostsForTest(srv.URL))
	got, err := p.Fetch(context.Background(), "https://telemost.yandex.ru/j/abc123", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Username != "user-A" {
		t.Errorf("Username=%q", got.Username)
	}
	if got.Password != "pass-A" {
		t.Errorf("Password=%q", got.Password)
	}
	if got.ServerAddr != "5.255.211.241:3478" {
		t.Errorf("ServerAddr=%q", got.ServerAddr)
	}
}

// TestProvider_HappyPath_WSCreds — current GOLOOM deployment: step 1
// only carries media_server_url + bootstrap, the actual TURN creds
// come over a WebSocket reply frame nested inside
// serverHello.rtcConfiguration.iceServers. Also verifies the ack-loop:
// the server sends a non-ack frame mid-stream and the client must
// echo back {uid, ack} or the server gives up.
func TestProvider_HappyPath_WSCreds(t *testing.T) {
	t.Parallel()
	gotAck := make(chan string, 4)
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("ws accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		// Read hello, capture uid for ack reply.
		_, helloRaw, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("ws read hello: %v", err)
			return
		}
		var hello struct {
			UID string `json:"uid"`
		}
		_ = json.Unmarshal(helloRaw, &hello)

		// Ack the hello.
		_ = conn.Write(ctx, websocket.MessageText, mustMarshal(map[string]any{
			"uid": hello.UID,
			"ack": map[string]any{"status": map[string]any{"code": "OK", "description": ""}},
		}))

		// Send a non-ack frame the client must ack BEFORE serverHello.
		// This proves the ack-loop is wired up.
		preUID := "11111111-2222-3333-4444-555555555555"
		_ = conn.Write(ctx, websocket.MessageText, mustMarshal(map[string]any{
			"uid":               preUID,
			"updateDescription": map[string]any{"description": []any{}},
		}))

		// Read the client's ack for that frame.
		_, ackRaw, err := conn.Read(ctx)
		if err == nil {
			var ackedUID struct {
				UID string `json:"uid"`
				Ack any    `json:"ack"`
			}
			_ = json.Unmarshal(ackRaw, &ackedUID)
			if ackedUID.Ack != nil {
				gotAck <- ackedUID.UID
			}
		}

		// Now send serverHello with the real ICE servers.
		reply := map[string]any{
			"uid": "99999999-aaaa-bbbb-cccc-dddddddddddd",
			"serverHello": map[string]any{
				"rtcConfiguration": map[string]any{
					"iceServers": []any{
						map[string]any{
							"urls":       []any{"stun:5.255.211.241:3478"},
							"username":   "",
							"credential": "",
						},
						map[string]any{
							"urls":       []any{"turn:turn.tel.yandex.net:443"},
							"username":   "1778182412:telemost:abcd:room-2",
							"credential": "B64+SECRET=",
						},
					},
				},
			},
		}
		_ = conn.Write(ctx, websocket.MessageText, mustMarshal(reply))
		time.Sleep(80 * time.Millisecond)
	}))
	defer wsSrv.Close()
	wsURL := strings.Replace(wsSrv.URL, "http://", "ws://", 1) + "/join"

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"room_id":     "room-2",
			"peer_id":     "peer-2",
			"credentials": "boot-token",
			"client_configuration": map[string]any{
				"media_server_url": wsURL,
				"service_name":     "telemost",
			},
		})
	}))
	defer httpSrv.Close()

	p := yandex.New(yandex.WithHostsForTest(httpSrv.URL))
	got, err := p.Fetch(context.Background(), "https://telemost.yandex.ru/j/xyz", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Username != "1778182412:telemost:abcd:room-2" {
		t.Errorf("Username=%q", got.Username)
	}
	if got.Password != "B64+SECRET=" {
		t.Errorf("Password=%q", got.Password)
	}
	if got.ServerAddr != "turn.tel.yandex.net:443" {
		t.Errorf("ServerAddr=%q", got.ServerAddr)
	}
	select {
	case uid := <-gotAck:
		if uid != "11111111-2222-3333-4444-555555555555" {
			t.Errorf("client ack'd wrong uid: %s", uid)
		}
	default:
		t.Errorf("client never ack'd the server's mid-stream frame")
	}
}

// mustMarshal is a test helper.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// TestProvider_BareID — a hint that's just the conference ID (no
// scheme/host) is accepted and assembled into the expected link.
func TestProvider_BareID(t *testing.T) {
	t.Parallel()
	var seenLink string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenLink = r.URL.Path
		writeJSON(w, map[string]any{
			"ice_servers": []any{
				map[string]any{
					"urls": "turn:9.9.9.9:3478", "username": "u", "credential": "p",
				},
			},
		})
	}))
	defer srv.Close()
	p := yandex.New(yandex.WithHostsForTest(srv.URL))
	if _, err := p.Fetch(context.Background(), "abc-only", 0); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(seenLink, "abc-only") {
		t.Errorf("URL didn't include the call id: %s", seenLink)
	}
	if !strings.Contains(seenLink, "telemost.yandex.ru") {
		t.Errorf("URL didn't assemble the canonical telemost link: %s", seenLink)
	}
}

func TestProvider_InvalidLink(t *testing.T) {
	t.Parallel()
	p := yandex.New() // no test host; we should fail before any network
	_, err := p.Fetch(context.Background(), "", 0)
	if !errors.Is(err, yandex.ErrInvalidLink) {
		t.Errorf("err=%v, want ErrInvalidLink", err)
	}
}

func TestProvider_ConferenceNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"ConferenceNotFound","description":"Conference not found.","message":"Видео встреча не найдена."}`)
	}))
	defer srv.Close()
	p := yandex.New(yandex.WithHostsForTest(srv.URL))
	_, err := p.Fetch(context.Background(), "https://telemost.yandex.ru/j/zzz", 0)
	if !errors.Is(err, wgturn.ErrAuthFailure) {
		t.Errorf("err=%v, want ErrAuthFailure", err)
	}
	if !strings.Contains(err.Error(), "ConferenceNotFound") {
		t.Errorf("err=%v should mention ConferenceNotFound", err)
	}
}

func TestProvider_ContextCancellation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	p := yandex.New(yandex.WithHostsForTest(srv.URL))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Fetch(ctx, "https://telemost.yandex.ru/j/abc", 0)
	if err == nil {
		t.Fatal("want error from cancelled ctx")
	}
}

// helpers

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

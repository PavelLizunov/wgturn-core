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

// TestProvider_HappyPath_InlineCreds — the simplest deployment: step 1
// already returns ice_servers with TURN creds at the response root.
// No WebSocket round-trip needed.
func TestProvider_HappyPath_InlineCreds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept any path under /telemost_front/v2/telemost/conferences/.
		if !strings.Contains(r.URL.Path, "/telemost/conferences/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(w, map[string]any{
			"room_id":          "room-1",
			"peer_id":          "peer-1",
			"media_server_url": "ws://unused.example.invalid/",
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

// TestProvider_HappyPath_WSCreds — newer deployment: step 1 only carries
// media_server_url + bootstrap, the actual TURN creds come over a
// WebSocket reply frame. Verifies the WS path works end-to-end against
// a fake Telemost media server.
func TestProvider_HappyPath_WSCreds(t *testing.T) {
	t.Parallel()
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("ws accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		// Read hello, ignore content.
		if _, _, err := conn.Read(ctx); err != nil {
			t.Errorf("ws read hello: %v", err)
			return
		}
		// Reply with a server_hello carrying ice_servers.
		reply := map[string]any{
			"server_hello": map[string]any{
				"rtc_configuration": map[string]any{
					"ice_servers": []any{
						map[string]any{
							"urls":       []any{"stun:5.255.211.241:3478", "turn:5.255.211.241:3478?transport=udp"},
							"username":   "user-WS",
							"credential": "pass-WS",
						},
					},
				},
			},
		}
		body, _ := json.Marshal(reply)
		_ = conn.Write(ctx, websocket.MessageText, body)
		// Linger briefly so the client reads the frame before close.
		time.Sleep(50 * time.Millisecond)
	}))
	defer wsSrv.Close()
	wsURL := strings.Replace(wsSrv.URL, "http://", "ws://", 1) + "/conf"

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"room_id":          "room-2",
			"peer_id":          "peer-2",
			"media_server_url": wsURL,
			// Note: no ice_servers — forces WS path.
			"credentials": map[string]any{"token": "boot-token"},
		})
	}))
	defer httpSrv.Close()

	p := yandex.New(yandex.WithHostsForTest(httpSrv.URL))
	got, err := p.Fetch(context.Background(), "https://telemost.yandex.ru/j/xyz", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Username != "user-WS" {
		t.Errorf("Username=%q", got.Username)
	}
	if got.Password != "pass-WS" {
		t.Errorf("Password=%q", got.Password)
	}
	if got.ServerAddr != "5.255.211.241:3478" {
		t.Errorf("ServerAddr=%q", got.ServerAddr)
	}
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

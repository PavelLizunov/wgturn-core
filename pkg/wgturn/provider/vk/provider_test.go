// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovn/wgturn-core/pkg/wgturn"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/vk"
)

// mockVKServer stands in for login.vk.ru, api.vk.ru, and calls.okcdn.ru.
// All three are co-tenanted on a single httptest server; we point all
// host overrides to its base URL via vk.WithHostsForTest. The handler
// is keyed by URL path; tests override per-step behaviour by
// registering a custom handler.
//
// Note: the login.vk.ru endpoint is at path "/" with action selected
// by the "?act=" query parameter — so the "/" handler must inspect
// the query to differentiate. The other endpoints have unique paths.
type mockVKServer struct {
	*httptest.Server
	handlers map[string]http.HandlerFunc
	calls    map[string]*atomic.Int64
}

func newMockVKServer(t *testing.T) *mockVKServer {
	t.Helper()
	m := &mockVKServer{
		handlers: map[string]http.HandlerFunc{},
		calls:    map[string]*atomic.Int64{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if c, ok := m.calls[key]; ok {
			c.Add(1)
		}
		if h, ok := m.handlers[key]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "no handler for "+key)
	}))
	m.Server = srv
	t.Cleanup(srv.Close)
	return m
}

func (m *mockVKServer) register(path string, fn http.HandlerFunc) {
	m.handlers[path] = fn
	m.calls[path] = &atomic.Int64{}
}

func (m *mockVKServer) hits(path string) int64 {
	if c, ok := m.calls[path]; ok {
		return c.Load()
	}
	return 0
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// happyPathHandlers wires up a working VK / OK simulator. Returns a
// closure that asserts the expected hit counts after Fetch returns.
func happyPathHandlers(t *testing.T, m *mockVKServer, turnURL string) func() {
	t.Helper()

	// "/" handles steps 1 and 3 (both POST login.vk.ru?act=get_anonym_token).
	loginCalls := atomic.Int32{}
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("act") != "get_anonym_token" {
			t.Errorf("/login: missing/wrong ?act, got %q", r.URL.Query().Get("act"))
		}
		_ = r.ParseForm()
		n := loginCalls.Add(1)
		var token string
		if n == 1 {
			token = "tok1-primary"
			if got := r.FormValue("scopes"); !strings.Contains(got, "anonymous") {
				t.Errorf("step1 scopes wrong: %q", got)
			}
		} else {
			if r.FormValue("token_type") != "messages" {
				t.Errorf("step3 token_type = %q", r.FormValue("token_type"))
			}
			if r.FormValue("payload") != "the-payload-blob" {
				t.Errorf("step3 payload = %q", r.FormValue("payload"))
			}
			token = "tok3-secondary"
		}
		writeJSON(w, map[string]any{
			"data": map[string]any{"access_token": token},
		})
	})

	m.register("/method/calls.getAnonymousAccessTokenPayload", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("access_token") != "tok1-primary" {
			t.Errorf("step2 access_token = %q", r.FormValue("access_token"))
		}
		writeJSON(w, map[string]any{
			"response": map[string]any{"payload": "the-payload-blob"},
		})
	})

	m.register("/method/calls.getAnonymousToken", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !strings.HasSuffix(r.FormValue("vk_join_link"), "/abc123") {
			t.Errorf("step4 vk_join_link = %q", r.FormValue("vk_join_link"))
		}
		if r.FormValue("access_token") != "tok3-secondary" {
			t.Errorf("step4 access_token = %q", r.FormValue("access_token"))
		}
		writeJSON(w, map[string]any{
			"response": map[string]any{"token": "tok4-call-scoped"},
		})
	})

	okCalls := atomic.Int32{}
	m.register("/fb.do", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		n := okCalls.Add(1)
		switch n {
		case 1:
			if r.FormValue("method") != "auth.anonymLogin" {
				t.Errorf("step5 method = %q", r.FormValue("method"))
			}
			writeJSON(w, map[string]any{"session_key": "sess-12345"})
		case 2:
			if r.FormValue("method") != "vchat.joinConversationByLink" {
				t.Errorf("step6 method = %q", r.FormValue("method"))
			}
			if r.FormValue("anonymToken") != "tok4-call-scoped" {
				t.Errorf("step6 anonymToken = %q", r.FormValue("anonymToken"))
			}
			if r.FormValue("session_key") != "sess-12345" {
				t.Errorf("step6 session_key = %q", r.FormValue("session_key"))
			}
			if r.FormValue("joinLink") != "abc123" {
				t.Errorf("step6 joinLink = %q", r.FormValue("joinLink"))
			}
			writeJSON(w, map[string]any{
				"turn_server": map[string]any{
					"username":   "vk-user-42",
					"credential": "vk-pass-42",
					"urls":       []any{turnURL},
				},
			})
		}
	})

	return func() {
		t.Helper()
		// "/" handles login twice (steps 1 and 3); "/fb.do" twice (steps 5 and 6).
		if got := m.hits("/"); got != 2 {
			t.Errorf("login hits = %d, want 2", got)
		}
		if got := m.hits("/method/calls.getAnonymousAccessTokenPayload"); got != 1 {
			t.Errorf("step2 hits = %d, want 1", got)
		}
		if got := m.hits("/method/calls.getAnonymousToken"); got != 1 {
			t.Errorf("step4 hits = %d, want 1", got)
		}
		if got := m.hits("/fb.do"); got != 2 {
			t.Errorf("ok hits = %d, want 2", got)
		}
	}
}

func TestProvider_HappyPath(t *testing.T) {
	m := newMockVKServer(t)
	verify := happyPathHandlers(t, m, "turn:1.2.3.4:3478?transport=tcp")
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	got, err := p.Fetch(context.Background(), "https://vk.com/call/join/abc123", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Username != "vk-user-42" || got.Password != "vk-pass-42" {
		t.Errorf("got %+v", got)
	}
	if got.ServerAddr != "1.2.3.4:3478" {
		t.Errorf("ServerAddr = %q", got.ServerAddr)
	}
	verify()
}

func TestProvider_BareIDHint(t *testing.T) {
	m := newMockVKServer(t)
	_ = happyPathHandlers(t, m, "turn:5.6.7.8:3478")
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	got, err := p.Fetch(context.Background(), "abc123", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.ServerAddr != "5.6.7.8:3478" {
		t.Errorf("ServerAddr = %q", got.ServerAddr)
	}
}

func TestProvider_InvalidLink(t *testing.T) {
	p := vk.New() // no test hosts; we should never hit network
	_, err := p.Fetch(context.Background(), "", 0)
	if !errors.Is(err, vk.ErrInvalidLink) {
		t.Errorf("err = %v, want ErrInvalidLink", err)
	}
}

func TestProvider_CaptchaRequired_StringForm(t *testing.T) {
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"error": "need_captcha"})
	})
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	_, err := p.Fetch(context.Background(), "abc123", 0)
	if !errors.Is(err, wgturn.ErrCaptchaRequired) {
		t.Errorf("err = %v, want ErrCaptchaRequired", err)
	}
}

func TestProvider_CaptchaRequired_ObjectForm_VKCode14(t *testing.T) {
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"error": map[string]any{
				"error_code": 14,
				"error_msg":  "Captcha needed",
			},
		})
	})
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	_, err := p.Fetch(context.Background(), "abc123", 0)
	if !errors.Is(err, wgturn.ErrCaptchaRequired) {
		t.Errorf("err = %v, want ErrCaptchaRequired", err)
	}
}

func TestProvider_AuthFailure_HTTP401(t *testing.T) {
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "{}")
	})
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	_, err := p.Fetch(context.Background(), "abc123", 0)
	if !errors.Is(err, wgturn.ErrAuthFailure) {
		t.Errorf("err = %v, want ErrAuthFailure", err)
	}
}

func TestProvider_AuthFailure_VKCode5(t *testing.T) {
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"error": map[string]any{
				"error_code": 5,
				"error_msg":  "User authorization failed",
			},
		})
	})
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	_, err := p.Fetch(context.Background(), "abc123", 0)
	if !errors.Is(err, wgturn.ErrAuthFailure) {
		t.Errorf("err = %v, want ErrAuthFailure", err)
	}
}

func TestProvider_BadJSON(t *testing.T) {
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html>oops</html>")
	})
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))
	_, err := p.Fetch(context.Background(), "abc123", 0)
	if err == nil || !strings.Contains(err.Error(), "decode JSON") {
		t.Errorf("err = %v", err)
	}
}

func TestProvider_ContextCancellation(t *testing.T) {
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Fetch(ctx, "abc123", 0)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestProvider_BadTurnURL(t *testing.T) {
	m := newMockVKServer(t)
	_ = happyPathHandlers(t, m, "garbage-not-a-turn-url")
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	_, err := p.Fetch(context.Background(), "abc123", 0)
	if err == nil {
		t.Fatal("expected error for malformed turn URL")
	}
	if !strings.Contains(err.Error(), "turn URL missing host:port") {
		t.Errorf("unexpected error: %v", err)
	}
}

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
// All three are co-tenanted on a single httptest server; the WithHostsForTest
// option points all three host overrides to its base URL. Handlers are
// keyed by URL path; tests override per-step behaviour by registering a
// handler for the corresponding path.
//
// Path map (matches the new 5-step flow at v=5.275):
//
//	/                                            login.vk.ru — get_anonym_token
//	/method/calls.getCallPreview                 api.vk.ru — preview (best-effort)
//	/method/calls.getAnonymousToken              api.vk.ru — call-scoped token
//	/fb.do                                       calls.okcdn.ru — OK CDN
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
//
// The 5-step flow (per kiper292 reference at v=5.275):
//
//  1. POST /?act=get_anonym_token              → token1
//  2. POST /method/calls.getCallPreview         (best-effort)
//  3. POST /method/calls.getAnonymousToken      → token2 (call-scoped)
//  4. POST /fb.do auth.anonymLogin              → session_key
//  5. POST /fb.do vchat.joinConversationByLink  → TURN creds
func happyPathHandlers(t *testing.T, m *mockVKServer, turnURL string) func() {
	t.Helper()

	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Query().Get("act") != "get_anonym_token" {
			t.Errorf("step1: ?act=%q", r.URL.Query().Get("act"))
		}
		// New flow: token_type=messages, no scopes.
		if r.FormValue("token_type") != "messages" {
			t.Errorf("step1: token_type=%q", r.FormValue("token_type"))
		}
		writeJSON(w, map[string]any{
			"data": map[string]any{"access_token": "tok1-anon"},
		})
	})

	m.register("/method/calls.getCallPreview", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("access_token") != "tok1-anon" {
			t.Errorf("step2 access_token=%q", r.FormValue("access_token"))
		}
		// best-effort; real VK can return arbitrary preview JSON
		writeJSON(w, map[string]any{
			"response": map[string]any{"call_title": "test call"},
		})
	})

	m.register("/method/calls.getAnonymousToken", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("access_token") != "tok1-anon" {
			t.Errorf("step3 access_token=%q (want tok1-anon)", r.FormValue("access_token"))
		}
		if r.FormValue("name") == "" {
			t.Errorf("step3 missing name")
		}
		if !strings.HasSuffix(r.FormValue("vk_join_link"), "/abc123") {
			t.Errorf("step3 vk_join_link=%q", r.FormValue("vk_join_link"))
		}
		writeJSON(w, map[string]any{
			"response": map[string]any{"token": "tok2-call-scoped"},
		})
	})

	okCalls := atomic.Int32{}
	m.register("/fb.do", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		n := okCalls.Add(1)
		switch n {
		case 1:
			if r.FormValue("method") != "auth.anonymLogin" {
				t.Errorf("step4 method=%q", r.FormValue("method"))
			}
			writeJSON(w, map[string]any{"session_key": "sess-12345"})
		case 2:
			if r.FormValue("method") != "vchat.joinConversationByLink" {
				t.Errorf("step5 method=%q", r.FormValue("method"))
			}
			if r.FormValue("anonymToken") != "tok2-call-scoped" {
				t.Errorf("step5 anonymToken=%q", r.FormValue("anonymToken"))
			}
			if r.FormValue("session_key") != "sess-12345" {
				t.Errorf("step5 session_key=%q", r.FormValue("session_key"))
			}
			if r.FormValue("capabilities") != "2F7F" {
				t.Errorf("step5 capabilities=%q (want 2F7F)", r.FormValue("capabilities"))
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
		if got := m.hits("/"); got != 1 {
			t.Errorf("login (step1) hits = %d, want 1 (no second login in new flow)", got)
		}
		if got := m.hits("/method/calls.getCallPreview"); got != 1 {
			t.Errorf("step2 hits = %d, want 1", got)
		}
		if got := m.hits("/method/calls.getAnonymousToken"); got != 1 {
			t.Errorf("step3 hits = %d, want 1", got)
		}
		if got := m.hits("/fb.do"); got != 2 {
			t.Errorf("/fb.do hits = %d, want 2", got)
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

func TestProvider_CaptchaRequired_NoSolver(t *testing.T) {
	// step3 returns captcha; without WithCaptchaSolver, the provider
	// must surface ErrCaptchaRequired so the embedder can decide.
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": map[string]any{"access_token": "tok1"},
		})
	})
	m.register("/method/calls.getCallPreview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"response": map[string]any{}})
	})
	m.register("/method/calls.getAnonymousToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"error": map[string]any{
				"error_code":  14,
				"error_msg":   "Captcha needed",
				"captcha_sid": "test-sid",
				"captcha_img": "https://example/captcha.png",
			},
		})
	})
	p := vk.New(vk.WithHostsForTest(m.URL, m.URL, m.URL))

	_, err := p.Fetch(context.Background(), "abc123", 0)
	if !errors.Is(err, wgturn.ErrCaptchaRequired) {
		t.Errorf("err = %v, want ErrCaptchaRequired", err)
	}
}

func TestProvider_CaptchaRequired_WithSolver(t *testing.T) {
	// step3 returns captcha; with WithCaptchaSolver, the provider
	// retries with captcha_sid + captcha_key and the second response
	// is a successful token.
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": map[string]any{"access_token": "tok1"},
		})
	})
	m.register("/method/calls.getCallPreview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"response": map[string]any{}})
	})
	step3Calls := atomic.Int32{}
	m.register("/method/calls.getAnonymousToken", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		n := step3Calls.Add(1)
		if n == 1 {
			writeJSON(w, map[string]any{
				"error": map[string]any{
					"error_code":  14,
					"error_msg":   "Captcha needed",
					"captcha_sid": "test-sid",
					"captcha_img": "https://example/captcha.png",
				},
			})
			return
		}
		// Second attempt — verify captcha fields present
		if r.FormValue("captcha_sid") != "test-sid" {
			t.Errorf("retry captcha_sid=%q", r.FormValue("captcha_sid"))
		}
		if r.FormValue("captcha_key") != "BANANA" {
			t.Errorf("retry captcha_key=%q", r.FormValue("captcha_key"))
		}
		writeJSON(w, map[string]any{
			"response": map[string]any{"token": "tok2-after-captcha"},
		})
	})
	okCalls := atomic.Int32{}
	m.register("/fb.do", func(w http.ResponseWriter, r *http.Request) {
		switch okCalls.Add(1) {
		case 1:
			writeJSON(w, map[string]any{"session_key": "sess-1"})
		case 2:
			_ = r.ParseForm()
			if r.FormValue("anonymToken") != "tok2-after-captcha" {
				t.Errorf("step5 used wrong anonymToken: %q", r.FormValue("anonymToken"))
			}
			writeJSON(w, map[string]any{
				"turn_server": map[string]any{
					"username":   "u",
					"credential": "p",
					"urls":       []any{"turn:99.99.99.99:3478"},
				},
			})
		}
	})

	solverCalls := atomic.Int32{}
	solver := vk.SolverFunc(func(ctx context.Context, ch vk.CaptchaChallenge) (vk.Solution, error) {
		solverCalls.Add(1)
		if ch.SID != "test-sid" {
			t.Errorf("solver got SID=%q", ch.SID)
		}
		if ch.ImgURL != "https://example/captcha.png" {
			t.Errorf("solver got ImgURL=%q", ch.ImgURL)
		}
		return vk.Solution{Key: "BANANA"}, nil
	})

	p := vk.New(
		vk.WithHostsForTest(m.URL, m.URL, m.URL),
		vk.WithCaptchaSolver(solver),
	)

	got, err := p.Fetch(context.Background(), "abc123", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.ServerAddr != "99.99.99.99:3478" {
		t.Errorf("ServerAddr = %q", got.ServerAddr)
	}
	if step3Calls.Load() != 2 {
		t.Errorf("step3 calls = %d, want 2 (one challenge, one solved)", step3Calls.Load())
	}
	if solverCalls.Load() != 1 {
		t.Errorf("solver calls = %d, want 1", solverCalls.Load())
	}
}

// TestProvider_CaptchaRequired_SuccessToken exercises the not-a-robot
// redirect path: VK returns redirect_uri + captcha_ts + captcha_attempt,
// the solver returns SuccessToken, and the retry MUST include the full
// envelope (success_token, captcha_ts, captcha_attempt, is_sound_captcha=0,
// empty captcha_key) — anything missing makes VK respond with a fresh
// challenge instead of advancing.
func TestProvider_CaptchaRequired_SuccessToken(t *testing.T) {
	const (
		wantSID     = "535662358251"
		wantTS      = "1778064843"
		wantAttempt = "1"
		wantToken   = "fake.success.JWT.payload"
	)
	m := newMockVKServer(t)
	m.register("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": map[string]any{"access_token": "tok1"},
		})
	})
	m.register("/method/calls.getCallPreview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"response": map[string]any{}})
	})
	step3Calls := atomic.Int32{}
	m.register("/method/calls.getAnonymousToken", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		n := step3Calls.Add(1)
		if n == 1 {
			writeJSON(w, map[string]any{
				"error": map[string]any{
					"error_code":      14,
					"error_msg":       "Captcha needed",
					"captcha_sid":     wantSID,
					"captcha_img":     "https://vk.ru/captcha.php?sid=" + wantSID,
					"redirect_uri":    "https://id.vk.ru/not_robot_captcha?session_token=xyz",
					"captcha_ts":      wantTS,
					"captcha_attempt": 1.0,
				},
			})
			return
		}
		// Second attempt — verify the slider-mode envelope.
		if got := r.FormValue("captcha_sid"); got != wantSID {
			t.Errorf("retry captcha_sid=%q, want %q", got, wantSID)
		}
		if got := r.FormValue("success_token"); got != wantToken {
			t.Errorf("retry success_token=%q, want %q", got, wantToken)
		}
		if got := r.FormValue("captcha_ts"); got != wantTS {
			t.Errorf("retry captcha_ts=%q, want %q", got, wantTS)
		}
		if got := r.FormValue("captcha_attempt"); got != wantAttempt {
			t.Errorf("retry captcha_attempt=%q, want %q", got, wantAttempt)
		}
		if got := r.FormValue("is_sound_captcha"); got != "0" {
			t.Errorf("retry is_sound_captcha=%q, want %q", got, "0")
		}
		// captcha_key MUST be present and empty — VK uses presence to
		// route to the slider-token validator rather than the legacy
		// image-OCR path.
		if _, ok := r.PostForm["captcha_key"]; !ok {
			t.Errorf("retry missing captcha_key (must be present empty)")
		}
		if got := r.FormValue("captcha_key"); got != "" {
			t.Errorf("retry captcha_key=%q, want empty string", got)
		}
		// captcha_token (legacy field) MUST NOT be sent in the redirect flow.
		if got := r.FormValue("captcha_token"); got != "" {
			t.Errorf("retry leaked legacy captcha_token=%q", got)
		}
		writeJSON(w, map[string]any{
			"response": map[string]any{"token": "tok2-after-captcha"},
		})
	})
	okCalls := atomic.Int32{}
	m.register("/fb.do", func(w http.ResponseWriter, r *http.Request) {
		switch okCalls.Add(1) {
		case 1:
			writeJSON(w, map[string]any{"session_key": "sess-1"})
		case 2:
			writeJSON(w, map[string]any{
				"turn_server": map[string]any{
					"username":   "u",
					"credential": "p",
					"urls":       []any{"turn:99.99.99.99:3478"},
				},
			})
		}
	})

	solverCalls := atomic.Int32{}
	solver := vk.SolverFunc(func(ctx context.Context, ch vk.CaptchaChallenge) (vk.Solution, error) {
		solverCalls.Add(1)
		if ch.SID != wantSID {
			t.Errorf("solver got SID=%q, want %q", ch.SID, wantSID)
		}
		if ch.RedirectURI == "" {
			t.Errorf("solver got empty RedirectURI")
		}
		if ch.TS != wantTS {
			t.Errorf("solver got TS=%q, want %q", ch.TS, wantTS)
		}
		if ch.Attempt != 1 {
			t.Errorf("solver got Attempt=%d, want 1", ch.Attempt)
		}
		return vk.Solution{SuccessToken: wantToken}, nil
	})

	p := vk.New(
		vk.WithHostsForTest(m.URL, m.URL, m.URL),
		vk.WithCaptchaSolver(solver),
	)
	got, err := p.Fetch(context.Background(), "abc123", 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.ServerAddr != "99.99.99.99:3478" {
		t.Errorf("ServerAddr = %q", got.ServerAddr)
	}
	if step3Calls.Load() != 2 {
		t.Errorf("step3 calls = %d, want 2", step3Calls.Load())
	}
	if solverCalls.Load() != 1 {
		t.Errorf("solver calls = %d, want 1", solverCalls.Load())
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

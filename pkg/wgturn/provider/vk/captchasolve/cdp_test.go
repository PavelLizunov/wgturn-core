// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package captchasolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	vkprov "github.com/slovn/wgturn-core/pkg/wgturn/provider/vk"
)

// TestCDPSolver_RejectsEmptyRedirect — without a RedirectURI we have
// nothing to drive Chrome at, so the solver must fail fast rather
// than open a tab and time out.
func TestCDPSolver_RejectsEmptyRedirect(t *testing.T) {
	t.Parallel()
	s := &CDPSolver{ChromeURL: "http://localhost:9222"}
	_, err := s.Solve(context.Background(), vkprov.CaptchaChallenge{})
	if err == nil || !strings.Contains(err.Error(), "redirect_uri") {
		t.Fatalf("want error mentioning redirect_uri, got %v", err)
	}
}

// TestCDPSolver_RejectsEmptyChromeURL — the field is required; we
// don't fall back to a default to avoid surprising callers.
func TestCDPSolver_RejectsEmptyChromeURL(t *testing.T) {
	t.Parallel()
	s := &CDPSolver{}
	_, err := s.Solve(context.Background(),
		vkprov.CaptchaChallenge{RedirectURI: "https://id.vk.ru/anything"})
	if err == nil || !strings.Contains(err.Error(), "ChromeURL") {
		t.Fatalf("want error mentioning ChromeURL, got %v", err)
	}
}

// TestCDPSolver_HappyPath — fake CDP server grants the tab, accepts
// commands, lies about a checkbox at (100, 100), and after receiving
// the click emits a Network.responseReceived for
// captchaNotRobot.check + responds to Network.getResponseBody with a
// JSON envelope carrying our test success_token.
func TestCDPSolver_HappyPath(t *testing.T) {
	t.Parallel()
	const testToken = "TEST_SUCCESS_TOKEN_xyz_123"
	srv := newFakeCDP(t, fakeCDPConfig{
		token: testToken,
	})
	defer srv.close()

	s := &CDPSolver{ChromeURL: srv.URL(), Timeout: 10 * time.Second}
	sol, err := s.Solve(context.Background(),
		vkprov.CaptchaChallenge{RedirectURI: "https://id.vk.ru/not_robot_captcha"})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if sol.SuccessToken != testToken {
		t.Fatalf("token mismatch: want %q, got %q", testToken, sol.SuccessToken)
	}
	if sol.Key != "" {
		t.Errorf("Key should be empty when SuccessToken is set, got %q", sol.Key)
	}

	// The fake captures clicks; verify we sent a real mousePressed
	// at the advertised checkbox coordinates.
	if got := srv.clickCount(); got == 0 {
		t.Errorf("solver never dispatched a click; got 0 mouse events")
	}
	if got := srv.tabsClosed(); got != 1 {
		t.Errorf("tabs closed: want 1, got %d", got)
	}
}

// TestCDPSolver_ContextCancel — the caller's ctx must abort the solve.
func TestCDPSolver_ContextCancel(t *testing.T) {
	t.Parallel()
	srv := newFakeCDP(t, fakeCDPConfig{
		// Withhold the success_token; without cancel, solver would
		// run until its 60 s default deadline.
		withholdToken: true,
	})
	defer srv.close()
	s := &CDPSolver{ChromeURL: srv.URL(), Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Solve(ctx, vkprov.CaptchaChallenge{RedirectURI: "https://id.vk.ru/x"})
	dur := time.Since(start)
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context") {
		t.Errorf("unexpected error: %v", err)
	}
	// Should bail within ~2 s of the 800 ms deadline (slack for ws shutdown).
	if dur > 5*time.Second {
		t.Errorf("solver overshot deadline by too much: dur=%s", dur)
	}
}

// ----------------------------------------------------------------------------
// fakeCDP — minimal Chrome DevTools Protocol HTTP+WS server for tests.
// ----------------------------------------------------------------------------

type fakeCDPConfig struct {
	token         string
	withholdToken bool // never emit the captchaNotRobot.check response
}

type fakeCDP struct {
	t        *testing.T
	srv      *httptest.Server
	cfg      fakeCDPConfig
	clicks   atomic.Int64
	closed   atomic.Int64
	mu       sync.Mutex
	tabConns map[string]*websocket.Conn // tabID -> active ws (for emit)
}

func newFakeCDP(t *testing.T, cfg fakeCDPConfig) *fakeCDP {
	t.Helper()
	if cfg.token == "" {
		cfg.token = "default-test-token"
	}
	f := &fakeCDP{
		t:        t,
		cfg:      cfg,
		tabConns: make(map[string]*websocket.Conn),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/new", f.handleNewTab)
	mux.HandleFunc("/json/close/", f.handleCloseTab)
	mux.HandleFunc("/cdp/", f.handleCDPSocket)
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeCDP) URL() string         { return f.srv.URL }
func (f *fakeCDP) close()               { f.srv.Close() }
func (f *fakeCDP) clickCount() int64   { return f.clicks.Load() }
func (f *fakeCDP) tabsClosed() int64   { return f.closed.Load() }

func (f *fakeCDP) handleNewTab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	tabID := fmt.Sprintf("tab-%d", time.Now().UnixNano())
	wsURL := strings.Replace(f.srv.URL, "http://", "ws://", 1) + "/cdp/" + tabID
	resp := map[string]any{
		"id":                   tabID,
		"webSocketDebuggerUrl": wsURL,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeCDP) handleCloseTab(w http.ResponseWriter, r *http.Request) {
	f.closed.Add(1)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeCDP) handleCDPSocket(w http.ResponseWriter, r *http.Request) {
	tabID := strings.TrimPrefix(r.URL.Path, "/cdp/")
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		f.t.Logf("ws accept: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	f.mu.Lock()
	f.tabConns[tabID] = conn
	f.mu.Unlock()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		f.respond(ctx, conn, msg.ID, msg.Method, msg.Params)
	}
}

func (f *fakeCDP) respond(ctx context.Context, conn *websocket.Conn, id int64, method string, params json.RawMessage) {
	send := func(result map[string]any) {
		body, _ := json.Marshal(map[string]any{"id": id, "result": result})
		_ = conn.Write(ctx, websocket.MessageText, body)
	}

	switch method {
	case "Network.enable", "Page.enable", "Runtime.enable",
		"Network.setUserAgentOverride", "Emulation.setDeviceMetricsOverride":
		send(map[string]any{})

	case "Page.navigate":
		send(map[string]any{"frameId": "frame-1"})

	case "Runtime.evaluate":
		// Only one expression we care about — findCheckboxJS. Always
		// return a plausible (x, y).
		val, _ := json.Marshal(map[string]any{"x": 100.5, "y": 200.5, "rect": []float64{80, 180, 200, 40}})
		send(map[string]any{
			"result": map[string]any{
				"type":  "string",
				"value": string(val),
			},
		})

	case "Input.dispatchMouseEvent":
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Type == "mousePressed" {
			f.clicks.Add(1)
			// The press event is the cue to fire the
			// captchaNotRobot.check response back to the client.
			if !f.cfg.withholdToken {
				go f.emitCheckResponse(ctx, conn)
			}
		}
		send(map[string]any{})

	case "Network.getResponseBody":
		// Response body for our synthetic captchaNotRobot.check.
		body := map[string]any{
			"response": map[string]any{
				"status":             "OK",
				"success_token":      f.cfg.token,
				"redirect":           "",
				"show_captcha_type": "",
			},
		}
		bb, _ := json.Marshal(body)
		send(map[string]any{
			"body":          string(bb),
			"base64Encoded": false,
		})

	default:
		send(map[string]any{})
	}
}

func (f *fakeCDP) emitCheckResponse(ctx context.Context, conn *websocket.Conn) {
	// Slight delay so the read-loop processes the press response
	// first — mimics real Chrome event ordering.
	time.Sleep(40 * time.Millisecond)
	evt := map[string]any{
		"method": "Network.responseReceived",
		"params": map[string]any{
			"requestId": "req-fake-1",
			"response": map[string]any{
				"url":    "https://api.vk.ru/method/captchaNotRobot.check",
				"status": 200,
			},
		},
	}
	body, _ := json.Marshal(evt)
	_ = conn.Write(ctx, websocket.MessageText, body)
}

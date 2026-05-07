// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package captchasolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
	vkprov "github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/vk"
)

// Compile-time interface satisfaction check.
var _ vkprov.CaptchaSolver = (*CDPSolver)(nil)

// CDPSolver drives a Chrome instance over the DevTools Protocol to
// render the VK not-a-robot redirect page, dispatch a realistic mouse
// click on the "I'm not a robot" checkbox, and harvest the
// success_token that VK returns from captchaNotRobot.check.
//
// Why a real Chrome and not a hand-rolled HTTP client?
//
// VK's anti-bot system at id.vk.ru gates the success_token on a
// componentDone request that ships browser fingerprint signals
// (navigator.webdriver, hardwareConcurrency, deviceMemory, languages,
// notifications permission, …). It also wants a proof-of-work hash
// computed by the page's JS, and an AES-encrypted answer payload using
// keys baked into the bundle. Re-implementing all of that out of
// browser would require shipping a JS runtime AND keeping it in sync
// with id.vk.ru deploys. Letting Chrome run the page is leaner and
// keeps us decoupled from VK's bundle internals.
//
// The solver is stateless and safe to share across goroutines — every
// Solve call opens a fresh DevTools target and tears it down on exit.
type CDPSolver struct {
	// ChromeURL is the DevTools Protocol HTTP root, e.g.
	// "http://localhost:9222". Required.
	ChromeURL string

	// HTTPClient overrides the client used for the HTTP /json/* and
	// websocket-upgrade calls into Chrome. Defaults to a 15 s client.
	HTTPClient *http.Client

	// Timeout bounds the entire Solve call. Defaults to 60 s.
	Timeout time.Duration

	// ViewportWidth/Height set the emulated viewport for the captcha
	// tab. Defaults to 1280x800. The captcha widget is sensitive to
	// viewport — narrower than ~360 px breaks the layout.
	ViewportWidth, ViewportHeight int

	// UserAgent overrides the navigator.userAgent of the captcha
	// tab. Empty falls back to whatever Chrome was launched with;
	// HeadlessChrome strings sometimes flunk VK's heuristics, so a
	// "real" Chrome UA is recommended.
	UserAgent string

	// Logger receives debug events (negotiation, click coords,
	// success_token length). Defaults to NoopLogger.
	Logger wgturn.Logger
}

// Solve implements vkprov.CaptchaSolver. The slider mode is the only
// one currently observed in the wild — text captchas are gone — so we
// require RedirectURI to be present.
func (s *CDPSolver) Solve(ctx context.Context, ch vkprov.CaptchaChallenge) (vkprov.Solution, error) {
	if ch.RedirectURI == "" {
		return vkprov.Solution{}, errors.New("captchasolve cdp: challenge has no redirect_uri (slider mode required)")
	}
	chromeURL := strings.TrimRight(s.ChromeURL, "/")
	if chromeURL == "" {
		return vkprov.Solution{}, errors.New("captchasolve cdp: ChromeURL is required")
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	httpc := s.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	log := s.logger()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tab, err := openTab(ctx, httpc, chromeURL)
	if err != nil {
		return vkprov.Solution{}, fmt.Errorf("cdp open tab: %w", err)
	}
	// Best-effort cleanup — even if Solve fails, the tab is gone so
	// Chrome doesn't leak processes.
	defer closeTab(httpc, chromeURL, tab.ID)

	log.Debugf("[cdp-solver] opened tab id=%s ws=%s", tab.ID, tab.WebSocketDebuggerURL)
	conn, dialResp, err := websocket.Dial(ctx, tab.WebSocketDebuggerURL, &websocket.DialOptions{HTTPClient: httpc})
	if err != nil {
		return vkprov.Solution{}, fmt.Errorf("cdp ws dial: %w", err)
	}
	// websocket.Dial returns the upgrade-response with a body whose
	// underlying conn is now hijacked for the websocket. Closing it
	// is a no-op functionally but linters insist (bodyclose).
	if dialResp != nil && dialResp.Body != nil {
		_ = dialResp.Body.Close()
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(8 * 1024 * 1024)

	sess := newCDPSession(conn, log)
	go sess.readLoop(ctx)

	if err := sess.setup(ctx, s); err != nil {
		return vkprov.Solution{}, err
	}

	if _, err := sess.call(ctx, "Page.navigate", map[string]any{"url": ch.RedirectURI}); err != nil {
		return vkprov.Solution{}, fmt.Errorf("Page.navigate: %w", err)
	}

	cbX, cbY, err := sess.waitForCheckbox(ctx, 20*time.Second)
	if err != nil {
		return vkprov.Solution{}, fmt.Errorf("wait for checkbox: %w", err)
	}
	log.Debugf("[cdp-solver] clicking checkbox at (%.1f, %.1f)", cbX, cbY)

	if err := sess.simulateHumanClick(ctx, cbX, cbY); err != nil {
		return vkprov.Solution{}, fmt.Errorf("click: %w", err)
	}

	token, err := sess.awaitSuccessToken(ctx, 30*time.Second)
	if err != nil {
		return vkprov.Solution{}, err
	}
	log.Debugf("[cdp-solver] got success_token (len=%d)", len(token))
	return vkprov.Solution{SuccessToken: token}, nil
}

func (s *CDPSolver) logger() wgturn.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return wgturn.NoopLogger{}
}

// ----------------------------------------------------------------------------
// HTTP /json helpers — open and close DevTools targets.
// ----------------------------------------------------------------------------

type cdpTabInfo struct {
	ID                   string `json:"id"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func openTab(ctx context.Context, hc *http.Client, root string) (*cdpTabInfo, error) {
	// Modern Chrome (>= ~M111) requires PUT for /json/new.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, root+"/json/new", nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var t cdpTabInfo
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, err
	}
	if t.WebSocketDebuggerURL == "" {
		return nil, errors.New("cdp: response missing webSocketDebuggerUrl")
	}
	return &t, nil
}

func closeTab(hc *http.Client, root, id string) {
	if id == "" {
		return
	}
	// Older Chromes accept GET /json/close/<id>; we don't need the body.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/json/close/"+id, nil)
	if err != nil {
		return
	}
	resp, err := hc.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// ----------------------------------------------------------------------------
// CDP session — minimal JSON-RPC over WebSocket with response routing.
// ----------------------------------------------------------------------------

type cdpSession struct {
	ws  *websocket.Conn
	log wgturn.Logger

	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan json.RawMessage
	tokenCh chan string // signalled with the success_token
}

func newCDPSession(ws *websocket.Conn, log wgturn.Logger) *cdpSession {
	return &cdpSession{
		ws:      ws,
		log:     log,
		pending: make(map[int64]chan json.RawMessage),
		tokenCh: make(chan string, 1),
	}
}

// readLoop pulls every frame off the socket, demuxing command replies
// (by id) from network events. Exits when the socket closes or ctx
// is cancelled.
func (s *cdpSession) readLoop(ctx context.Context) {
	for {
		_, raw, err := s.ws.Read(ctx)
		if err != nil {
			s.failPending(err)
			return
		}
		var env struct {
			ID     int64           `json:"id,omitempty"`
			Result json.RawMessage `json:"result,omitempty"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
			Method string          `json:"method,omitempty"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.ID != 0 {
			s.mu.Lock()
			ch, ok := s.pending[env.ID]
			if ok {
				delete(s.pending, env.ID)
			}
			s.mu.Unlock()
			if ok {
				if env.Error != nil {
					ch <- mustMarshal(map[string]any{
						"_cdp_error": env.Error.Message,
						"_cdp_code":  env.Error.Code,
					})
				} else {
					ch <- env.Result
				}
			}
			continue
		}
		if env.Method == "Network.responseReceived" {
			s.handleResponseReceived(ctx, env.Params)
		}
	}
}

func (s *cdpSession) failPending(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.pending {
		ch <- mustMarshal(map[string]any{"_cdp_error": err.Error()})
		close(ch)
		delete(s.pending, id)
	}
}

// call dispatches a CDP method and waits for its reply. Returns the
// raw "result" object (which the caller may unmarshal) or an error if
// the WebSocket closed, ctx expired, or Chrome returned a CDP error.
func (s *cdpSession) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	if params == nil {
		params = map[string]any{}
	}
	frame := map[string]any{"id": id, "method": method, "params": params}
	body, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	ch := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.ws.Write(ctx, websocket.MessageText, body); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("ws write %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	case raw := <-ch:
		var probe struct {
			Err  string `json:"_cdp_error"`
			Code int    `json:"_cdp_code"`
		}
		if err := json.Unmarshal(raw, &probe); err == nil && probe.Err != "" {
			return nil, fmt.Errorf("%s: %s (code=%d)", method, probe.Err, probe.Code)
		}
		return raw, nil
	}
}

// setup runs the standard Network/Page/Runtime.enable trio and
// applies viewport + UA overrides.
func (s *cdpSession) setup(ctx context.Context, c *CDPSolver) error {
	for _, m := range []string{"Network.enable", "Page.enable", "Runtime.enable"} {
		if _, err := s.call(ctx, m, nil); err != nil {
			return fmt.Errorf("%s: %w", m, err)
		}
	}
	if c.UserAgent != "" {
		if _, err := s.call(ctx, "Network.setUserAgentOverride", map[string]any{"userAgent": c.UserAgent}); err != nil {
			return fmt.Errorf("Network.setUserAgentOverride: %w", err)
		}
	}
	vw, vh := c.ViewportWidth, c.ViewportHeight
	if vw == 0 {
		vw = 1280
	}
	if vh == 0 {
		vh = 800
	}
	if _, err := s.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": vw, "height": vh, "deviceScaleFactor": 1, "mobile": false,
	}); err != nil {
		return fmt.Errorf("Emulation.setDeviceMetricsOverride: %w", err)
	}
	return nil
}

// findCheckboxJS picks the input[type=checkbox] sitting next to the
// "I'm not a robot" / "Я не робот" label and returns the geometric
// centre of its visible hit area. It returns a JSON-encoded object so
// that one Runtime.evaluate round-trip carries everything we need.
//
// The captcha layout marks the real input as VisuallyHidden (offscreen
// for screen readers) but its bounding-rect still reflects the
// label's hit area thanks to a CSS sibling overlay. We click 14 px in
// from the left edge to land on the visual checkbox icon, not the
// text — clicking the text doesn't always toggle VK's React handler.
const findCheckboxJS = `
(function(){
  const labelTexts = ["I'm not a robot", "Я не робот", "Je ne suis pas un robot", "Ich bin kein Roboter"];
  const candidates = [
    ...document.querySelectorAll('input[type=checkbox]'),
    ...document.querySelectorAll('[class*="VkConnectCheckbox"]'),
  ];
  const visible = candidates.map(e => ({el: e, rect: e.getBoundingClientRect()}))
                            .filter(o => o.rect.width > 30 && o.rect.height > 10);
  if (!visible.length) return JSON.stringify({err: 'no_checkbox'});

  // Pick the candidate closest to a label with one of the known texts.
  let label = null;
  for (const e of document.querySelectorAll('label, span, div')) {
    const t = (e.textContent || '').trim();
    if (labelTexts.includes(t)) { label = e; break; }
  }
  let chosen = visible[0];
  if (label) {
    const lr = label.getBoundingClientRect();
    for (const v of visible) {
      if (Math.abs(v.rect.top - lr.top) < 60) { chosen = v; break; }
    }
  }
  const r = chosen.rect;
  return JSON.stringify({
    x: r.x + 14,
    y: r.y + r.height / 2,
    rect: [r.x, r.y, r.width, r.height],
  });
})()
`

type checkboxResult struct {
	Err  string  `json:"err"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Rect [4]float64
}

// waitForCheckbox polls Runtime.evaluate up to deadline, returning the
// click coordinates as soon as the captcha widget renders.
func (s *cdpSession) waitForCheckbox(ctx context.Context, timeout time.Duration) (float64, float64, error) {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return 0, 0, errors.New("timeout waiting for checkbox to render")
		}
		raw, err := s.call(ctx, "Runtime.evaluate", map[string]any{
			"expression":    findCheckboxJS,
			"returnByValue": true,
		})
		if err != nil {
			return 0, 0, err
		}
		var probe struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &probe); err == nil && probe.Result.Value != "" {
			var cb checkboxResult
			if err := json.Unmarshal([]byte(probe.Result.Value), &cb); err == nil && cb.Err == "" && cb.X > 0 {
				return cb.X, cb.Y, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// simulateHumanClick dispatches mouseMoved + mousePressed + mouseReleased
// at the given coordinates with realistic-ish timing. VK's anti-bot
// looks for too-clean inputs (sub-millisecond press/release deltas),
// so we leave ~80 ms between press and release and a 300 ms
// pre-hover.
func (s *cdpSession) simulateHumanClick(ctx context.Context, x, y float64) error {
	dispatch := func(typ string) error {
		_, err := s.call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": typ, "x": x, "y": y, "button": "left", "clickCount": 1,
		})
		return err
	}
	// Pre-hover from a slightly offset position.
	if _, err := s.call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": x - 50, "y": y - 30, "button": "none",
	}); err != nil {
		return err
	}
	sleep(ctx, 250*time.Millisecond)
	if _, err := s.call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": x, "y": y, "button": "none",
	}); err != nil {
		return err
	}
	sleep(ctx, 150*time.Millisecond)
	if err := dispatch("mousePressed"); err != nil {
		return err
	}
	sleep(ctx, 80*time.Millisecond)
	return dispatch("mouseReleased")
}

// handleResponseReceived watches Network events for a response from
// captchaNotRobot.check that contains a success_token. As soon as one
// arrives, the body is fetched and the token is pushed onto tokenCh.
func (s *cdpSession) handleResponseReceived(ctx context.Context, params json.RawMessage) {
	var p struct {
		RequestID string `json:"requestId"`
		Response  struct {
			URL    string `json:"url"`
			Status int    `json:"status"`
		} `json:"response"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if !strings.Contains(p.Response.URL, "captchaNotRobot.check") {
		return
	}
	if p.Response.Status != 200 {
		return
	}
	// Drop into a goroutine so this read-loop doesn't block on the
	// nested Network.getResponseBody round-trip.
	go func() {
		raw, err := s.call(ctx, "Network.getResponseBody", map[string]any{
			"requestId": p.RequestID,
		})
		if err != nil {
			return
		}
		var body struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return
		}
		var doc struct {
			Response struct {
				Status       string `json:"status"`
				SuccessToken string `json:"success_token"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(body.Body), &doc); err != nil {
			return
		}
		if doc.Response.SuccessToken == "" {
			return
		}
		select {
		case s.tokenCh <- doc.Response.SuccessToken:
		default:
		}
	}()
}

// awaitSuccessToken blocks until the read-loop signals a token,
// timeout fires, or ctx is cancelled.
func (s *cdpSession) awaitSuccessToken(ctx context.Context, timeout time.Duration) (string, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-t.C:
		return "", errors.New("timeout waiting for success_token from captchaNotRobot.check")
	case tok := <-s.tokenCh:
		return tok, nil
	}
}

// ----------------------------------------------------------------------------
// Helpers.
// ----------------------------------------------------------------------------

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

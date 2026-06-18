// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// Default endpoint hosts. Constants instead of variables so the
// callsite is grep-able when VK shuffles them.
const (
	hostLogin    = "https://login.vk.ru"
	hostAPI      = "https://api.vk.ru"
	hostOK       = "https://calls.okcdn.ru"
	hostUserBase = "https://vk.com" // for vk_join_link assembly
)

// VK Web App credentials. The default pair is the public anonymous
// app id used by the VK Calls web client. Override via
// Provider.AppID / Provider.ClientSecret if VK rotates them.
const (
	defaultAppID        = "6287487"
	defaultClientSecret = "QbYic1K3lEV5kTGiqlq2"
)

// VK Calls API version. As of 2026-04 VK requires v=5.275 for the
// anonymous-token + getCallPreview pair; older 5.264 returns
// "Rate limit reached" (error_code 29) on getAnonymousAccessTokenPayload.
const apiVersion = "5.275"

// Random "guest names" — VK accepts any short string here, but a
// fresh-looking value reduces bot-detection signal compared to a
// literal "123" or empty.
var guestNames = []string{"Гость", "Anna", "Ivan", "Maxim", "Olga", "Sergey", "Maria", "Pavel"}

func pickGuestName() string { return guestNames[rand.IntN(len(guestNames))] }

// vkResponse is the lowest-common-denominator shape of every JSON
// response from VK / OK we touch. We decode into map[string]any and
// inspect specific paths, because the actual schemas are richer than
// we need and prone to drift.
type vkResponse map[string]any

// apiClient is the stateful glue around an http.Client that talks to
// VK / OK and walks through the 5-step anonymous-token flow.
type apiClient struct {
	http      *http.Client
	profile   browserProfile
	logger    wgturn.Logger
	appID     string
	clientSec string
	hosts     apiHosts
	captcha   CaptchaSolver
}

// apiHosts pulls every endpoint host into one struct so tests can
// rewrite them all at once to a single httptest server.
type apiHosts struct {
	login string // login.vk.ru
	api   string // api.vk.ru
	ok    string // calls.okcdn.ru
}

func defaultHosts() apiHosts {
	return apiHosts{login: hostLogin, api: hostAPI, ok: hostOK}
}

// post issues a form-encoded POST and decodes JSON. The headers are
// the bare minimum needed to look like a browser; we don't attempt to
// match the exact wire of vk.com beyond UA + Sec-CH-UA hints.
func (c *apiClient) post(ctx context.Context, fullURL string, form url.Values) (vkResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	// Browser-shaped header set. Chrome + cross-origin POST is the
	// shape VK Calls' web client uses; missing Origin / Referer /
	// Sec-Fetch-* triggers the captcha challenge on
	// calls.getAnonymousToken even after a JA3-correct handshake.
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("sec-ch-ua", c.profile.SecChUa)
	req.Header.Set("sec-ch-ua-mobile", c.profile.SecChUaMobile)
	req.Header.Set("sec-ch-ua-platform", c.profile.SecChUaPlatform)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,ru;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Origin", "https://vk.com")
	req.Header.Set("Referer", "https://vk.com/")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("DNT", "1")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB safety cap
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: HTTP %d %s", wgturn.ErrAuthFailure, resp.StatusCode, bytesPreview(body))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, resp.Status, bytesPreview(body))
	}

	var out vkResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode JSON: %w (body preview: %s)", err, bytesPreview(body))
	}

	if err := detectVKError(out); err != nil {
		return nil, err
	}
	return out, nil
}

// fetchTurn runs the 5-step VK Calls handshake and returns the TURN
// credentials. callID is the bare invite id (no scheme, no path),
// already extracted by extractCallID.
//
// Wire flow (matches kiper292/wireguard-turn-android, v=5.275):
//
//  1. POST login.vk.ru?act=get_anonym_token             → token1
//  2. POST api.vk.ru/method/calls.getCallPreview        (best-effort)
//  3. POST api.vk.ru/method/calls.getAnonymousToken      → token2 (call-scoped)
//  4. POST calls.okcdn.ru/fb.do auth.anonymLogin         → session_key
//  5. POST calls.okcdn.ru/fb.do vchat.joinConversationByLink → TURN creds
//
// Returns username, password, server_addr (host:port — port mandatory).
func (c *apiClient) fetchTurn(ctx context.Context, callID string) (string, string, string, error) {
	// Step 1: anonymous primary token.
	resp, err := c.post(ctx, c.hosts.login+"/?act=get_anonym_token",
		url.Values{
			"client_id":     {c.appID},
			"token_type":    {"messages"},
			"client_secret": {c.clientSec},
			"version":       {"1"},
			"app_id":        {c.appID},
		})
	if err != nil {
		return "", "", "", fmt.Errorf("step1 anonym token: %w", err)
	}
	token1, err := digString(resp, "data", "access_token")
	if err != nil {
		return "", "", "", fmt.Errorf("step1: %w", err)
	}

	// Step 2: getCallPreview is informational and best-effort. VK
	// uses it as a fingerprint-collection step before issuing the
	// call-scoped token in step 3 — calling it with the same token1
	// keeps the bot heuristics happier. Errors here do NOT abort.
	previewLink := hostUserBase + "/call/join/" + callID // vk.com domain
	if c.hosts.api == hostAPI {
		// vk.ru in production mirrors the canonical URL the kiper292
		// reference uses. In tests we stay on whatever host is set.
		previewLink = "https://vk.ru/call/join/" + callID
	}
	_, _ = c.post(ctx,
		c.hosts.api+"/method/calls.getCallPreview?v="+apiVersion+"&client_id="+c.appID,
		url.Values{
			"vk_join_link": {previewLink},
			"fields":       {"photo_200"},
			"access_token": {token1},
		})

	// Step 3: call-scoped anonymous token. As of 2026-Q2 VK ALWAYS
	// requires a captcha here for the public anonymous app id; if
	// the embedder didn't wire a CaptchaSolver, we return early
	// with ErrCaptchaRequired. Up to maxCaptchaAttempts retries
	// are allowed in case the user mis-typed the first time.
	step3Form := url.Values{
		"vk_join_link": {hostUserBase + "/call/join/" + callID},
		"name":         {pickGuestName()},
		"access_token": {token1},
	}
	step3URL := c.hosts.api + "/method/calls.getAnonymousToken?v=" + apiVersion + "&client_id=" + c.appID

	const maxCaptchaAttempts = 3
	// Bound the whole captcha-retry loop: 3 attempts each Solve-ing for up to
	// ~60s could otherwise block one Fetch ~3 min, far past StartTimeout. Cap
	// total captcha cost per Fetch (caller's ctx still wins if shorter).
	captchaCtx, captchaCancel := context.WithTimeout(ctx, 90*time.Second)
	defer captchaCancel()
	var token2 string
attempts:
	for attempt := 0; attempt < maxCaptchaAttempts; attempt++ {
		resp, err = c.post(captchaCtx, step3URL, step3Form)
		switch {
		case err == nil:
			break attempts
		case errors.Is(err, errCaptchaChallenge):
			ch := lastCaptchaChallenge(err)
			if ch == nil || c.captcha == nil {
				return "", "", "", fmt.Errorf("step3 anon token: %w", wgturn.ErrCaptchaRequired)
			}
			c.logger.Infof("[vk] captcha required (attempt %d/%d): sid=%s img=%s",
				attempt+1, maxCaptchaAttempts, ch.SID, ch.ImgURL)

			sol, solveErr := c.captcha.Solve(captchaCtx, *ch)
			if solveErr != nil {
				return "", "", "", fmt.Errorf("step3 captcha solver: %w", solveErr)
			}
			if applyErr := applySolution(step3Form, *ch, sol); applyErr != nil {
				return "", "", "", fmt.Errorf("step3 captcha apply: %w", applyErr)
			}
			c.logger.Debugf("[vk] retrying step3 with captcha solution")
			continue
		default:
			return "", "", "", fmt.Errorf("step3 anon token: %w", err)
		}
	}
	if err != nil {
		return "", "", "", fmt.Errorf("step3 anon token after %d captcha attempts: %w",
			maxCaptchaAttempts, err)
	}
	token2, err = digString(resp, "response", "token")
	if err != nil {
		return "", "", "", fmt.Errorf("step3: %w", err)
	}

	// Step 4: OK CDN session_key. Note the response shape: session_key
	// is a TOP-level field, not under "response".
	deviceID := uuid.New().String()
	sessionData := fmt.Sprintf(
		`{"version":2,"device_id":"%s","client_version":1.1,"client_type":"SDK_JS"}`,
		deviceID)
	resp, err = c.post(ctx, c.hosts.ok+"/fb.do",
		url.Values{
			"session_data":    {sessionData},
			"method":          {"auth.anonymLogin"},
			"format":          {"JSON"},
			"application_key": {"CGMMEJLGDIHBABABA"},
		})
	if err != nil {
		return "", "", "", fmt.Errorf("step4 ok login: %w", err)
	}
	sessionKey, err := digString(resp, "session_key")
	if err != nil {
		return "", "", "", fmt.Errorf("step4: %w", err)
	}

	// Step 5: vchat join → TURN credentials. capabilities=2F7F is the
	// kiper292 default and apparently the value VK / OK CDN expect now.
	resp, err = c.post(ctx, c.hosts.ok+"/fb.do",
		url.Values{
			"joinLink":        {callID},
			"isVideo":         {"false"},
			"protocolVersion": {"5"},
			"capabilities":    {"2F7F"},
			"anonymToken":     {token2},
			"method":          {"vchat.joinConversationByLink"},
			"format":          {"JSON"},
			"application_key": {"CGMMEJLGDIHBABABA"},
			"session_key":     {sessionKey},
		})
	if err != nil {
		return "", "", "", fmt.Errorf("step5 vchat: %w", err)
	}
	user, err := digString(resp, "turn_server", "username")
	if err != nil {
		return "", "", "", fmt.Errorf("step5 username: %w", err)
	}
	pass, err := digString(resp, "turn_server", "credential")
	if err != nil {
		return "", "", "", fmt.Errorf("step5 credential: %w", err)
	}
	urls, err := digSlice(resp, "turn_server", "urls")
	if err != nil {
		return "", "", "", fmt.Errorf("step5 urls: %w", err)
	}
	if len(urls) == 0 {
		return "", "", "", errors.New("step5: empty turn_server.urls")
	}
	rawURL, ok := urls[0].(string)
	if !ok {
		return "", "", "", errors.New("step5: turn_server.urls[0] is not a string")
	}
	addr, err := parseTurnURL(rawURL)
	if err != nil {
		return "", "", "", fmt.Errorf("step5 url: %w", err)
	}
	return user, pass, addr, nil
}

// digString walks a vkResponse along the given keys and returns the
// final value as a string. It returns a clear error if any segment
// is missing or the leaf is the wrong type.
func digString(resp vkResponse, keys ...string) (string, error) {
	v, err := dig(resp, keys...)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("field %v is not a string (got %T)", keys, v)
	}
	return s, nil
}

func digSlice(resp vkResponse, keys ...string) ([]any, error) {
	v, err := dig(resp, keys...)
	if err != nil {
		return nil, err
	}
	s, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("field %v is not an array (got %T)", keys, v)
	}
	return s, nil
}

func dig(resp vkResponse, keys ...string) (any, error) {
	var cur any = map[string]any(resp)
	for i, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %v: segment %d is %T not object", keys, i, cur)
		}
		cur, ok = m[k]
		if !ok {
			return nil, fmt.Errorf("path %v: missing key %q", keys, k)
		}
	}
	return cur, nil
}

// bytesPreview returns up to 200 bytes of a buffer for use in error
// messages. Avoids dumping multi-megabyte bodies into logs.
func bytesPreview(b []byte) string {
	const max = 200
	if len(b) <= max {
		return string(bytes.TrimSpace(b))
	}
	return string(bytes.TrimSpace(b[:max])) + "...(truncated)"
}

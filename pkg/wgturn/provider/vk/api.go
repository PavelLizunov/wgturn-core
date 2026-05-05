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
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/slovn/wgturn-core/pkg/wgturn"
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

// vkResponse is the lowest-common-denominator shape of every JSON
// response from VK / OK we touch. We decode into map[string]any and
// inspect specific paths, because the actual schemas are richer than
// we need and prone to drift.
type vkResponse map[string]any

// apiClient is the stateful glue around an http.Client that talks to
// VK / OK and walks through the 6-step anonymous-token flow.
type apiClient struct {
	http       *http.Client
	profile    browserProfile
	logger     wgturn.Logger
	appID      string
	clientSec  string
	hosts      apiHosts
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
// match the exact wire of vk.com beyond UA + Origin.
func (c *apiClient) post(ctx context.Context, fullURL string, form url.Values) (vkResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Sec-CH-UA", c.profile.SecChUa)
	req.Header.Set("Sec-CH-UA-Mobile", c.profile.SecChUaMobile)
	req.Header.Set("Sec-CH-UA-Platform", c.profile.SecChUaPlatform)
	req.Header.Set("Accept", "application/json, text/plain, */*")

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

// fetchTurn runs the full 6-step dance. callID is the bare invite id
// (no scheme, no path), already extracted by extractCallID.
//
// Returns username, password, server_addr (host:port — port mandatory).
func (c *apiClient) fetchTurn(ctx context.Context, callID string) (string, string, string, error) {
	// Step 1: primary anonymous token.
	resp, err := c.post(ctx, c.hosts.login+"/?act=get_anonym_token",
		url.Values{
			"client_secret":             {c.clientSec},
			"client_id":                 {c.appID},
			"scopes":                    {"audio_anonymous,video_anonymous,photos_anonymous,profile_anonymous"},
			"isApiOauthAnonymEnabled":   {"false"},
			"version":                   {"1"},
			"app_id":                    {c.appID},
		})
	if err != nil {
		return "", "", "", fmt.Errorf("step1 anonym token: %w", err)
	}
	token1, err := digString(resp, "data", "access_token")
	if err != nil {
		return "", "", "", fmt.Errorf("step1: %w", err)
	}

	// Step 2: anonymous-access-token payload.
	resp, err = c.post(ctx,
		c.hosts.api+"/method/calls.getAnonymousAccessTokenPayload?v=5.264&client_id="+c.appID,
		url.Values{"access_token": {token1}})
	if err != nil {
		return "", "", "", fmt.Errorf("step2 payload: %w", err)
	}
	payload, err := digString(resp, "response", "payload")
	if err != nil {
		return "", "", "", fmt.Errorf("step2: %w", err)
	}

	// Step 3: secondary token using payload.
	resp, err = c.post(ctx, c.hosts.login+"/?act=get_anonym_token",
		url.Values{
			"client_id":     {c.appID},
			"token_type":    {"messages"},
			"payload":       {payload},
			"client_secret": {c.clientSec},
			"version":       {"1"},
			"app_id":        {c.appID},
		})
	if err != nil {
		return "", "", "", fmt.Errorf("step3 token: %w", err)
	}
	token3, err := digString(resp, "data", "access_token")
	if err != nil {
		return "", "", "", fmt.Errorf("step3: %w", err)
	}

	// Step 4: call-scoped anonymous token.
	resp, err = c.post(ctx, c.hosts.api+"/method/calls.getAnonymousToken?v=5.264",
		url.Values{
			"vk_join_link": {hostUserBase + "/call/join/" + callID},
			"name":         {"123"}, // upstream uses literal "123"; appears unparsed
			"access_token": {token3},
		})
	if err != nil {
		return "", "", "", fmt.Errorf("step4 anon token: %w", err)
	}
	token4, err := digString(resp, "response", "token")
	if err != nil {
		return "", "", "", fmt.Errorf("step4: %w", err)
	}

	// Step 5: OK CDN session_key.
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
		return "", "", "", fmt.Errorf("step5 ok login: %w", err)
	}
	sessionKey, err := digString(resp, "session_key")
	if err != nil {
		return "", "", "", fmt.Errorf("step5: %w", err)
	}

	// Step 6: vchat join → TURN credentials.
	resp, err = c.post(ctx, c.hosts.ok+"/fb.do",
		url.Values{
			"joinLink":        {callID},
			"isVideo":         {"false"},
			"protocolVersion": {"5"},
			"anonymToken":     {token4},
			"method":          {"vchat.joinConversationByLink"},
			"format":          {"JSON"},
			"application_key": {"CGMMEJLGDIHBABABA"},
			"session_key":     {sessionKey},
		})
	if err != nil {
		return "", "", "", fmt.Errorf("step6 vchat: %w", err)
	}
	user, err := digString(resp, "turn_server", "username")
	if err != nil {
		return "", "", "", fmt.Errorf("step6 username: %w", err)
	}
	pass, err := digString(resp, "turn_server", "credential")
	if err != nil {
		return "", "", "", fmt.Errorf("step6 credential: %w", err)
	}
	urls, err := digSlice(resp, "turn_server", "urls")
	if err != nil {
		return "", "", "", fmt.Errorf("step6 urls: %w", err)
	}
	if len(urls) == 0 {
		return "", "", "", errors.New("step6: empty turn_server.urls")
	}
	rawURL, ok := urls[0].(string)
	if !ok {
		return "", "", "", errors.New("step6: turn_server.urls[0] is not a string")
	}
	addr, err := parseTurnURL(rawURL)
	if err != nil {
		return "", "", "", fmt.Errorf("step6 url: %w", err)
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

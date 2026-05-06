// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package yandex

import (
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

// fetchTurn drives the Telemost anonymous-conference API end to end
// and returns (username, password, server_addr, err). The flow:
//
//  1. GET cloud-api.yandex.ru/telemost_front/v2/telemost/conferences/
//     <URL-ENCODED-LINK>/connection?next_gen_media_platform_allowed=false
//  2. Parse the response. If it carries TURN credentials directly
//     (the simple-anon path that Telemost briefly used in 2025),
//     return them.
//  3. Otherwise, open a WebSocket to media_server_url, send a hello
//     frame, read ServerHello.RtcConfiguration.IceServers, return the
//     first turn:/turns: entry.
//
// Step 3 is the post-2026 shape — Telemost moved the TURN credentials
// out of the bootstrap response and into the WebSocket negotiation
// to mirror Yandex Cloud's WebRTC flow.
func fetchTurn(ctx context.Context, hc *http.Client, base, callID string, log wgturn.Logger) (string, string, string, error) {
	if hc == nil {
		return "", "", "", errors.New("yandex: http client is nil")
	}
	if base == "" {
		base = cloudAPIHost
	}
	base = strings.TrimRight(base, "/")

	link := "https://telemost.yandex.ru/j/" + callID
	endpoint := fmt.Sprintf("%s/telemost_front/v2/telemost/conferences/%s/connection?next_gen_media_platform_allowed=false",
		base, url.PathEscape(link))

	log.Debugf("[yandex] step1 GET %s", endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("build step1 request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,ru;q=0.8")
	req.Header.Set("Referer", "https://telemost.yandex.ru/")
	req.Header.Set("Origin", "https://telemost.yandex.ru")
	req.Header.Set("Client-Instance-Id", uuid.New().String())

	resp, err := hc.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("step1 http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", fmt.Errorf("step1 read: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// happy path; fall through.
	case http.StatusNotFound:
		var apiErr struct {
			Error       string `json:"error"`
			Description string `json:"description"`
			Message     string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiErr)
		return "", "", "", fmt.Errorf("%w: telemost: %s — %s",
			wgturn.ErrAuthFailure, apiErr.Error, apiErr.Description)
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", "", "", fmt.Errorf("%w: HTTP %d %s", wgturn.ErrAuthFailure,
			resp.StatusCode, bytesPreview(body))
	default:
		return "", "", "", fmt.Errorf("step1 HTTP %d: %s", resp.StatusCode, bytesPreview(body))
	}

	var conf conferenceResponse
	if err := json.Unmarshal(body, &conf); err != nil {
		return "", "", "", fmt.Errorf("step1 decode: %w (body=%s)", err, bytesPreview(body))
	}

	// Path A: response already carries ice_servers / turn_servers
	// directly (some Telemost deployments do this for thin clients).
	if u, p, addr, ok := turnFromConference(&conf); ok {
		log.Debugf("[yandex] step1 yielded TURN inline (no WS needed)")
		return u, p, addr, nil
	}

	// Path B: WebSocket negotiation.
	if conf.MediaServerURL == "" {
		return "", "", "", errors.New("yandex: step1 missing media_server_url and no inline TURN creds")
	}
	log.Debugf("[yandex] step1 ok, opening WS to %s", conf.MediaServerURL)

	user, pass, addr, err := wsFetchTurn(ctx, hc, &conf, log)
	if err != nil {
		return "", "", "", fmt.Errorf("step2 ws: %w", err)
	}
	return user, pass, addr, nil
}

// conferenceResponse mirrors the JSON shape returned by step 1. We
// keep it lenient — extra fields are ignored, and we look in two
// well-known locations for the TURN creds (root and `credentials`).
type conferenceResponse struct {
	RoomID         string            `json:"room_id"`
	PeerID         string            `json:"peer_id"`
	MediaServerURL string            `json:"media_server_url"`
	Credentials    *credentialsBlock `json:"credentials"`

	// Some deployments return ice_servers inline at the root.
	IceServers []iceServer `json:"ice_servers,omitempty"`
}

type credentialsBlock struct {
	Token      string      `json:"token"`
	IceServers []iceServer `json:"ice_servers,omitempty"`
}

type iceServer struct {
	Urls       jsonStringOrSlice `json:"urls"`
	Username   string            `json:"username"`
	Credential string            `json:"credential"`
}

// jsonStringOrSlice accepts either a single string or an array of
// strings — Telemost has used both shapes for IceServer.urls. Normalise
// to a slice on decode.
type jsonStringOrSlice []string

func (j *jsonStringOrSlice) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*j = []string{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	*j = arr
	return nil
}

// turnFromConference returns the first turn:/turns: ICE server in the
// step-1 response (either at the root or under credentials.ice_servers).
func turnFromConference(c *conferenceResponse) (string, string, string, bool) {
	pools := [][]iceServer{c.IceServers}
	if c.Credentials != nil {
		pools = append(pools, c.Credentials.IceServers)
	}
	for _, pool := range pools {
		for _, srv := range pool {
			for _, raw := range srv.Urls {
				if !isTurnURL(raw) {
					continue
				}
				addr, err := parseTurnURL(raw)
				if err != nil {
					continue
				}
				return srv.Username, srv.Credential, addr, true
			}
		}
	}
	return "", "", "", false
}

func isTurnURL(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "turn:") || strings.HasPrefix(s, "turns:")
}

// defaultUA mirrors a recent stable Chrome on Linux. Yandex's API
// doesn't seem to fingerprint UA strictly, but a sane value reduces
// the chance of triggering bot heuristics on related endpoints.
const defaultUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"

// bytesPreview returns up to 200 bytes of a buffer for use in error
// messages. Avoids dumping multi-megabyte bodies into logs.
func bytesPreview(b []byte) string {
	const max = 200
	if len(b) <= max {
		return strings.TrimSpace(string(b))
	}
	return strings.TrimSpace(string(b[:max])) + "...(truncated)"
}

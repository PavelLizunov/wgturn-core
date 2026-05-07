// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgadmin

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"
	"strings"
)

// nameTag is the comment tag we write next to every provisioned peer
// so List can recover the friendly name. Kept compatible with the
// legacy provision-user.sh format.
const nameTag = "# wgturn-name ="

// Peer is one wg0.conf [Peer] entry, decorated with the friendly
// name we tag in a comment. Unset Name means the peer was added by
// hand without our tag — List preserves it as "".
type Peer struct {
	// Name is the friendly identifier from the `# wgturn-name = …`
	// comment. Empty when the peer wasn't provisioned through
	// wgadmin.
	Name string

	// PublicKey is the peer's WG public key (base64).
	PublicKey string

	// PresharedKey is the optional symmetric key (base64).
	PresharedKey string

	// AllowedIPs is the set of CIDRs whose packets route to this
	// peer. For a wgturn server's wg0 this is typically a single
	// /32 — the client's tunnel address.
	AllowedIPs []netip.Prefix

	// raw block, including the trailing blank line. Preserved so we
	// can rewrite the file with minimum visual churn.
	rawLines []string
}

// confState is the parsed shape of wg0.conf: the [Interface] section
// (we care about PrivateKey to derive the server's public key) plus
// the ordered list of peers.
type confState struct {
	// rawLines is the entire file with peer blocks replaced by
	// placeholders so writeConf can splice in the up-to-date peer
	// list. Lines outside [Peer] sections are preserved byte-for-byte.
	header []string

	privateKey string
	peers      []Peer
}

// parseConf reads a wg-quick-style config file. The grammar we care
// about: [Interface] / [Peer] section headers, and Key = Value lines
// inside them. PostUp / Table / FwMark and friends pass through
// untouched in header / rawLines.
func parseConf(r io.Reader) (*confState, error) {
	state := &confState{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	type section int
	const (
		sectionNone section = iota
		sectionInterface
		sectionPeer
	)

	cur := sectionNone
	var pendingPeer *Peer
	flushPeer := func() {
		if pendingPeer != nil {
			state.peers = append(state.peers, *pendingPeer)
			pendingPeer = nil
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Section headers.
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			flushPeer()
			name := strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			switch name {
			case "interface":
				cur = sectionInterface
				state.header = append(state.header, line)
			case "peer":
				cur = sectionPeer
				pendingPeer = &Peer{rawLines: []string{line}}
			default:
				cur = sectionNone
				state.header = append(state.header, line)
			}
			continue
		}

		// Comment tagged with our friendly name marker — record it,
		// keep it inside the peer block.
		if cur == sectionPeer && strings.HasPrefix(trimmed, nameTag) {
			pendingPeer.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, nameTag))
			pendingPeer.rawLines = append(pendingPeer.rawLines, line)
			continue
		}

		// Pure comment / blank lines pass through.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			if cur == sectionPeer {
				pendingPeer.rawLines = append(pendingPeer.rawLines, line)
			} else {
				state.header = append(state.header, line)
			}
			continue
		}

		// Key = Value.
		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			// Treat as opaque; keep verbatim.
			if cur == sectionPeer {
				pendingPeer.rawLines = append(pendingPeer.rawLines, line)
			} else {
				state.header = append(state.header, line)
			}
			continue
		}
		key := strings.ToLower(strings.TrimSpace(trimmed[:eq]))
		val := strings.TrimSpace(trimmed[eq+1:])

		switch cur {
		case sectionInterface:
			state.header = append(state.header, line)
			if key == "privatekey" {
				state.privateKey = val
			}
		case sectionPeer:
			pendingPeer.rawLines = append(pendingPeer.rawLines, line)
			switch key {
			case "publickey":
				pendingPeer.PublicKey = val
			case "presharedkey":
				pendingPeer.PresharedKey = val
			case "allowedips":
				prefs, err := parsePrefixList(val)
				if err != nil {
					return nil, fmt.Errorf("wgadmin: AllowedIPs %q: %w", val, err)
				}
				pendingPeer.AllowedIPs = prefs
			}
		case sectionNone:
			state.header = append(state.header, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("wgadmin: read conf: %w", err)
	}
	flushPeer()
	return state, nil
}

// writeConf serialises a confState back to wg-quick form. Pre-existing
// peer blocks are emitted via their rawLines (preserving original
// formatting); newly appended peers come out via formatPeer.
func writeConf(w io.Writer, state *confState) error {
	for _, line := range state.header {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	for _, p := range state.peers {
		if len(p.rawLines) > 0 {
			for _, l := range p.rawLines {
				if _, err := fmt.Fprintln(w, l); err != nil {
					return err
				}
			}
			continue
		}
		if err := formatPeer(w, p); err != nil {
			return err
		}
	}
	return nil
}

// formatPeer emits a fresh [Peer] block in the canonical layout the
// legacy provision-user.sh produces. Each provisioned peer has the
// `# wgturn-name = …` tag right after the section header so List can
// round-trip the friendly name.
func formatPeer(w io.Writer, p Peer) error {
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "[Peer]"); err != nil {
		return err
	}
	if p.Name != "" {
		if _, err := fmt.Fprintf(w, "%s %s\n", nameTag, p.Name); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "PublicKey = %s\n", p.PublicKey); err != nil {
		return err
	}
	if p.PresharedKey != "" {
		if _, err := fmt.Fprintf(w, "PresharedKey = %s\n", p.PresharedKey); err != nil {
			return err
		}
	}
	if len(p.AllowedIPs) > 0 {
		parts := make([]string, len(p.AllowedIPs))
		for i, a := range p.AllowedIPs {
			parts[i] = a.String()
		}
		if _, err := fmt.Fprintf(w, "AllowedIPs = %s\n", strings.Join(parts, ", ")); err != nil {
			return err
		}
	}
	return nil
}

// parsePrefixList splits a wg-quick AllowedIPs list (comma- or
// whitespace-separated) into typed prefixes. Bare addresses become
// /32 (IPv4) or /128 (IPv6) — matches wg-quick's lenience.
func parsePrefixList(v string) ([]netip.Prefix, error) {
	tokens := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]netip.Prefix, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if pref, err := netip.ParsePrefix(t); err == nil {
			out = append(out, pref)
			continue
		}
		addr, err := netip.ParseAddr(t)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR or address %q", t)
		}
		bits := 32
		if addr.Is6() {
			bits = 128
		}
		out = append(out, netip.PrefixFrom(addr, bits))
	}
	return out, nil
}

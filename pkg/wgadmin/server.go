// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgadmin

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgshare"
)

// ErrPeerExists is returned by Provision when a peer with the
// requested name is already present in the conf. Revoke first or
// pick a different name.
var ErrPeerExists = errors.New("wgadmin: peer already exists")

// ErrPeerNotFound is returned by Revoke when no peer matches the
// supplied name.
var ErrPeerNotFound = errors.New("wgadmin: peer not found")

// Server bundles the static configuration of a single wgturn server's
// WireGuard plane. Construct with NewServer; call Provision / Revoke /
// List to manage clients.
//
// Server is safe for concurrent use within one process. Across
// processes, take a file lock yourself (e.g. flock on
// /var/lock/wgturn-provision.lock); the legacy admin scripts use
// the same convention.
type Server struct {
	// ConfPath is the wg-quick-style config file. Typically
	// /etc/wireguard/wg0.conf. Read on every operation; truncated and
	// rewritten on Provision / Revoke.
	ConfPath string

	// Interface is the kernel WG interface name passed to
	// `wg syncconf`. Typically "wg0".
	Interface string

	// Subnet is the CIDR client tunnel addresses are allocated from.
	// Typically 10.7.0.0/24. Server's own address is reserved (see
	// ServerAddress).
	Subnet netip.Prefix

	// ServerAddress is the gateway address inside Subnet (typically
	// the first host, e.g. 10.7.0.1). Skipped during allocation so
	// clients don't collide with the server side.
	ServerAddress netip.Addr

	// Endpoint is the public host:port of the wgturn DTLS listener,
	// emitted into every share URL the server produces. Not the
	// underlying WG endpoint — that's a runtime concept.
	Endpoint string

	// AllowedIPs is the set of CIDRs put into each client profile's
	// AllowedIPs. Default: [0.0.0.0/0] (full tunnel).
	AllowedIPs []netip.Prefix

	// DNS, MTU, PersistentKeepalive are forwarded into Profile
	// untouched. Zero values mean "leave unset".
	DNS                 []netip.Addr
	MTU                 int
	PersistentKeepalive time.Duration

	// SyncCommand overrides the `wg syncconf <iface> <stripped-conf>`
	// invocation the server runs after each Provision/Revoke. The
	// hook expects a working stdin (the stripped wg0.conf), and
	// returns the running cmd. Default: real `wg` binary.
	//
	// Tests inject a stub that records calls.
	SyncCommand func(iface string, strippedConf []byte) error

	// mu is a pointer to a mutex so callers can copy Server values
	// (config shapes) without tripping copylocks; NewServer always
	// allocates a fresh mutex. Direct struct-literal construction
	// is supported because the mu==nil branch in lock() handles it.
	mu *sync.Mutex
}

// NewServer returns a Server with sensible defaults filled in for any
// fields the caller leaves zero:
//
//   - Subnet = 10.7.0.0/24
//   - ServerAddress = 10.7.0.1
//   - AllowedIPs = [0.0.0.0/0]
//   - SyncCommand = real `wg` binary
//
// The caller still has to set ConfPath, Interface, and Endpoint.
func NewServer(s Server) *Server {
	if !s.Subnet.IsValid() {
		s.Subnet = netip.MustParsePrefix("10.7.0.0/24")
	}
	if !s.ServerAddress.IsValid() {
		// First usable host in the subnet.
		s.ServerAddress = s.Subnet.Masked().Addr().Next()
	}
	if len(s.AllowedIPs) == 0 {
		s.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	}
	if s.SyncCommand == nil {
		s.SyncCommand = realSyncConf
	}
	if s.mu == nil {
		s.mu = &sync.Mutex{}
	}
	return &s
}

// lock guards mutating operations. Tolerates a Server constructed by
// struct literal (mu==nil) by lazily allocating once; subsequent
// callers see the allocated mutex. Not race-safe for concurrent
// first-callers — pair with NewServer in any threaded use.
func (s *Server) lock() {
	if s.mu == nil {
		s.mu = &sync.Mutex{}
	}
	s.mu.Lock()
}

func (s *Server) unlock() { s.mu.Unlock() }

// List enumerates the peers currently in ConfPath. Peers without a
// `# wgturn-name = …` tag come back with Name=="".
func (s *Server) List() ([]Peer, error) {
	s.lock()
	defer s.unlock()
	state, err := s.readState()
	if err != nil {
		return nil, err
	}
	out := make([]Peer, len(state.peers))
	copy(out, state.peers)
	// Strip rawLines so callers don't see internals.
	for i := range out {
		out[i].rawLines = nil
	}
	return out, nil
}

// Provision creates a new client with the given friendly name:
//   - generates a fresh keypair and PSK
//   - allocates the lowest free /32 in Subnet
//   - appends a [Peer] block to ConfPath
//   - runs `wg syncconf` so the live interface picks up the new peer
//     without bouncing existing sessions
//   - returns the wgshare.Profile the operator hands to the user
//     (typically via Encode → wgturn:// URL)
//
// name must be non-empty and unique within ConfPath. Concurrent
// Provision calls within one Server instance are serialised; across
// processes use a file lock.
func (s *Server) Provision(name string) (wgshare.Profile, error) {
	if name == "" {
		return wgshare.Profile{}, errors.New("wgadmin: peer name is required")
	}

	s.lock()
	defer s.unlock()

	state, err := s.readState()
	if err != nil {
		return wgshare.Profile{}, err
	}
	for _, p := range state.peers {
		if p.Name == name {
			return wgshare.Profile{}, fmt.Errorf("%w: %q", ErrPeerExists, name)
		}
	}

	serverPub, err := PublicKeyFor(state.privateKey)
	if err != nil {
		return wgshare.Profile{}, fmt.Errorf("wgadmin: derive server public: %w", err)
	}

	cliKey, err := GenerateKeypair()
	if err != nil {
		return wgshare.Profile{}, err
	}
	psk, err := GeneratePresharedKey()
	if err != nil {
		return wgshare.Profile{}, err
	}

	addr, err := AllocateClientIP(s.Subnet, s.ServerAddress, existingPeerAddrs(state.peers))
	if err != nil {
		return wgshare.Profile{}, err
	}

	newPeer := Peer{
		Name:         name,
		PublicKey:    cliKey.Public,
		PresharedKey: psk,
		AllowedIPs:   []netip.Prefix{netip.PrefixFrom(addr, addr.BitLen())},
	}
	state.peers = append(state.peers, newPeer)

	if err := s.writeState(state); err != nil {
		return wgshare.Profile{}, err
	}
	if err := s.sync(state); err != nil {
		return wgshare.Profile{}, err
	}

	return wgshare.Profile{
		Label:               name,
		ServerPublicKey:     serverPub,
		ClientPrivateKey:    cliKey.Private,
		PresharedKey:        psk,
		Endpoint:            s.Endpoint,
		Address:             netip.PrefixFrom(addr, s.Subnet.Bits()),
		AllowedIPs:          s.AllowedIPs,
		DNS:                 s.DNS,
		MTU:                 s.MTU,
		PersistentKeepalive: s.PersistentKeepalive,
	}, nil
}

// Revoke removes the peer with the given name from ConfPath and
// resyncs the live interface. ErrPeerNotFound when there is no such
// peer. The recovered /32 becomes available to subsequent Provisions.
func (s *Server) Revoke(name string) error {
	if name == "" {
		return errors.New("wgadmin: peer name is required")
	}
	s.lock()
	defer s.unlock()

	state, err := s.readState()
	if err != nil {
		return err
	}
	idx := -1
	for i, p := range state.peers {
		if p.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %q", ErrPeerNotFound, name)
	}
	state.peers = append(state.peers[:idx], state.peers[idx+1:]...)

	if err := s.writeState(state); err != nil {
		return err
	}
	return s.sync(state)
}

// readState parses ConfPath under the assumption it already exists;
// callers are expected to have set up wg-quick at server-deploy time.
func (s *Server) readState() (*confState, error) {
	if s.ConfPath == "" {
		return nil, errors.New("wgadmin: ConfPath is required")
	}
	f, err := os.Open(s.ConfPath) //nolint:gosec // operator-supplied admin path
	if err != nil {
		return nil, fmt.Errorf("wgadmin: open conf: %w", err)
	}
	defer func() { _ = f.Close() }()
	state, err := parseConf(f)
	if err != nil {
		return nil, err
	}
	if state.privateKey == "" {
		return nil, errors.New("wgadmin: [Interface] PrivateKey not found in conf")
	}
	return state, nil
}

// writeState atomically rewrites ConfPath: dump to a temp file
// alongside it, fsync, rename. Crash-safe — wg syncconf runs after
// the file is fully on disk.
func (s *Server) writeState(state *confState) error {
	tmp, err := os.CreateTemp(filepath.Dir(s.ConfPath), filepath.Base(s.ConfPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("wgadmin: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // no-op if rename succeeded
	}()

	if err := writeConf(tmp, state); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("wgadmin: write conf: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("wgadmin: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("wgadmin: close tmp: %w", err)
	}

	// Preserve the original mode (typically 0600 for /etc/wireguard
	// content). os.Rename clobbers permissions on some filesystems
	// otherwise.
	if info, err := os.Stat(s.ConfPath); err == nil {
		_ = os.Chmod(tmpName, info.Mode())
	}
	if err := os.Rename(tmpName, s.ConfPath); err != nil {
		return fmt.Errorf("wgadmin: rename: %w", err)
	}
	return nil
}

// sync re-renders state into wg-quick stripped form and pipes it
// into `wg syncconf`. The strip leaves only [Interface]/[Peer] keys
// wireguard-tools recognises (no PostUp, no Address, no comments).
func (s *Server) sync(state *confState) error {
	stripped := strippedConf(state)
	if err := s.SyncCommand(s.Interface, stripped); err != nil {
		return fmt.Errorf("wgadmin: wg syncconf: %w", err)
	}
	return nil
}

// strippedConf produces the format `wg syncconf` understands: the
// PrivateKey + ListenPort from [Interface] (no Address / DNS / MTU /
// PostUp), plus [Peer] blocks containing only PublicKey,
// PresharedKey, AllowedIPs, Endpoint, PersistentKeepalive.
func strippedConf(state *confState) []byte {
	var buf bytes.Buffer
	buf.WriteString("[Interface]\n")
	buf.WriteString("PrivateKey = ")
	buf.WriteString(state.privateKey)
	buf.WriteByte('\n')
	for _, line := range state.header {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(trimmed), "listenport") {
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	for _, p := range state.peers {
		buf.WriteString("\n[Peer]\n")
		buf.WriteString("PublicKey = ")
		buf.WriteString(p.PublicKey)
		buf.WriteByte('\n')
		if p.PresharedKey != "" {
			buf.WriteString("PresharedKey = ")
			buf.WriteString(p.PresharedKey)
			buf.WriteByte('\n')
		}
		if len(p.AllowedIPs) > 0 {
			parts := make([]string, len(p.AllowedIPs))
			for i, a := range p.AllowedIPs {
				parts[i] = a.String()
			}
			buf.WriteString("AllowedIPs = ")
			buf.WriteString(strings.Join(parts, ", "))
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

// realSyncConf shells out to `wg syncconf <iface> /dev/stdin` with
// the stripped conf piped to stdin. Errors propagate verbatim with
// stderr captured.
func realSyncConf(iface string, strippedConf []byte) error {
	cmd := exec.Command("wg", "syncconf", iface, "/dev/stdin")
	cmd.Stdin = bytes.NewReader(strippedConf)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

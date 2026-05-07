// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgadmin_test

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PavelLizunov/wgturn-core/pkg/wgadmin"
)

// recordingSync stands in for the real `wg syncconf` invocation. It
// captures every (iface, conf) pair so tests can assert the right
// content reaches wireguard-tools without forking actual binaries.
type recordingSync struct {
	mu    sync.Mutex
	calls []struct {
		iface string
		conf  string
	}
}

func (r *recordingSync) Sync(iface string, conf []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct {
		iface string
		conf  string
	}{iface, string(conf)})
	return nil
}

// writeServerConf seeds a wg0.conf with the supplied private key and
// no peers. Use this as the starting point for Provision tests so
// each gets a clean slate.
func writeServerConf(t *testing.T, priv string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wg0.conf")
	body := "[Interface]\nPrivateKey = " + priv + "\nAddress = 10.7.0.1/24\nListenPort = 51820\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	return path
}

// TestServer_Provision_FirstUser walks the happy path: one Provision
// call must pick 10.7.0.2, append a tagged [Peer] block to the conf,
// run wg syncconf with the stripped form, and return a Profile that
// round-trips through wgshare.
func TestServer_Provision_FirstUser(t *testing.T) {
	srvKey, err := wgadmin.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	confPath := writeServerConf(t, srvKey.Private)

	rec := &recordingSync{}
	s := wgadmin.NewServer(wgadmin.Server{
		ConfPath:    confPath,
		Interface:   "wg0",
		Endpoint:    "is-01.example.com:56000",
		SyncCommand: rec.Sync,
	})

	prof, err := s.Provision("alice")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if prof.Label != "alice" {
		t.Errorf("Label = %q", prof.Label)
	}
	if prof.ServerPublicKey != srvKey.Public {
		t.Errorf("ServerPublicKey not derived from conf private")
	}
	if prof.ClientPrivateKey == "" {
		t.Error("ClientPrivateKey empty")
	}
	if prof.PresharedKey == "" {
		t.Error("PresharedKey empty")
	}
	if prof.Address.String() != "10.7.0.2/24" {
		t.Errorf("Address = %v, want 10.7.0.2/24", prof.Address)
	}

	// The conf should now contain a [Peer] block tagged alice.
	body, err := os.ReadFile(confPath) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# wgturn-name = alice") {
		t.Errorf("conf missing peer tag:\n%s", body)
	}
	if !strings.Contains(string(body), "10.7.0.2/32") {
		t.Errorf("conf missing /32 AllowedIPs:\n%s", body)
	}

	// One sync call should have happened with the stripped form.
	if len(rec.calls) != 1 {
		t.Fatalf("sync calls = %d, want 1", len(rec.calls))
	}
	if rec.calls[0].iface != "wg0" {
		t.Errorf("synced iface = %q", rec.calls[0].iface)
	}
	if !strings.Contains(rec.calls[0].conf, "PublicKey = ") {
		t.Errorf("stripped conf missing PublicKey lines:\n%s", rec.calls[0].conf)
	}
	if strings.Contains(rec.calls[0].conf, "Address") {
		t.Errorf("stripped conf must not contain Address (wg syncconf rejects it):\n%s", rec.calls[0].conf)
	}
}

// TestServer_Provision_SequentialIPs gives sequential clients .2,
// .3, .4 — confirms IP allocation walks forward and finds free slots
// after the previous Provision wrote them to the conf.
func TestServer_Provision_SequentialIPs(t *testing.T) {
	srvKey, _ := wgadmin.GenerateKeypair()
	confPath := writeServerConf(t, srvKey.Private)
	rec := &recordingSync{}
	s := wgadmin.NewServer(wgadmin.Server{
		ConfPath: confPath, Interface: "wg0", Endpoint: "is-01:56000", SyncCommand: rec.Sync,
	})

	wantAddrs := []string{"10.7.0.2/24", "10.7.0.3/24", "10.7.0.4/24"}
	for i, name := range []string{"alice", "bob", "carol"} {
		prof, err := s.Provision(name)
		if err != nil {
			t.Fatalf("Provision %s: %v", name, err)
		}
		if prof.Address.String() != wantAddrs[i] {
			t.Errorf("%s got %v, want %s", name, prof.Address, wantAddrs[i])
		}
	}

	peers, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 3 {
		t.Errorf("List len = %d, want 3", len(peers))
	}
}

// TestServer_Provision_DuplicateName errors with ErrPeerExists; the
// second call must NOT alter the conf or call sync.
func TestServer_Provision_DuplicateName(t *testing.T) {
	srvKey, _ := wgadmin.GenerateKeypair()
	confPath := writeServerConf(t, srvKey.Private)
	rec := &recordingSync{}
	s := wgadmin.NewServer(wgadmin.Server{
		ConfPath: confPath, Interface: "wg0", Endpoint: "is-01:56000", SyncCommand: rec.Sync,
	})

	if _, err := s.Provision("alice"); err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	_, err := s.Provision("alice")
	if !errors.Is(err, wgadmin.ErrPeerExists) {
		t.Errorf("err = %v, want ErrPeerExists", err)
	}
	if len(rec.calls) != 1 {
		t.Errorf("sync calls = %d (a failed Provision must not sync)", len(rec.calls))
	}
}

// TestServer_Revoke removes a peer cleanly: List no longer surfaces
// it, the conf no longer mentions the tag, and sync runs with the
// updated stripped form.
func TestServer_Revoke(t *testing.T) {
	srvKey, _ := wgadmin.GenerateKeypair()
	confPath := writeServerConf(t, srvKey.Private)
	rec := &recordingSync{}
	s := wgadmin.NewServer(wgadmin.Server{
		ConfPath: confPath, Interface: "wg0", Endpoint: "is-01:56000", SyncCommand: rec.Sync,
	})

	if _, err := s.Provision("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Provision("bob"); err != nil {
		t.Fatal(err)
	}

	if err := s.Revoke("alice"); err != nil {
		t.Fatalf("Revoke alice: %v", err)
	}

	peers, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 {
		t.Fatalf("List after revoke = %d, want 1", len(peers))
	}
	if peers[0].Name != "bob" {
		t.Errorf("survivor = %q, want bob", peers[0].Name)
	}

	body, err := os.ReadFile(confPath) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "alice") {
		t.Errorf("alice still in conf:\n%s", body)
	}
}

// TestServer_Revoke_FreesIP confirms a revoked /32 becomes available
// to subsequent Provisions — without this, long-lived servers
// fragment subnets monotonically.
func TestServer_Revoke_FreesIP(t *testing.T) {
	srvKey, _ := wgadmin.GenerateKeypair()
	confPath := writeServerConf(t, srvKey.Private)
	rec := &recordingSync{}
	s := wgadmin.NewServer(wgadmin.Server{
		ConfPath: confPath, Interface: "wg0", Endpoint: "is-01:56000", SyncCommand: rec.Sync,
	})

	a, _ := s.Provision("alice")
	if err := s.Revoke("alice"); err != nil {
		t.Fatal(err)
	}
	b, err := s.Provision("bob")
	if err != nil {
		t.Fatal(err)
	}
	if a.Address != b.Address {
		t.Errorf("alice's freed addr %v not reused by bob (got %v)", a.Address, b.Address)
	}
}

// TestServer_Revoke_NotFound surfaces ErrPeerNotFound and does NOT
// touch the conf or call sync — preserving idempotency for retries.
func TestServer_Revoke_NotFound(t *testing.T) {
	srvKey, _ := wgadmin.GenerateKeypair()
	confPath := writeServerConf(t, srvKey.Private)
	rec := &recordingSync{}
	s := wgadmin.NewServer(wgadmin.Server{
		ConfPath: confPath, Interface: "wg0", Endpoint: "is-01:56000", SyncCommand: rec.Sync,
	})
	err := s.Revoke("ghost")
	if !errors.Is(err, wgadmin.ErrPeerNotFound) {
		t.Errorf("err = %v, want ErrPeerNotFound", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("unexpected sync call on missing peer")
	}
}

// TestServer_Provision_Defaults: when the operator supplies just
// ConfPath/Interface/Endpoint, NewServer must fill Subnet (10.7.0.0/24),
// ServerAddress (10.7.0.1), and AllowedIPs ([0.0.0.0/0]).
func TestServer_Provision_Defaults(t *testing.T) {
	srvKey, _ := wgadmin.GenerateKeypair()
	confPath := writeServerConf(t, srvKey.Private)
	rec := &recordingSync{}
	s := wgadmin.NewServer(wgadmin.Server{
		ConfPath: confPath, Interface: "wg0", Endpoint: "is-01:56000", SyncCommand: rec.Sync,
	})

	prof, err := s.Provision("alice")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Address.String() != "10.7.0.2/24" {
		t.Errorf("Address = %v, want 10.7.0.2/24 (default subnet)", prof.Address)
	}
	want := netip.MustParsePrefix("0.0.0.0/0")
	if len(prof.AllowedIPs) != 1 || prof.AllowedIPs[0] != want {
		t.Errorf("AllowedIPs = %v, want [%v]", prof.AllowedIPs, want)
	}
}

// TestServer_Provision_NoPrivateKey errors loudly when the seed conf
// is missing the [Interface] PrivateKey we need to derive the server
// public for the Profile.
func TestServer_Provision_NoPrivateKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg0.conf")
	if err := os.WriteFile(path, []byte("[Interface]\nAddress = 10.7.0.1/24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := wgadmin.NewServer(wgadmin.Server{ConfPath: path, Interface: "wg0", Endpoint: "is-01:56000"})
	_, err := s.Provision("alice")
	if err == nil || !strings.Contains(err.Error(), "PrivateKey") {
		t.Errorf("err = %v, want PrivateKey-required error", err)
	}
}

// TestServer_Provision_AtomicWrite — interrupting after readState
// but before writeState (simulated by passing a non-existent dir
// for ConfPath) leaves the original file untouched. We achieve this
// indirectly: a Provision against a missing conf must error and
// must NOT call sync.
func TestServer_Provision_AtomicWrite(t *testing.T) {
	rec := &recordingSync{}
	s := wgadmin.NewServer(wgadmin.Server{
		ConfPath:  "/nonexistent/path/wg0.conf",
		Interface: "wg0", Endpoint: "is-01:56000", SyncCommand: rec.Sync,
	})
	_, err := s.Provision("alice")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if len(rec.calls) != 0 {
		t.Errorf("unexpected sync on failed read")
	}
}

// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgadmin"
	"github.com/PavelLizunov/wgturn-core/pkg/wgshare"
)

// runProvisionURL is the `wgturn-cli provision-url <name>...` subcommand:
// generates one or more new client peers on the local wg0.conf, applies
// each via `wg syncconf` (no downtime), and prints the resulting
// wgturn:// URLs to stdout — one per name.
//
// Run on the server itself (where /etc/wireguard/wg0.conf and `wg`
// live); for remote operators the natural shape is:
//
//	ssh root@is-01 wgturn-cli provision-url alice bob carol
//
// Each name produces an idempotent line on stdout the operator can pipe
// into a distribution flow:
//
//	$ sudo wgturn-cli provision-url alice bob > urls.txt
//	$ # share urls.txt[1] with alice, urls.txt[2] with bob, ...
func runProvisionURL(args []string) error {
	fs := flag.NewFlagSet("wgturn-cli provision-url", flag.ContinueOnError)
	var (
		confPath = fs.String("conf", "/etc/wireguard/wg0.conf",
			"path to the server's wg-quick config")
		iface = fs.String("interface", "wg0",
			"WireGuard interface name (passed to `wg syncconf`)")
		subnet = fs.String("subnet", "10.7.0.0/24",
			"client tunnel subnet")
		serverAddr = fs.String("server-addr", "",
			"server's gateway address inside --subnet (default: first host)")
		endpoint = fs.String("endpoint", "",
			"public host:port of the wgturn DTLS listener (required, baked into every URL)")
		allowed = fs.String("allowed", "0.0.0.0/0",
			"AllowedIPs for the produced profile (comma-separated CIDRs)")
		dns = fs.String("dns", "",
			"DNS servers for the produced profile (comma-separated IPs)")
		mtu       = fs.Int("mtu", 0, "MTU for the produced profile (0 = unset)")
		keepalive = fs.Duration("keepalive", 0, "PersistentKeepalive (0 = unset)")
		printConf = fs.Bool("print-conf", false,
			"also emit a wg-quick-style .conf for legacy clients (after the URL)")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(),
			"Usage: wgturn-cli provision-url [flags] <name>...\n\n"+
				"Generate one or more client peers on the local wg0.conf and emit a\n"+
				"wgturn:// share URL per name. Updates the live interface via\n"+
				"`wg syncconf` so existing sessions don't drop.\n\n"+
				"Defaults match the homelab handoff convention; override --subnet /\n"+
				"--endpoint / --conf for non-default deployments.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	names := fs.Args()
	if len(names) == 0 {
		fs.Usage()
		return errors.New("provision-url: at least one name is required")
	}
	if *endpoint == "" {
		return errors.New("provision-url: --endpoint <host:port> is required")
	}

	subnetPrefix, err := netip.ParsePrefix(*subnet)
	if err != nil {
		return fmt.Errorf("provision-url: --subnet %q: %w", *subnet, err)
	}
	allowedPrefixes, err := parseCIDRList(*allowed)
	if err != nil {
		return fmt.Errorf("provision-url: --allowed: %w", err)
	}
	dnsAddrs, err := parseIPList(*dns)
	if err != nil {
		return fmt.Errorf("provision-url: --dns: %w", err)
	}

	var srvAddr netip.Addr
	if *serverAddr != "" {
		srvAddr, err = netip.ParseAddr(*serverAddr)
		if err != nil {
			return fmt.Errorf("provision-url: --server-addr %q: %w", *serverAddr, err)
		}
	}

	srv := wgadmin.NewServer(wgadmin.Server{
		ConfPath:            *confPath,
		Interface:           *iface,
		Subnet:              subnetPrefix,
		ServerAddress:       srvAddr,
		Endpoint:            *endpoint,
		AllowedIPs:          allowedPrefixes,
		DNS:                 dnsAddrs,
		MTU:                 *mtu,
		PersistentKeepalive: *keepalive,
	})

	for _, name := range names {
		profile, err := srv.Provision(name)
		if err != nil {
			return fmt.Errorf("provision-url: %s: %w", name, err)
		}
		url, err := profile.Encode()
		if err != nil {
			return fmt.Errorf("provision-url: %s encode: %w", name, err)
		}
		fmt.Println(url)
		if *printConf {
			fmt.Print(formatWGQuickConf(profile))
		}
	}
	return nil
}

// formatWGQuickConf renders a Profile as a standard wg-quick .conf
// (no #@wgt: metadata, no wgturn-specific Endpoint magic — the
// resulting file works directly with `wg-quick up` against a
// hypothetical exposed WG endpoint, useful for users who want to
// keep both legacy and wgturn paths). The Endpoint baked in is the
// wgturn DTLS listener; clients still need to layer wgturn-cli on
// top to actually use the tunnel in white-list mode.
func formatWGQuickConf(p wgshare.Profile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# wgturn-name = %s\n", p.Label)
	fmt.Fprintln(&b, "[Interface]")
	fmt.Fprintf(&b, "PrivateKey = %s\n", p.ClientPrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", p.Address)
	if len(p.DNS) > 0 {
		parts := make([]string, len(p.DNS))
		for i, d := range p.DNS {
			parts[i] = d.String()
		}
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(parts, ", "))
	}
	if p.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", p.MTU)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Peer]")
	fmt.Fprintf(&b, "PublicKey = %s\n", p.ServerPublicKey)
	if p.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", p.PresharedKey)
	}
	fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
	if len(p.AllowedIPs) > 0 {
		parts := make([]string, len(p.AllowedIPs))
		for i, a := range p.AllowedIPs {
			parts[i] = a.String()
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(parts, ", "))
	}
	if p.PersistentKeepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", int(p.PersistentKeepalive/time.Second))
	}
	fmt.Fprintln(&b)
	return b.String()
}

// parseCIDRList splits a comma-separated CIDR list. Empty input
// returns nil (no AllowedIPs) — the Profile-level default kicks in.
func parseCIDRList(s string) ([]netip.Prefix, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	tokens := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	out := make([]netip.Prefix, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		pp, err := netip.ParsePrefix(t)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", t, err)
		}
		out = append(out, pp)
	}
	return out, nil
}

// parseIPList splits a comma-separated IP-address list.
func parseIPList(s string) ([]netip.Addr, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	tokens := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	out := make([]netip.Addr, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		addr, err := netip.ParseAddr(t)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q: %w", t, err)
		}
		out = append(out, addr)
	}
	return out, nil
}

// runRevokeURL is the symmetric `wgturn-cli revoke-url <name>...` —
// removes one or more peers from wg0.conf and re-syncs the
// interface. Returns ErrPeerNotFound (per name) if any are missing
// but continues with the rest, so a partial input doesn't abort.
func runRevokeURL(args []string) error {
	fs := flag.NewFlagSet("wgturn-cli revoke-url", flag.ContinueOnError)
	var (
		confPath = fs.String("conf", "/etc/wireguard/wg0.conf", "path to wg0.conf")
		iface    = fs.String("interface", "wg0", "WireGuard interface name")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(),
			"Usage: wgturn-cli revoke-url [flags] <name>...\n\n"+
				"Remove one or more provisioned peers from wg0.conf and re-sync the\n"+
				"running interface.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	names := fs.Args()
	if len(names) == 0 {
		fs.Usage()
		return errors.New("revoke-url: at least one name is required")
	}
	srv := wgadmin.NewServer(wgadmin.Server{ConfPath: *confPath, Interface: *iface})
	var firstErr error
	for _, name := range names {
		if err := srv.Revoke(name); err != nil {
			fmt.Fprintf(os.Stderr, "revoke-url: %s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fmt.Fprintf(os.Stderr, "revoke-url: %s: revoked\n", name)
	}
	return firstErr
}

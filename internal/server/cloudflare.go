package server

import (
	"net/netip"
	"strings"
)

// cloudflareRanges are the addresses Cloudflare's proxy connects from, as
// published at https://www.cloudflare.com/ips (checked 2026-09-25). They change
// rarely, and a range missing here only means a visitor behind it is seen as
// Cloudflare rather than as themselves — the state of things without this list.
var cloudflareRanges = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

func isCloudflare(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range cloudflareRanges {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// behindCloudflare returns the visitor's own address from CF-Connecting-IP,
// when the connection really came from Cloudflare's proxy.
//
// Only then: the header is a claim anybody can send, and trusting it from
// anywhere would let an attacker pick the address their failed sign-ins are
// counted against. Cloudflare sets it itself, over whatever the visitor sent,
// so from one of its addresses it is the truth.
func behindCloudflare(peer, header string) (string, bool) {
	if header == "" || !isCloudflare(peer) {
		return "", false
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(header))
	if err != nil {
		return "", false
	}
	return addr.Unmap().String(), true
}

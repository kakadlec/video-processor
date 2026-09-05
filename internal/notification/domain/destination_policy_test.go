package domain_test

import (
	"errors"
	"net/netip"
	"testing"

	"video-processor/internal/notification/domain"
)

func mustDestination(t *testing.T, raw string) domain.Destination {
	t.Helper()
	destination, err := domain.NewDestination(raw)
	if err != nil {
		t.Fatalf("NewDestination(%q) error = %v", raw, err)
	}
	return destination
}

// TestDestinationPolicy_RefusesEveryEnumeratedPrefix carries one case per
// prefix the policy enumerates. The table is the assertion: a prefix dropped
// from the policy leaves its case here failing, which a test written as
// "some private address is refused" would not.
func TestDestinationPolicy_RefusesEveryEnumeratedPrefix(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)

	tests := []struct {
		prefix string
		addr   string
	}{
		// IPv4.
		{"0.0.0.0/8", "0.1.2.3"},
		{"10.0.0.0/8", "10.1.2.3"},
		{"100.64.0.0/10", "100.64.1.1"},
		{"127.0.0.0/8", "127.0.0.1"},
		{"169.254.0.0/16", "169.254.169.254"},
		{"172.16.0.0/12", "172.16.0.1"},
		{"192.0.0.0/24", "192.0.0.8"},
		{"192.0.2.0/24", "192.0.2.1"},
		{"192.168.0.0/16", "192.168.1.1"},
		{"198.18.0.0/15", "198.18.0.1"},
		{"198.51.100.0/24", "198.51.100.1"},
		{"203.0.113.0/24", "203.0.113.1"},
		{"224.0.0.0/4", "224.0.0.1"},
		{"240.0.0.0/4", "240.0.0.1"},
		{"240.0.0.0/4 (broadcast)", "255.255.255.255"},

		// IPv6.
		{"::/128", "::"},
		{"::1/128", "::1"},
		{"64:ff9b:1::/48", "64:ff9b:1::808:808"},
		{"100::/64", "100::1"},
		{"100:0:0:1::/64", "100:0:0:1::1"},
		// Unassigned space inside 2001::/23 inherits that row rather than
		// falling through to the default: it is the block's remainder, and
		// the reason the enclosing row is listed at all.
		{"2001::/23 (unassigned remainder)", "2001:5::1"},
		{"2001::/23 (deprecated ORCHID)", "2001:10::1"},
		{"2001::/23 (ORCHIDv2)", "2001:20::1"},
		{"2001::/32 (Teredo)", "2001::1"},
		{"2001:2::/48 (benchmarking)", "2001:2::1"},
		{"2001:db8::/32", "2001:db8::1"},
		{"2002::/16 (6to4)", "2002:808:808::1"},
		{"3fff::/20", "3fff::1"},
		{"5f00::/16", "5f00::1"},
		{"fc00::/7", "fc00::1"},
		{"fc00::/7 (fd00 half)", "fd00::1"},
		{"fe80::/10", "fe80::1"},
		{"ff00::/8", "ff02::1"},
	}

	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)
			if err := policy.CheckAddr(addr); !errors.Is(err, domain.ErrDestinationRefused) {
				t.Fatalf("CheckAddr(%s) error = %v, want %v", tt.addr, err, domain.ErrDestinationRefused)
			}
		})
	}
}

// TestDestinationPolicy_RefusesRewrittenFormsOfARefusedAddress covers the
// forms that carry an IPv4 address inside an IPv6 one. The mapped and
// well-known NAT64 forms are unwrapped and judged as what they embed; 6to4
// and Teredo are refused as prefixes whatever they embed, and the local-use
// translation prefix is refused rather than unwrapped even though it reads
// like the well-known one.
func TestDestinationPolicy_RefusesRewrittenFormsOfARefusedAddress(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)

	tests := []struct {
		name string
		addr string
	}{
		{"IPv4-mapped loopback", "::ffff:127.0.0.1"},
		{"IPv4-mapped RFC 1918", "::ffff:10.0.0.1"},
		{"IPv4-mapped link-local", "::ffff:169.254.169.254"},
		{"NAT64 loopback", "64:ff9b::7f00:1"},
		{"NAT64 RFC 1918", "64:ff9b::a00:1"},
		{"6to4 embedding a public address", "2002:808:808::1"},
		{"Teredo embedding a public address", "2001:0:808:808::1"},
		{"local-use translation embedding a public address", "64:ff9b:1::808:808"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)
			if err := policy.CheckAddr(addr); !errors.Is(err, domain.ErrDestinationRefused) {
				t.Fatalf("CheckAddr(%s) error = %v, want %v", tt.addr, err, domain.ErrDestinationRefused)
			}
		})
	}
}

func TestDestinationPolicy_AcceptsGloballyReachableUnicast(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)

	tests := []struct {
		name string
		addr string
	}{
		{"public IPv4", "8.8.8.8"},
		{"public IPv6", "2606:4700:4700::1111"},
		{"IPv4-mapped public IPv4", "::ffff:8.8.8.8"},
		{"NAT64 wrapping a public IPv4", "64:ff9b::808:808"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)
			if err := policy.CheckAddr(addr); err != nil {
				t.Fatalf("CheckAddr(%s) error = %v, want nil", tt.addr, err)
			}
		})
	}
}

// TestDestinationPolicy_RefusesAZonedAddress pins the guard the prefix table
// depends on: netip.Prefix.Contains reports false for any address carrying a
// zone, so without an explicit refusal a zoned address in a prefix that has
// no standard-library predicate — documentation space, here — would be
// accepted.
func TestDestinationPolicy_RefusesAZonedAddress(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)

	for _, raw := range []string{"fe80::1%eth0", "2001:db8::1%eth0"} {
		t.Run(raw, func(t *testing.T) {
			addr := netip.MustParseAddr(raw)
			if err := policy.CheckAddr(addr); !errors.Is(err, domain.ErrDestinationRefused) {
				t.Fatalf("CheckAddr(%s) error = %v, want %v", raw, err, domain.ErrDestinationRefused)
			}
		})
	}
}

func TestDestinationPolicy_RefusesTheInvalidAddress(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)

	if err := policy.CheckAddr(netip.Addr{}); !errors.Is(err, domain.ErrDestinationRefused) {
		t.Fatalf("CheckAddr(zero) error = %v, want %v", err, domain.ErrDestinationRefused)
	}
}

func TestDestinationPolicy_CheckDestination(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)

	tests := []struct {
		name    string
		raw     string
		refused bool
	}{
		{"https to a public name", "https://hooks.example.com/notify", false},
		{"http is refused", "http://hooks.example.com/notify", true},
		{"localhost is refused", "https://localhost:8443/notify", true},
		{"localhost with a trailing dot is refused", "https://LOCALHOST./notify", true},
		{"a name under .localhost is refused", "https://receiver.localhost/notify", true},
		{"an IPv4 literal in private space is refused", "https://192.168.1.10:8443/notify", true},
		{"an IPv4 literal in loopback space is refused", "https://127.0.0.1/notify", true},
		{"an IPv6 loopback literal is refused", "https://[::1]/notify", true},
		{"an IPv6 documentation literal is refused", "https://[2001:db8::1]/notify", true},
		{"a public IPv4 literal is accepted", "https://8.8.8.8/notify", false},
		{"a public IPv6 literal is accepted", "https://[2606:4700:4700::1111]/notify", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := policy.CheckDestination(mustDestination(t, tt.raw))
			if tt.refused && !errors.Is(err, domain.ErrDestinationRefused) {
				t.Fatalf("CheckDestination(%q) error = %v, want %v", tt.raw, err, domain.ErrDestinationRefused)
			}
			if !tt.refused && err != nil {
				t.Fatalf("CheckDestination(%q) error = %v, want nil", tt.raw, err)
			}
		})
	}
}

func TestDestinationPolicy_RefusesTheZeroDestination(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)

	if err := policy.CheckDestination(domain.Destination{}); !errors.Is(err, domain.ErrDestinationRefused) {
		t.Fatalf("CheckDestination(zero) error = %v, want %v", err, domain.ErrDestinationRefused)
	}
}

// TestDestinationPolicy_CheckDestinationDoesNotResolveNames states the split
// between the two entry points: a name is accepted at registration whatever
// it resolves to, and the dial-time check is what catches it. Registration
// resolving names would answer a different question on every retry and would
// still not survive the answer changing later.
func TestDestinationPolicy_CheckDestinationDoesNotResolveNames(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)

	if err := policy.CheckDestination(mustDestination(t, "https://internal.example.com/notify")); err != nil {
		t.Fatalf("CheckDestination error = %v, want nil", err)
	}
}

// TestDestinationPolicy_RelaxationCoversBothRules pins that the one boolean
// relaxes the scheme rule and the address rule together. A relaxation that
// covered only one of them would leave the compose stack unable to deliver
// while looking configured.
func TestDestinationPolicy_RelaxationCoversBothRules(t *testing.T) {
	policy := domain.NewDestinationPolicy(true)

	if !policy.AllowsInsecure() {
		t.Fatal("AllowsInsecure() = false, want true")
	}
	for _, raw := range []string{
		"http://receiver:9000/notify",
		"http://localhost:9000/notify",
		"http://172.20.0.5:9000/notify",
	} {
		if err := policy.CheckDestination(mustDestination(t, raw)); err != nil {
			t.Fatalf("CheckDestination(%q) error = %v, want nil under the relaxation", raw, err)
		}
	}
	for _, raw := range []string{"127.0.0.1", "172.20.0.5", "fe80::1"} {
		if err := policy.CheckAddr(netip.MustParseAddr(raw)); err != nil {
			t.Fatalf("CheckAddr(%s) error = %v, want nil under the relaxation", raw, err)
		}
	}
}

// TestDestinationPolicy_DefaultsToRestrictive pins that the zero value of the
// policy is the closed one, so a composition root that forgets to configure
// it fails closed rather than open.
func TestDestinationPolicy_DefaultsToRestrictive(t *testing.T) {
	var policy domain.DestinationPolicy

	if policy.AllowsInsecure() {
		t.Fatal("the zero DestinationPolicy allows insecure destinations, want restrictive")
	}
	if err := policy.CheckAddr(netip.MustParseAddr("127.0.0.1")); !errors.Is(err, domain.ErrDestinationRefused) {
		t.Fatalf("CheckAddr(127.0.0.1) error = %v, want %v", err, domain.ErrDestinationRefused)
	}
}

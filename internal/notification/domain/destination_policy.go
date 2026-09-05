package domain

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

// ErrDestinationRefused reports that a destination, or the address it
// resolved to, is outside what the policy permits.
//
// One sentinel rather than one per rule, deliberately. The registration path
// answers 400 and states that the destination was refused without naming
// which rule caught it: the rules enumerate this deployment's internal
// address space, and a caller who can tell "private" from "benchmarking"
// from "documentation" apart by resubmitting has been handed a probe.
var ErrDestinationRefused = errors.New("notification: destination refused by policy")

// DestinationPolicy decides whether a user-supplied destination may be
// stored and whether the address it resolves to may be dialled.
//
// It is consulted at two points and neither is redundant. A write-time check
// alone cannot survive a hostname that resolves somewhere else later, nor a
// policy tightened after the row was stored; a dial-time check alone accepts
// a destination that will never be delivered to, which is indistinguishable
// to its owner from one that works.
//
// The address rule is a permission, not a prohibition: only globally
// reachable unicast may be dialled. Forgetting a range in a deny-list yields
// a reachable internal host, while an over-broad refusal costs a
// re-registration, so the asymmetry decides the shape.
type DestinationPolicy struct {
	allowInsecure bool
}

// NewDestinationPolicy builds the policy. allowInsecure relaxes the scheme
// rule and the address rule together — never one without the other. They are
// wanted in exactly the same situation (a local stack with no TLS and no
// public addressing) and splitting them into two knobs would invite enabling
// half of it where neither belongs.
func NewDestinationPolicy(allowInsecure bool) DestinationPolicy {
	return DestinationPolicy{allowInsecure: allowInsecure}
}

// AllowsInsecure reports whether the relaxation is in effect. It exists for
// the composition roots to log which posture they started under.
func (p DestinationPolicy) AllowsInsecure() bool { return p.allowInsecure }

// CheckDestination is the registration-time entry point: it judges a
// destination by its scheme and its host.
//
// It can only do half the job. A hostname is checked here only when it is
// already a literal address; a name is resolved at dial time and judged
// there, by CheckAddr, which is the half that catches a name resolving to
// 169.254.169.254.
func (p DestinationPolicy) CheckDestination(destination Destination) error {
	if destination.IsZero() {
		return ErrDestinationRefused
	}
	parsed, err := url.Parse(destination.String())
	if err != nil {
		return ErrDestinationRefused
	}

	if !p.allowInsecure && parsed.Scheme != "https" {
		return ErrDestinationRefused
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return ErrDestinationRefused
	}
	if p.allowInsecure {
		return nil
	}

	// RFC 6761 reserves localhost and everything under .localhost for the
	// loopback interface, so the name is refused without resolving it. A
	// trailing dot is the same name written absolutely.
	name := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return ErrDestinationRefused
	}

	if addr, err := netip.ParseAddr(hostname); err == nil {
		return p.CheckAddr(addr)
	}
	return nil
}

// CheckAddr is the dial-time entry point: it judges the address a connection
// is about to be opened to.
//
// It takes a netip.Addr rather than a string so a caller inside
// net.Dialer.Control cannot pass the hostname it was trying to reach instead
// of the address it resolved to — the mistake that would make the whole
// enumeration below decorative.
func (p DestinationPolicy) CheckAddr(addr netip.Addr) error {
	if p.allowInsecure {
		return nil
	}
	if !addr.IsValid() {
		return ErrDestinationRefused
	}
	// A zone is refused before anything else, and not merely out of caution:
	// netip.Prefix.Contains reports false for any address carrying one, so a
	// zoned address would slip past every prefix below. The refusal is also
	// correct on its own terms — a scoped address is not globally reachable
	// by definition.
	if addr.Zone() != "" {
		return ErrDestinationRefused
	}

	addr = unwrapEmbeddedIPv4(addr)

	// The standard-library predicates first, then the explicit table. The
	// table is in addition to them, never instead of them: IsGlobalUnicast
	// answers true for 100.64.0.0/10, 198.18.0.0/15 and 240.0.0.0/4, all of
	// which route inside real networks, so a check built from the predicates
	// alone leaves exactly the gap this policy exists to close.
	if addr.IsUnspecified() ||
		addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsPrivate() ||
		!addr.IsGlobalUnicast() {
		return ErrDestinationRefused
	}

	for _, prefix := range refusedPrefixes {
		if prefix.Contains(addr) {
			return ErrDestinationRefused
		}
	}
	return nil
}

// wellKnownNAT64 is the only NAT64 prefix that is unwrapped. Its local-use
// sibling 64:ff9b:1::/48 differs by one field, reads as the same thing, and
// is refused as a prefix in the table below instead: it translates inside an
// operator's network, so what it embeds is reached through a translator this
// policy does not control.
var wellKnownNAT64 = netip.MustParsePrefix("64:ff9b::/96")

// unwrapEmbeddedIPv4 reduces the two forms that carry an IPv4 address
// end-to-end to that address, so a refused address cannot be reached by
// rewriting it as ::ffff:10.0.0.1 or 64:ff9b::10.0.0.1. It runs before every
// other check for the same reason.
//
// 6to4 and Teredo also embed an IPv4 address and are deliberately NOT
// unwrapped: they reach it through a relay, which is not ours to reason
// about, so the table refuses those prefixes outright.
func unwrapEmbeddedIPv4(addr netip.Addr) netip.Addr {
	if addr.Is4In6() {
		return addr.Unmap()
	}
	if wellKnownNAT64.Contains(addr) {
		octets := addr.As16()
		return netip.AddrFrom4([4]byte{octets[12], octets[13], octets[14], octets[15]})
	}
	return addr
}

// refusedPrefixes is the explicit table. Every entry is listed literally,
// including the ones a standard-library predicate already covers, so that
// the set this policy refuses can be read in one place and tested range by
// range rather than inferred from what a predicate happens to answer.
//
// The IPv6 half is its own list rather than "the IPv4 equivalents". Its
// normative extent is every prefix the IANA IPv6 Special-Purpose Address
// Registry marks as not globally reachable
// (https://www.iana.org/assignments/iana-ipv6-special-registry/), which is a
// maintained source rather than a recollection; the entries below were
// reconciled against it. Writing "and the v6 equivalents" would have omitted
// documentation, discard-only, benchmarking, SRv6 and translation space,
// none of which has an IPv4 counterpart and all of which a general global
// unicast predicate accepts.
var refusedPrefixes = []netip.Prefix{
	// IPv4.
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("10.0.0.0/8"),      // RFC 1918
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space (CGNAT)
	netip.MustParsePrefix("127.0.0.0/8"),     // loopback
	netip.MustParsePrefix("169.254.0.0/16"),  // link-local, and so 169.254.169.254
	netip.MustParsePrefix("172.16.0.0/12"),   // RFC 1918
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation (TEST-NET-1)
	netip.MustParsePrefix("192.168.0.0/16"),  // RFC 1918
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation (TEST-NET-2)
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation (TEST-NET-3)
	netip.MustParsePrefix("224.0.0.0/4"),     // multicast
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, and so 255.255.255.255

	// IPv6.
	netip.MustParsePrefix("::/128"),         // unspecified
	netip.MustParsePrefix("::1/128"),        // loopback
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use IPv4/IPv6 translation
	netip.MustParsePrefix("100::/64"),       // discard-only
	netip.MustParsePrefix("100:0:0:1::/64"), // dummy prefix (RFC 9780)
	// 2001::/23 is the IETF-protocol-assignments row, and the registry
	// marks it not globally reachable: the assigned blocks inside it that
	// are reachable are listed individually, so everything unassigned —
	// the majority of the block — inherits the row. Refusing it is what
	// keeps that remainder out, and it also covers the deprecated ORCHID
	// range, which the registry leaves as N/A. The reachable children are
	// refused with it, deliberately and at no cost: they are anycast
	// (PCP, TURN, DNS-SD, AMT), the AS112-v6 sinkhole, and the ORCHIDv2
	// and DET identifier ranges — none of them an address an HTTP
	// receiver answers on. Globally reachable is a necessary condition
	// for a destination here, not a sufficient one. The two rows below
	// are listed anyway: each is named by the spec in its own right and
	// carries its own test case, so a later narrowing of the block cannot
	// silently take them with it.
	netip.MustParsePrefix("2001::/23"),     // IETF protocol assignments
	netip.MustParsePrefix("2001::/32"),     // Teredo
	netip.MustParsePrefix("2001:2::/48"),   // benchmarking
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("2002::/16"),     // 6to4
	netip.MustParsePrefix("3fff::/20"),     // documentation
	netip.MustParsePrefix("5f00::/16"),     // SRv6 SIDs
	netip.MustParsePrefix("fc00::/7"),      // unique-local
	netip.MustParsePrefix("fe80::/10"),     // link-local
	netip.MustParsePrefix("ff00::/8"),      // multicast
}

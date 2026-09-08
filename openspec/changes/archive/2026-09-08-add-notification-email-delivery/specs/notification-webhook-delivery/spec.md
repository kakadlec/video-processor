## MODIFIED Requirements

### Requirement: A Destination Is Refused Both Where It Is Registered and Where It Is Dialled

The system SHALL apply one destination policy at two points to every destination that is a **connection target** — one the delivery path opens a connection to: when such a preference is written, and again when the connection is opened. A destination that is not a connection target, because its channel connects to infrastructure this deployment configures rather than to the value the user supplied, SHALL NOT be judged by this policy; `notification-email-delivery` states that case and why it is not a gap. The branch between the two SHALL be taken on the closed channel set, so no third path exists that applies neither rule. The policy SHALL require a transport-secure scheme. At dial time it SHALL be evaluated against the **resolved network address**, not against the hostname alone. Redirects SHALL NOT be followed, the response body SHALL be bounded, and every attempt SHALL be bounded in time.

The guarded dial SHALL be the only path to the destination: the delivery transport's proxy SHALL be nil, and a proxy SHALL NOT be taken from the process environment. This is a condition for the address rule meaning anything, not a hardening preference. With a proxy configured, the connection the guarded dial opens is a connection to the *proxy* — whose address is public and duly approved — after which the proxy resolves the user's hostname itself and connects to whatever it resolves to, so the entire enumeration below is bypassed by an environment variable an operator may have set for a reason that has nothing to do with this system.

The address rule SHALL be expressed as a permission rather than a prohibition: **only an address that is globally reachable unicast may be dialled**, and everything else SHALL be refused. A deny-list of the ranges one happens to think of is the wrong shape for this rule, because the failure mode of forgetting a range is a reachable internal host, while the failure mode of an over-broad refusal is a destination a user must re-register.

Refusal SHALL cover, at minimum, in IPv4: loopback, the unspecified address, link-local (which is what covers `169.254.169.254`), multicast, RFC 1918 private space, **shared address space `100.64.0.0/10`**, **benchmarking `198.18.0.0/15`**, IETF protocol assignments `192.0.0.0/24`, the documentation ranges `192.0.2.0/24`, `198.51.100.0/24` and `203.0.113.0/24`, reserved `240.0.0.0/4`, and `0.0.0.0/8`. The three ranges named in bold are called out because a general "is this a global unicast address" predicate answers *yes* for all of them, so a policy built only from such a predicate would leave exactly the gap this requirement exists to close.

The IPv6 enumeration SHALL be its own list rather than "the equivalents", because the same predicate accepts native IPv6 special-use space that has no IPv4 counterpart. Its normative extent SHALL be **every prefix the IANA IPv6 Special-Purpose Address Registry marks as not globally reachable**, so the list is anchored to a maintained source rather than to what an author happened to recall; the enumeration that follows is that set as it stood when this was written, and it SHALL be reconciled against the registry when implemented. It SHALL cover: the unspecified address and loopback, link-local `fe80::/10`, unique-local `fc00::/7`, multicast `ff00::/8`, documentation `2001:db8::/32` and `3fff::/20`, the discard-only prefix `100::/64`, the dummy prefix `100:0:0:1::/64`, the IETF-protocol-assignments block **`2001::/23`**, **benchmarking `2001:2::/48`**, **local-use IPv4/IPv6 translation `64:ff9b:1::/48`**, and **SRv6 SIDs `5f00::/16`**. `2001::/23` SHALL be refused **as a block**: the registry marks that row not globally reachable and lists the reachable assignments inside it individually, so the unassigned remainder — the majority of the block — inherits the row, and refusing the whole is what keeps that remainder out. Its reachable children are refused with it deliberately: they are anycast service addresses, the AS112-v6 sinkhole, and identifier ranges, none of them an address an HTTP receiver answers on. Globally reachable is a necessary condition for a destination here, not a sufficient one. The prefixes named separately inside that block SHALL remain listed in their own right, so a later narrowing of it cannot silently take them with it. Benchmarking is called out because it is the exact IPv6 counterpart of the `198.18.0.0/15` refused above, and refusing one twin while admitting the other is the asymmetry a separate list exists to prevent. It SHALL also cover the two prefixes that embed an IPv4 address in an IPv6 one — **6to4 `2002::/16`** and **Teredo `2001::/32`** — which SHALL be refused outright rather than merely unwrapped, since an address in either reaches its embedded IPv4 destination through a relay this policy does not control.

An address in an IPv4-mapped (`::ffff:0:0/96`) or NAT64 (`64:ff9b::/96`) form SHALL be unwrapped to the IPv4 address it embeds and evaluated as that address, so a refused address cannot be reached by rewriting it. **Only the well-known NAT64 prefix `64:ff9b::/96` SHALL be unwrapped; the local-use translation prefix `64:ff9b:1::/48` SHALL be refused outright.** The two differ by one field and read as the same thing, but the local-use prefix exists to translate *inside* an operator's network, so unwrapping it would evaluate an embedded address that is reached through a translator this policy does not control — the same reason 6to4 and Teredo are refused by prefix. The whole enumeration, IPv4 and IPv6 alike, SHALL be explicit and SHALL be tested range by range.

Two evaluations are required rather than one, and neither is redundant. A write-time check alone cannot survive a hostname that resolves differently later, nor a policy tightened after the row was stored. A dial-time check alone silently accepts a destination that will never be delivered to, which is the outcome the closed `Channel` set exists to prevent: a preference the system stores and never acts on is indistinguishable, to its owner, from one that works. That is the argument for judging a connection target where it is registered; it is not an argument for judging a destination that is never dialled, which is why the scope above is stated over connection targets rather than over destinations generally.

A single configuration switch MAY relax the policy for environments that have no TLS and no public addressing — local development and the compose stack. It SHALL default to the restrictive behaviour, and it SHALL relax both the scheme rule and the address rule together, because they are wanted in exactly the same situation and separating them invites enabling half of it where neither belongs.

Preferences stored before this policy took effect SHALL NOT be migrated or deleted. One that the policy now refuses SHALL simply not deliver, and the recorded reason SHALL say so.

#### Scenario: A proxy in the environment does not bypass the address rule

- **GIVEN** a proxy is configured in the process environment
- **WHEN** a delivery is attempted
- **THEN** the connection is opened directly to the resolved destination address, the proxy is not used, and the address rule is evaluated against the destination rather than against the proxy

#### Scenario: A native IPv6 benchmarking or local-translation address is refused

- **GIVEN** a destination resolving to an address in `2001:2::/48` or `64:ff9b:1::/48`
- **WHEN** the connection is opened
- **THEN** it is refused before any packet is sent, and the local-translation address is refused as a prefix rather than unwrapped to what it embeds

#### Scenario: A plaintext destination is refused at registration

- **GIVEN** the policy in its default, restrictive configuration
- **WHEN** a user submits a preference whose destination uses `http`
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: An internal address is refused at registration

- **WHEN** a user submits a destination naming a loopback, private, or link-local address, or one in shared, benchmarking, documentation, or reserved space
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A hostname resolving to an internal address is refused at dial

- **GIVEN** a stored destination whose hostname resolves to a private or link-local address
- **WHEN** a delivery to it is attempted
- **THEN** no connection is made to that address, and the delivery is recorded as refused by policy

#### Scenario: A globally-unicast but non-public address is refused

- **GIVEN** a destination whose address is in shared address space, benchmarking space, or reserved space — each of which a general global-unicast predicate accepts
- **WHEN** it is registered, and when a delivery to it is attempted
- **THEN** it is refused at both points

#### Scenario: A native IPv6 special-use address is refused

- **GIVEN** a destination whose address is in IPv6 documentation, discard-only, 6to4, or Teredo space — none of which has an IPv4 counterpart and each of which a general global-unicast predicate accepts
- **WHEN** it is registered, and when a delivery to it is attempted
- **THEN** it is refused at both points, and a 6to4 or Teredo address is refused whatever IPv4 address it embeds

#### Scenario: A mapped form of a refused address is still refused

- **GIVEN** a destination resolving to an IPv4-mapped or NAT64 form of an address the policy refuses
- **WHEN** the dial-time check runs
- **THEN** the embedded address is evaluated and the connection is refused

#### Scenario: A redirect is not followed

- **GIVEN** a destination that answers with a redirect to another address
- **WHEN** a delivery is attempted
- **THEN** the redirect is not followed and the attempt does not succeed

#### Scenario: A previously stored destination that the policy now refuses does not deliver

- **GIVEN** a preference stored before the policy took effect whose destination the policy now refuses
- **WHEN** a matching event is handled
- **THEN** nothing is delivered, the preference row is left untouched, and the recorded reason names the policy

#### Scenario: The relaxation is opt-in and covers both rules

- **GIVEN** the relaxation switch is enabled
- **WHEN** a destination using `http` and naming a private address is registered and delivered to
- **THEN** both are accepted, and with the switch absent or disabled both are refused

#### Scenario: A destination that is not a connection target is not judged by this policy

- **GIVEN** a preference on a channel whose delivery connects to infrastructure this deployment configures rather than to the stored destination
- **WHEN** the preference is written and later delivered to
- **THEN** the destination policy is not applied to it at either point, and the decision is taken on the channel set rather than by omitting a check

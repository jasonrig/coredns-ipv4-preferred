# Implementation notes

## Why a custom plugin

The maintained CoreDNS template plugin can synthesize an empty AAAA answer, but
does not perform a conditional companion A lookup. Rewriting AAAA queries to A
does not meet the required DNS response semantics. DNS64 synthesizes IPv6 rather
than preferring IPv4. No suitable maintained conditional plugin was found in the
official plugin catalogs. The custom middleware is deliberately small and uses
CoreDNS for listening, forwarding, connection reuse, raw caching, health, reload,
errors and metrics. It is not another embedded DNS server.

## Request flow

```text
A/other: client -> cache -> forward -> unchanged answer
AAAA:   client -> capture raw cache/forward response
                  |
                  +-- no positive usable AAAA chain / failure -> unchanged
                  +-- DO/CD/unsupported semantics -> bypass before capture
                  +-- positive AAAA -> bounded A proof through cache/forward
                                        |
                                        +-- usable A, same terminal -> filter
                                        +-- anything else -> original AAAA
```

Only linked answer-section records are evidence. Unrelated A glue cannot cause
filtering. CNAME owners cannot simultaneously contain address records, conflicting
aliases are rejected, and names are compared case-insensitively. Partial CNAME
chains cause subsequent A lookups at the terminal name, within one total timeout
and chain budget. Cross-message loops and different canonical destinations fail
open. No special assumptions are made about one-address or round-robin RRsets.

The detached response writer never writes to the client. Companion lookups that
outlast their caller can safely complete through CoreDNS, retaining a concurrency
slot until they finish. Each caller waits at most its probe budget. Upstream reads
also have CoreDNS's configured timeout. `singleflight` keys include query flags,
EDNS, canonical destination, transport and client IP; it does not introduce a
separate long-lived proof cache or share mutable DNS messages between clients.

## Cache correctness

Policy middleware wraps the raw CoreDNS cache. Both the AAAA result and A evidence
therefore keep their own TTLs. Every eligible AAAA response is reevaluated against
unexpired A evidence; a filtered result is never stored in the inner cache.
MINTTL=0 prevents artificially extending short-lived A evidence. Stale serving and
SERVFAIL caching are off. Upstream genuine negatives retain their SOA/RCODE and
are cached normally. Filtered policy negatives deliberately omit an SOA; per
RFC 2308 section 5, negative replies without SOA should not be cached. Forcing a
negative TTL downstream can weaken this property and is not recommended.

## DNSSEC

Cloudflare validates upstream DNSSEC by default when CD is clear. This plugin is
not an independent validating resolver. TLS authenticates the upstream transport;
it does not provide authoritative DNSSEC proofs for locally modified data.

A real signed AAAA RRset cannot be turned into authenticated NODATA without the
zone's signing keys. Removing the RRSIG or clearing AD does not fix that for an
independent validator. Thus the default `dnssec bypass` returns unmodified answers
for DO or CD queries. This preserves the validation path, at the cost of no IPv4
preference for those queries. Ordinary unsigned/stub lookups still get the policy,
even if the validating upstream had set AD on the original positive response.

`dnssec filter` is an explicit operator opt-in for deployments that prioritize
the policy and do not have validating downstream clients. It clears AD/AA and
omits all signatures and authoritative denial data from synthetic replies. It
does not make those replies validate. A self-validating client that omits DO may
still reject a modified answer; do not promise universal DNSSEC compatibility.

## Query types and availability

HTTPS/SVCB records, including IPv6 hints, are preserved. Their consumers may reach
IPv6 without an AAAA lookup. ANY, MX, TXT, SRV, PTR and address glue in unrelated
responses are not policy-filtered. DNAME and ECS are deliberately outside scope.
COOKIE, padding, NSID and TCP keepalive options are allowed; unknown EDNS options
bypass policy. Special semantics must not become accidental proof of A absence.

No A proof is inferred from timeout, NXDOMAIN conflicting with positive AAAA,
SERVFAIL, truncation, referral or malformed responses. A valid NOERROR NODATA
response with an enclosing SOA is classified separately as `no_a`. Both absence
and inconclusive outcomes preserve the original AAAA response. This policy cannot
measure real IPv4 service reachability or guarantee connection-level failover.

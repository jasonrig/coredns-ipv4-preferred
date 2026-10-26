# CoreDNS IPv4-preferred resolver

A small CoreDNS appliance for networks with good native IPv4 and slower or
differently geolocated IPv6 transit. It prefers IPv4 at the DNS layer while
retaining IPv6-only destinations. Pi-hole continues to handle client DNS, local
names and ad blocking; point its upstream at this service when you are ready.

```text
Clients -> Pi-hole -> this CoreDNS image -> Cloudflare DNS over TLS
```

This project uses **CoreDNS 1.15.0**, a small Go middleware plugin, and a static,
non-root OCI image. No Pi-hole patches, shell, init system or package manager are
included. Runtime settings are visible in the Corefile and environment variables.

## Policy

| Upstream result | Ordinary client-visible result |
|---|---|
| A only | Complete A answer; ordinary AAAA NODATA |
| AAAA only | Complete AAAA answer |
| Both | Complete A answer; AAAA addresses suppressed |
| Neither | Original NXDOMAIN/NODATA response and authority data |
| Multiple addresses | Every A record retained; every AAAA retained when IPv6-only |
| CNAME chain | Apply the same decision to its canonical destination; retain aliases |
| Failed/inconclusive A probe | Original AAAA answer, preserving availability |
| Other types | Ordinary forwarding, including HTTPS/SVCB and their hints |

An A response is never substituted into an AAAA answer. Filtering produces
NOERROR, retains the CNAME chain, clears AD/AA, removes address records and
authoritative denial data, and adds EDNS Extended DNS Error 17 (Filtered) when
the client uses EDNS. It invents no SOA, NSEC or negative TTL. Such policy replies
are intentionally not reusable authoritative negative-cache proofs.

The filter checks the normal AAAA response first. A successful answer with IPv6
addresses triggers an A check through the same raw-answer cache and forwarder.
The check has a 500 ms total budget, follows at most 16 CNAME links, coalesces
concurrent identical probes, and limits active probes to 64. Saturation, timeout,
SERVFAIL, malformed/inconsistent chains and ambiguous responses preserve IPv6.
Truncated A probes are retried over TCP. A valid private/loopback IPv4 address is
usable; unspecified, multicast and limited-broadcast addresses are not.

## Build and test

Requirements: Docker with Buildx and a running engine. Go 1.27.3 is used by the
pinned builder; local tests require Go with automatic toolchain download enabled.

```sh
# Local vet, unit tests, real-server integration tests, and race detector:
bash scripts/test.sh

# Build and run the actual image with an isolated mock DNS upstream; needs dig:
bash scripts/smoke-container.sh

# Linux/amd64 OCI archive and SHA-256, suitable for an x86 Proxmox node:
bash build-oci.sh

# A separate ARM64 archive:
PLATFORMS=linux/arm64 bash build-oci.sh

# Or a multi-architecture archive for a registry/container runtime:
PLATFORMS=linux/amd64,linux/arm64 bash build-oci.sh
```

The archive defaults to `artifacts/coredns-ipv4-preferred-1.0.0-linux-amd64.oci.tar`.
Existing archives are never overwritten. Set `IMAGE`, `PLATFORMS`, or
`BUILDX_BUILDER` as needed; the first script argument overrides the output path.
Builds do not modify Proxmox or Pi-hole or push to a registry.

The multi-stage Dockerfile tests on the builder architecture, then cross-compiles
with CGO disabled for the target. Go modules are pinned by `go.mod`/`go.sum`, and
the Go builder is pinned by multi-architecture digest. The runtime is `scratch`
with the binary, Corefile, CA roots and dependency licenses. Compiler/build-tool
metadata can affect archive digests; retain a tested archive and its checksum as
the exact deployment artifact rather than assuming byte-identical rebuilds.

## Run with Docker or Podman

```sh
docker build --target runtime -t home/coredns-ipv4-preferred:1.0.0 .
docker run -d --name coredns-ipv4-preferred --restart unless-stopped \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --memory 128m --cpus 1 --pids-limit 64 \
  -p 127.0.0.1:1053:1053/udp -p 127.0.0.1:1053:1053/tcp \
  home/coredns-ipv4-preferred:1.0.0
docker exec coredns-ipv4-preferred /coredns -healthcheck
```

Replace `docker` with `podman` for equivalent build/run commands. The service
uses UID/GID **65532:65532** and unprivileged port **1053**, supporting both UDP
and TCP. For a Pi-hole on another machine, publish on the resolver host's LAN IP
instead of `127.0.0.1`, or map LAN port 53 to container port 1053. Restrict access
to Pi-hole/trusted hosts with the host or Proxmox firewall; do not publish an open
resolver on the Internet. The supplied `compose.yaml` defaults to loopback too.

Native Proxmox OCI/LXC has its own LAN IP and no Docker-style port mapping:
Pi-hole should use `RESOLVER_IP#1053`. See [Proxmox deployment](docs/proxmox.md).

## Upstream encryption and configuration

Defaults:

| Environment variable | Value |
|---|---|
| `UPSTREAM_1` | `tls://1.1.1.1` |
| `UPSTREAM_2` | `tls://1.0.0.1` |
| `TLS_SERVER_NAME` | `one.one.one.one` |
| `GOMEMLIMIT` | `96MiB` (soft Go heap/runtime target, not a hard RSS cap) |

These use Cloudflare's standard resolver, with IPv4 transport, TCP port 853,
TLS certificate/hostname verification and connection reuse. There is **no
plaintext fallback** and no bootstrap DNS lookup for the upstream IP addresses.
A correct system clock and working CA roots are required. This encrypts the
CoreDNS-to-Cloudflare leg; the trusted LAN leg from Pi-hole uses ordinary DNS.

For DNS over HTTPS on port 443 instead, use the same image and server name:

```sh
docker run --rm -p 127.0.0.1:1053:1053/udp -p 127.0.0.1:1053:1053/tcp \
  -e UPSTREAM_1=https://1.1.1.1 \
  -e UPSTREAM_2=https://1.0.0.1 \
  -e TLS_SERVER_NAME=one.one.one.one \
  home/coredns-ipv4-preferred:1.0.0
```

Other DoT/DoH resolvers can be selected with these variables; all configured
upstreams must support the same TLS server name. Do not use `/etc/resolv.conf` or
Pi-hole as this service's upstream: that would risk a forwarding loop.
For a single upstream, set both upstream variables to the same endpoint. Corefile
environment substitutions are individual tokens, not space-separated lists.

To change plugin limits or bind addresses, supply a custom Corefile at
`/etc/coredns/Corefile`. It must be readable by UID 65532 (for example mode 0444).
The compiled middleware order always places `prefer_ipv4` **before** the cache,
regardless of text order. Cache settings use zero minimum TTL, no stale answers,
no cached SERVFAIL, up to 4096 positive and 1024 genuine negative entries. Upper
cache lifetimes are 300 seconds positive and 60 seconds negative. Do not wrap
the filter in another cache or force a downstream synthetic-negative TTL.

## Connect Pi-hole

1. Keep Pi-hole's local records, ad blocking and client-facing settings as they are.
2. In its upstream DNS settings, add the deployed resolver as `RESOLVER_IP#1053`.
3. Disable other upstream choices if you want consistent preference. For redundancy,
   run a second copy of this resolver with the same policy rather than an ordinary
   unfiltered secondary. Pi-hole may use either configured upstream.
4. Flush existing DNS caches when switching, then run the checks below against both
   CoreDNS and Pi-hole. Keep the former upstream settings for an easy rollback.

**DNSSEC:** By default, queries with DO=1 or CD=1 bypass filtering. Downstream
validation cannot authenticate a fabricated absence of AAAA. Pi-hole's DNSSEC
mode therefore bypasses preference for the queries it validates. For normal
filtered use, leave downstream DNSSEC validation off; Cloudflare still validates
its upstream resolution when checking is not disabled. Advanced users can set
`dnssec filter` in the plugin block, explicitly trading away downstream validation
of modified answers. This never sets AD or creates fake DNSSEC proofs. See
[implementation and DNSSEC details](docs/implementation.md).

## Verify

Use a dual-stack domain of your choice for live tests; authoritative records may
change. A/AAAA tests below target the local Docker example; use the deployed IP
and port 1053 for Proxmox. Ordinary `dig` without `+dnssec` exercises preference.

```sh
dig @127.0.0.1 -p 1053 example.com A +noall +answer
dig @127.0.0.1 -p 1053 example.com AAAA +noall +answer +comments
dig @127.0.0.1 -p 1053 example.com A +tcp
dig @127.0.0.1 -p 1053 example.com HTTPS
dig @127.0.0.1 -p 1053 example.com AAAA +dnssec  # intentional bypass
```

For deterministic, fully reproducible checks, the isolated image smoke test uses
`dual.test`, `v6.test`, `multi.test`, `servfail.test` and `timeout.test` rather than
depending on public DNS. See its `dig` commands in `scripts/smoke-container.sh`.
The Go suite additionally covers cache expiry, DNSSEC, concurrent requests,
round-robin sets, multi-message aliases, NXDOMAIN/NODATA and malformed proof.

## Limitations and troubleshooting

- **DNS preference is not connection failover.** If an A record exists but its
  IPv4 service/path is unreachable, this resolver cannot discover that and will
  still suppress AAAA. Cached addresses or direct IP use bypass this policy.
- HTTPS/SVCB `ipv6hint`, application DoH, hard-coded addresses and DNSSEC bypass
  can still lead to IPv6 connections. The service deliberately preserves other
  record types; it does not promise that every application uses IPv4.
- Only ordinary IN AAAA queries are filtered. DNAME, unknown/view-changing EDNS
  options (including ECS), TSIG and malformed or inconsistent chains pass through.
- `/health` on 8080, `/ready` on 8181 and Prometheus on 9153 are management endpoints.
  Docker does not publish them by default. Restrict them on direct-LAN deployments.
  Readiness checks local service readiness, not the correctness of every upstream
  DNS answer. Restart policies react to exit; an unhealthy Docker status alone
  does not automatically restart a container.
- Check status/health first, then test both UDP and TCP with `dig`. If upstreams
  fail, check outbound TCP 853 (or 443 for DoH), clock, CA roots and TLS server name.
  Do not disable certificate verification as a workaround.
- Add `log` to the Corefile for temporary query diagnostics; it is off by default.
  Errors go to stderr. The `coredns_prefer_ipv4_decisions_total{result=...}` metric
  distinguishes suppression, confirmed absence, inconclusive probes and bypass.
  A high inconclusive count warrants checking timeout/saturation/upstream health.
- There are no container login credentials or SSH server. Administer through the
  image, Corefile, environment and container manager; replace the image to update.

## Updates

Update the CoreDNS version in `go.mod`, choose a supported patched Go toolchain,
update its Dockerfile digest and `go.mod` toolchain, run `go mod tidy`, bump
`VERSION`, and run tests plus both architecture builds. Review the cache, forward
and server APIs when upgrading CoreDNS. Deploy a new CT alongside the old one,
test it, switch the Pi-hole upstream, and retain the old archive until satisfied.
The image does not self-update. [Validation record](docs/validation.md) records
the actual checks performed for this build.

## References

- [CoreDNS release](https://github.com/coredns/coredns/releases/tag/v1.15.0)
- [CoreDNS forwarding](https://coredns.io/plugins/forward/) and [cache](https://coredns.io/plugins/cache/)
- [Cloudflare DNS over TLS](https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-tls/)
- [Reference OCI packaging project](https://github.com/jasonrig/cloudflare-connector)
- [RFC 2308: negative caching](https://www.rfc-editor.org/rfc/rfc2308.html)
- [RFC 8914: extended DNS errors](https://www.rfc-editor.org/rfc/rfc8914.html)

Apache-2.0. Dependency licenses are included in the image under `/licenses`.

# Validation — 27 October 2026 (JST)

- CoreDNS 1.15.0; Go 1.27.3; pinned builder and Go module checksums.
- `scripts/test.sh`: gofmt, go vet, unit tests, real-server integration tests and
  the race detector passed on macOS ARM64.
- Controlled fixtures cover A-only, AAAA-only, multiple A/AAAA, aliases and split
  CNAME chains, NXDOMAIN/NODATA, SERVFAIL, timeout, malformed proof, DNSSEC bypass
  and explicit filtering, EDNS, TCP fallback, raw-cache TTL expiry and concurrency.
- The supplied Corefile was also tested against a controlled TLS upstream. Its
  CA/hostname verification succeeds with the correct name and rejects the wrong
  name; no certificate-validation bypass is used.
- `scripts/smoke-container.sh` passed for native Linux/ARM64 and emulated
  Linux/AMD64: actual non-root/read-only images, health, UDP/TCP and controlled
  conditional policy. The fixture containers/networks were removed afterward.
- Both single-platform OCI archives built successfully. Their checksums and OCI
  platform, UID/GID and application command metadata were verified.
- Live Cloudflare DNS-over-TLS returned both A records for example.com and
  NOERROR/empty AAAA with EDE Filtered. IPv6-only ipv6.test-ipv6.com retained its
  AAAA record. TCP client DNS also worked.
- Optional Cloudflare DNS-over-HTTPS worked using `https://1.1.1.1` and
  `https://1.0.0.1`; CoreDNS supplies `/dns-query` itself, so do not put a path in
  those upstream URIs.
- The live ARM64 resolver used about 14.5 MiB after a small set of queries, under
  a 128 MiB container limit. This is a pilot observation, not a load benchmark.
- ShellCheck, shell syntax and Docker Compose configuration checks passed.
- Deployed the AMD64 OCI archive as an unprivileged Proxmox application CT with
  128 MiB RAM, one CPU, DHCP, and automatic startup enabled. Its imported command
  and Cloudflare TLS environment were verified through the GUI. Live UDP/TCP
  queries and the readiness endpoint passed; both IPv4 and IPv6 client transport
  worked. Dual-stack AAAA suppression, complete A answers, IPv6-only answers and
  the intentional DNSSEC bypass were confirmed on the deployed service.

Site-specific Proxmox addressing is kept outside this repository. Production
readiness still includes observing actual DNS volume, restart/boot behavior and
resource use on the deployment host. Default DNSSEC and HTTPS/SVCB limitations
are documented in the README and implementation notes.

## OCI archive checksums

```text
e25901ade9b1e41e3bb285c3726ba93039388ac64760c77ee85f5ac5cfd66c6a  coredns-ipv4-preferred-1.0.0-linux-amd64.oci.tar
9eadfd022e27a759bc26ec0a50f779c63d4f79020a7313371705f482c7d3639e  coredns-ipv4-preferred-1.0.0-linux-arm64.oci.tar
```

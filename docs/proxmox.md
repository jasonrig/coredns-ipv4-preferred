# Proxmox OCI application container

This follows the local OCI archive pattern used by
[cloudflare-connector](https://github.com/jasonrig/cloudflare-connector). The
Proxmox host does not need Docker. Native OCI application support depends on the
installed Proxmox release; verify the imported command, user and environment in
the GUI. [Official container documentation](https://github.com/proxmox/pve-docs/blob/master/pct.adoc)
describes uploading OCI images in a storage's container-template view.

## Create through the GUI

1. Run `bash build-oci.sh` on a workstation. For an x86 node use the single-platform
   `linux/amd64` archive, and verify its `.sha256` file.
2. In Proxmox, choose a storage supporting container templates and open **CT
   Templates**. Upload the OCI archive if your version accepts raw `.tar` files.
   Some versions' Upload form incorrectly permits only compressed templates;
   this was [reported on the developer list](https://lore.proxmox.com/pve-devel/882dcd98-9fb3-4715-8caf-b79041e2efab%40proxmox.com/).
   On affected versions use **Download from URL**, which accepts the raw OCI
   archive, and select SHA-256 with the value from its checksum file. Published
   archives are available on the project's [releases page](https://github.com/jasonrig/coredns-ipv4-preferred/releases).
   A temporary
   workstation HTTP server serving only `artifacts/`, bound to its trusted LAN IP,
   can provide that URL; stop it after the verified download. For example:
   `python3 -m http.server 18080 --bind WORKSTATION_LAN_IP --directory artifacts`.
   Do not gzip the outer archive just to satisfy the Upload form: the OCI detector
   checks the `.tar` suffix. This is an OCI application image, not a complete
   Debian/systemd root filesystem.
3. Create an **unprivileged** CT from that image. Choose an unused CT ID, a clear
   hostname such as `coredns-ipv4-preferred`, one CPU, 128 MiB RAM, optional 64 MiB
   swap, and a small root disk. A 4 GiB thin-provisioned disk is ample. These are
   initial home-lab limits; monitor workload and memory before tightening them.
4. Set the intended bridge and VLAN tag in the CT's network settings. IPv4 DHCP is
   fine for initial validation; later reserve the lease before making critical DNS
   depend on it. Enable **Host-Managed** networking because this image has no
   guest DHCP client. On the validated Proxmox 9.2.21 installation, selecting
   **SLAAC** caused startup to fail with "SLAAC is not supported for host-managed
   network configuration". Matching the existing working OCI container instead
   used **IPv6: Static**, with its address and gateway left blank. The container
   then acquired an IPv6 address from router advertisements as well as its DHCP
   IPv4 lease. Verify the actual assigned addresses after startup rather than
   assuming the UI mode guarantees IPv6. Upstream transport uses IPv4; AAAA
   answers work independently of whether the container has an IPv6 address.
5. Keep the imported OCI startup command `/coredns -conf /etc/coredns/Corefile`,
   working directory `/`, and process user/group `65532:65532`. The OCI importer
   maps the image's numeric user/group to LXC's init UID/GID; these may not be
   displayed in the Options table. There are no login
   credentials, shell or SSH daemon. Do not override the application with systemd
   or a getty/console login process.
6. Inspect the visible environment settings. Defaults from the image are:
   `UPSTREAM_1=tls://1.1.1.1`, `UPSTREAM_2=tls://1.0.0.1`,
   `TLS_SERVER_NAME=one.one.one.one`, `GOMEMLIMIT=96MiB`.
   No token, password, secret mount or host-side configuration directory is needed.
7. Enable **Start at boot** and set startup order after networking and before
   Pi-hole where practical. Configure the same restart-on-exit policy supported
   by your existing OCI application containers. Verify actual process-exit and
   node-boot behavior on the installed release; Docker HEALTHCHECK and Docker's
   `restart` setting do not automatically transfer into LXC lifecycle semantics.
8. Start the CT. Read its assigned address from Network or Summary. Permit trusted Pi-hole
   hosts to reach UDP/TCP 1053 and allow outbound TCP 853 to both Cloudflare IPs.
   Keep management ports 8080/8181/9153 limited to monitoring/admin sources.

## Validate before changing Pi-hole

From a trusted workstation, substitute the assigned address:

```sh
dig @RESOLVER_IP -p 1053 example.com A
dig @RESOLVER_IP -p 1053 example.com AAAA
dig @RESOLVER_IP -p 1053 example.com A +tcp
curl --fail http://RESOLVER_IP:8181/ready
```

Use a known IPv6-only destination as well, or run the project's deterministic
fixture tests before deployment. Once satisfied, configure Pi-hole's upstream as
`RESOLVER_IP#1053` yourself. Keep Pi-hole's old upstream values for rollback.
Ensure Pi-hole is not forwarding DNSSEC-aware queries if you expect default
filtering; see the README's DNSSEC section.

## Updates and rollback

Build a new versioned archive, import it, and create a replacement CT alongside
the running service. Copy only the visible resolver settings and network policy,
validate it, then switch Pi-hole's upstream. Do not patch files in the Proxmox host
root or mutate the image's executable in place. A prior archive/CT is the rollback
unit. Reverting Pi-hole's upstream restores its former DNS path immediately,
subject to existing client/Pi-hole cache lifetimes.

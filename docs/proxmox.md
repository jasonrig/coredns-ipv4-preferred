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
2. In Proxmox, choose a storage supporting container templates, open **CT
   Templates**, and upload the OCI archive. This is an application image, not a
   complete Debian/systemd root filesystem.
3. Create an **unprivileged** CT from that image. Choose an unused CT ID, a clear
   hostname such as `coredns-ipv4-preferred`, one CPU, 128 MiB RAM, optional 64 MiB
   swap, and a small root disk. A 4 GiB thin-provisioned disk is ample. These are
   initial home-lab limits; monitor workload and memory before tightening them.
4. Set the intended bridge and VLAN tag in the CT's network settings. IPv4 DHCP is
   fine for initial validation; later reserve the lease before making critical DNS
   depend on it. IPv6 SLAAC is optional: upstream transport is IPv4 and the DNS
   listener/answers support both address families independently of transport.
5. Keep the imported OCI startup command `/coredns -conf /etc/coredns/Corefile`,
   working directory `/`, and process user/group `65532:65532`. There are no login
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
8. Start the CT. Read its assigned address from Summary. Permit trusted Pi-hole
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

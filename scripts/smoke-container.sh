#!/usr/bin/env bash
# Actual image test, using an isolated Docker network and a controlled upstream.
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
command -v dig >/dev/null || { echo 'Install dig (dnsutils/bind-utils).' >&2; exit 1; }
image=${IMAGE:-home/coredns-ipv4-preferred:test}
fixture_image=home/coredns-ipv4-preferred:fixture
port=${DNS_TEST_PORT:-15353}
name="coredns-test-$$"
scratch=$(mktemp -d "$PWD/.smoke.XXXXXX")
chmod 0755 "$scratch"
cleanup() {
    docker rm -f "$name" "$name-upstream" >/dev/null 2>&1 || true
    docker network rm "$name" >/dev/null 2>&1 || true
    rm -rf -- "$scratch"
}
trap cleanup EXIT
trap 'echo "Smoke test failed at line $LINENO: $BASH_COMMAND" >&2; docker logs "$name" 2>/dev/null || true' ERR
platform=${PLATFORM:-$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')}
docker build --platform "$platform" --target fixture -t "$fixture_image" .
docker build --platform "$platform" --target runtime -t "$image" .
docker network create "$name" >/dev/null
docker run -d --platform "$platform" --name "$name-upstream" --network "$name" --network-alias fixture \
    --read-only --cap-drop ALL --security-opt no-new-privileges "$fixture_image" >/dev/null
# Use the supplied Corefile, replacing only encrypted external forwarding with
# the isolated fixture. All policy/cache/health directives remain unchanged.
sed '/tls_servername/d' Corefile > "$scratch/Corefile"
chmod 0444 "$scratch/Corefile"
docker run -d --platform "$platform" --name "$name" --network "$name" \
    --read-only --cap-drop ALL --security-opt no-new-privileges --memory 128m \
    -e UPSTREAM_1=fixture:5353 -e UPSTREAM_2=fixture:5353 -p "127.0.0.1:$port:1053/udp" -p "127.0.0.1:$port:1053/tcp" \
    -v "$scratch/Corefile:/etc/coredns/Corefile:ro" "$image" >/dev/null
ready=false
for ((attempt=0; attempt<30; attempt++)); do
    if docker exec "$name" /coredns -healthcheck >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
done
[[ "$ready" == true ]] || { docker logs "$name"; exit 1; }
[[ $(docker inspect --format '{{.Config.User}}' "$name") == 65532:65532 ]]
for transport in +notcp +tcp; do
    for record_type in A AAAA; do
        output=$(dig @127.0.0.1 -p "$port" dual.test "$record_type" "$transport" +short +time=2 +tries=1)
        if [[ "$record_type" == A ]]; then [[ $(printf '%s\n' "$output" | awk 'NF {n++} END {print n+0}') == 3 ]]
        else [[ -z "$output" ]]; fi
    done
    output=$(dig @127.0.0.1 -p "$port" v6.test AAAA "$transport" +short +time=2 +tries=1)
    [[ $(printf '%s\n' "$output" | awk '/^2001:db8:/ {n++} END {print n+0}') == 2 ]]
    output=$(dig @127.0.0.1 -p "$port" multi.test AAAA "$transport" +short +time=2 +tries=1)
    [[ "$output" == $'middle.test.\ndual.test.' ]]
    output=$(dig @127.0.0.1 -p "$port" servfail.test AAAA "$transport" +short +time=2 +tries=1)
    [[ $(printf '%s\n' "$output" | awk '/^2001:db8:/ {n++} END {print n+0}') == 2 ]]
    output=$(dig @127.0.0.1 -p "$port" timeout.test AAAA "$transport" +short +time=2 +tries=1)
    [[ $(printf '%s\n' "$output" | awk '/^2001:db8:/ {n++} END {print n+0}') == 2 ]]
done
echo 'Container smoke tests passed: non-root, health, UDP/TCP, full A sets, IPv6-only, CNAME, transient failures.'

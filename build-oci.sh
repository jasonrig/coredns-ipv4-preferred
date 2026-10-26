#!/usr/bin/env bash
# Export a local OCI archive. Does not contact or modify Proxmox or Pi-hole.
set -euo pipefail
recipe_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
version=$(cat "$recipe_dir/VERSION")
platforms=${PLATFORMS:-linux/amd64}
builder_name=${BUILDX_BUILDER:-coredns-ipv4-preferred-oci}
image=${IMAGE:-home/coredns-ipv4-preferred:$version}
archive_path=${1:-"$recipe_dir/artifacts/coredns-ipv4-preferred-$version-${platforms//[\/,]/-}.oci.tar"}
command -v docker >/dev/null || { echo 'Docker with Buildx is required.' >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo 'Start the Docker engine first.' >&2; exit 1; }
docker buildx version >/dev/null
[[ ! -e "$archive_path" ]] || { echo "Archive already exists: $archive_path" >&2; exit 1; }
mkdir -p -- "$(dirname -- "$archive_path")"
if ! docker buildx inspect "$builder_name" >/dev/null 2>&1; then
    docker buildx create --name "$builder_name" --driver docker-container >/dev/null
fi
docker buildx build --builder "$builder_name" --platform "$platforms" \
    --target runtime --build-arg "VERSION=$version" --provenance=false --sbom=false \
    --tag "$image" --output "type=oci,dest=$archive_path" "$recipe_dir"
(
    cd -- "$(dirname -- "$archive_path")"
    if command -v sha256sum >/dev/null; then sha256sum "$(basename -- "$archive_path")"
    else shasum -a 256 "$(basename -- "$archive_path")"; fi
) > "$archive_path.sha256"
echo "Built OCI archive: $archive_path"
echo "Checksum: $archive_path.sha256"

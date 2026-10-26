#!/usr/bin/env bash
# Collect the licenses of modules actually imported by the production binary.
set -euo pipefail
destination=${1:?output directory required}
mkdir -p "$destination"
cp LICENSE "$destination/PROJECT-LICENSE"
cp NOTICE "$destination/NOTICE"
cp "$(go env GOROOT)/LICENSE" "$destination/GO-LICENSE"
go list -deps -f '{{if .Module}}{{.Module.Path}}{{"\t"}}{{.Module.Dir}}{{end}}' ./cmd/coredns | sort -u |
while IFS=$'\t' read -r module directory; do
    [[ -n "$module" && -n "$directory" ]] || continue
    module_destination="$destination/$module"
    mkdir -p "$module_destination"
    for license_file in "$directory"/LICENSE* "$directory"/COPYING* "$directory"/NOTICE*; do
        [[ -f "$license_file" ]] || continue
        cp "$license_file" "$module_destination/"
    done
done

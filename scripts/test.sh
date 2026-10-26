#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
unformatted=$(gofmt -l cmd plugin internal integration)
[[ -z "$unformatted" ]] || { echo "$unformatted"; echo 'Run gofmt on these files.' >&2; exit 1; }
go vet ./...
go test -race -count=1 ./...

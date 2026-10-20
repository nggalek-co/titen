#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export GOCACHE="${GOCACHE:-$PWD/.cache/go}"
mkdir -p "$GOCACHE"
go test -race ./...
go vet ./...
go run github.com/egoist/mygo/cmd/mygo vet .

#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
[ "$(uname -s)" = Linux ] || { echo "Linux is required" >&2; exit 1; }
version="${TITEN_VERSION:-dev}"
case "$version" in ""|*[!A-Za-z0-9._+-]*) echo "Invalid version" >&2; exit 1;; esac
arch=$(go env GOARCH)
case "$arch" in amd64|arm64) ;; *) echo "The Linux package targets amd64 and arm64" >&2; exit 1;; esac
export CGO_ENABLED=0
export GOCACHE="${GOCACHE:-$PWD/.cache/go}"
mkdir -p "$GOCACHE" build/linux
production_ldflag=
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*|[0-9]*.[0-9]*.[0-9]*) production_ldflag=' -X github.com/egoist/mygo.production=1' ;;
esac
go build -trimpath -ldflags "-s -w -X main.version=$version$production_ldflag" -o build/linux/titen .
go build -trimpath -ldflags "-s -w" -o build/linux/titen-audio ./native/linux
./build/linux/titen-audio --self-test

package="build/linux-$arch/Titen"
rm -rf "build/linux-$arch"
mkdir -p "$package"
cp build/linux/titen build/linux/titen-audio README.md "$package"/
echo "Built $package"

mkdir -p dist
archive="Titen-${version}-linux-${arch}.tar.gz"
tar -czf "dist/$archive" -C "build/linux-$arch" Titen
(cd dist && sha256sum "$archive" > "$archive.sha256")
echo "Packaged dist/$archive"

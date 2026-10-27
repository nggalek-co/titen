#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
[ "$(uname -s)" = Linux ] || { echo "Linux is required" >&2; exit 1; }
version="${TITEN_VERSION:-dev}"
case "$version" in ""|*[!A-Za-z0-9._+-]*) echo "Invalid version" >&2; exit 1;; esac
arch=$(go env GOARCH)
case "$arch" in amd64|arm64) ;; *) echo "The Linux package targets amd64 and arm64" >&2; exit 1;; esac
command -v dpkg-deb >/dev/null 2>&1 || { echo "dpkg-deb is required to finalize the Debian package" >&2; exit 1; }
mygo_version=$(printf '%s' "$version" | sed -nE 's/^v?([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$/\1/p')
mygo_version="${mygo_version:-0.0.0}"
export CGO_ENABLED=0
export GOCACHE="${GOCACHE:-$PWD/.cache/go}"
mkdir -p "$GOCACHE" build/linux

resource_dir="resources/linux-$arch"
resource_path="$resource_dir/titen-audio"
config_backup=$(mktemp)
package_backup=".package.json.titen-build.$$"
tmp=""
cp mygo.json "$config_backup"
restore() {
  cp "$config_backup" mygo.json
  rm -f "$config_backup" "$resource_path"
  rmdir "$resource_dir" 2>/dev/null || true
  if [ -f "$package_backup" ]; then mv "$package_backup" package.json; fi
  if [ -n "$tmp" ]; then rm -rf "$tmp"; fi
}
trap restore EXIT

go build -trimpath -ldflags "-s -w" -o build/linux/titen-audio ./native/linux
./build/linux/titen-audio --self-test
mkdir -p "$resource_dir"
cp build/linux/titen-audio "$resource_path"
sed -i "s/\"version\": \"[^\"]*\"/\"version\": \"$mygo_version\"/" mygo.json

# MyGo treats the root package.json as a web frontend and runs the app to
# generate bindings, which needs a display. Titen has no web frontend.
mv package.json "$package_backup"
if [ "$mygo_version" = "$version" ] || [ "v$mygo_version" = "$version" ]; then
  go run github.com/egoist/mygo/cmd/mygo build -platform "linux/$arch" -o dist
else
  go run github.com/egoist/mygo/cmd/mygo build -debug -platform "linux/$arch" -o dist
fi
mv "$package_backup" package.json

stage="dist/linux-$arch"
set -- "$stage"/titen_*_"$arch".deb
[ "$#" -eq 1 ] && [ -f "$1" ] || { echo "MyGo did not produce one Debian package" >&2; exit 1; }
deb="$1"
tmp=$(mktemp -d)
dpkg-deb -R "$deb" "$tmp"
sed -i -E 's/, ?libwebkit2gtk-4\.1-0//g; s/^Depends: , /Depends: /' "$tmp/DEBIAN/control"
if grep -q 'libwebkit2gtk' "$tmp/DEBIAN/control"; then
  echo "The Debian package still declares WebKitGTK" >&2
  exit 1
fi
if ! grep -q 'pulseaudio-utils' "$tmp/DEBIAN/control" || ! grep -q 'libsecret-1-0' "$tmp/DEBIAN/control"; then
  echo "The Debian package does not declare its audio and credential runtime dependencies" >&2
  exit 1
fi
rm -f "$deb"
dpkg-deb -Zgzip --build --root-owner-group "$tmp" "$deb" >/dev/null
rm -rf "$tmp"
tmp=""
(cd "$stage" && sha256sum "$(basename "$deb")" > "$(basename "$deb").sha256")
rm -f "$stage"/titen-*-linux-*.tar.gz "$stage"/install.sh

package="build/linux-$arch/Titen"
rm -rf "build/linux-$arch"
mkdir -p "$package"
cp "$stage/titen" "$stage/titen-audio" README.md "$package"/
echo "Built $package"

mkdir -p dist
rm -f "dist"/Titen-*-linux-"$arch".tar.gz "dist"/Titen-*-linux-"$arch".tar.gz.sha256
archive="Titen-${version}-linux-${arch}.tar.gz"
tar -czf "dist/$archive" -C "build/linux-$arch" Titen
(cd dist && sha256sum "$archive" > "$archive.sha256")
echo "Packaged dist/$archive"
echo "Packaged $deb"

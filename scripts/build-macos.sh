#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
[ "$(uname -s)" = Darwin ] || { echo "macOS is required" >&2; exit 1; }
. scripts/signing-config.sh
version="${TITEN_VERSION:-dev}"
case "$version" in ""|*[!A-Za-z0-9._+-]*) echo "Invalid version" >&2; exit 1;; esac
sh scripts/sign-cert.sh
signing_keychain="${TITEN_CODESIGN_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"
signing_fingerprint=$(security find-identity -v -p codesigning "$signing_keychain" 2>/dev/null \
	| awk -v identity="\"$TITEN_CODESIGN_IDENTITY\"" 'index($0, identity) { print $2 }')
set -- $signing_fingerprint
[ "$#" -eq 1 ] || { echo "Expected one valid $TITEN_CODESIGN_IDENTITY identity in $signing_keychain; found $#" >&2; exit 1; }
signing_fingerprint="$1"
bundle_version=$(printf '%s' "$version" | sed -nE 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*$/\1/p')
bundle_version="${bundle_version:-0.0.0}"
export MACOSX_DEPLOYMENT_TARGET=15.0
export GOCACHE="${GOCACHE:-$PWD/.cache/go}"
export CLANG_MODULE_CACHE_PATH="${CLANG_MODULE_CACHE_PATH:-$PWD/.cache/clang}"
export SWIFT_MODULECACHE_PATH="${SWIFT_MODULECACHE_PATH:-$PWD/.cache/swift}"
mkdir -p "$GOCACHE" "$CLANG_MODULE_CACHE_PATH" "$SWIFT_MODULECACHE_PATH"
swift build --disable-sandbox --package-path native/macos -c release
bundle="build/Titen.app"
mkdir -p "$bundle/Contents/MacOS" "$bundle/Contents/Resources"
production_ldflag=
case "$version" in
	v[0-9]*.[0-9]*.[0-9]*|[0-9]*.[0-9]*.[0-9]*) production_ldflag=' -X github.com/egoist/mygo.production=1' ;;
esac
go build -trimpath -ldflags "-s -w -X main.version=$version$production_ldflag" -o "$bundle/Contents/MacOS/titen" .
cp native/macos/.build/release/titen-audio "$bundle/Contents/MacOS/titen-audio"
cp resources/icon.png "$bundle/Contents/Resources/icon.png"
cat > "$bundle/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>Titen</string>
<key>CFBundleDisplayName</key><string>Titen</string>
<key>CFBundleIdentifier</key><string>$TITEN_APP_BUNDLE_ID</string>
<key>CFBundleExecutable</key><string>titen</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$bundle_version</string>
<key>CFBundleVersion</key><string>1</string>
<key>LSMinimumSystemVersion</key><string>15.0</string>
<key>NSHighResolutionCapable</key><true/>
<key>NSMicrophoneUsageDescription</key><string>Titen records your microphone for visible meeting transcription.</string>
<key>NSScreenCaptureUsageDescription</key><string>Titen records meeting playback for visible transcription, including when you use headphones.</string>
</dict></plist>
PLIST
plutil -lint "$bundle/Contents/Info.plist"
plutil -lint "$TITEN_ENTITLEMENTS"
codesign --force --timestamp=none --sign "$signing_fingerprint" \
  --identifier "$TITEN_AUDIO_BUNDLE_ID" \
  --requirements "=$TITEN_AUDIO_DESIGNATED_REQUIREMENT" \
  --entitlements "$TITEN_ENTITLEMENTS" "$bundle/Contents/MacOS/titen-audio"
codesign --force --timestamp=none --sign "$signing_fingerprint" \
  --identifier "$TITEN_APP_BUNDLE_ID" \
  --requirements "=$TITEN_APP_DESIGNATED_REQUIREMENT" \
  --entitlements "$TITEN_ENTITLEMENTS" "$bundle"
codesign --verify --strict "$bundle/Contents/MacOS/titen-audio"
codesign --verify --deep --strict "$bundle"
verify_signed_target() {
  path="$1"
  expected_id="$2"
  details=$(codesign -d --verbose=4 "$path" 2>&1)
  actual_id=$(printf '%s\n' "$details" | sed -n 's/^Identifier=//p' | head -n 1)
  authority=$(printf '%s\n' "$details" | sed -n 's/^Authority=//p' | head -n 1)
  [ "$actual_id" = "$expected_id" ] || { echo "Unexpected code identifier for $path: $actual_id" >&2; exit 1; }
  [ "$authority" = "$TITEN_CODESIGN_IDENTITY" ] || { echo "Unexpected code signer for $path: $authority" >&2; exit 1; }
  actual_requirement=$(codesign -d -r- "$path" 2>&1 | sed -n '/^designated =>/p')
  case "$actual_requirement" in
    *"identifier \"$expected_id\""*"certificate leaf[subject.CN] = \"$TITEN_CODESIGN_IDENTITY\""*) ;;
    *) echo "Unexpected designated requirement for $path: $actual_requirement" >&2; exit 1;;
  esac
  entitlements_dump=$(mktemp)
  if ! codesign -d --entitlements :- "$path" > "$entitlements_dump" 2>/dev/null; then
    rm -f "$entitlements_dump"
    echo "Could not read signed entitlements for $path" >&2
    exit 1
  fi
  if [ -s "$entitlements_dump" ]; then
    actual_entitlements=$(plutil -convert json -o - "$entitlements_dump") || {
      rm -f "$entitlements_dump"
      echo "Could not parse signed entitlements for $path" >&2
      exit 1
    }
  else
    actual_entitlements='{}'
  fi
  rm -f "$entitlements_dump"
  expected_entitlements=$(plutil -convert json -o - "$TITEN_ENTITLEMENTS")
  [ "$actual_entitlements" = "$expected_entitlements" ] || { echo "Unexpected signed entitlements for $path: $actual_entitlements" >&2; exit 1; }
}
verify_signed_target "$bundle/Contents/MacOS/titen-audio" "$TITEN_AUDIO_BUNDLE_ID"
verify_signed_target "$bundle" "$TITEN_APP_BUNDLE_ID"
app_identifier=$(plutil -extract CFBundleIdentifier raw -o - "$bundle/Contents/Info.plist")
[ "$app_identifier" = "$TITEN_APP_BUNDLE_ID" ] || { echo "Unexpected app bundle identifier: $app_identifier" >&2; exit 1; }
echo "Built $bundle"

arch=$(go env GOARCH)
mkdir -p dist
archive="Titen-${version}-macos-${arch}.zip"
ditto -c -k --sequesterRsrc --keepParent "$bundle" "dist/$archive"
(cd dist && shasum -a 256 "$archive" > "$archive.sha256")
echo "Packaged dist/$archive"

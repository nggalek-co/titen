#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
[ "$(uname -s)" = Darwin ] || { echo "macOS is required" >&2; exit 1; }
export MACOSX_DEPLOYMENT_TARGET=15.0
export GOCACHE="${GOCACHE:-$PWD/.cache/go}"
export CLANG_MODULE_CACHE_PATH="${CLANG_MODULE_CACHE_PATH:-$PWD/.cache/clang}"
export SWIFT_MODULECACHE_PATH="${SWIFT_MODULECACHE_PATH:-$PWD/.cache/swift}"
mkdir -p "$GOCACHE" "$CLANG_MODULE_CACHE_PATH" "$SWIFT_MODULECACHE_PATH"
swift build --disable-sandbox --package-path native/macos -c release
bundle="build/Titen.app"
mkdir -p "$bundle/Contents/MacOS" "$bundle/Contents/Resources"
go build -trimpath -o "$bundle/Contents/MacOS/titen" .
cp native/macos/.build/release/titen-audio "$bundle/Contents/MacOS/titen-audio"
cp resources/icon.png "$bundle/Contents/Resources/icon.png"
cat > "$bundle/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>Titen</string>
<key>CFBundleDisplayName</key><string>Titen</string>
<key>CFBundleIdentifier</key><string>co.nggalek.titen</string>
<key>CFBundleExecutable</key><string>titen</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>0.1.0</string>
<key>CFBundleVersion</key><string>1</string>
<key>LSMinimumSystemVersion</key><string>15.0</string>
<key>NSHighResolutionCapable</key><true/>
<key>NSMicrophoneUsageDescription</key><string>Titen records your microphone for visible meeting transcription.</string>
<key>NSScreenCaptureUsageDescription</key><string>Titen records meeting playback for visible transcription, including when you use headphones.</string>
</dict></plist>
PLIST
plutil -lint "$bundle/Contents/Info.plist"
codesign --force --sign - "$bundle/Contents/MacOS/titen-audio"
codesign --force --sign - "$bundle"
codesign --verify --deep --strict "$bundle"
echo "Built $bundle"

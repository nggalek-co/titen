#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
[ "$(uname -s)" = Darwin ] || { echo "macOS is required" >&2; exit 1; }
. scripts/signing-config.sh

bundle="build/Titen.app"
helper="$bundle/Contents/MacOS/titen-audio"
keychain="${TITEN_CODESIGN_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"
tmp_dir=$(mktemp -d)
version="signing-check-$(date +%Y%m%d%H%M%S)-$$"
arch=$(go env GOARCH)
archive_one="dist/Titen-${version}-1-macos-${arch}.zip"
archive_two="dist/Titen-${version}-2-macos-${arch}.zip"
trap 'rm -rf "$tmp_dir"; rm -f "$archive_one" "$archive_one.sha256" "$archive_two" "$archive_two.sha256"' EXIT HUP INT TERM

snapshot() {
	label="$1"
	directory="$2"
	mkdir -p "$directory"
	app_identifier=$(plutil -extract CFBundleIdentifier raw -o - "$bundle/Contents/Info.plist")
	[ "$app_identifier" = "$TITEN_APP_BUNDLE_ID" ] || { echo "Unexpected app bundle identifier: $app_identifier" >&2; return 1; }
	printf 'app-bundle-id=%s\n' "$app_identifier" > "$directory/identity.txt"
	for target in app helper; do
		if [ "$target" = app ]; then path="$bundle"; expected_id="$TITEN_APP_BUNDLE_ID"; else path="$helper"; expected_id="$TITEN_AUDIO_BUNDLE_ID"; fi
		codesign --verify --strict "$path"
		details=$(codesign -d --verbose=4 "$path" 2>&1)
		identifier=$(printf '%s\n' "$details" | sed -n 's/^Identifier=//p' | head -n 1)
		authority=$(printf '%s\n' "$details" | sed -n 's/^Authority=//p' | head -n 1)
		[ "$identifier" = "$expected_id" ] || { echo "$target signature identifier changed: $identifier" >&2; return 1; }
		[ "$authority" = "$TITEN_CODESIGN_IDENTITY" ] || { echo "$target signer changed: $authority" >&2; return 1; }
		requirement=$(codesign -d -r- "$path" 2>&1 | sed -n '/^designated =>/p')
		case "$requirement" in
			*"identifier \"$expected_id\""*"certificate leaf[subject.CN] = \"$TITEN_CODESIGN_IDENTITY\""*) ;;
			*) echo "$target designated requirement changed: $requirement" >&2; return 1;;
		esac
		printf '%s-id=%s\n%s-signer=%s\n%s-requirement=%s\n' "$target" "$identifier" "$target" "$authority" "$target" "$requirement" >> "$directory/identity.txt"
		codesign -d --entitlements :- "$path" > "$directory/$target-entitlements.plist" 2>/dev/null || {
			echo "Could not read signed entitlements for $path" >&2
			return 1
		}
		if [ -s "$directory/$target-entitlements.plist" ]; then
			plutil -lint "$directory/$target-entitlements.plist" >/dev/null
			actual_entitlements=$(plutil -convert json -o - "$directory/$target-entitlements.plist") || return 1
		else
			actual_entitlements='{}'
		fi
		expected_entitlements=$(plutil -convert json -o - "$TITEN_ENTITLEMENTS")
		[ "$actual_entitlements" = "$expected_entitlements" ] || { echo "$target signed entitlements differ from $TITEN_ENTITLEMENTS" >&2; return 1; }
		printf '%s-entitlements=%s\n' "$target" "$actual_entitlements" >> "$directory/identity.txt"
	done
	identity_fingerprints=$(security find-identity -v -p codesigning "$keychain" \
		| awk -v identity="\"$TITEN_CODESIGN_IDENTITY\"" 'index($0, identity) { print $2 }')
	set -- $identity_fingerprints
	[ "$#" -eq 1 ] || { echo "Expected one installed $TITEN_CODESIGN_IDENTITY identity; found $#" >&2; return 1; }
	printf 'certificate-fingerprint=%s\n' "$1" >> "$directory/identity.txt"
	shasum -a 256 "$TITEN_ENTITLEMENTS" | awk '{print "entitlements-file-sha256=" $1}' >> "$directory/identity.txt"
	printf 'Recorded %s signing identity and requirements\n' "$label"
}

assert_permissions() {
	stage="$1"
	permissions=$("$helper" --check-permissions)
	printf '%s permissions: %s\n' "$stage" "$permissions"
	printf '%s' "$permissions" | grep -Fq '"microphone":true' || {
		echo "$stage microphone permission is not authorized" >&2
		return 1
	}
	printf '%s' "$permissions" | grep -Fq '"systemAudio":true' || {
		echo "$stage Screen Recording permission is not authorized" >&2
		return 1
	}
}

TITEN_VERSION="$version-1" sh scripts/build-macos.sh
echo "Requesting any missing microphone and Screen Recording grants; approve the macOS prompts to continue."
"$helper" --request-permissions
assert_permissions "After build 1"
snapshot "build 1" "$tmp_dir/build-1"

TITEN_VERSION="$version-2" sh scripts/build-macos.sh
snapshot "build 2" "$tmp_dir/build-2"
diff -u "$tmp_dir/build-1/identity.txt" "$tmp_dir/build-2/identity.txt"
assert_permissions "After build 2"

echo "Verified microphone and Screen Recording grants survived two consecutive signed builds."

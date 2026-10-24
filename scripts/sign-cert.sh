#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
. scripts/signing-config.sh

[ "$(uname -s)" = Darwin ] || { echo "macOS is required" >&2; exit 1; }

keychain="${TITEN_CODESIGN_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"
[ -f "$keychain" ] || { echo "Signing keychain not found: $keychain" >&2; exit 1; }

identity_fingerprints() {
	security find-identity -v -p codesigning "$keychain" 2>/dev/null \
		| awk -v identity="\"$TITEN_CODESIGN_IDENTITY\"" 'index($0, identity) { print $2 }'
}

certificate_fingerprint() {
	file="$1"
	passphrase="$2"
	output="$tmp_dir/fingerprint-cert.pem"
	if ! openssl pkcs12 -in "$file" -clcerts -nokeys -passin "pass:$passphrase" -out "$output" 2>/dev/null; then
		openssl pkcs12 -legacy -in "$file" -clcerts -nokeys -passin "pass:$passphrase" -out "$output" 2>/dev/null || return 1
	fi
	openssl x509 -in "$output" -noout -fingerprint -sha1 \
		| sed 's/^.*=//; s/://g' | tr '[:lower:]' '[:upper:]'
}

certificate_dir="${TITEN_SIGNING_CERT_DIR:-$HOME/Library/Application Support/Titen/Signing}"
certificate_path="${TITEN_SIGNING_CERTIFICATE_PATH:-}"
certificate_password="${TITEN_SIGNING_CERTIFICATE_PASSWORD:-}"
legacy_password_path="$certificate_dir/titen-self-signed.password"

store_local_password() {
	printf '%s' "$1" | swift scripts/keychain-password.swift set "$TITEN_SIGNING_PASSWORD_SERVICE" "$TITEN_SIGNING_PASSWORD_ACCOUNT" >/dev/null
}

installed_fingerprints=$(identity_fingerprints)

if [ -z "$certificate_path" ]; then
	local_certificate_path="$certificate_dir/titen-self-signed.p12"
	if [ -f "$local_certificate_path" ]; then
		certificate_path="$local_certificate_path"
		if ! certificate_password=$(swift scripts/keychain-password.swift get "$TITEN_SIGNING_PASSWORD_SERVICE" "$TITEN_SIGNING_PASSWORD_ACCOUNT" 2>/dev/null); then
			[ -f "$legacy_password_path" ] || {
				echo "Signing passphrase is missing from the login Keychain and $legacy_password_path" >&2
				exit 1
			}
			certificate_password=$(cat "$legacy_password_path")
			store_local_password "$certificate_password"
			rm -f "$legacy_password_path"
			echo "Moved the existing signing passphrase into the login Keychain and removed its plaintext file."
		fi
	elif [ -n "$installed_fingerprints" ]; then
		set -- $installed_fingerprints
		if [ "$#" -eq 1 ]; then
			echo "Code-signing identity already present: \"$TITEN_CODESIGN_IDENTITY\""
			exit 0
		fi
		echo "Multiple valid \"$TITEN_CODESIGN_IDENTITY\" certificates are installed in $keychain." >&2
		echo "Remove duplicates so Titen cannot be signed with an unpredictable key." >&2
		exit 1
	else
		certificate_path="$local_certificate_path"
		mkdir -p "$certificate_dir"
		chmod 700 "$certificate_dir"
		tmp_dir=$(mktemp -d "$certificate_dir/.create.XXXXXX")
		trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
		umask 077
		certificate_password=$(openssl rand -hex 32)
		openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
			-keyout "$tmp_dir/key.pem" -out "$tmp_dir/cert.pem" \
			-subj "/CN=$TITEN_CODESIGN_IDENTITY" \
			-addext "keyUsage=critical,digitalSignature" \
			-addext "extendedKeyUsage=critical,codeSigning" >/dev/null 2>&1
		openssl pkcs12 -export -legacy -inkey "$tmp_dir/key.pem" -in "$tmp_dir/cert.pem" \
			-out "$tmp_dir/titen-self-signed.p12" -passout "pass:$certificate_password" >/dev/null 2>&1
		store_local_password "$certificate_password"
		cp "$tmp_dir/cert.pem" "$certificate_dir/titen-self-signed.pem"
		cp "$tmp_dir/titen-self-signed.p12" "$certificate_path"
		chmod 600 "$certificate_dir/titen-self-signed.pem" "$certificate_path"
		rm -rf "$tmp_dir"
		trap - EXIT HUP INT TERM
		echo "Created persistent signing certificate at $certificate_path"
	fi
fi

[ -f "$certificate_path" ] || { echo "Signing certificate not found: $certificate_path" >&2; exit 1; }
[ -n "$certificate_password" ] || { echo "Set TITEN_SIGNING_CERTIFICATE_PASSWORD for this certificate" >&2; exit 1; }

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
expected_fingerprint=$(certificate_fingerprint "$certificate_path" "$certificate_password") || {
	echo "Cannot read the signing certificate. Check its password and OpenSSL legacy support." >&2
	exit 1
}
[ -n "$expected_fingerprint" ] || { echo "The PKCS#12 file has no readable signing certificate" >&2; exit 1; }

installed_fingerprints=$(identity_fingerprints)
if [ -n "$installed_fingerprints" ]; then
	if printf '%s\n' "$installed_fingerprints" | grep -Fxiq "$expected_fingerprint"; then
		echo "Code-signing identity already present: \"$TITEN_CODESIGN_IDENTITY\""
		exit 0
	fi
	if [ -n "${TITEN_SIGNING_CERTIFICATE_PATH:-}" ]; then
		echo "Another valid \"$TITEN_CODESIGN_IDENTITY\" certificate is already installed in $keychain." >&2
		echo "Remove it or use a keychain containing only the shared release certificate." >&2
		exit 1
	fi
	set -- $installed_fingerprints
	if [ "$#" -gt 1 ]; then
		echo "Multiple valid \"$TITEN_CODESIGN_IDENTITY\" certificates are installed in $keychain." >&2
		echo "Remove duplicates so Titen cannot be signed with an unpredictable key." >&2
		exit 1
	fi
	# The generated certificate and keychain may have been installed together by
	# an earlier run; a fingerprint mismatch means the local state was replaced.
	echo "The installed signing identity does not match $certificate_path" >&2
	exit 1
fi

openssl pkcs12 -in "$certificate_path" -clcerts -nokeys -passin "pass:$certificate_password" \
	-out "$tmp_dir/cert.pem" 2>/dev/null || \
openssl pkcs12 -legacy -in "$certificate_path" -clcerts -nokeys -passin "pass:$certificate_password" \
	-out "$tmp_dir/cert.pem" 2>/dev/null || {
	echo "Cannot extract the signing certificate from $certificate_path" >&2
	exit 1
}

security import "$certificate_path" -k "$keychain" -P "$certificate_password" -T /usr/bin/codesign >/dev/null
security add-trusted-cert -r trustRoot -p codeSign -k "$keychain" "$tmp_dir/cert.pem" >/dev/null

installed_fingerprints=$(identity_fingerprints)
if ! printf '%s\n' "$installed_fingerprints" | grep -Fxiq "$expected_fingerprint"; then
	echo "Imported certificate is not a valid code-signing identity in $keychain" >&2
	echo "Check the keychain trust settings and confirm the certificate has codeSigning usage." >&2
	exit 1
fi

echo "Installed persistent code-signing identity: \"$TITEN_CODESIGN_IDENTITY\""

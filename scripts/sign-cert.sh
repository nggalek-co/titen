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
creation_dir=""
ephemeral_dir=""
tmp_dir=""

cleanup() {
	[ -z "$creation_dir" ] || rm -rf "$creation_dir"
	[ -z "$ephemeral_dir" ] || rm -rf "$ephemeral_dir"
	[ -z "$tmp_dir" ] || rm -rf "$tmp_dir"
}
trap cleanup EXIT HUP INT TERM

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
		umask 077
		if [ "${TITEN_SIGNING_EPHEMERAL:-0}" = 1 ]; then
			ephemeral_dir=$(mktemp -d "${TMPDIR:-/tmp}/titen-signing.XXXXXX")
			certificate_path="$ephemeral_dir/titen-self-signed.p12"
			certificate_password=$(openssl rand -hex 32)
			if ! openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
				-keyout "$ephemeral_dir/key.pem" -out "$ephemeral_dir/cert.pem" \
				-subj "/CN=$TITEN_CODESIGN_IDENTITY" \
				-addext "keyUsage=critical,digitalSignature" \
				-addext "extendedKeyUsage=critical,codeSigning" >/dev/null 2>&1; then
				echo "Could not create the temporary code-signing certificate." >&2
				exit 1
			fi
			if ! openssl pkcs12 -export -inkey "$ephemeral_dir/key.pem" -in "$ephemeral_dir/cert.pem" \
				-out "$certificate_path" -passout "pass:$certificate_password" \
				-keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-3DES -macalg sha1 >/dev/null 2>&1; then
				echo "Could not package the temporary code-signing certificate." >&2
				exit 1
			fi
			chmod 600 "$ephemeral_dir/key.pem" "$ephemeral_dir/cert.pem" "$certificate_path"
			echo "Created a temporary signing certificate for this GitHub Actions job."
		else
			mkdir -p "$certificate_dir"
			chmod 700 "$certificate_dir"
			creation_dir=$(mktemp -d "$certificate_dir/.create.XXXXXX")
			certificate_password=$(openssl rand -hex 32)
			openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
				-keyout "$creation_dir/key.pem" -out "$creation_dir/cert.pem" \
				-subj "/CN=$TITEN_CODESIGN_IDENTITY" \
				-addext "keyUsage=critical,digitalSignature" \
				-addext "extendedKeyUsage=critical,codeSigning" >/dev/null 2>&1
			openssl pkcs12 -export -legacy -inkey "$creation_dir/key.pem" -in "$creation_dir/cert.pem" \
				-out "$creation_dir/titen-self-signed.p12" -passout "pass:$certificate_password" >/dev/null 2>&1
			store_local_password "$certificate_password"
			cp "$creation_dir/cert.pem" "$certificate_dir/titen-self-signed.pem"
			cp "$creation_dir/titen-self-signed.p12" "$certificate_path"
			chmod 600 "$certificate_dir/titen-self-signed.pem" "$certificate_path"
			rm -rf "$creation_dir"
			creation_dir=""
			echo "Created persistent signing certificate at $certificate_path"
		fi
	fi
fi

[ -f "$certificate_path" ] || { echo "Signing certificate not found: $certificate_path" >&2; exit 1; }
[ -n "$certificate_password" ] || { echo "Set TITEN_SIGNING_CERTIFICATE_PASSWORD for this certificate" >&2; exit 1; }

tmp_dir=$(mktemp -d)
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

echo "Importing the signing identity into the selected keychain."
security import "$certificate_path" -k "$keychain" -P "$certificate_password" -T /usr/bin/codesign >/dev/null
if [ -n "${TITEN_CODESIGN_KEYCHAIN_PASSWORD:-}" ]; then
	echo "Configuring codesign access to the selected keychain."
	security set-key-partition-list -S apple-tool:,apple:,codesign: -s \
		-k "$TITEN_CODESIGN_KEYCHAIN_PASSWORD" "$keychain" >/dev/null
fi
echo "Trusting the self-signed code-signing certificate."
if [ "${TITEN_CODESIGN_CI:-0}" = 1 ]; then
	sudo -n security add-trusted-cert -d -r trustRoot -p codeSign \
		-k /Library/Keychains/System.keychain "$tmp_dir/cert.pem" >/dev/null
else
	security add-trusted-cert -r trustRoot -p codeSign -k "$keychain" "$tmp_dir/cert.pem" >/dev/null
fi

echo "Checking that the signing identity is available to codesign."
installed_fingerprints=$(identity_fingerprints)
if ! printf '%s\n' "$installed_fingerprints" | grep -Fxiq "$expected_fingerprint"; then
	echo "Imported certificate is not a valid code-signing identity in $keychain" >&2
	echo "Check the keychain trust settings and confirm the certificate has codeSigning usage." >&2
	exit 1
fi

echo "Installed persistent code-signing identity: \"$TITEN_CODESIGN_IDENTITY\""

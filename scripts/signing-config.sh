#!/bin/sh

# These values define Titen's persistent macOS code identity. Changing any of
# them changes the designated requirement that TCC uses to match permissions.
TITEN_CODESIGN_IDENTITY='Titen Self-Signed Code Signing'
TITEN_APP_BUNDLE_ID='co.nggalek.titen'
TITEN_AUDIO_BUNDLE_ID='co.nggalek.titen.audio'
TITEN_SIGNING_PASSWORD_SERVICE='co.nggalek.titen.codesign.p12'
TITEN_SIGNING_PASSWORD_ACCOUNT='Titen Self-Signed Code Signing'
TITEN_APP_DESIGNATED_REQUIREMENT="designated => identifier \"$TITEN_APP_BUNDLE_ID\" and certificate leaf[subject.CN] = \"$TITEN_CODESIGN_IDENTITY\""
TITEN_AUDIO_DESIGNATED_REQUIREMENT="designated => identifier \"$TITEN_AUDIO_BUNDLE_ID\" and certificate leaf[subject.CN] = \"$TITEN_CODESIGN_IDENTITY\""
TITEN_ENTITLEMENTS="$PWD/packaging/macos/Titen.entitlements"

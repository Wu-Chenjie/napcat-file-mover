#!/usr/bin/env bash
set -euo pipefail

APP_PATH="${1:-build/bin/NapCatFileMover.app}"

missing=()
[[ -n "${APPLE_SIGN_IDENTITY:-}" ]] || missing+=("APPLE_SIGN_IDENTITY")
[[ -n "${APPLE_TEAM_ID:-}" ]] || missing+=("APPLE_TEAM_ID")
[[ -n "${APPLE_NOTARY_PROFILE:-}" ]] || missing+=("APPLE_NOTARY_PROFILE")

if [[ ${#missing[@]} -gt 0 ]]; then
  printf 'missing required environment variables: %s\n' "${missing[*]}" >&2
  exit 2
fi

if [[ ! -d "$APP_PATH" ]]; then
  printf 'app bundle not found: %s\n' "$APP_PATH" >&2
  exit 2
fi

codesign --force --deep --options runtime --timestamp \
  --sign "$APPLE_SIGN_IDENTITY" "$APP_PATH"

ZIP_PATH="${APP_PATH%.app}-notary.zip"
ditto -c -k --keepParent "$APP_PATH" "$ZIP_PATH"

xcrun notarytool submit "$ZIP_PATH" \
  --keychain-profile "$APPLE_NOTARY_PROFILE" \
  --team-id "$APPLE_TEAM_ID" \
  --wait

xcrun stapler staple "$APP_PATH"
spctl --assess --type execute --verbose "$APP_PATH"

#!/bin/bash
set -euo pipefail

app="${1:?Usage: bash scripts/package-macos.sh app-path output-dmg}"
output="${2:?Usage: bash scripts/package-macos.sh app-path output-dmg}"
staging="$(mktemp -d)"
trap 'rm -r "$staging"' EXIT

ditto "$app" "$staging/wails-ssh.app"
ln -s /Applications "$staging/Applications"
hdiutil create -volname '云桥' -srcfolder "$staging" -format UDZO "$output"
hdiutil verify "$output"

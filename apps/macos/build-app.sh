#!/bin/bash
# Build only; never launches the application or a backend process.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="${1:-$HERE/build}"
APP="$OUT/Nightwire.app"
swift build --package-path "$HERE" -c release
BIN="$(swift build --package-path "$HERE" -c release --show-bin-path)"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
swiftc "$HERE/Sources/Nightwire/MothArtwork.swift" "$HERE/Tools/GenerateIcons.swift" -o "$OUT/generate-icons"
"$OUT/generate-icons" "$OUT/icons"
iconutil -c icns "$OUT/icons/Nightwire.iconset" -o "$APP/Contents/Resources/Nightwire.icns"
cp "$BIN/Nightwire" "$APP/Contents/MacOS/Nightwire"
cp "$HERE/Sources/Nightwire/Resources/source-catalog.json" "$APP/Contents/Resources/"
go -C "$ROOT" build -trimpath -o "$APP/Contents/MacOS/forumwatch" ./cmd/forumwatch
cp "$HERE/Info.plist" "$APP/Contents/Info.plist"
chmod 755 "$APP/Contents/MacOS/Nightwire" "$APP/Contents/MacOS/forumwatch"
# Ad-hoc local signing. Distribution/notarization is intentionally not configured.
codesign --force --sign - "$APP/Contents/MacOS/forumwatch"
codesign --force --sign - "$APP"
printf 'Built %s\n' "$APP"

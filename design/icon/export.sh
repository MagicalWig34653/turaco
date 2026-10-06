#!/bin/bash
# Run on macOS with Xcode (Icon Composer + Swift) installed. No third-party packages.
set -euo pipefail
ICON_ROOT="$(cd "$(dirname "$0")" && pwd)"
ICTOOL="${ICTOOL:-$(xcode-select -p)/../Applications/Icon Composer.app/Contents/Executables/ictool}"
if [[ ! -x "$ICTOOL" ]]; then
  ICTOOL='/Applications/Icon Composer.app/Contents/Executables/ictool'
fi
[[ -x "$ICTOOL" ]] || { echo 'Set ICTOOL to Icon Composer.app/Contents/Executables/ictool.' >&2; exit 1; }
mkdir -p "$ICON_ROOT/Turaco.icon/Assets" "$ICON_ROOT/preview" "$ICON_ROOT/export"
for layer in crest head feather; do
  cp "$ICON_ROOT/source/$layer.svg" "$ICON_ROOT/Turaco.icon/Assets/$layer.svg"
done
for rendition in Default Dark TintedLight TintedDark; do
  for size in 1024 128 32 16; do
    "$ICTOOL" "$ICON_ROOT/Turaco.icon" --export-image \
      --output-file "$ICON_ROOT/preview/$(echo "$rendition" | tr '[:upper:]' '[:lower:]')-$size.png" \
      --platform iOS --rendition "$rendition" --width "$size" --height "$size" \
      --scale 1 --design-generation 26
  done
done
"$ICTOOL" "$ICON_ROOT/Turaco.icon" --export-image \
  --output-file "$ICON_ROOT/preview/macos-1024.png" --platform macOS \
  --rendition Default --width 1024 --height 1024 --scale 1 --design-generation 26
ICON_CACHE="$(mktemp -d "${TMPDIR:-/tmp}/turaco-icon.XXXXXX")"
trap 'rm -rf "$ICON_CACHE"' EXIT
CLANG_MODULE_CACHE_PATH="$ICON_CACHE/clang" swift -module-cache-path "$ICON_CACHE/swift" "$ICON_ROOT/export.swift" "$ICON_ROOT"
for spec in '32 favicon' '180 apple-touch-icon' '192 icon-192'; do
  read -r size name <<< "$spec"
  sips -z "$size" "$size" "$ICON_ROOT/export/icon-512.png" --out "$ICON_ROOT/export/$name.png" >/dev/null
done
# sips on current macOS can write ICO. PNG remains a usable fallback on older hosts.
if ! sips -s format ico "$ICON_ROOT/export/favicon.png" --out "$ICON_ROOT/export/favicon.ico" >/dev/null 2>&1; then
  rm -f "$ICON_ROOT/export/favicon.ico"
  echo 'ICO unavailable; use favicon.png (32px).' >&2
fi
cat > "$ICON_ROOT/export/site.webmanifest" <<'JSON'
{
  "name": "Turaco",
  "short_name": "Turaco",
  "description": "One connected workspace for IT operations",
  "start_url": "/",
  "display": "standalone",
  "background_color": "#142e25",
  "theme_color": "#246747",
  "icons": [
    { "src": "icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any" },
    { "src": "icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any" },
    { "src": "icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable" }
  ]
}
JSON
printf 'Icon previews and web exports written to %s\n' "$ICON_ROOT"

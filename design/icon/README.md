# Turaco icon

A right-facing turaco profile with three swept crest feathers, a warm ivory head,
and one crimson flight-feather accent. Broad curves and an open silhouette carry
recognition at small sizes; no lettering or fine network ornament is needed.
The three independent foreground layers receive native glass edges, restrained
specular highlights and a 24% neutral shadow. Translucency is explicitly disabled
to preserve contrast. The background is an editable emerald gradient in the
Composer document, not a flattened image. Dark and system-tinted appearances are
included. The palette is independent of UI themes and semantic status colors.

| Color | Purpose |
| --- | --- |
| `#246747` | Brand/theme green; adaptive favicon on light backgrounds |
| `#327858` → `#142e25` | Default background gradient |
| `#204735` → `#0e2019` | Dark background gradient |
| `#92c7a1` | Sage crest |
| `#edf3df` | Warm ivory head; favicon on dark backgrounds |
| `#c8323c` | Crimson feather |
| `#f5f3eb` | Social-card paper |

Name and branding remain **subject to trademark clearance**. See
[the clearance record](../../docs/product/name-clearance.md). No registration or
exclusive ownership is claimed.

## Files and editing

- `Turaco.icon/`: real Icon Composer document (`icon.json` plus `Assets/`).
- `source/crest.svg`, `head.svg`, `feather.svg`: editable 1024 × 1024 transparent
  foreground artwork; edit these and run the export script to synchronize Assets.
- `source/web-icon.svg`, `maskable-icon.svg`, `og-image.svg`: generated editable
  vector compositions. Change their layout in `export.swift` to keep regeneration
  reproducible. Social text is converted to system-font outlines with CoreText;
  its appearance can differ slightly across macOS font versions.
- `preview/`: native iOS Default, Dark, TintedLight and TintedDark at 1024, 128,
  32 and 16 px, plus the macOS default at 1024 px. Previews include Apple's mask.
- `export/`: deployable web assets, independent of the native glass rendering.

Open with Finder, or from the repository root:

```bash
open -a '/Applications/Xcode.app/Contents/Applications/Icon Composer.app' \
  design/icon/Turaco.icon
```

The layer list in `icon.json` is **front to back**: feather, head, crest.
Keep all SVG canvases at 1024, with transparent unused space. Apple provides
native edge lighting and shadows; do not bake these into the foreground SVGs.
If editing imported assets in Composer, copy those changes back to `source/`
before running the script (which treats source layers as authoritative).

## Regeneration

Requires macOS and Xcode with its bundled Icon Composer. No third-party packages,
fonts, image tools or network access are required. `export.sh` uses Composer's
`ictool`, Apple's Swift/AppKit/CoreText and `sips`. Run from any working directory:

```bash
./design/icon/export.sh
```

Set `ICTOOL=/absolute/path/to/Icon\ Composer.app/Contents/Executables/ictool` if
Composer is installed elsewhere. On this machine, `/Applications/Icon Composer.app`
is absent, and `xcrun ictool` / Xcode's Developer `usr/bin/ictool` resolve to an
asset-catalog tool that rejects `--help`. The **Composer-bundled** executable has
the image-export interface:

```bash
ICTOOL='/Applications/Xcode.app/Contents/Applications/Icon Composer.app/Contents/Executables/ictool'
"$ICTOOL" --help
"$ICTOOL" "$PWD/design/icon/Turaco.icon" --export-image \
  --output-file "$PWD/design/icon/preview/default-1024.png" \
  --platform iOS --rendition Default --width 1024 --height 1024 \
  --scale 1 --design-generation 26
```

Use `Dark`, `TintedLight` or `TintedDark` for `--rendition`, and set both dimensions
to 128, 32 or 16 for smaller previews. The 26 generation is explicit because this
installed tool also supports 27. Tinted previews use Apple's default tint;
`--tint-color` and `--tint-strength` can explore user-selected tint treatments.
The OS owns final tint and material behavior.

Inside the coding sandbox, even Apple's unchanged empty `.icon` template reports
“couldn't be opened.” Rendering succeeds with normal macOS service access. All
checked-in previews were produced by the real Composer renderer, not simulated.
The format was checked against Xcode's bundled Icon Composer file template, local
framework property names, and a [working layered document](https://tangled.org/kepelet.com/flo/commit/f1d91c6cc059ce9866a294ce7055ac9467d1893e).

Web PNGs are intentionally full-bleed squares without a baked Apple corner mask.
The export helper rasterizes the source SVGs with AppKit; `sips` creates 180, 192
and 32 px versions and the single-resolution 32 px ICO. On systems lacking ICO
encoding, the script retains `favicon.png` as the fallback. The adaptive SVG
favicon uses a simplified single-color silhouette; the pinned-tab mask is black.
The maskable mark is reduced to 80% of its original scale, placing all foreground
art inside the central circle of radius 40% of the canvas width. Background color
extends to every edge. The social card is 1200 × 630 with outlined English copy;
localized social cards should be generated when wiring localized pages.

## Web integration

Copy the **contents** of `export/` to the public root. No files in `frontend/` or
`site/` are modified by this work. The manifest assumes root deployment; adjust
`start_url` and URL prefixes for a subpath installation. Paste into the HTML head:

```html
<link rel="icon" href="/favicon.ico" sizes="32x32">
<link rel="icon" href="/favicon.svg" type="image/svg+xml" sizes="any">
<link rel="apple-touch-icon" href="/apple-touch-icon.png" sizes="180x180">
<link rel="mask-icon" href="/mask-icon.svg" color="#246747">
<link rel="manifest" href="/site.webmanifest">
<meta name="theme-color" content="#246747">
<meta name="apple-mobile-web-app-title" content="Turaco">
<meta property="og:type" content="website">
<meta property="og:site_name" content="Turaco">
<meta property="og:title" content="Turaco">
<meta property="og:description" content="One connected workspace for IT operations">
<meta property="og:image" content="https://YOUR-PUBLIC-ORIGIN/og-image.png">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta property="og:image:alt" content="Turaco — One connected workspace for IT operations">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="Turaco">
<meta name="twitter:description" content="One connected workspace for IT operations">
<meta name="twitter:image" content="https://YOUR-PUBLIC-ORIGIN/og-image.png">
<meta name="twitter:image:alt" content="Turaco — One connected workspace for IT operations">
```

Replace `https://YOUR-PUBLIC-ORIGIN` with the deployment's actual public origin;
OG consumers require an absolute image URL. If ICO encoding is unavailable,
replace the first link with:

```html
<link rel="icon" href="/favicon.png" type="image/png" sizes="32x32">
```

For a native app, add `Turaco.icon` to its Xcode target and select Turaco as its
app icon. The PNG previews are review artifacts, not a replacement for the layered
document. Review the silhouette at actual 16/32 px, contrast on dark/tinted home
screens, and both circular and rounded-square masks before release.

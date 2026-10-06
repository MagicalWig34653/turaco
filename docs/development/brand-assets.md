# Brand assets

The editable app icon lives in [design/icon](../../design/icon/README.md), including
an Apple Icon Composer document, three transparent SVG layers, rendered appearance
previews and generated web exports. The mark is a geometric turaco profile with a
sage crest, warm ivory head and small crimson feather over emerald. It is theme
independent; brand accents do not replace semantic status colors.

Run `./design/icon/export.sh` on macOS with Xcode to synchronize the source layers,
render default/dark/tinted previews and regenerate favicons, app icons, the
maskable PWA icon and the social card. The asset README contains exact HTML tags,
manifest deployment assumptions and native-app integration instructions. Generated
assets are staged in `design/icon/export/`; frontend and site wiring is separate.

The [existing brand direction](../product/branding.md) remains background context;
this icon's precise palette and geometry are documented with its source assets.
Public name/branding use remains subject to [trademark clearance](../product/name-clearance.md).

# Captain / Formation logo kit

Formation is the direction selected by Moshe. This kit develops it in Signal orange and Graphite, with original outlined lettering and dedicated dark, reversed, and small-size artwork. It contains design assets only; application integration is separate.

Start with [the identity overview](presentation/overview.png), [the usage guide](GUIDELINES.md), and [the presentation board](presentation/board.html). The board shows six illustrative contexts: README, app icon, website header, terminal, sticker, and social profile. The terminal example uses existing Captain commands; it is not a captured CLI session.

## Files

| Folder | Contents |
| --- | --- |
| `source/` | Colour horizontal/stacked/symbol/wordmark masters, dark lockups, reverse symbol, small drawing |
| `digital/` | Black, white, orange, Graphite, and warm-signal SVGs; transparent PNG exports |
| `web/` | Adaptive SVG favicon, ICO with 16/32/48 px frames, matching PNGs, app tile, Apple/PWA icons, manifest, head snippet |
| `social/` | Graphite avatar with a circle-safe symbol, SVG and 1024 px PNG |
| `presentation/` | Overview SVG/PNG, HTML board and spec, browser mockup capture, and export QA page/screenshots |

The primary source files are `source/captain-horizontal.svg` for light backgrounds and `source/captain-horizontal-dark.svg` for dark backgrounds. Use `source/captain-symbol-small.svg` below 48 px. White and warm small versions are in `digital/`. The ordinary white symbol and white lockups have slightly lighter symbol geometry to balance apparent weight on dark backgrounds.

The small drawing opens the band's gaps from 20 to 24 units and makes the central band 48 units high. Favicons use that drawing. App artwork occupies 66% of its tile; the maskable icon reduces it to 50% so the mark remains inside the platform's safe circle. PWA and Apple icons have full-bleed backgrounds; the decorative app tile retains rounded corners.

## Web handover

Copy the files in `web/` to the host application's public root and include the markup in `web/head-snippet.html`. The manifest's image paths are relative to the manifest; the supplied head snippet assumes a public-root installation. For a different prefix, update the snippet's URLs to match. Review the host's existing manifest before replacing it.

The SVG favicon changes from Signal orange to Warm signal under `prefers-color-scheme: dark`. Its default fill remains explicit for SVG consumers that do not process the style block. PNG and ICO alternatives use the primary orange and do not adapt.

To inspect the local presentation and export checks from the repository root:

```bash
python3 -m http.server 19372 --bind 127.0.0.1 --directory assets/brand/captain-formation
```

Open `http://127.0.0.1:19372/presentation/board.html` for the deck or `http://127.0.0.1:19372/presentation/qa.html` for the exact-size checks. Stop the server when finished. The overview SVG and HTML board contain their artwork internally and remain portable when shared.

## Verification

- SVG structure and geometry checked across every master and exported SVG. The stacked lockups retain an informational complexity note because their seven outlined letters contribute most of the points. The favicon's conditional style and the app tile also have expected informational notes.
- Light and dark logo/background pairs passed the audit's 3:1 logo-contrast threshold. Colour carries no meaning that is lost in the one-colour versions.
- Actual browser checks covered 16, 24, 32, and 48 px symbols; PNG favicons; minimum lockup sizes; the adaptive favicon in light and dark modes; and the QA page at desktop and 390 px widths.
- PNG dimensions checked: favicons 16/32/48, Apple 180, PWA 192/512, maskable 512, app tile 512/1024, horizontal colour 1776×512, stacked colour 1280×1024, avatar 1024×1024. The ICO contains three sizes. Transparent logo exports retain alpha; full-bleed install icons are opaque.
- The six mockups were inspected in Chrome through agent-browser. Browser checks succeeded in this pass; the earlier concept-round timeouts were not reproduced.

Run the artwork audit from the repository root:

```bash
python3 /Users/moshe/.agents/skills/logo-design/scripts/svg_audit.py assets/brand/captain-formation/source/*.svg assets/brand/captain-formation/digital/*.svg assets/brand/captain-formation/web/*.svg assets/brand/captain-formation/social/*.svg
```

The palette, clear space, and size rules are in [GUIDELINES.md](GUIDELINES.md). All lettering is filled vector geometry with no installed-font dependency. No physical print proof, Pantone assignment, or professional trademark clearance is included. Application build/lint gates are outside this asset-only change; no application source was edited. PNG exports follow the repository's existing `*.png` ignore rule; the downloadable kit archive includes them.

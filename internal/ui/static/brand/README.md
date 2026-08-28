# Orbit Hub application assets

These files are optimized, embedded derivatives of the approved Orbit Hub SVG
masters. They are application delivery assets, not the editable source of truth.

- CSS serves the outlined SVG lockups and marks from this directory.
- Their editable masters live under `assets/branding/selected/`.
- PNG lockups are trimmed and resized to 768 px wide; PNG marks are 256 px wide
  and remain available as compatibility fallbacks.
- Theme-specific 16 px and 32 px favicons retain transparency and are rendered
  from the matching light/dark SVG marks.
- `favicon.ico` is the universal fallback on a Deep Space tile.
- `apple-touch-icon.png` is an opaque 180 px Deep Space application icon.
- `../../../../assets/branding/build_svg_logos.py` rebuilds the full family,
  including the matching documentation favicon.

The CSS in `../style.css` selects the correct lockup and mark from the resolved
`data-theme`. The media-qualified favicon links in `../index.html` follow the
browser chrome theme. An operator favicon override replaces the entire stock
icon set; other branding combinations retain it as the fallback.

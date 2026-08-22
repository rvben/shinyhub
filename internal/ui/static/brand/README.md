# Orbit Hub application assets

These files are optimized, embedded derivatives of the approved Orbit Hub
masters. They are application delivery assets, not the editable source of
truth.

- Light lockup and mark: `assets/branding/selected/shinyhub-orbit-hub.png` and
  `assets/branding/selected/shinyhub-orbit-hub-mark.png`.
- Dark lockup and mark: the approved Signal Hierarchy treatment at
  `assets/branding/iterations/dark-mode-v4/01-signal-hierarchy.png` and its mark.
- The lockups are trimmed and resized to 768 px wide; marks are 256 px wide.
- Theme-specific 16 px and 32 px favicons retain transparency.
- `favicon.ico` is the universal fallback on a Deep Space tile.
- `apple-touch-icon.png` is an opaque 180 px Deep Space application icon.

The CSS in `../style.css` selects the correct lockup and mark from the resolved
`data-theme`. The media-qualified favicon links in `../index.html` follow the
browser chrome theme. Operator branding removes the entire stock icon set when
a white-label identity is configured.

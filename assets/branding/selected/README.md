# Selected ShinyHub logo direction

The four SVG files are the editable source of truth for **Orbit Hub**:

- `shinyhub-orbit-hub.svg`: light-surface lockup.
- `shinyhub-orbit-hub-dark.svg`: dark-surface Crisp Ensemble lockup, with
  Starlight (`#E8EEFF`) “Shiny” and Soft Starlight (`#A8B4D4`) structure.
- `shinyhub-orbit-hub-mark.svg`: light-surface emblem.
- `shinyhub-orbit-hub-mark-dark.svg`: dark-surface emblem.

Every letter is an outlined path; the SVGs contain no live font and no embedded
raster. Lockups preserve the 2172 × 724 master canvas, while marks preserve the
48 px clear space of the approved emblem crops. `../build_svg_logos.py` records
the semantic reconstruction from the repaired production geometry and rebuilds
the application PNG, favicon, touch-icon, and documentation-icon derivatives;
it requires Python with Pillow, `potrace`, and `rsvg-convert`.

The matching PNG files remain compatibility and historical raster assets for
software that cannot consume SVG.

Each legacy PNG retains embedded production provenance, mirrored in the adjacent
prompt file. The identity remains unchanged except for one approved geometry repair:
the lower-right node now joins the orbit continuously along both edges. The dark
lockup carries the same bounded contour repair and remains a deterministic
color-separated derivative of the selected source everywhere else.

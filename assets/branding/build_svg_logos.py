#!/usr/bin/env python3
"""Build the editable Orbit Hub SVG masters from the repaired raster geometry.

The selected PNG predates a vector master. This one-time reconstruction traces
semantic, high-resolution masks from the repaired dark lockup, then writes clean
SVGs with outlined paths, shared palette roles, and no embedded raster or font.
"""

from __future__ import annotations

import shutil
import subprocess
import tempfile
import xml.etree.ElementTree as ET
from pathlib import Path

from PIL import Image, ImageDraw


ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "assets/branding/iterations/dark-mode-v4/01-signal-hierarchy.png"
SELECTED = ROOT / "assets/branding/selected"
DELIVERY = ROOT / "internal/ui/static/brand"
DOCS_ICON = ROOT / "docs/images/favicon.png"

LOCKUP_WIDTH = 2172
LOCKUP_HEIGHT = 724
MARK_VIEWBOX = "-3 9 701 587"


def semantic_masks(source: Path) -> dict[str, Image.Image]:
    image = Image.open(source).convert("RGBA")
    if image.size != (LOCKUP_WIDTH, LOCKUP_HEIGHT):
        raise ValueError(f"unexpected source size: {image.size}")

    masks = {
        name: Image.new("1", image.size, 1)
        for name in ("orbit", "spark", "pale", "shiny", "hub")
    }
    outputs = {name: mask.load() for name, mask in masks.items()}
    pixels = image.load()

    for y in range(LOCKUP_HEIGHT):
        for x in range(LOCKUP_WIDTH):
            r, g, b, alpha = pixels[x, y]
            if alpha < 96:
                continue

            # The repaired dark master uses exact semantic RGB values beneath
            # its alpha, so these masks retain the full antialiased contours
            # without inheriting detached, near-transparent generation noise.
            if x < 650 and (r, g, b) == (107, 122, 163):
                outputs["orbit"][x, y] = 0
            elif x < 650 and r <= 80 and g >= 140 and b >= 220:
                outputs["spark"][x, y] = 0
            elif x < 650 and r >= 140 and g >= 170 and b >= 190:
                outputs["pale"][x, y] = 0

    # Classify complete wordmark components instead of cutting at an x value:
    # the y descender optically overhangs the gap before H. Component ownership
    # keeps that intentional contour Starlight while preserving the H as cyan.
    remaining = {
        (x, y)
        for y in range(LOCKUP_HEIGHT)
        # The emblem's painted bounds end at x=649. Starting at 620 also
        # classified its right-hand orbit arc as part of the Shiny wordmark,
        # which became a pale overlay in the dark variant.
        for x in range(650, LOCKUP_WIDTH)
        if pixels[x, y][3] >= 96
    }
    while remaining:
        start = remaining.pop()
        component = [start]
        stack = [start]
        while stack:
            x, y = stack.pop()
            for dy in (-1, 0, 1):
                for dx in (-1, 0, 1):
                    if dx == 0 and dy == 0:
                        continue
                    neighbor = (x + dx, y + dy)
                    if neighbor in remaining:
                        remaining.remove(neighbor)
                        component.append(neighbor)
                        stack.append(neighbor)
        if len(component) < 64:
            continue
        centroid_x = sum(x for x, _ in component) / len(component)
        target = outputs["shiny"] if centroid_x < 1402 else outputs["hub"]
        for x, y in component:
            target[x, y] = 0

    return masks


def trace(mask: Image.Image, temp_dir: Path, name: str) -> tuple[str, list[str]]:
    bitmap = temp_dir / f"{name}.pbm"
    vector = temp_dir / f"{name}.svg"
    mask.save(bitmap)
    subprocess.run(
        [
            "potrace",
            str(bitmap),
            "--svg",
            "--flat",
            "--turdsize",
            "8",
            "--alphamax",
            "1",
            "--opttolerance",
            "0.12",
            "--output",
            str(vector),
        ],
        check=True,
    )

    root = ET.parse(vector).getroot()
    namespace = "{http://www.w3.org/2000/svg}"
    group = root.find(f"{namespace}g")
    if group is None:
        raise ValueError(f"potrace emitted no group for {name}")
    paths = [path.attrib["d"] for path in group.findall(f"{namespace}path")]
    if not paths:
        raise ValueError(f"potrace emitted no path for {name}")
    return group.attrib["transform"], paths


def layer(paths: list[str], fill: str, label: str) -> str:
    joined = "\n".join(f'      <path d="{path}"/>' for path in paths)
    return f'''    <g id="{label}" fill="{fill}">
{joined}
    </g>'''


def svg_document(
    traced: dict[str, tuple[str, list[str]]],
    *,
    dark: bool,
    mark: bool,
    trim: bool = False,
) -> str:
    title = "ShinyHub Orbit Hub mark" if mark else "ShinyHub Orbit Hub logo"
    description = (
        "Orbit Hub emblem with a cyan signal held by three connected nodes."
        if mark
        else "Orbit Hub emblem followed by the ShinyHub wordmark."
    )
    structure = "#A8B4D4" if dark else "#030510"
    shiny = "#E8EEFF" if dark else "#030510"
    variant = f"{'dark' if dark else 'light'}-{'mark' if mark else 'lockup'}"
    title_id = f"orbit-hub-title-{variant}"
    description_id = f"orbit-hub-description-{variant}"
    signal_id = f"orbit-hub-signal-{variant}"
    sparkle_id = f"orbit-hub-sparkle-{variant}"
    if mark:
        view_box = "45 57 605 491" if trim else MARK_VIEWBOX
        width, height = (605, 491) if trim else (701, 587)
    else:
        view_box = "45 57 2018 538" if trim else f"0 0 {LOCKUP_WIDTH} {LOCKUP_HEIGHT}"
        width, height = (2018, 538) if trim else (LOCKUP_WIDTH, LOCKUP_HEIGHT)
    transform = traced["orbit"][0]

    emblem_layers = [
        layer(traced["orbit"][1], structure, "orbit"),
        layer(traced["spark"][1], f"url(#{signal_id})", "signal"),
        layer(traced["pale"][1], f"url(#{sparkle_id})", "sparkle-core"),
    ]
    word_layers = [] if mark else [
        layer(traced["shiny"][1], shiny, "shiny"),
        layer(traced["hub"][1], f"url(#{signal_id})", "hub"),
    ]
    layers = "\n".join(emblem_layers + word_layers)

    return f'''<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="{view_box}" role="img" aria-labelledby="{title_id} {description_id}" shape-rendering="geometricPrecision">
  <title id="{title_id}">{title}</title>
  <desc id="{description_id}">{description}</desc>
  <metadata>Outlined vector reconstruction from the repaired 2172 x 724 Orbit Hub production master. No fonts or raster images are embedded.</metadata>
  <defs>
    <linearGradient id="{signal_id}" x1="0" y1="0" x2="0" y2="1" gradientUnits="objectBoundingBox">
      <stop offset="0" stop-color="#24B9FC"/>
      <stop offset="0.5" stop-color="#19B2FB"/>
      <stop offset="1" stop-color="#18B0FA"/>
    </linearGradient>
    <linearGradient id="{sparkle_id}" x1="0" y1="0" x2="0" y2="1" gradientUnits="objectBoundingBox">
      <stop offset="0" stop-color="#D9F5FD"/>
      <stop offset="1" stop-color="#BAE6FD"/>
    </linearGradient>
  </defs>
  <g transform="{transform}">
{layers}
  </g>
</svg>
'''


def render_svg(svg: Path, width: int, temp_dir: Path, name: str) -> Image.Image:
    output = temp_dir / f"{name}-{width}.png"
    subprocess.run(
        [
            "rsvg-convert",
            "--width",
            str(width),
            "--output",
            str(output),
            str(svg),
        ],
        check=True,
    )
    with Image.open(output) as rendered:
        return rendered.convert("RGBA")


def center_on_square(
    mark: Image.Image,
    size: int,
    *,
    optical_offset: tuple[int, int] = (0, 0),
) -> Image.Image:
    canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    x = (size - mark.width) // 2 + optical_offset[0]
    y = (size - mark.height) // 2 + optical_offset[1]
    canvas.alpha_composite(mark, (x, y))
    return canvas


def deep_space_tile(
    mark: Image.Image,
    size: int,
    *,
    rounded: bool,
    optical_offset: tuple[int, int] = (0, 0),
) -> Image.Image:
    tile = Image.new("RGBA", (size, size), (6, 9, 20, 255))
    if rounded:
        alpha = Image.new("L", (size, size), 0)
        ImageDraw.Draw(alpha).rounded_rectangle(
            (0, 0, size - 1, size - 1),
            radius=round(size * 0.22),
            fill=255,
        )
        tile.putalpha(alpha)
    x = (size - mark.width) // 2 + optical_offset[0]
    y = (size - mark.height) // 2 + optical_offset[1]
    tile.alpha_composite(mark, (x, y))
    return tile


def write_raster_derivatives() -> None:
    if shutil.which("rsvg-convert") is None:
        raise RuntimeError("rsvg-convert is required to build raster logo derivatives")

    light_lockup = DELIVERY / "orbit-hub-lockup-light.svg"
    dark_lockup = DELIVERY / "orbit-hub-lockup-dark.svg"
    light_mark = DELIVERY / "orbit-hub-mark-light.svg"
    dark_mark = DELIVERY / "orbit-hub-mark-dark.svg"

    with tempfile.TemporaryDirectory(prefix="shinyhub-raster-") as temp:
        temp_dir = Path(temp)

        render_svg(light_lockup, 768, temp_dir, "lockup-light").save(
            DELIVERY / "orbit-hub-lockup-light.png", optimize=True
        )
        render_svg(dark_lockup, 768, temp_dir, "lockup-dark").save(
            DELIVERY / "orbit-hub-lockup-dark.png", optimize=True
        )
        render_svg(light_mark, 256, temp_dir, "mark-light").save(
            DELIVERY / "orbit-hub-mark-light.png", optimize=True
        )
        render_svg(dark_mark, 256, temp_dir, "mark-dark").save(
            DELIVERY / "orbit-hub-mark-dark.png", optimize=True
        )

        # Transparent browser-chrome icons keep the same optical 14/16 and
        # 27/32 footprint as the approved raster set while inheriting the
        # current light/dark vector palette.
        for size, mark_width in ((16, 14), (32, 27)):
            for theme, source in (("light", light_mark), ("dark", dark_mark)):
                mark = render_svg(source, mark_width, temp_dir, f"favicon-{theme}")
                center_on_square(mark, size, optical_offset=(-1, -1)).save(
                    DELIVERY / f"favicon-{theme}-{size}.png", optimize=True
                )

        # The asymmetric orbit and lower-right node put more visual mass below
        # and to the right of the geometric centre. These bounded offsets place
        # the sparkle core on the perceived centre without changing the mark.
        favicon_mark = render_svg(dark_mark, 46, temp_dir, "favicon-tile")
        favicon_64 = deep_space_tile(
            favicon_mark,
            64,
            rounded=True,
            optical_offset=(-2, -1),
        )
        favicon_64.save(DELIVERY / "favicon-64.png", optimize=True)
        favicon_64.save(DOCS_ICON, optimize=True)

        touch_mark = render_svg(dark_mark, 126, temp_dir, "touch-icon")
        deep_space_tile(
            touch_mark,
            180,
            rounded=False,
            optical_offset=(-6, -3),
        ).save(
            DELIVERY / "apple-touch-icon.png", optimize=True
        )

        ico_mark = render_svg(dark_mark, 180, temp_dir, "favicon-ico")
        deep_space_tile(
            ico_mark,
            256,
            rounded=True,
            optical_offset=(-8, -4),
        ).save(
            DELIVERY / "favicon.ico",
            format="ICO",
            sizes=[(16, 16), (32, 32), (48, 48)],
        )


def write_outputs() -> None:
    if shutil.which("potrace") is None:
        raise RuntimeError("potrace is required to build the SVG logo masters")

    masks = semantic_masks(SOURCE)
    with tempfile.TemporaryDirectory(prefix="shinyhub-svg-") as temp:
        temp_dir = Path(temp)
        traced = {name: trace(mask, temp_dir, name) for name, mask in masks.items()}

    outputs = {
        SELECTED / "shinyhub-orbit-hub.svg": svg_document(traced, dark=False, mark=False),
        SELECTED / "shinyhub-orbit-hub-dark.svg": svg_document(traced, dark=True, mark=False),
        SELECTED / "shinyhub-orbit-hub-mark.svg": svg_document(traced, dark=False, mark=True),
        SELECTED / "shinyhub-orbit-hub-mark-dark.svg": svg_document(traced, dark=True, mark=True),
    }
    for path, content in outputs.items():
        path.write_text(content, encoding="utf-8")

    delivery_outputs = {
        DELIVERY / "orbit-hub-lockup-light.svg": svg_document(traced, dark=False, mark=False, trim=True),
        DELIVERY / "orbit-hub-lockup-dark.svg": svg_document(traced, dark=True, mark=False, trim=True),
        DELIVERY / "orbit-hub-mark-light.svg": svg_document(traced, dark=False, mark=True, trim=True),
        DELIVERY / "orbit-hub-mark-dark.svg": svg_document(traced, dark=True, mark=True, trim=True),
    }
    for path, content in delivery_outputs.items():
        path.write_text(content, encoding="utf-8")

    write_raster_derivatives()


if __name__ == "__main__":
    write_outputs()

#!/usr/bin/env python3
"""Assert every built page carries its own meta description.

This reads the generated HTML rather than the Markdown frontmatter, because the
two disagree in a way that matters: an unquoted colon in a YAML scalar makes
Zensical drop the key and silently fall back to the site-wide description, so a
page whose source looks correct still ships the wrong tag.

The site description is treated as failure rather than as a default. A page
serving it is a page that told search engines nothing specific about itself.
"""

from __future__ import annotations

import re
import sys
import tomllib
from pathlib import Path

MIN_LENGTH = 110
MAX_LENGTH = 155

DESCRIPTION = re.compile(
    r'<meta\s+name="description"\s+content="([^"]*)"', re.IGNORECASE
)


def main() -> int:
    site = Path(sys.argv[1] if len(sys.argv) > 1 else "site")
    config = Path(sys.argv[2] if len(sys.argv) > 2 else "zensical.toml")

    if not site.is_dir():
        print(f"docs descriptions: {site}/ is missing; run `zensical build` first")
        return 1

    site_description = tomllib.loads(config.read_text())["project"]["site_description"]

    # Pages excluded from the repository are built locally but never published,
    # so holding them to the published-page contract would fail every local run.
    pages = [p for p in sorted(site.rglob("index.html")) if "superpowers" not in p.parts]
    if not pages:
        print(f"docs descriptions: no pages found under {site}/")
        return 1

    problems: list[str] = []
    seen: dict[str, Path] = {}

    for page in pages:
        found = DESCRIPTION.search(page.read_text(encoding="utf-8"))
        rel = page.relative_to(site)
        if not found:
            problems.append(f"{rel}: no meta description at all")
            continue
        description = found.group(1)
        if description == site_description:
            problems.append(f"{rel}: still serving the site-wide description")
            continue
        if not MIN_LENGTH <= len(description) <= MAX_LENGTH:
            problems.append(
                f"{rel}: {len(description)} chars, outside {MIN_LENGTH}-{MAX_LENGTH}"
            )
        if description in seen:
            problems.append(f"{rel}: identical to {seen[description]}")
        seen[description] = rel

    if problems:
        print("docs descriptions: " + f"{len(problems)} problem(s)")
        for problem in problems:
            print(f"  {problem}")
        return 1

    print(f"docs descriptions: {len(pages)} pages, each with its own description")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

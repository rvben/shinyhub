"""Explore native UI restore failures without making upstream bugs CI contracts."""

from __future__ import annotations

import argparse
import asyncio
import json
from datetime import date, datetime
from importlib.metadata import version
from random import Random

from _support import json_values, root_get
from hypothesis import find, settings
from hypothesis.errors import NoSuchExample
from shiny import ui
from shinyhub_bookmarks import input_selectize, input_slider


def failure(control, saved):
    try:
        asyncio.run(root_get(control, saved))
    except Exception as error:
        return {"type": type(error).__name__, "message": str(error)}
    return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--examples", type=int, default=200)
    parser.add_argument("--seed", type=int, default=0)
    args = parser.parse_args()
    if args.examples < 1:
        parser.error("--examples must be positive")
    controls = {
        "selectize-native": (lambda: ui.input_selectize("x", "X", ["a", "b"]), "b"),
        "selectize-helper": (lambda: input_selectize("x", "X", ["a", "b"]), "b"),
        "selectize-multiple-helper": (
            lambda: input_selectize("x", "X", ["a", "b"], multiple=True),
            ["b"],
        ),
        "select": (lambda: ui.input_select("x", "X", ["a", "b"]), "b"),
        "radio": (lambda: ui.input_radio_buttons("x", "X", ["a", "b"]), "b"),
        "checkbox-group": (
            lambda: ui.input_checkbox_group("x", "X", ["a", "b"]),
            ["b"],
        ),
        "checkbox": (lambda: ui.input_checkbox("x", "X"), True),
        "numeric": (lambda: ui.input_numeric("x", "X", 1), 2),
        "text": (lambda: ui.input_text("x", "X"), "hello"),
        "slider": (lambda: ui.input_slider("x", "X", 0, 10, 5), 7),
        "range-slider": (lambda: ui.input_slider("x", "X", 0, 10, (2, 5)), [3, 8]),
        "date": (lambda: ui.input_date("x", "X", value=date(2026, 1, 1)), "2026-02-01"),
        "date-range": (
            lambda: ui.input_date_range(
                "x", "X", start=date(2026, 1, 1), end=date(2026, 2, 1)
            ),
            ["2026-01-02", "2026-02-02"],
        ),
        "date-slider-helper": (
            lambda: input_slider(
                "x", "X", date(2026, 1, 1), date(2026, 12, 31), date(2026, 2, 1)
            ),
            "2026-03-01",
        ),
        "datetime-slider-helper": (
            lambda: input_slider(
                "x",
                "X",
                datetime(2026, 1, 1),
                datetime(2026, 12, 31),
                datetime(2026, 2, 1),
            ),
            "2026-03-01T00:00:00",
        ),
        "date-slider": (
            lambda: ui.input_slider(
                "x", "X", date(2026, 1, 1), date(2026, 12, 31), date(2026, 2, 1)
            ),
            "2026-03-01",
        ),
        "datetime-slider": (
            lambda: ui.input_slider(
                "x",
                "X",
                datetime(2026, 1, 1),
                datetime(2026, 12, 31),
                datetime(2026, 2, 1),
            ),
            "2026-03-01T00:00:00",
        ),
    }
    print(
        json.dumps(
            {
                "shiny": version("shiny"),
                "seed": args.seed,
                "max_examples_per_search": args.examples,
            }
        )
    )
    for name, (control, saved) in controls.items():
        normal_failure = failure(control, saved)
        try:
            bad = find(
                json_values,
                lambda value: failure(control, value) is not None,
                settings=settings(
                    max_examples=args.examples, deadline=None, database=None
                ),
                random=Random(args.seed),
            )
            finding = {"saved": bad, "failure": failure(control, bad)}
        except NoSuchExample:
            finding = None
        print(
            json.dumps(
                {
                    "control": name,
                    "representative_saved": saved,
                    "representative_render_failure": normal_failure,
                    "minimized_render_failure": finding,
                }
            ),
            flush=True,
        )


if __name__ == "__main__":
    main()

"""Local fixture for auditing actual browser-generated bookmarks."""

import json
import os
from datetime import date, datetime

from shiny import App, render, ui
from shinyhub_bookmarks import input_selectize, input_slider

slider = (
    ui.input_slider
    if os.environ.get("SHINY_BOOKMARK_AUDIT_NATIVE_SLIDERS") == "1"
    else input_slider
)


def app_ui(request):
    return ui.page_fluid(
        slider("day", "Day", date(2026, 1, 1), date(2026, 12, 31), date(2026, 2, 1)),
        slider(
            "moment",
            "Moment",
            datetime(2026, 1, 1),
            datetime(2026, 12, 31),
            datetime(2026, 2, 1),
        ),
        ui.input_bookmark_button(),
        input_selectize("single", "Single", ["a", "b", "c"], selected="b"),
        input_selectize(
            "multi", "Multiple", ["a", "b", "c"], selected=["a", "c"], multiple=True
        ),
        ui.output_code("values"),
    )


def server(input, output, session):
    @render.code
    def values():
        return json.dumps(
            dict(
                day=input.day(),
                moment=input.moment(),
                single=input.single(),
                multi=input.multi(),
            ),
            default=str,
        )


app = App(app_ui, server, bookmark_store="url")

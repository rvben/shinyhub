import json
import os
from datetime import date, datetime, timedelta
from pathlib import Path
from tempfile import TemporaryDirectory

from starlette.requests import Request
from htmltools import TagList

from shiny import App, Inputs, Outputs, Session, module, reactive, render, ui
from shiny.module import ResolvedId
from shinyhub_bookmarks import bookmarking_dependency, input_slider, register


@module.ui
def module_slider():
    return input_slider(
        "day",
        "Module date",
        date(2026, 1, 1),
        date(2026, 12, 31),
        date(2026, 2, 1),
        step=timedelta(days=1),
    )


def app_ui(request: Request):
    return ui.page_fluid(
        input_slider(
            "day",
            "Date",
            date(2026, 1, 1),
            date(2026, 12, 31),
            date(2026, 2, 1),
            step=timedelta(days=1),
        ),
        input_slider(
            "days",
            "Date range",
            date(2026, 1, 1),
            date(2026, 12, 31),
            (date(2026, 2, 1), date(2026, 3, 1)),
            step=timedelta(days=1),
        ),
        input_slider(
            "moment",
            "Datetime",
            datetime(2026, 1, 1),
            datetime(2026, 12, 31),
            datetime(2026, 2, 1),
            step=timedelta(seconds=1),
            timezone="+0000",
        ),
        input_slider(
            "moments",
            "Datetime range",
            datetime(2026, 1, 1),
            datetime(2026, 12, 31),
            (datetime(2026, 2, 1), datetime(2026, 3, 1)),
            step=timedelta(seconds=1),
            timezone="+0000",
        ),
        input_slider("number", "Number", 0, 10, 2),
        input_slider("numbers", "Number range", 0, 10, (2, 3)),
        module_slider("m"),
        bookmarking_dependency(),
        ui.output_ui("late"),
        ui.output_code("adjustments"),
        ui.input_action_button("change", "Change values"),
        ui.input_bookmark_button(),
        ui.output_code("values"),
    )


def server(input: Inputs, output: Outputs, session: Session):
    session.bookmark.exclude.append("change")
    registration = None
    if session.bookmark.store == "url":
        registration = register(
            session=session,
            input=input,
            fields={
                "day": "Date",
                "days": "Date range",
                "moment": "Datetime",
                "moments": "Datetime range",
                "number": "Number",
                "numbers": "Number range",
                "m-day": "Module date",
                "late_day": "Dynamic date",
                "late_days": "Dynamic range",
            },
        )

    @render.ui
    def late():
        return TagList(
            input_slider(
                "late_day",
                "Dynamic date",
                date(2026, 1, 1),
                date(2026, 12, 31),
                date(2026, 2, 1),
                step=timedelta(days=1),
            ),
            input_slider(
                "late_days",
                "Dynamic range",
                date(2026, 1, 1),
                date(2026, 12, 31),
                (date(2026, 2, 1), date(2026, 3, 1)),
                step=timedelta(days=1),
            ),
        )

    @render.code
    def adjustments():
        # Read the live inputs so the output also follows deferred dynamic restore.
        input.day(), input.days(), input.late_day(), input.late_days()
        return json.dumps(
            [item.as_message() for item in registration.adjustments]
            if registration
            else []
        )

    @reactive.effect
    @reactive.event(input.change, ignore_init=True)
    def change_values():
        ui.update_slider("day", value=date(2026, 6, 15))
        ui.update_slider("days", value=(date(2026, 6, 15), date(2026, 9, 20)))
        ui.update_slider("moment", value=datetime(2026, 6, 15, 12, 34, 56))
        ui.update_slider(
            "moments",
            value=(datetime(2026, 6, 15, 12, 34, 56), datetime(2026, 9, 20, 1, 2, 3)),
        )
        ui.update_slider("late_day", value=date(2026, 7, 1))
        ui.update_slider("late_days", value=(date(2026, 7, 1), date(2026, 8, 1)))
        ui.update_slider("number", value=7)
        ui.update_slider("numbers", value=(6, 9))
        ui.update_slider(ResolvedId("m-day"), value=date(2026, 6, 16))

    @render.code
    def values():
        return json.dumps(
            dict(
                day=input.day(),
                days=input.days(),
                moment=input.moment(),
                moments=input.moments(),
                number=input.number(),
                numbers=input.numbers(),
                module_day=input[ResolvedId("m-day")](),
                late_day=input.late_day(),
                late_days=input.late_days(),
            ),
            default=str,
        )


store = (
    "server"
    if os.environ.get("SHINY_SLIDER_BOOKMARK_STORE", "url") == "server"
    else "url"
)
app = App(app_ui, server, bookmark_store=store)
bookmark_tmp = TemporaryDirectory(prefix="shiny-slider-bookmark-")
bookmark_dir = Path(os.environ.get("SHINY_SLIDER_BOOKMARK_DIR", bookmark_tmp.name))


def save_dir(state_id: str) -> Path:
    path = bookmark_dir / state_id
    path.mkdir()
    return path


app.set_bookmark_save_dir_fn(save_dir)
app.set_bookmark_restore_dir_fn(lambda state_id: bookmark_dir / state_id)

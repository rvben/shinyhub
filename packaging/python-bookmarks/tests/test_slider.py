"""Exercise slider bookmark restoration during the initial HTTP request."""

from __future__ import annotations

import builtins
import inspect
from types import SimpleNamespace
from datetime import date, datetime, timedelta, timezone
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlencode

import pytest
from htmltools import Tag
from starlette.requests import Request

from shiny import App, module, ui
from shinyhub_bookmarks import input_slider
from shiny.bookmark import restore_input
from shiny.bookmark._restore_state import RestoreContext, restore_context
from shiny.module import resolve_id
from shiny.bookmark._types import BookmarkStore
from shiny.bookmark._utils import to_json_str


class SliderMarkup(HTMLParser):
    def __init__(self, html: str):
        super().__init__()
        self.inputs: list[dict[str, str | None]] = []
        self.feed(html)

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attributes = dict(attrs)
        if tag == "input" and attributes.get("class") == "js-range-slider":
            self.inputs.append(attributes)


@pytest.mark.parametrize(
    "store,bookmarked", [("url", False), ("url", True), ("server", True)]
)
@pytest.mark.parametrize("namespaced", [False, True], ids=["root", "module"])
@pytest.mark.parametrize("ranged", [False, True], ids=["single", "range"])
@pytest.mark.parametrize("kind", ["date", "datetime", "number"])
async def test_slider_bookmark_root_get(
    store: BookmarkStore,
    namespaced: bool,
    bookmarked: bool,
    ranged: bool,
    kind: str,
    tmp_path: Path,
) -> None:
    def control() -> Tag:
        if kind == "date":
            return input_slider(
                "x",
                "X",
                date(2026, 1, 1),
                date(2026, 12, 31),
                (date(2026, 2, 1), date(2026, 3, 1)) if ranged else date(2026, 2, 1),
            )
        if kind == "datetime":
            return input_slider(
                "x",
                "X",
                datetime(2026, 1, 1),
                datetime(2026, 12, 31),
                (
                    (datetime(2026, 2, 1), datetime(2026, 3, 1))
                    if ranged
                    else datetime(2026, 2, 1)
                ),
            )
        return input_slider("x", "X", 0, 10, (2, 3) if ranged else 2)

    namespaced_control = module.ui(control)

    def app_ui(request: Request) -> Tag:
        return ui.page_fluid(namespaced_control("m") if namespaced else control())

    app = App(app_ui, server=None, bookmark_store=store)
    input_id = "m-x" if namespaced else "x"
    if kind == "date":
        saved = [date(2026, 6, 15), date(2026, 9, 20)] if ranged else date(2026, 6, 15)
        values = (
            [datetime(2026, 6, 15), datetime(2026, 9, 20)]
            if bookmarked
            else [datetime(2026, 2, 1), datetime(2026, 3, 1)]
        )
        expected = [
            value.replace(tzinfo=timezone.utc).timestamp() * 1000 for value in values
        ]
    elif kind == "datetime":
        saved = (
            [datetime(2026, 6, 15, 12, 34, 56), datetime(2026, 9, 20, 1, 2, 3)]
            if ranged
            else datetime(2026, 6, 15, 12, 34, 56)
        )
        values = (
            [datetime(2026, 6, 15, 12, 34, 56), datetime(2026, 9, 20, 1, 2, 3)]
            if bookmarked
            else [datetime(2026, 2, 1), datetime(2026, 3, 1)]
        )
        expected = [
            value.replace(tzinfo=timezone.utc).timestamp() * 1000 for value in values
        ]
    else:
        saved = [7, 9] if ranged else 7
        expected = [7, 9] if bookmarked else [2, 3]

    query = ""
    if bookmarked:
        if store == "server":
            (tmp_path / "input.json").write_text(
                to_json_str({input_id: saved}), encoding="utf-8"
            )
            app.set_bookmark_restore_dir_fn(lambda state_id: tmp_path)
            query = "_state_id_=bookmark"
        else:
            query = "_inputs_&" + urlencode({input_id: to_json_str(saved)})

    request = Request(
        dict(
            type="http",
            method="GET",
            path="/",
            scheme="http",
            server=("test", 80),
            headers=[],
            query_string=query.encode(),
        )
    )
    response = await app._on_root_request_cb(request)
    assert response.status_code == 200
    markup = SliderMarkup(bytes(response.body).decode())
    assert len(markup.inputs) == 1
    attrs = markup.inputs[0]
    assert attrs["id"] == input_id
    assert attrs["data-data-type"] == kind
    assert float(str(attrs["data-from"])) == expected[0]
    if ranged:
        assert attrs["data-type"] == "double"
        assert float(str(attrs["data-to"])) == expected[1]
    else:
        assert "data-to" not in attrs
    # Restoring a value must not change the app's bounds or step.
    default_attrs = SliderMarkup(str(control())).inputs[0]
    for key in ("data-min", "data-max", "data-step"):
        assert attrs[key] == default_attrs[key]


@pytest.mark.parametrize("kind", ["date", "datetime"])
@pytest.mark.parametrize("ranged", [False, True])
@pytest.mark.parametrize(
    "saved",
    [
        None,
        True,
        7,
        {},
        "invalid",
        [],
        ["2026-06-15"],
        ["invalid", "2026-09-20"],
        ["2026-06-15", "2026-09-20", "2026-10-01"],
    ],
)
async def test_invalid_date_bookmark_uses_default(kind, ranged, saved):
    value_type = date if kind == "date" else datetime
    default = value_type(2026, 2, 1)
    value = (default, value_type(2026, 3, 1)) if ranged else default
    app = App(
        lambda request: ui.page_fluid(
            input_slider(
                "x", "X", value_type(2026, 1, 1), value_type(2026, 12, 31), value
            )
        ),
        server=None,
        bookmark_store="url",
    )
    query = "_inputs_&" + urlencode({"x": to_json_str(saved)})
    response = await app._on_root_request_cb(
        Request(
            dict(
                type="http",
                method="GET",
                path="/",
                scheme="http",
                server=("test", 80),
                headers=[],
                query_string=query.encode(),
            )
        )
    )
    assert response.status_code == 200
    attrs = SliderMarkup(bytes(response.body).decode()).inputs[0]
    assert float(attrs["data-from"]) == 1769904000000
    if ranged:
        assert attrs["data-type"] == "double"
        assert float(attrs["data-to"]) == 1772323200000
    else:
        assert "data-to" not in attrs


def test_signature_and_normal_rendering_match_shiny():
    native = ui.input_slider
    assert list(inspect.signature(native).parameters) == list(
        inspect.signature(input_slider).parameters
    )
    for name, param in inspect.signature(native).parameters.items():
        actual = inspect.signature(input_slider).parameters[name]
        assert actual.kind == param.kind
        assert actual.default == param.default
    for minimum, maximum, value, step in [
        (0, 100, (20, 60), 5),
        (date(2026, 1, 1), date(2026, 12, 31), date(2026, 2, 1), timedelta(days=1)),
        (
            datetime(2026, 1, 1),
            datetime(2026, 12, 31),
            datetime(2026, 2, 1),
            timedelta(seconds=1),
        ),
    ]:
        kwargs = dict(
            step=step,
            ticks=True,
            animate={"interval": 900, "loop": True},
            width="20rem",
            sep=".",
            pre="before",
            post="after",
            time_format="%F",
            timezone="+0000",
            drag_range=False,
        )
        assert str(input_slider("x", "X", minimum, maximum, value, **kwargs)) == str(
            native("x", "X", minimum, maximum, value, **kwargs)
        )
    assert ui.input_slider is native


def test_session_backed_dynamic_restore(monkeypatch):
    ctx = RestoreContext()
    ctx.input._values = {resolve_id("x"): "2026-06-15"}
    session = SimpleNamespace(bookmark=SimpleNamespace(_restore_context=ctx))
    monkeypatch.setattr("shiny.session.get_current_session", lambda: session)
    attrs = SliderMarkup(
        str(
            input_slider(
                "x", "X", date(2026, 1, 1), date(2026, 12, 31), date(2026, 2, 1)
            )
        )
    ).inputs[0]
    assert float(attrs["data-from"]) == 1781481600000
    assert ctx.input.is_pending(resolve_id("x"))


def test_fixed_upstream_receives_decoded_value_and_options(monkeypatch):
    native = ui.input_slider
    ctx = RestoreContext()
    ctx.input._values = {resolve_id("x"): "2026-06-15", resolve_id("other"): "restored"}

    def fixed(id, label, min, max, value, **kwargs):
        assert restore_input(resolve_id(id), value) == date(2026, 6, 15)
        assert kwargs["ticks"] is True
        assert kwargs["drag_range"] is False
        return native(id, label, min, max, value, **kwargs)

    monkeypatch.setattr(ui, "input_slider", fixed)
    with restore_context(ctx):
        attrs = SliderMarkup(
            str(
                input_slider(
                    "x",
                    "X",
                    date(2026, 1, 1),
                    date(2026, 12, 31),
                    date(2026, 2, 1),
                    ticks=True,
                    drag_range=False,
                )
            )
        ).inputs[0]
        assert float(attrs["data-from"]) == 1781481600000
        assert restore_input(resolve_id("other"), "default") == "restored"


def test_context_survives_render_error(monkeypatch):
    ctx = RestoreContext()
    ctx.input._values = {resolve_id("x"): "2026-06-15", resolve_id("other"): "restored"}

    def fail(*args, **kwargs):
        raise ValueError("render failed")

    monkeypatch.setattr(ui, "input_slider", fail)
    with restore_context(ctx):
        with pytest.raises(ValueError, match="render failed"):
            input_slider(
                "x", "X", date(2026, 1, 1), date(2026, 12, 31), date(2026, 2, 1)
            )
        assert restore_input(resolve_id("other"), "default") == "restored"


@pytest.mark.parametrize("saved", [False, True])
def test_guarded_private_api(monkeypatch, saved):
    original_import = builtins.__import__

    def guarded_import(name, *args, **kwargs):
        if name == "shiny.bookmark._restore_state":
            raise ImportError("API removed")
        return original_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", guarded_import)
    ctx = RestoreContext()
    if saved:
        ctx.input._values = {resolve_id("x"): "2026-06-15"}
    with restore_context(ctx):
        # Numeric sliders never use the private context API, even with saved state.
        input_slider("x", "X", 0, 10, 5)
        if saved:
            with pytest.raises(RuntimeError, match="Upgrade shinyhub-bookmarks"):
                input_slider(
                    "x", "X", date(2026, 1, 1), date(2026, 12, 31), date(2026, 2, 1)
                )
        else:
            input_slider(
                "x", "X", date(2026, 1, 1), date(2026, 12, 31), date(2026, 2, 1)
            )


@pytest.mark.parametrize(
    "saved",
    [
        "0001-01-01T00:00:00+14:00",
        "9999-12-31T23:59:59-14:00",
        "2026-06-15T12:00:00+05:00",
        "2026-06-15",
    ],
)
async def test_extreme_and_offset_datetime_bookmarks_render(saved):
    app = App(
        lambda request: ui.page_fluid(
            input_slider(
                "x",
                "X",
                datetime(2026, 1, 1),
                datetime(2026, 12, 31),
                datetime(2026, 2, 1),
            )
        ),
        server=None,
        bookmark_store="url",
    )
    query = "_inputs_&" + urlencode({"x": to_json_str(saved)})
    response = await app._on_root_request_cb(
        Request(
            dict(
                type="http",
                method="GET",
                path="/",
                scheme="http",
                server=("test", 80),
                headers=[],
                query_string=query.encode(),
            )
        )
    )
    assert response.status_code == 200
    restored = datetime.fromisoformat(saved)
    if restored.tzinfo is None:
        restored = restored.replace(tzinfo=timezone.utc)
    attrs = SliderMarkup(bytes(response.body).decode()).inputs[0]
    assert float(attrs["data-from"]) == restored.timestamp() * 1000

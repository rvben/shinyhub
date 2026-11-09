from __future__ import annotations

import builtins
import inspect
from html.parser import HTMLParser
from types import SimpleNamespace
from urllib.parse import urlencode

import pytest
from shiny import App, module, ui
from shiny.bookmark import restore_input
from shiny.bookmark._restore_state import RestoreContext, restore_context
from shiny.module import resolve_id
from starlette.requests import Request

from shinyhub_bookmarks import input_selectize


class SelectMarkup(HTMLParser):
    def __init__(self, markup):
        super().__init__()
        self.options = []
        self.groups = []
        self.other = None
        self.option_text = []
        self.in_option = False
        self.feed(markup)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "option":
            self.options.append(attrs)
            self.option_text.append("")
            self.in_option = True
        elif tag == "optgroup":
            self.groups.append(attrs["label"])
        elif tag == "input" and attrs.get("id") == "other":
            self.other = attrs.get("value")

    def handle_endtag(self, tag):
        if tag == "option":
            self.in_option = False

    def handle_data(self, data):
        if self.in_option:
            self.option_text[-1] += data


@pytest.mark.parametrize("namespaced", [False, True], ids=["root", "module"])
@pytest.mark.parametrize("grouped", [False, True], ids=["list", "groups"])
@pytest.mark.parametrize(
    "multiple,saved,selected",
    [
        pytest.param(False, '"Week"', ["Week"], id="single"),
        pytest.param(False, '"Quarter"', [], id="single-stale"),
        pytest.param(False, "null", ["Day"], id="single-null"),
        pytest.param(False, None, ["Month"], id="single-default"),
        pytest.param(False, "other-only", ["Month"], id="single-unsaved"),
        pytest.param(True, '["Week"]', ["Week"], id="multiple-one"),
        pytest.param(True, '["Week","Day"]', ["Day", "Week"], id="multiple-two"),
        pytest.param(True, "null", [], id="multiple-null"),
        pytest.param(True, "[]", [], id="multiple-empty"),
        pytest.param(True, '["Quarter"]', [], id="multiple-stale"),
        pytest.param(True, None, ["Month"], id="multiple-default"),
        pytest.param(True, "other-only", ["Month"], id="multiple-unsaved"),
    ],
)
async def test_root_get_preserves_choices(
    namespaced, grouped, multiple, saved, selected
):
    choices = (
        {"Short": {"Day": "Daily", "Week": "Weekly"}, "Long": {"Month": "Monthly"}}
        if grouped
        else ["Day", "Week", "Month"]
    )

    def control():
        return input_selectize(
            "res",
            "Resolution",
            choices,
            selected=["Month"] if multiple else "Month",
            multiple=multiple,
        )

    namespaced_ui = module.ui(control)

    def app_ui(request):
        return ui.page_fluid(
            namespaced_ui("m") if namespaced else control(),
            ui.input_text("other", "Other", value="default"),
        )

    query = ""
    if saved is not None:
        values = {"other": '"restored"'}
        if saved != "other-only":
            values["m-res" if namespaced else "res"] = saved
        query = "_inputs_&" + urlencode(values)
    app = App(app_ui, server=None, bookmark_store="url")
    request = Request(
        {
            "type": "http",
            "method": "GET",
            "path": "/",
            "scheme": "http",
            "server": ("test", 80),
            "headers": [],
            "query_string": query.encode(),
        }
    )
    response = await app._on_root_request_cb(request)
    assert response.status_code == 200
    markup = SelectMarkup(bytes(response.body).decode())
    assert [item["value"] for item in markup.options] == ["Day", "Week", "Month"]
    assert markup.option_text == (
        ["Daily", "Weekly", "Monthly"] if grouped else ["Day", "Week", "Month"]
    )
    # A stale single selection has no explicit HTML selection; the browser may
    # choose the first option before ChoiceRestore applies its session policy.
    assert [item["value"] for item in markup.options if "selected" in item] == selected
    assert markup.groups == (["Short", "Long"] if grouped else [])
    assert markup.other == ("default" if saved is None else "restored")


def test_signature_and_normal_rendering_match_shiny():
    native = ui.input_selectize
    assert list(inspect.signature(native).parameters) == list(
        inspect.signature(input_selectize).parameters
    )
    for name, param in inspect.signature(native).parameters.items():
        actual = inspect.signature(input_selectize).parameters[name]
        assert actual.kind == param.kind
        assert actual.default == param.default
    options = {"plugins": ["drag_drop"], "placeholder": "Choose", "maxItems": 2}
    kwargs = dict(
        selected=["Week"],
        multiple=True,
        width="18rem",
        remove_button=False,
        options=options,
    )
    choices = ("Day", "Week", "Month")
    assert str(input_selectize("res", "Resolution", choices, **kwargs)) == str(
        native("res", "Resolution", choices, **kwargs)
    )
    assert ui.input_selectize is native
    assert options == {"plugins": ["drag_drop"], "placeholder": "Choose", "maxItems": 2}


def test_session_backed_dynamic_restore(monkeypatch):
    ctx = RestoreContext()
    ctx.input._values = {resolve_id("res"): "Week"}
    session = SimpleNamespace(bookmark=SimpleNamespace(_restore_context=ctx))
    monkeypatch.setattr("shiny.session.get_current_session", lambda: session)
    markup = SelectMarkup(str(input_selectize("res", "Resolution", ["Day", "Week"])))
    assert [item["value"] for item in markup.options] == ["Day", "Week"]
    assert [item["value"] for item in markup.options if "selected" in item] == ["Week"]
    assert ctx.input.is_pending(resolve_id("res"))


def test_parent_restore_context_survives_render_error(monkeypatch):
    ctx = RestoreContext()
    ctx.input._values = {resolve_id("res"): "Week", resolve_id("other"): "restored"}

    def fail(*args, **kwargs):
        raise ValueError("render failed")

    monkeypatch.setattr(ui, "input_selectize", fail)
    with restore_context(ctx):
        with pytest.raises(ValueError, match="render failed"):
            input_selectize("res", "Resolution", ["Day", "Week"])
        assert restore_input(resolve_id("other"), "default") == "restored"


def test_missing_private_api_has_actionable_error(monkeypatch):
    original_import = builtins.__import__

    def guarded_import(name, *args, **kwargs):
        if name == "shiny.bookmark._restore_state":
            raise ImportError("API removed")
        return original_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", guarded_import)
    ctx = RestoreContext()
    ctx.input._values = {resolve_id("res"): "Week"}
    with restore_context(ctx):
        with pytest.raises(RuntimeError, match="Upgrade shinyhub-bookmarks"):
            input_selectize("res", "Resolution", ["Day", "Week"])


def test_clean_input_needs_no_private_context_api(monkeypatch):
    original_import = builtins.__import__

    def guarded_import(name, *args, **kwargs):
        if name == "shiny.bookmark._restore_state":
            raise AssertionError("clean input must delegate directly to Shiny")
        return original_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", guarded_import)
    result = SelectMarkup(str(input_selectize("res", "Resolution", ["Day", "Week"])))
    assert [item["value"] for item in result.options] == ["Day", "Week"]

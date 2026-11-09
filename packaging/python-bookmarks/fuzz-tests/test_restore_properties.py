"""Bounded property tests; run with ``make fuzz-py-bookmarks``."""

from __future__ import annotations

import asyncio
import copy
import json
import os
from datetime import date, datetime, timedelta, timezone
from urllib.parse import urlencode

from _support import Markup, json_values, root_get, text
from hypothesis import event, given, settings
from hypothesis import strategies as st
from shiny import module
from shiny.bookmark import restore_input
from shiny.module import ResolvedId
from shinyhub_bookmarks import ChoiceRestore, input_selectize, input_slider
from shinyhub_bookmarks._restore import MISSING, resolve_choice, values_equal

settings.register_profile(
    "bookmarks",
    max_examples=int(os.environ.get("BOOKMARKS_FUZZ_EXAMPLES", "500")),
    deadline=None,
    print_blob=True,
    derandomize=os.environ.get("CI", "").lower() == "true",
)
settings.load_profile("bookmarks")


@given(
    pairs=st.lists(st.tuples(text, text), unique_by=lambda pair: pair[0], max_size=8),
    saved=json_values,
    multiple=st.booleans(),
    grouped=st.booleans(),
    depth=st.integers(0, 3),
    data=st.data(),
)
def test_selectize_untrusted_restore_preserves_ui(
    pairs, saved, multiple, grouped, depth, data
):
    if pairs:
        members = st.sampled_from([key for key, _ in pairs])
        saved = data.draw(
            st.one_of(st.just(saved), members, st.lists(members, max_size=8)),
            label="saved selection",
        )
    choices = dict(pairs)
    if grouped:
        midpoint = len(pairs) // 2
        choices = {"First": dict(pairs[:midpoint]), "Second": dict(pairs[midpoint:])}
    original_choices = copy.deepcopy(choices)
    original_saved = copy.deepcopy(saved)

    def control():
        return input_selectize("x", "Choice", choices, multiple=multiple)

    @module.ui
    def nested(level):
        return nested("child", level - 1) if level else control()

    input_id = "m-" + "child-" * (depth - 1) + "x" if depth else "x"
    markup = Markup(
        asyncio.run(
            root_get(
                lambda: nested("m", depth - 1) if depth else control(), saved, input_id
            )
        )
    )
    assert [option["value"] for option in markup.options] == [key for key, _ in pairs]
    assert markup.labels == [label for _, label in pairs]
    assert markup.groups == (["First", "Second"] if grouped else [])
    expected = (
        [pairs[0][0]]
        if saved is None and not multiple and pairs
        else (
            [key for key, _ in pairs if key in saved]
            if isinstance(saved, list)
            else [key for key, _ in pairs if key == saved]
        )
    )
    selected_options = [
        option["value"] for option in markup.options if "selected" in option
    ]
    well_shaped = saved is None or (
        isinstance(saved, str)
        if not multiple
        else isinstance(saved, list) and all(isinstance(item, str) for item in saved)
    )
    if well_shaped:
        assert selected_options == expected
    else:
        assert all(value in dict(pairs) for value in selected_options)
    assert markup.select_id == input_id
    assert markup.sibling == "restored"
    assert choices == original_choices
    assert saved == original_saved
    assert restore_input(ResolvedId(input_id), "outside") == "outside"


@given(
    choices=st.lists(text, min_size=1, max_size=8, unique=True),
    multiple=st.booleans(),
    data=st.data(),
)
def test_valid_selectize_bookmark_round_trip(choices, multiple, data):
    members = st.sampled_from(choices)
    selected = data.draw(st.lists(members, max_size=8) if multiple else members)
    # The empty live multi-select is serialized as null by Shiny's browser input.
    saved = selected or None if multiple else selected
    markup = Markup(
        asyncio.run(
            root_get(
                lambda: input_selectize("x", "X", choices, multiple=multiple), saved
            )
        )
    )
    actual = [option["value"] for option in markup.options if "selected" in option]
    expected = (
        [choice for choice in choices if choice in selected] if multiple else [selected]
    )
    assert actual == expected
    assert [option["value"] for option in markup.options] == choices


@given(
    pairs=st.lists(
        st.tuples(
            st.sampled_from(["x", "unknown", "m-x", "_values_", "_inputs_"]),
            st.one_of(
                text,
                st.sampled_from(
                    [
                        "NaN",
                        "Infinity",
                        "{",
                        "",
                        '{"a":1,"a":2}',
                        "999999999999999999999999999999999999",
                    ]
                ),
            ),
        ),
        max_size=8,
    ),
    marker=st.booleans(),
    fragment=st.one_of(
        st.sampled_from(["%", "%ZZ", "%E0%A4", "=x", "&&", "x", "x=", "+"]),
        st.text(alphabet="abcXYZ012%=&+_[]{}", max_size=30),
    ),
)
def test_raw_query_does_not_break_selectize(pairs, marker, fragment):
    query = ("_inputs_&" if marker else "") + urlencode(pairs) + "&" + fragment
    markup = Markup(
        asyncio.run(
            root_get(
                lambda: input_selectize("x", "X", ["a", "b"]), None, raw_query=query
            )
        )
    )
    assert [option["value"] for option in markup.options] == ["a", "b"]
    assert restore_input(ResolvedId("x"), "outside") == "outside"


@given(
    choices=st.lists(
        text.map(lambda value: "v:" + value), min_size=1, max_size=8, unique=True
    ),
    saved=json_values,
    multiple=st.booleans(),
    radio=st.booleans(),
    data=st.data(),
)
def test_choice_resolution_is_valid_and_stable(choices, saved, multiple, radio, data):
    multiple = multiple and not radio
    current = (
        data.draw(
            st.sampled_from([None, [], [choices[0]]]), label="current multi selection"
        )
        if multiple
        else choices[0]
    )
    members = st.sampled_from(choices + ["retired", "stale"])
    saved = data.draw(
        st.one_of(st.just(saved), members, st.lists(members, max_size=8)),
        label="saved selection",
    )
    policy = ChoiceRestore(
        choices=choices,
        aliases={"retired": choices[-1]},
        default=data.draw(
            st.sampled_from(
                [MISSING, choices[-1], [choices[-1]], "stale", ["stale"], None]
            ),
            label="fallback",
        ),
        control="radio" if radio else "selectize",
    )
    original = copy.deepcopy(saved)
    resolved = resolve_choice(policy, saved, current)
    if saved is None and current is None:
        assert resolved.value is None
        assert resolved.kind is None
        return
    # A valid saved selection must survive; merely returning any valid default
    # is insufficient. Aliases are disjoint from the available choices.
    items = saved if isinstance(saved, list) else ([] if saved is None else [saved])
    mapped = [choices[-1] if item == "retired" else item for item in items]
    valid = all(isinstance(item, str) and item in choices for item in mapped)
    event(
        "empty selection"
        if not items
        else (
            "saved contains available/alias values"
            if valid
            else "saved contains stale/wrong-shaped values"
        )
    )
    if valid and multiple:
        assert resolved.value == [choice for choice in choices if choice in mapped]
        assert resolved.kind == (
            "migrated"
            if (saved is not None and not isinstance(saved, list)) or "retired" in items
            else None
        )
    elif valid and len(mapped) == 1:
        assert resolved.value == mapped[0]
        assert resolved.kind == (
            "migrated" if isinstance(saved, list) or "retired" in items else None
        )
    else:
        assert resolved.kind == "fallback"
    if multiple:
        assert isinstance(resolved.value, list)
        assert resolved.value == [value for value in choices if value in resolved.value]
    else:
        assert resolved.value in choices
    repeated = resolve_choice(policy, resolved.value, current)
    assert values_equal(repeated.value, resolved.value)
    assert repeated.kind is None
    assert saved == original


@given(left=json_values, right=json_values)
def test_json_value_equality_survives_serialization(left, right):
    assert values_equal(left, left)
    assert values_equal(left, json.loads(json.dumps(left)))
    assert values_equal(left, right) == values_equal(right, left)
    # An independently specified JSON equivalence oracle distinguishes booleans
    # from numbers, but treats 1 and 1.0 as the same numeric value.
    assert values_equal(left, right) == (canonical(left) == canonical(right))
    assert not values_equal(left, {"wrapped": left})


def canonical(value):
    if value is None:
        return ("null",)
    if isinstance(value, bool):
        return ("boolean", value)
    if isinstance(value, (int, float)):
        return ("number", value)
    if isinstance(value, str):
        return ("string", value)
    if isinstance(value, list):
        return ("array", tuple(canonical(item) for item in value))
    return ("object", frozenset((key, canonical(item)) for key, item in value.items()))


@given(value=json_values)
def test_related_json_values(value):
    assert values_equal([value], (value,))
    assert not values_equal([value], [value, value])
    assert not values_equal(True, 1)
    assert not values_equal(False, 0)
    assert values_equal(1, 1.0)
    assert values_equal(-0.0, 0.0)
    assert values_equal({"a": value, "b": 1}, {"b": 1, "a": value})


@given(
    kind=st.sampled_from(["date", "datetime"]),
    ranged=st.booleans(),
    depth=st.integers(0, 3),
    data=st.data(),
)
def test_valid_date_slider_round_trip(kind, ranged, depth, data):
    value_type = date if kind == "date" else datetime
    values = (
        st.dates()
        if kind == "date"
        else st.datetimes(
            timezones=st.sampled_from(
                [
                    None,
                    timezone.utc,
                    timezone(timedelta(hours=14)),
                    timezone(timedelta(hours=-14)),
                ]
            )
        )
    )
    saved = (
        sorted(
            data.draw(st.lists(values, min_size=2, max_size=2)),
            key=lambda v: (
                datetime(v.year, v.month, v.day, tzinfo=timezone.utc).timestamp()
                if not isinstance(v, datetime)
                else (
                    v.replace(tzinfo=timezone.utc) if v.tzinfo is None else v
                ).timestamp()
            ),
        )
        if ranged
        else data.draw(values)
    )
    original = copy.deepcopy(saved)
    default = (
        [value_type(2000, 2, 1), value_type(2000, 3, 1)]
        if ranged
        else value_type(2000, 2, 1)
    )

    def control():
        return input_slider(
            "x", "X", value_type(2000, 1, 1), value_type(2035, 1, 1), default
        )

    @module.ui
    def nested(level):
        return nested("child", level - 1) if level else control()

    input_id = "m-" + "child-" * (depth - 1) + "x" if depth else "x"
    markup = Markup(
        asyncio.run(
            root_get(
                lambda: nested("m", depth - 1) if depth else control(), saved, input_id
            )
        )
    )
    attrs = markup.sliders[0]
    expected = saved if ranged else [saved]
    for key, value in zip(["data-from", "data-to"], expected):
        if kind == "date":
            value = datetime.combine(value, datetime.min.time())
        if value.tzinfo is None:
            value = value.replace(tzinfo=timezone.utc)
        assert float(attrs[key]) == value.timestamp() * 1000
    assert attrs["id"] == input_id
    assert markup.sibling == "restored"
    assert saved == original
    assert restore_input(ResolvedId(input_id), "outside") == "outside"


@given(
    saved=st.one_of(
        json_values.map(lambda value: {"unexpected": value}),
        st.none(),
        st.booleans(),
        st.integers(min_value=-(2**53), max_value=2**53),
        text.map(lambda value: "invalid:" + value),
        st.lists(text.map(lambda value: "invalid:" + value), max_size=5),
    ),
    kind=st.sampled_from(["date", "datetime"]),
    ranged=st.booleans(),
)
def test_untrusted_date_slider_restore_stays_renderable(saved, kind, ranged):
    value_type = date if kind == "date" else datetime
    default = (
        [value_type(2026, 2, 1), value_type(2026, 3, 1)]
        if ranged
        else value_type(2026, 2, 1)
    )
    original = copy.deepcopy(saved)
    markup = Markup(
        asyncio.run(
            root_get(
                lambda: input_slider(
                    "x", "X", value_type(2026, 1, 1), value_type(2026, 12, 31), default
                ),
                saved,
            )
        )
    )
    attrs = markup.sliders[0]
    assert float(attrs["data-from"]) == 1769904000000
    if ranged:
        assert attrs["data-type"] == "double"
        assert float(attrs["data-to"]) == 1772323200000
    else:
        assert "data-to" not in attrs
    assert markup.sibling == "restored"
    assert saved == original
    assert restore_input(ResolvedId("x"), "outside") == "outside"

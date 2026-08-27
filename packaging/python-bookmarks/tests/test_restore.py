from __future__ import annotations

from dataclasses import dataclass
from datetime import date, datetime, time
from enum import Enum
from uuid import UUID

import pytest
from shinyhub_bookmarks import ChoiceRestore, Field
from shinyhub_bookmarks._adapter import _normalise_fields, _restore_adjustments
from shinyhub_bookmarks._restore import resolve_choice, values_equal


def test_choice_restore_migrates_a_retired_value():
    result = resolve_choice(
        ChoiceRestore(
            choices=["All products", "Planning"],
            default="All products",
            aliases={"Legacy planning": "Planning"},
        ),
        "Legacy planning",
        "All products",
    )

    assert result.value == "Planning"
    assert result.kind == "migrated"


def test_choice_restore_uses_the_declared_default_for_an_unavailable_value():
    result = resolve_choice(
        ChoiceRestore(choices=["Europe", "Americas"], default="Europe"),
        "Atlantis",
        "Americas",
    )

    assert result.value == "Europe"
    assert result.kind == "fallback"


def test_choice_restore_retains_shinys_current_fallback_without_a_declared_default():
    result = resolve_choice(
        ChoiceRestore(choices=["Europe", "Americas"]),
        "Atlantis",
        "Americas",
    )

    assert result.value == "Americas"
    assert result.kind == "fallback"


def test_choice_restore_preserves_available_values_from_a_multiple_selection():
    result = resolve_choice(
        ChoiceRestore(choices=["A", "B", "C"], default=["A"]),
        ["B", "retired", "C"],
        ["A"],
    )

    assert result.value == ["B", "C"]
    assert result.kind == "fallback"


def test_choice_restore_supports_dynamic_and_grouped_choices():
    result = resolve_choice(
        ChoiceRestore(choices=lambda: {"Current": {"eu": "Europe", "us": "Americas"}}),
        "us",
        "eu",
    )

    assert result.value == "us"
    assert result.kind is None


def test_choice_restore_rejects_an_unknown_control():
    with pytest.raises(ValueError, match="control"):
        ChoiceRestore(choices=["A"], control="dropdown")  # type: ignore[arg-type]


def test_choice_restore_rejects_unordered_choices():
    with pytest.raises(TypeError, match="display order"):
        ChoiceRestore(choices={"A", "B"})


def test_choice_restore_preserves_an_empty_multiple_selection():
    result = resolve_choice(
        ChoiceRestore(choices=["A", "B"]),
        None,
        None,
    )

    assert result.value is None
    assert result.kind is None


def test_choice_restore_canonicalises_multiple_values_to_display_order():
    result = resolve_choice(
        ChoiceRestore(choices=["A", "B", "C"]),
        ["C", "A", "C"],
        ("A", "C"),
    )

    assert result.value == ["A", "C"]
    assert result.kind is None
    assert values_equal(result.value, ("A", "C"))


@pytest.mark.parametrize(
    ("saved", "current"),
    [
        (None, None),
        (["C", "A"], ("A", "C")),
    ],
)
def test_restore_adjustments_ignore_equivalent_multiple_choice_values(
    saved, current
):
    registered = _normalise_fields(
        {
            "products": Field(
                "Products",
                restore=ChoiceRestore(choices=["A", "B", "C"]),
            )
        },
        resolve=lambda value: value,
    )

    adjustments, updates = _restore_adjustments(
        state_input={"products": saved},
        registered=registered,
        current_values={"products": current},
        legacy_fields={},
    )

    assert not adjustments
    assert updates == {}


def test_values_equal_treats_list_and_tuple_transport_as_equivalent():
    assert values_equal(["A", "B"], ("A", "B"))


@pytest.mark.parametrize(
    ("saved", "current"),
    [
        ("2026-08-01", date(2026, 8, 1)),
        ("2026-08-01T12:30:00", datetime(2026, 8, 1, 12, 30)),
        (
            ["2026-08-01", "2026-08-27"],
            (date(2026, 8, 1), date(2026, 8, 27)),
        ),
    ],
)
def test_values_equal_treats_temporal_values_as_iso_bookmark_transport(
    saved, current
):
    assert values_equal(saved, current)


def test_values_equal_detects_different_temporal_values():
    assert not values_equal(["2026-08-01"], (date(2026, 8, 2),))


class Status(Enum):
    READY = "ready"


@dataclass
class NestedValue:
    when: date
    at: time


def test_values_equal_recurses_through_json_transport_shapes():
    saved = {
        "items": [["A"], {"when": "2026-08-27", "at": "12:30:00"}],
        "status": "ready",
        "identifier": "12345678-1234-5678-1234-567812345678",
    }
    current = {
        "items": (("A",), NestedValue(date(2026, 8, 27), time(12, 30))),
        "status": Status.READY,
        "identifier": UUID("12345678-1234-5678-1234-567812345678"),
    }

    assert values_equal(saved, current)


def test_values_equal_does_not_conflate_booleans_and_numbers():
    assert not values_equal(True, 1)
    assert not values_equal(False, 0.0)


class LiveValue:
    def __init__(self, value):
        self.value = value


def test_field_normalizer_supports_custom_transport_representations():
    registered = _normalise_fields(
        {
            "custom": Field(
                "Custom",
                restore=ChoiceRestore(choices=["saved"]),
                normalizer=lambda value: (
                    value.value if isinstance(value, LiveValue) else value
                ),
            )
        },
        resolve=lambda value: value,
    )

    adjustments, updates = _restore_adjustments(
        state_input={"custom": "saved"},
        registered=registered,
        current_values={"custom": LiveValue("saved")},
        legacy_fields={},
    )

    assert not adjustments
    assert updates == {}


def test_restore_adjustments_ignore_matching_date_range_transport_values():
    registered = _normalise_fields(
        {"daterange": Field("Date Range")},
        resolve=lambda value: value,
    )

    adjustments, updates = _restore_adjustments(
        state_input={"daterange": ["2026-08-01", "2026-08-27"]},
        registered=registered,
        current_values={
            "daterange": (date(2026, 8, 1), date(2026, 8, 27)),
        },
        legacy_fields={},
    )

    assert not adjustments
    assert updates == {}


def test_restore_adjustments_report_migration_rename_and_removed_field():
    registered = _normalise_fields(
        {
            "product": Field(
                "Product",
                restore=ChoiceRestore(
                    choices=["All products", "Planning"],
                    default="All products",
                    aliases={"Legacy planning": "Planning"},
                ),
            ),
            "region": Field(
                "Region",
                restore=ChoiceRestore(choices=["Europe", "Americas"], default="Europe"),
                renamed_from={"territory": "Territory"},
            ),
        },
        resolve=lambda value: value,
    )

    adjustments, updates = _restore_adjustments(
        state_input={
            "product": "Legacy planning",
            "territory": "Americas",
            "segment": "Enterprise",
        },
        registered=registered,
        current_values={"product": "All products", "region": "Europe"},
        legacy_fields={"segment": "Market segment"},
    )

    assert updates == {"product": "Planning", "region": "Americas"}
    assert [item.as_message() for item in adjustments] == [
        {
            "kind": "migrated",
            "label": "Product",
            "previous": "Legacy planning",
            "current": "Planning",
        },
        {
            "kind": "renamed",
            "label": "Region",
            "previous": "Americas",
            "current": "Americas",
            "sourceLabel": "Territory",
        },
        {
            "kind": "removed",
            "label": "Market segment",
            "previous": "Enterprise",
            "current": "Ignored",
        },
    ]


def test_restore_adjustments_detect_native_shiny_fallback_without_policy():
    registered = _normalise_fields(
        {"product": Field("Product")},
        resolve=lambda value: value,
    )

    adjustments, updates = _restore_adjustments(
        state_input={"product": "Retired"},
        registered=registered,
        current_values={"product": "All products"},
        legacy_fields={},
    )

    assert updates == {}
    assert adjustments[0].kind == "fallback"
    assert adjustments[0].previous == "Retired"
    assert adjustments[0].current == "All products"


def test_restore_adjustments_report_unknown_inputs_with_their_saved_values():
    registered = _normalise_fields(
        {"region": Field("Region")},
        resolve=lambda value: value,
    )

    adjustments, updates = _restore_adjustments(
        state_input={
            "region": "Europe",
            "<script>alert(1)</script>": "secret-value",
            "invented-filter": "also-secret",
        },
        registered=registered,
        current_values={"region": "Europe"},
        legacy_fields={},
    )

    assert updates == {}
    assert [item.as_message() for item in adjustments] == [
        {
            "kind": "unknown",
            "label": "<script>alert(1)</script>",
            "previous": "secret-value",
            "current": "Ignored",
        },
        {
            "kind": "unknown",
            "label": "invented-filter",
            "previous": "also-secret",
            "current": "Ignored",
        },
    ]


def test_unknown_input_display_removes_controls_and_bounds_text():
    registered = _normalise_fields(
        {"region": Field("Region")},
        resolve=lambda value: value,
    )
    long_id = "filter\u202e" + ("x" * 200)

    adjustments, _ = _restore_adjustments(
        state_input={long_id: "line one\n" + ("v" * 300)},
        registered=registered,
        current_values={"region": "Europe"},
        legacy_fields={},
    )

    assert "\u202e" not in adjustments[0].label
    assert "\n" not in adjustments[0].previous
    assert len(adjustments[0].label) == 120
    assert len(adjustments[0].previous) == 240
    assert adjustments[0].label.endswith("…")
    assert adjustments[0].previous.endswith("…")


def test_restore_adjustments_ignore_framework_inputs_and_do_not_double_count_legacy():
    registered = _normalise_fields(
        {"region": Field("Region")},
        resolve=lambda value: value,
    )

    adjustments, _ = _restore_adjustments(
        state_input={
            "region": "Europe",
            "segment": "Enterprise",
            ".shinyhub_bookmark_request": {"unsafe": True},
            "clientdata_url_hostname": "example.test",
            42: "not-an-input-id",
        },
        registered=registered,
        current_values={"region": "Europe"},
        legacy_fields={"segment": "Market segment"},
    )

    assert [item.kind for item in adjustments] == ["removed"]


def test_unknown_inputs_are_capped_with_an_honest_overflow_summary():
    registered = _normalise_fields(
        {"region": Field("Region")},
        resolve=lambda value: value,
    )
    legacy = {f"old-{index}": f"Old filter {index}" for index in range(20)}
    state_input = {key: "value" for key in legacy}
    for index in range(6):
        state_input[f"made-up-{index}"] = f"saved-{index}"

    adjustments, _ = _restore_adjustments(
        state_input=state_input,
        registered=registered,
        current_values={"region": "Europe"},
        legacy_fields=legacy,
    )

    assert len(adjustments) == 20
    assert [item.label for item in adjustments[-4:]] == [
        "made-up-0",
        "made-up-1",
        "made-up-2",
        "3 more unrecognized bookmark settings",
    ]
    assert adjustments[-1].kind == "unknown_summary"

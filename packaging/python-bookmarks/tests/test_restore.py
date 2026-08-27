from __future__ import annotations

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


def test_values_equal_treats_list_and_tuple_transport_as_equivalent():
    assert values_equal(["A", "B"], ("A", "B"))


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

from __future__ import annotations

from dataclasses import dataclass

import pytest
from shinyhub_bookmarks import ChoiceRestore, register
from shinyhub_bookmarks._adapter import (
    BOOKMARK_METADATA_KEY,
    Field,
    _create_url,
    _display_value,
    _normalise_fields,
    _selection_from_request,
)


class Inputs:
    def __dir__(self):
        return ["region", "period", "clientdata_url_hostname", ".private"]

    def __init__(self, values=None):
        self.values = values or {}

    def __getitem__(self, name):
        return lambda: self.values.get(name)


@dataclass
class Bookmark:
    exclude: list[str]
    url: str = "https://example.test/app?_inputs_=saved"

    async def get_bookmark_url(self):
        return self.url


class LifecycleBookmark(Bookmark):
    store = "url"

    def __init__(self):
        super().__init__(exclude=[])
        self.bookmark_callbacks = []
        self.restore_callbacks = []

    def on_bookmark(self, callback):
        self.bookmark_callbacks.append(callback)
        return callback

    def on_restore(self, callback):
        self.restore_callbacks.append(callback)
        return callback


class Session:
    def __init__(self):
        self.bookmark = LifecycleBookmark()
        self.messages = []

    def ns(self, value):
        return value

    async def send_custom_message(self, message_type, message):
        self.messages.append((message_type, message))


def test_normalise_fields_accepts_labels_and_resolves_ids():
    fields = _normalise_fields(
        {"region": "Region", "period": Field("Reporting period")},
        resolve=lambda value: f"module-{value}",
    )
    assert [(field.input_id, field.resolved_id, field.label) for field in fields] == [
        ("region", "module-region", "Region"),
        ("period", "module-period", "Reporting period"),
    ]


def test_field_validates_restore_and_renamed_metadata():
    with pytest.raises(TypeError, match="ChoiceRestore"):
        Field("Region", restore="unsafe")  # type: ignore[arg-type]
    with pytest.raises(ValueError, match="public input IDs"):
        Field(
            "Region",
            restore=ChoiceRestore(choices=["Europe"]),
            renamed_from={".private": "Old region"},
        )
    with pytest.raises(ValueError, match="requires a ChoiceRestore"):
        Field("Region", renamed_from={"territory": "Old region"})


def test_field_preserves_positional_restore_arguments():
    policy = ChoiceRestore(choices=["Europe"])
    field = Field("Region", None, policy, {"territory": "Old region"})

    assert field.restore is policy
    assert field.renamed_from == {"territory": "Old region"}
    assert field.normalizer is None


def test_normalise_fields_rejects_ambiguous_renamed_ids():
    with pytest.raises(ValueError, match="ambiguous"):
        _normalise_fields(
            {
                "region": Field(
                    "Region",
                    restore=ChoiceRestore(choices=["Europe"]),
                    renamed_from={"territory": "Territory"},
                ),
                "market": Field(
                    "Market",
                    restore=ChoiceRestore(choices=["Europe"]),
                    renamed_from={"territory": "Territory"},
                ),
            },
            resolve=lambda value: value,
        )


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        (None, "Not set"),
        (True, "On"),
        ([], "None"),
        (["A", "B"], "A, B"),
        ({"a": 1}, "1 selected"),
    ],
)
def test_display_value_is_human_readable(value, expected):
    assert _display_value(value, None) == expected


def test_selection_rejects_unknown_and_empty_fields():
    with pytest.raises(ValueError, match="at least one"):
        _selection_from_request(
            {"version": 1, "requestId": "r1", "include": []}, {"region"}
        )
    with pytest.raises(ValueError, match="unknown"):
        _selection_from_request(
            {"version": 1, "requestId": "r1", "include": ["secret"]}, {"region"}
        )


@pytest.mark.asyncio
async def test_create_url_excludes_unselected_inputs_and_restores_author_settings():
    bookmark = Bookmark(exclude=["author_excluded"])

    # The fake asserts the effective exclusion while serialising. The original
    # app-authored list is put back even after the await.
    async def get_url():
        assert bookmark.exclude == [
            ".shinyhub_bookmark_discover",
            ".shinyhub_bookmark_request",
            "author_excluded",
            "period",
        ]
        return bookmark.url

    bookmark.get_bookmark_url = get_url

    result = await _create_url(
        bookmark=bookmark,
        inputs=Inputs(),
        selected=["region"],
        max_url_length=8192,
    )

    assert result == bookmark.url
    assert bookmark.exclude == ["author_excluded"]


@pytest.mark.asyncio
async def test_create_url_restores_exclusions_after_failure():
    bookmark = Bookmark(exclude=["keep"])

    async def fail():
        raise RuntimeError("boom")

    bookmark.get_bookmark_url = fail

    with pytest.raises(RuntimeError, match="boom"):
        await _create_url(
            bookmark=bookmark, inputs=Inputs(), selected=["region"], max_url_length=8192
        )

    assert bookmark.exclude == ["keep"]


@pytest.mark.asyncio
async def test_selected_registration_temporarily_wins_over_an_author_exclusion():
    bookmark = Bookmark(exclude=["region"])

    async def get_url():
        assert bookmark.exclude == [
            ".shinyhub_bookmark_discover",
            ".shinyhub_bookmark_request",
            "period",
        ]
        return bookmark.url

    bookmark.get_bookmark_url = get_url

    await _create_url(
        bookmark=bookmark, inputs=Inputs(), selected=["region"], max_url_length=8192
    )

    assert bookmark.exclude == ["region"]


@pytest.mark.asyncio
async def test_register_versions_links_and_reports_a_restored_migration(monkeypatch):
    from shiny import reactive, ui

    def inert_effect(fn=None, **_kwargs):
        return fn if fn is not None else lambda callback: callback

    monkeypatch.setattr(reactive, "effect", inert_effect)
    monkeypatch.setattr(
        reactive, "event", lambda *_args, **_kwargs: lambda callback: callback
    )
    updates = []
    monkeypatch.setattr(
        ui,
        "update_select",
        lambda input_id, *, selected, session: updates.append(
            (input_id, selected, session)
        ),
    )

    session = Session()
    inputs = Inputs({"product": "All products"})
    registration = register(
        session=session,
        input=inputs,
        schema_version=4,
        fields={
            "product": Field(
                "Product",
                restore=ChoiceRestore(
                    choices=["All products", "Planning"],
                    default="All products",
                    aliases={"Legacy planning": "Planning"},
                ),
            )
        },
    )

    save_state = type("SaveState", (), {"values": {}})()
    await session.bookmark.bookmark_callbacks[0](save_state)
    assert save_state.values[BOOKMARK_METADATA_KEY] == {"version": 1, "schema": 4}

    restore_state = type(
        "RestoreState",
        (),
        {
            "input": {"product": "Legacy planning"},
            "values": {BOOKMARK_METADATA_KEY: {"version": 1, "schema": 2}},
        },
    )()
    await session.bookmark.restore_callbacks[0](restore_state)

    assert updates == [("product", "Planning", session)]
    assert registration.adjustments[0].kind == "migrated"
    assert registration.restored_schema_version == 2
    message_type, payload = session.messages[-1]
    assert message_type == "shinyhub-bookmark-capabilities"
    assert payload["schemaVersion"] == 4
    assert payload["restoredSchemaVersion"] == 2
    assert payload["fields"][0]["value"] == "Planning"
    assert payload["adjustments"][0]["previous"] == "Legacy planning"

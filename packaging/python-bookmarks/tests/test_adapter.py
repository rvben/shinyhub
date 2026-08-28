from __future__ import annotations

import asyncio
from dataclasses import dataclass

import pytest
from shinyhub_bookmarks import ChoiceRestore, register
from shinyhub_bookmarks._adapter import (
    BOOKMARK_METADATA_KEY,
    DISCOVER_INPUT_ID,
    Field,
    REQUEST_INPUT_ID,
    SYNC_ACK_INPUT_ID,
    _create_url,
    _display_value,
    _normalise_fields,
    _selection_exclusions,
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
        self.last_save_state = None

    async def get_bookmark_url(self):
        state = type(
            "SaveState",
            (),
            {"values": {}, "exclude": list(self.exclude)},
        )()
        for callback in self.bookmark_callbacks:
            await callback(state)
        self.last_save_state = state
        return self.url

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


@pytest.fixture
def inert_reactivity(monkeypatch):
    from shiny import reactive

    def inert_effect(fn=None, **_kwargs):
        return fn if fn is not None else lambda callback: callback

    monkeypatch.setattr(reactive, "effect", inert_effect)
    monkeypatch.setattr(
        reactive, "event", lambda *_args, **_kwargs: lambda callback: callback
    )


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
async def test_create_url_validates_the_generated_url_without_mutating_bookmark():
    bookmark = Bookmark(exclude=["author_excluded"])

    result = await _create_url(bookmark=bookmark, max_url_length=8192)

    assert result == bookmark.url
    assert bookmark.exclude == ["author_excluded"]


@pytest.mark.asyncio
async def test_create_url_propagates_serialization_failure():
    bookmark = Bookmark(exclude=["keep"])

    async def fail():
        raise RuntimeError("boom")

    bookmark.get_bookmark_url = fail

    with pytest.raises(RuntimeError, match="boom"):
        await _create_url(bookmark=bookmark, max_url_length=8192)

    assert bookmark.exclude == ["keep"]


def test_selection_exclusions_are_request_local_and_keep_selected_fields():
    assert _selection_exclusions(
        ["author_excluded", "region"],
        {"region", "period", "other-input"},
        ["region"],
    ) == [
        DISCOVER_INPUT_ID,
        REQUEST_INPUT_ID,
        SYNC_ACK_INPUT_ID,
        "author_excluded",
        "other-input",
        "period",
    ]


@pytest.mark.asyncio
async def test_register_versions_links_and_reports_a_restored_migration(
    monkeypatch, inert_reactivity
):
    from shiny import ui

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


def test_register_rejects_module_scoped_sessions_before_installing_callbacks():
    from shiny.module import ResolvedId

    session = Session()
    session.ns = ResolvedId("filters")  # type: ignore[method-assign]

    with pytest.raises(ValueError, match="top-level server session"):
        register(
            session=session,
            input=Inputs({"region": "Europe"}),
            fields={"region": "Region"},
        )

    assert session.bookmark.bookmark_callbacks == []
    assert session.bookmark.restore_callbacks == []


def test_register_rejects_duplicate_protocol_handlers(inert_reactivity):
    from shiny.module import ResolvedId

    session = Session()
    session.ns = ResolvedId("")  # type: ignore[method-assign]
    inputs = Inputs({"region": "Europe"})

    register(session=session, input=inputs, fields={"region": "Region"})

    with pytest.raises(RuntimeError, match="only once"):
        register(session=session, input=inputs, fields={"region": "Region"})

    assert len(session.bookmark.bookmark_callbacks) == 1
    assert len(session.bookmark.restore_callbacks) == 1


@pytest.mark.asyncio
async def test_register_supports_resolved_module_ids_from_the_root_session(
    inert_reactivity,
):
    from shiny.module import ResolvedId

    session = Session()
    session.ns = ResolvedId("")  # type: ignore[method-assign]
    registration = register(
        session=session,
        input=Inputs({"filters-region": "Europe"}),
        fields={"filters-region": "Region"},
    )
    restore_state = type(
        "RestoreState",
        (),
        {"input": {"filters-region": "Europe"}, "values": {}},
    )()

    await session.bookmark.restore_callbacks[0](restore_state)

    assert registration.fields[0].resolved_id == "filters-region"
    assert registration.adjustments == ()
    assert session.messages[-1][1]["fields"][0]["value"] == "Europe"


@pytest.mark.asyncio
async def test_register_contains_formatter_failures(inert_reactivity):
    def broken_formatter(_value):
        raise ValueError("formatter bug")

    session = Session()
    register(
        session=session,
        input=Inputs({"region": "Europe"}),
        fields={"region": Field("Region", formatter=broken_formatter)},
    )
    restore_state = type("RestoreState", (), {"input": {}, "values": {}})()

    await session.bookmark.restore_callbacks[0](restore_state)

    message_type, payload = session.messages[-1]
    assert message_type == "shinyhub-bookmark-capabilities"
    assert payload["fields"][0]["value"] == "Europe"


@pytest.mark.asyncio
async def test_registered_input_changes_request_automatic_url_sync(monkeypatch):
    from shiny import reactive

    effects = []

    def capture_effect(fn=None, **_kwargs):
        if fn is None:
            return capture_effect
        effects.append(fn)
        return fn

    monkeypatch.setattr(reactive, "effect", capture_effect)
    monkeypatch.setattr(
        reactive, "event", lambda *_args, **_kwargs: lambda callback: callback
    )

    session = Session()
    inputs = Inputs({"region": "Europe"})
    register(session=session, input=inputs, fields={"region": "Region"})
    publish = next(
        callback
        for callback in effects
        if callback.__name__ == "_publish_capabilities"
    )

    await publish()
    assert session.messages[-1][1]["autoSync"] is False
    assert session.messages[-1][1]["syncRevision"] == 0

    inputs.values["region"] = "Americas"
    await publish()
    assert session.messages[-1][1]["autoSync"] is True
    assert session.messages[-1][1]["syncRevision"] == 1
    assert session.messages[-1][1]["fields"][0]["value"] == "Americas"

    republish = next(
        callback
        for callback in effects
        if callback.__name__ == "_republish_capabilities"
    )
    await republish()
    assert session.messages[-1][1]["autoSync"] is True
    assert session.messages[-1][1]["syncRevision"] == 1

    acknowledge = next(
        callback
        for callback in effects
        if callback.__name__ == "_acknowledge_url_sync"
    )
    inputs.values[SYNC_ACK_INPUT_ID] = {"version": 1, "syncRevision": 1}
    await acknowledge()
    await republish()
    assert session.messages[-1][1]["autoSync"] is False


@pytest.mark.asyncio
async def test_request_scopes_exclusions_to_its_bookmark_state(monkeypatch):
    from shiny import reactive

    effects = []

    def capture_effect(fn=None, **_kwargs):
        if fn is None:
            return capture_effect
        effects.append(fn)
        return fn

    monkeypatch.setattr(reactive, "effect", capture_effect)
    monkeypatch.setattr(
        reactive, "event", lambda *_args, **_kwargs: lambda callback: callback
    )

    session = Session()
    session.bookmark.exclude = ["author_excluded", "region"]
    inputs = Inputs(
        {
            "region": "Europe",
            REQUEST_INPUT_ID: {
                "version": 1,
                "requestId": "url-sync-test",
                "include": ["region"],
                "purpose": "sync",
                "syncRevision": 3,
            },
        }
    )
    register(session=session, input=inputs, fields={"region": "Region"})
    handle = next(
        callback for callback in effects if callback.__name__ == "_handle_request"
    )

    await handle()

    assert session.bookmark.exclude == ["author_excluded", "region"]
    assert session.bookmark.last_save_state.exclude == [
        DISCOVER_INPUT_ID,
        REQUEST_INPUT_ID,
        SYNC_ACK_INPUT_ID,
        "author_excluded",
        "period",
    ]
    message_type, payload = session.messages[-1]
    assert message_type == "shinyhub-bookmark-result"
    assert payload["purpose"] == "sync"
    assert payload["syncRevision"] == 3


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("purpose", "expected_code"),
    [(None, "request_timeout"), ("sync", "sync_timeout")],
)
async def test_request_timeouts_release_the_lock_and_report_the_right_kind(
    monkeypatch, purpose, expected_code
):
    from shiny import reactive
    from shinyhub_bookmarks import _adapter

    effects = []

    def capture_effect(fn=None, **_kwargs):
        if fn is None:
            return capture_effect
        effects.append(fn)
        return fn

    monkeypatch.setattr(reactive, "effect", capture_effect)
    monkeypatch.setattr(
        reactive, "event", lambda *_args, **_kwargs: lambda callback: callback
    )
    monkeypatch.setattr(_adapter, "AUTO_SYNC_TIMEOUT_SECONDS", 0.001)
    monkeypatch.setattr(_adapter, "MANUAL_REQUEST_TIMEOUT_SECONDS", 0.001)

    session = Session()
    request = {
        "version": 1,
        "requestId": "first",
        "include": ["region"],
    }
    if purpose:
        request.update({"purpose": purpose, "syncRevision": 1})
    inputs = Inputs({"region": "Europe", REQUEST_INPUT_ID: request})
    register(session=session, input=inputs, fields={"region": "Region"})
    handle = next(
        callback for callback in effects if callback.__name__ == "_handle_request"
    )

    async def never_finishes():
        await asyncio.sleep(60)

    session.bookmark.get_bookmark_url = never_finishes
    await handle()
    assert session.messages[-1][0] == "shinyhub-bookmark-error"
    assert session.messages[-1][1]["code"] == expected_code
    assert session.messages[-1][1].get("purpose") == purpose

    async def succeeds():
        return session.bookmark.url

    session.bookmark.get_bookmark_url = succeeds
    inputs.values[REQUEST_INPUT_ID] = {
        "version": 1,
        "requestId": "second",
        "include": ["region"],
    }
    await handle()
    assert session.messages[-1][0] == "shinyhub-bookmark-result"
    assert session.messages[-1][1]["requestId"] == "second"


@pytest.mark.asyncio
async def test_overlapping_native_bookmark_does_not_inherit_adapter_selection(
    monkeypatch
):
    from shiny import reactive

    effects = []

    def capture_effect(fn=None, **_kwargs):
        if fn is None:
            return capture_effect
        effects.append(fn)
        return fn

    monkeypatch.setattr(reactive, "effect", capture_effect)
    monkeypatch.setattr(
        reactive, "event", lambda *_args, **_kwargs: lambda callback: callback
    )

    session = Session()
    inputs = Inputs(
        {
            "region": "Europe",
            REQUEST_INPUT_ID: {
                "version": 1,
                "requestId": "adapter",
                "include": ["region"],
            },
        }
    )
    register(session=session, input=inputs, fields={"region": "Region"})
    handle = next(
        callback for callback in effects if callback.__name__ == "_handle_request"
    )
    started = asyncio.Event()
    release = asyncio.Event()

    async def paused_url():
        started.set()
        await release.wait()
        state = type("SaveState", (), {"values": {}, "exclude": []})()
        await session.bookmark.bookmark_callbacks[0](state)
        session.bookmark.last_save_state = state
        return session.bookmark.url

    session.bookmark.get_bookmark_url = paused_url
    request_task = asyncio.create_task(handle())
    await started.wait()

    native_state = type(
        "NativeState", (), {"values": {}, "exclude": ["app-owned"]}
    )()
    await session.bookmark.bookmark_callbacks[0](native_state)
    assert native_state.exclude == ["app-owned"]

    release.set()
    await request_task
    assert session.bookmark.last_save_state.exclude == [
        DISCOVER_INPUT_ID,
        REQUEST_INPUT_ID,
        SYNC_ACK_INPUT_ID,
        "period",
    ]

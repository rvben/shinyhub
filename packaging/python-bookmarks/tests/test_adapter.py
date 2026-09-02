from __future__ import annotations

import asyncio
from dataclasses import dataclass

import pytest

from shinyhub_bookmarks import ChoiceRestore, register
from shinyhub_bookmarks._adapter import (
    DISCOVER_INPUT_ID,
    REQUEST_INPUT_ID,
    SYNC_ACK_INPUT_ID,
    Field,
    _create_url,
    _display_value,
    _normalise_fields,
    _selection_exclusions,
    _selection_from_request,
)
from shinyhub_bookmarks._restore import MISSING


class Inputs:
    def __dir__(self):
        return sorted(
            {
                "region",
                "period",
                "clientdata_url_hostname",
                ".private",
                *self.values,
            }
        )

    def __init__(self, values=None):
        self.values = values or {}

    def __getitem__(self, name):
        return lambda: self.values.get(name)


class DynamicInputs(Inputs):
    def __init__(self, values=None, unavailable=None):
        super().__init__(values)
        self.unavailable = set(unavailable or ())

    def __getitem__(self, name):
        from shiny.types import SilentException

        def read():
            if name in self.unavailable:
                raise SilentException
            return self.values.get(name)

        return read


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
        self.flushed_callbacks = []

    def ns(self, value):
        return value

    async def send_custom_message(self, message_type, message):
        self.messages.append((message_type, message))

    def on_flushed(self, callback, once=True):
        assert once is True
        self.flushed_callbacks.append(callback)
        return lambda: self.flushed_callbacks.remove(callback)


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
    assert field.baseline is MISSING


def test_normalise_fields_keeps_baseline_separate_from_restore_fallback():
    fields = _normalise_fields(
        {
            "year": Field("Year", baseline=2026),
            "region": Field(
                "Region",
                restore=ChoiceRestore(choices=["Europe", "Asia"], default="Europe"),
                baseline="Asia",
            ),
            "segments": Field(
                "Segments",
                restore=ChoiceRestore(
                    choices=["Alpha", "Beta"],
                    default=["Beta", "Alpha"],
                ),
            ),
        },
        resolve=lambda value: value,
    )

    assert fields[0].baseline == 2026
    assert fields[1].baseline == "Asia"
    assert fields[2].baseline is MISSING


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

    assert _selection_from_request(
        {"version": 1, "requestId": "r1", "include": []},
        {"region"},
        allow_empty=True,
    ) == ("r1", ())


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
async def test_register_keeps_links_metadata_free_and_reports_a_restored_migration(
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

    save_state = type("SaveState", (), {"values": {}, "exclude": []})()
    await session.bookmark.bookmark_callbacks[0](save_state)
    assert save_state.values == {}

    restore_state = type(
        "RestoreState",
        (),
        {
            "input": {"product": "Legacy planning"},
            "values": {},
        },
    )()
    await session.bookmark.restore_callbacks[0](restore_state)

    assert updates == [("product", "Planning", session)]
    assert registration.adjustments[0].kind == "migrated"
    message_type, payload = session.messages[-1]
    assert message_type == "shinyhub-bookmark-capabilities"
    assert "schemaVersion" not in payload
    assert "restoredSchemaVersion" not in payload
    assert payload["fields"][0]["value"] == "Planning"
    assert payload["adjustments"][0]["previous"] == "Legacy planning"


@pytest.mark.asyncio
async def test_restore_skips_uninitialized_input_and_restores_other_fields(
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
    inputs = DynamicInputs(
        {"product": "All products"},
        unavailable={"empty_choices_filter"},
    )
    registration = register(
        session=session,
        input=inputs,
        fields={
            "product": Field(
                "Product",
                restore=ChoiceRestore(
                    choices=["All products", "Planning"],
                    aliases={"Legacy planning": "Planning"},
                ),
            ),
            "empty_choices_filter": Field(
                "Empty choices filter",
                restore=ChoiceRestore(choices=[], control="selectize"),
            ),
        },
    )
    restore_state = type(
        "RestoreState",
        (),
        {
            "input": {
                "product": "Legacy planning",
                "empty_choices_filter": [],
            },
            "values": {},
        },
    )()

    await session.bookmark.restore_callbacks[0](restore_state)

    assert updates == [("product", "Planning", session)]
    assert [adjustment.label for adjustment in registration.adjustments] == ["Product"]
    assert [field["id"] for field in session.messages[-1][1]["fields"]] == ["product"]


def test_register_rejects_the_removed_schema_version_argument(inert_reactivity):
    session = Session()

    with pytest.raises(TypeError, match="unexpected keyword argument 'schema_version'"):
        register(
            session=session,
            input=Inputs({"region": "Europe"}),
            fields={"region": "Region"},
            schema_version=2,  # type: ignore[call-arg]
        )


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
        callback for callback in effects if callback.__name__ == "_publish_capabilities"
    )

    await publish()
    assert session.messages[-1][1]["autoSync"] is False
    assert session.messages[-1][1]["syncRevision"] == 0
    assert session.messages[-1][1]["syncFields"] == []

    clean_restore = type("RestoreState", (), {"input": {}, "values": {}})()
    await session.bookmark.restore_callbacks[0](clean_restore)

    inputs.values["region"] = "Americas"
    await publish()
    assert session.messages[-1][1]["autoSync"] is True
    assert session.messages[-1][1]["syncRevision"] == 1
    assert session.messages[-1][1]["fields"][0]["value"] == "Americas"
    assert session.messages[-1][1]["syncFields"] == ["region"]

    inputs.values["region"] = "Europe"
    await publish()
    assert session.messages[-1][1]["syncRevision"] == 2
    assert session.messages[-1][1]["syncFields"] == []

    republish = next(
        callback
        for callback in effects
        if callback.__name__ == "_republish_capabilities"
    )
    await republish()
    assert session.messages[-1][1]["autoSync"] is True
    assert session.messages[-1][1]["syncRevision"] == 2

    acknowledge = next(
        callback for callback in effects if callback.__name__ == "_acknowledge_url_sync"
    )
    inputs.values[SYNC_ACK_INPUT_ID] = {"version": 1, "syncRevision": 2}
    await acknowledge()
    await republish()
    assert session.messages[-1][1]["autoSync"] is False


@pytest.mark.asyncio
async def test_falsy_values_different_from_baselines_remain_in_live_url(
    monkeypatch,
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
    register(
        session=session,
        input=Inputs({"forecast": False, "segments": []}),
        fields={
            "forecast": Field("Forecast", baseline=True),
            "segments": Field("Segments", baseline=["All"]),
        },
    )
    publish = next(
        callback for callback in effects if callback.__name__ == "_publish_capabilities"
    )

    await publish()

    assert session.messages[-1][1]["syncFields"] == ["forecast", "segments"]


def test_uncopyable_explicit_baseline_fails_before_registering_callbacks(
    inert_reactivity,
):
    class Uncopyable:
        def __deepcopy__(self, memo):
            raise RuntimeError("cannot copy")

    session = Session()

    with pytest.raises(TypeError, match="safely copyable"):
        register(
            session=session,
            input=Inputs({"custom": Uncopyable()}),
            fields={"custom": Field("Custom", baseline=Uncopyable())},
        )

    assert session.bookmark.bookmark_callbacks == []
    assert session.bookmark.restore_callbacks == []


def test_normalizer_can_make_an_explicit_baseline_safely_comparable(
    inert_reactivity,
):
    class Uncopyable:
        def __deepcopy__(self, memo):
            raise RuntimeError("cannot copy")

    session = Session()
    registration = register(
        session=session,
        input=Inputs({"custom": Uncopyable()}),
        fields={
            "custom": Field(
                "Custom",
                normalizer=lambda _value: "stable",
                baseline=Uncopyable(),
            )
        },
    )

    assert registration.fields[0].input_id == "custom"


@pytest.mark.asyncio
async def test_uncopyable_inferred_baseline_is_retained_and_warned(
    inert_reactivity, caplog
):
    class Uncopyable:
        def __deepcopy__(self, memo):
            raise RuntimeError("cannot copy")

    session = Session()
    register(
        session=session,
        input=Inputs({"custom": Uncopyable()}),
        fields={"custom": "Custom"},
    )
    clean_restore = type("RestoreState", (), {"input": {}, "values": {}})()

    with caplog.at_level("WARNING", logger="shinyhub_bookmarks._adapter"):
        await session.bookmark.restore_callbacks[0](clean_restore)

    assert session.messages[-1][1]["syncFields"] == ["custom"]
    assert "custom" in caplog.text
    assert "cannot copy" not in caplog.text


@pytest.mark.asyncio
async def test_mismatched_declared_baseline_keeps_current_value_in_live_url(
    monkeypatch, caplog
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
    inputs = Inputs({"region": "Asia"})
    register(
        session=session,
        input=inputs,
        fields={"region": Field("Region", baseline="Europe")},
    )
    restore_state = type("RestoreState", (), {"input": {}, "values": {}})()

    with caplog.at_level("WARNING", logger="shinyhub_bookmarks._adapter"):
        await session.bookmark.restore_callbacks[0](restore_state)

    assert session.messages[-1][1]["syncFields"] == ["region"]
    assert "region" in caplog.text
    assert "Asia" not in caplog.text
    assert "Europe" not in caplog.text

    inputs.values["region"] = "Europe"
    publish = next(
        callback for callback in effects if callback.__name__ == "_publish_capabilities"
    )
    await publish()
    assert session.messages[-1][1]["syncFields"] == []


@pytest.mark.asyncio
async def test_declared_baseline_is_not_validated_against_restored_value(
    inert_reactivity, caplog
):
    session = Session()
    register(
        session=session,
        input=Inputs({"region": "Asia"}),
        fields={"region": Field("Region", baseline="Europe")},
    )
    restore_state = type(
        "RestoreState",
        (),
        {"input": {"region": "Asia"}, "values": {}},
    )()

    with caplog.at_level("WARNING", logger="shinyhub_bookmarks._adapter"):
        await session.bookmark.restore_callbacks[0](restore_state)

    assert session.messages[-1][1]["syncFields"] == ["region"]
    assert "baseline does not match" not in caplog.text


@pytest.mark.asyncio
async def test_dynamic_input_waits_until_it_materializes_before_learning_baseline(
    monkeypatch,
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
    inputs = DynamicInputs(
        {"region": "Europe", "segment": "All"},
        unavailable={"segment"},
    )
    register(
        session=session,
        input=inputs,
        fields={"region": "Region", "segment": "Segment"},
    )
    publish = next(
        callback for callback in effects if callback.__name__ == "_publish_capabilities"
    )

    await publish()
    assert [field["id"] for field in session.messages[-1][1]["fields"]] == ["region"]

    clean_restore = type("RestoreState", (), {"input": {}, "values": {}})()
    await session.bookmark.restore_callbacks[0](clean_restore)

    inputs.unavailable.remove("segment")
    await publish()
    assert session.messages[-1][1]["syncFields"] == []

    inputs.values["segment"] = "Enterprise"
    await publish()
    assert session.messages[-1][1]["syncFields"] == ["segment"]


@pytest.mark.asyncio
async def test_first_flush_finalizes_baselines_when_restore_callback_is_suppressed(
    monkeypatch,
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
    inputs = Inputs({"region": "Europe"})
    register(session=session, input=inputs, fields={"region": "Region"})
    publish = next(
        callback for callback in effects if callback.__name__ == "_publish_capabilities"
    )

    await publish()
    await session.flushed_callbacks[0]()
    inputs.values["region"] = "Asia"
    await publish()

    assert session.messages[-1][1]["syncFields"] == ["region"]


@pytest.mark.asyncio
async def test_dynamic_restores_are_validated_when_inputs_materialize(
    monkeypatch,
):
    from shiny import reactive, ui

    effects = []
    updates = []

    def capture_effect(fn=None, **_kwargs):
        if fn is None:
            return capture_effect
        effects.append(fn)
        return fn

    monkeypatch.setattr(reactive, "effect", capture_effect)
    monkeypatch.setattr(
        reactive, "event", lambda *_args, **_kwargs: lambda callback: callback
    )
    monkeypatch.setattr(
        ui,
        "update_select",
        lambda input_id, *, selected, session: updates.append(
            (str(input_id), selected, session)
        ),
    )

    session = Session()
    inputs = DynamicInputs(
        {"segment": "A", "product": "All products"},
        unavailable={"segment", "product"},
    )
    registration = register(
        session=session,
        input=inputs,
        fields={
            "segment": Field(
                "Segment",
                restore=ChoiceRestore(choices=["A", "B"], default="A"),
                baseline="A",
            ),
            "product": Field(
                "Product",
                restore=ChoiceRestore(
                    choices=["All products", "Planning"],
                    aliases={"Legacy planning": "Planning"},
                ),
                renamed_from={"old_product": "Old product"},
                baseline="All products",
            ),
        },
    )
    restore_state = type(
        "RestoreState",
        (),
        {
            "input": {
                "segment": "Retired",
                "old_product": "Legacy planning",
            },
            "values": {},
        },
    )()
    await session.bookmark.restore_callbacks[0](restore_state)
    assert registration.adjustments == ()

    inputs.unavailable.clear()
    publish = next(
        callback for callback in effects if callback.__name__ == "_publish_capabilities"
    )
    await publish()

    assert updates == [("product", "Planning", session)]
    assert [adjustment.kind for adjustment in registration.adjustments] == [
        "fallback",
        "migrated",
    ]
    assert registration.adjustments[1].source_label == "Old product"
    assert session.messages[-1][1]["syncFields"] == ["product"]
    assert [field["value"] for field in session.messages[-1][1]["fields"]] == [
        "A",
        "Planning",
    ]


@pytest.mark.asyncio
async def test_inferred_mutable_baseline_is_an_independent_snapshot(monkeypatch):
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
    inputs = Inputs({"segments": ["All"]})
    register(session=session, input=inputs, fields={"segments": "Segments"})
    clean_restore = type("RestoreState", (), {"input": {}, "values": {}})()
    await session.bookmark.restore_callbacks[0](clean_restore)

    inputs.values["segments"].append("Enterprise")
    publish = next(
        callback for callback in effects if callback.__name__ == "_publish_capabilities"
    )
    await publish()

    assert session.messages[-1][1]["syncFields"] == ["segments"]


@pytest.mark.asyncio
async def test_restored_field_without_declared_baseline_is_preserved(
    inert_reactivity,
):
    session = Session()
    register(
        session=session,
        input=Inputs({"region": "Americas", "year": 2026}),
        fields={"region": "Region", "year": "Year"},
    )
    restore_state = type(
        "RestoreState",
        (),
        {"input": {"region": "Americas"}, "values": {}},
    )()

    await session.bookmark.restore_callbacks[0](restore_state)

    assert session.messages[-1][1]["syncFields"] == ["region"]


@pytest.mark.asyncio
async def test_initial_effect_does_not_learn_a_value_that_was_restored(
    monkeypatch,
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
    inputs = Inputs({"region": "Asia"})
    register(session=session, input=inputs, fields={"region": "Region"})
    publish = next(
        callback for callback in effects if callback.__name__ == "_publish_capabilities"
    )

    await publish()
    restore_state = type(
        "RestoreState",
        (),
        {"input": {"region": "Asia"}, "values": {}},
    )()
    await session.bookmark.restore_callbacks[0](restore_state)

    assert session.messages[-1][1]["syncFields"] == ["region"]


@pytest.mark.asyncio
async def test_shiny_serializes_empty_and_app_owned_bookmark_state_distinctly():
    from shiny.bookmark import BookmarkState

    class SerializableInputs:
        async def _serialize(self, *, exclude, state_dir):
            assert exclude == []
            assert state_dir is None
            return {}

    async def add_app_state(state):
        state.values["theme"] = "dark"

    empty = BookmarkState(SerializableInputs(), [], None)
    app_owned = BookmarkState(SerializableInputs(), [], add_app_state)

    assert await empty._encode_state() == ""
    assert await app_owned._encode_state() == "_values_&theme=%22dark%22"


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
async def test_automatic_sync_can_exclude_every_field_at_baseline(monkeypatch):
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
                "requestId": "url-sync-baseline",
                "include": [],
                "purpose": "sync",
                "syncRevision": 2,
            },
        }
    )
    register(session=session, input=inputs, fields={"region": "Region"})
    handle = next(
        callback for callback in effects if callback.__name__ == "_handle_request"
    )

    await handle()

    assert session.bookmark.last_save_state.exclude == [
        DISCOVER_INPUT_ID,
        REQUEST_INPUT_ID,
        SYNC_ACK_INPUT_ID,
        "period",
        "region",
    ]
    message_type, payload = session.messages[-1]
    assert message_type == "shinyhub-bookmark-result"
    assert payload["purpose"] == "sync"
    assert payload["syncRevision"] == 2


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
    monkeypatch,
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

    native_state = type("NativeState", (), {"values": {}, "exclude": ["app-owned"]})()
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

from __future__ import annotations

import asyncio
import logging
from collections.abc import Callable, Iterable, Mapping, Sequence
from contextvars import ContextVar
from dataclasses import dataclass
from typing import Any, Protocol

from ._restore import Adjustment, ChoiceRestore, resolve_choice, values_equal

PROTOCOL_VERSION = 1
REQUEST_INPUT_ID = ".shinyhub_bookmark_request"
DISCOVER_INPUT_ID = ".shinyhub_bookmark_discover"
SYNC_ACK_INPUT_ID = ".shinyhub_bookmark_sync_ack"
CAPABILITIES_MESSAGE = "shinyhub-bookmark-capabilities"
RESULT_MESSAGE = "shinyhub-bookmark-result"
ERROR_MESSAGE = "shinyhub-bookmark-error"
DEFAULT_MAX_URL_LENGTH = 8_192
BOOKMARK_METADATA_KEY = ".shinyhub_bookmarks"
BOOKMARK_METADATA_VERSION = 1
MAX_ADJUSTMENTS = 20
MAX_UNKNOWN_DETAILS = 3
MAX_UNKNOWN_LABEL_LENGTH = 120
MAX_UNKNOWN_VALUE_LENGTH = 240
REGISTRATION_MARKER = "_shinyhub_bookmarks_registration"
AUTO_SYNC_TIMEOUT_SECONDS = 2.0
MANUAL_REQUEST_TIMEOUT_SECONDS = 6.0

logger = logging.getLogger(__name__)

Formatter = Callable[[Any], str]
Normalizer = Callable[[Any], Any]


@dataclass(frozen=True, slots=True)
class Field:
    """A Shiny input that visitors may include in a bookmark."""

    label: str
    formatter: Formatter | None = None
    restore: ChoiceRestore | None = None
    renamed_from: Mapping[str, str] | None = None
    normalizer: Normalizer | None = None

    def __post_init__(self) -> None:
        if not isinstance(self.label, str) or not self.label.strip():
            raise ValueError("Field labels must be non-empty strings")
        if self.normalizer is not None and not callable(self.normalizer):
            raise TypeError("Field normalizer must be callable")
        if self.restore is not None and not isinstance(self.restore, ChoiceRestore):
            raise TypeError("Field restore must be a ChoiceRestore")
        if self.renamed_from is not None:
            if not isinstance(self.renamed_from, Mapping):
                raise TypeError("Field renamed_from must be a mapping")
            for input_id, label in self.renamed_from.items():
                if (
                    not isinstance(input_id, str)
                    or not input_id
                    or input_id.startswith(".")
                ):
                    raise ValueError(
                        "Renamed bookmark field IDs must be public input IDs"
                    )
                if not isinstance(label, str) or not label.strip():
                    raise ValueError(
                        "Renamed bookmark field labels must be non-empty strings"
                    )
            if self.renamed_from and self.restore is None:
                raise ValueError("Field renamed_from requires a ChoiceRestore policy")


@dataclass(frozen=True, slots=True)
class _RegisteredField:
    input_id: str
    resolved_id: str
    label: str
    formatter: Formatter | None
    normalizer: Normalizer | None
    restore: ChoiceRestore | None
    renamed_from: Mapping[str, str]


class _Bookmark(Protocol):
    store: str
    exclude: list[str]

    async def get_bookmark_url(self) -> str | None: ...

    def on_bookmark(self, callback: Callable[[Any], Any]) -> Any: ...

    def on_restore(self, callback: Callable[[Any], Any]) -> Any: ...


@dataclass(slots=True)
class Registration:
    """A live adapter registration, returned mainly for tests and diagnostics."""

    fields: tuple[_RegisteredField, ...]
    max_url_length: int
    schema_version: int
    adjustments: tuple[Adjustment, ...] = ()
    restored_schema_version: int | None = None


def _normalise_fields(
    fields: Mapping[str, Field | str] | Iterable[tuple[str, Field | str]],
    *,
    resolve: Callable[[str], str],
) -> tuple[_RegisteredField, ...]:
    items = fields.items() if isinstance(fields, Mapping) else fields
    result: list[_RegisteredField] = []
    seen: set[str] = set()
    for raw_id, raw_field in items:
        input_id = str(raw_id).strip()
        if not input_id or input_id.startswith("."):
            raise ValueError("Bookmark field IDs must be non-empty public input IDs")
        field = Field(raw_field) if isinstance(raw_field, str) else raw_field
        if not isinstance(field, Field):
            raise TypeError(
                f"Bookmark field {input_id!r} must be a Field or label string"
            )
        resolved_id = str(resolve(input_id))
        if resolved_id in seen:
            raise ValueError(
                f"Bookmark field {input_id!r} resolves to a duplicate input ID"
            )
        seen.add(resolved_id)
        result.append(
            _RegisteredField(
                input_id=input_id,
                resolved_id=resolved_id,
                label=field.label.strip(),
                formatter=field.formatter,
                normalizer=field.normalizer,
                restore=field.restore,
                renamed_from=dict(field.renamed_from or {}),
            )
        )
    if not result:
        raise ValueError("Register at least one bookmark field")
    current_ids = {field.input_id for field in result}
    old_ids: set[str] = set()
    for field in result:
        for old_id in field.renamed_from:
            if old_id in current_ids or old_id in old_ids:
                raise ValueError(f"Renamed bookmark field {old_id!r} is ambiguous")
            old_ids.add(old_id)
    return tuple(result)


def _display_value(value: Any, formatter: Formatter | None) -> str:
    if formatter is not None:
        return str(formatter(value))
    if value is None:
        return "Not set"
    if isinstance(value, bool):
        return "On" if value else "Off"
    if isinstance(value, (list, tuple, set, frozenset)):
        values = [str(item) for item in value]
        if not values:
            return "None"
        return ", ".join(values)
    if isinstance(value, Mapping):
        return f"{len(value)} selected"
    text = str(value).strip()
    return text if text else "Not set"


def _safe_display_value(value: Any, formatter: Formatter | None) -> str:
    try:
        return _display_value(value, formatter)
    except Exception:
        return _display_value(value, None)


def _field_values_equal(field: _RegisteredField, left: Any, right: Any) -> bool:
    if field.normalizer is not None:
        try:
            left = field.normalizer(left)
            right = field.normalizer(right)
        except Exception:
            return False
    return values_equal(left, right)


def _bounded_untrusted_display(value: Any, limit: int) -> str:
    text = _safe_display_value(value, None)
    printable = "".join(
        character if character.isprintable() else " " for character in text
    )
    cleaned = " ".join(printable.split()) or "Not set"
    return cleaned if len(cleaned) <= limit else cleaned[: limit - 1] + "…"


def _restored_value(
    state_input: Mapping[str, Any], field: _RegisteredField
) -> tuple[str, str, Any] | None:
    if field.input_id in state_input:
        return field.input_id, field.label, state_input[field.input_id]
    for old_id, old_label in field.renamed_from.items():
        if old_id in state_input:
            return old_id, old_label, state_input[old_id]
    return None


def _restore_adjustments(
    *,
    state_input: Mapping[str, Any],
    registered: Sequence[_RegisteredField],
    current_values: Mapping[str, Any],
    legacy_fields: Mapping[str, str],
) -> tuple[tuple[Adjustment, ...], dict[str, Any]]:
    adjustments: list[Adjustment] = []
    updates: dict[str, Any] = {}
    claimed = {
        input_id
        for field in registered
        for input_id in (field.input_id, field.resolved_id)
    }
    for field in registered:
        claimed.update(field.renamed_from)
        restored = _restored_value(state_input, field)
        if restored is None:
            continue
        source_id, source_label, saved = restored
        current = current_values[field.input_id]
        target = current
        kind: str | None = None
        if field.restore is not None:
            resolution = resolve_choice(
                field.restore,
                saved,
                current,
                equal=lambda left, right: _field_values_equal(
                    field, left, right
                ),
            )
            target = resolution.value
            kind = resolution.kind
        elif not _field_values_equal(field, saved, current):
            kind = "fallback"

        renamed = source_id != field.input_id
        if (
            not _field_values_equal(field, target, current)
            and field.restore is not None
        ):
            updates[field.input_id] = target
        if kind or renamed:
            adjustments.append(
                Adjustment(
                    kind=kind or "renamed",  # type: ignore[arg-type]
                    label=field.label,
                    previous=_safe_display_value(saved, field.formatter),
                    current=_safe_display_value(target, field.formatter),
                    source_label=source_label if renamed else "",
                )
            )

    for old_id, old_label in legacy_fields.items():
        was_claimed = old_id in claimed
        claimed.add(old_id)
        if was_claimed or old_id not in state_input:
            continue
        adjustments.append(
            Adjustment(
                kind="removed",
                label=old_label,
                previous=_safe_display_value(state_input[old_id], None),
                current="Ignored",
            )
        )

    unknown_inputs = [
        (input_id, state_input[input_id])
        for input_id in state_input
        if isinstance(input_id, str)
        and input_id
        and not input_id.startswith(".")
        and not input_id.startswith("clientdata_")
        and input_id not in claimed
    ]
    if unknown_inputs:
        visible_unknowns = unknown_inputs[:MAX_UNKNOWN_DETAILS]
        hidden_unknowns = len(unknown_inputs) - len(visible_unknowns)
        reserved = len(visible_unknowns) + (1 if hidden_unknowns else 0)
        adjustments = adjustments[: MAX_ADJUSTMENTS - reserved]
        for input_id, saved in visible_unknowns:
            adjustments.append(
                Adjustment(
                    kind="unknown",
                    label=_bounded_untrusted_display(
                        input_id, MAX_UNKNOWN_LABEL_LENGTH
                    ),
                    previous=_bounded_untrusted_display(
                        saved, MAX_UNKNOWN_VALUE_LENGTH
                    ),
                    current="Ignored",
                )
            )
        if hidden_unknowns:
            noun = "setting" if hidden_unknowns == 1 else "settings"
            adjustments.append(
                Adjustment(
                    kind="unknown_summary",
                    label=f"{hidden_unknowns} more unrecognized saved {noun}",
                    current="Ignored for safety",
                )
            )

    return tuple(adjustments[:MAX_ADJUSTMENTS]), updates


def _apply_choice_update(session: Any, field: _RegisteredField, value: Any) -> None:
    if field.restore is None:
        return
    from shiny import ui
    from shiny.module import ResolvedId

    input_id = ResolvedId(field.resolved_id)

    if field.restore.control == "selectize":
        ui.update_selectize(input_id, selected=value, session=session)
    elif field.restore.control == "radio":
        ui.update_radio_buttons(input_id, selected=value, session=session)
    else:
        ui.update_select(input_id, selected=value, session=session)


def _known_input_ids(inputs: Any) -> set[str]:
    """Return Shiny's current input IDs through its public ``dir(input)`` view."""

    return {
        name
        for name in dir(inputs)
        if name and not name.startswith(".") and not name.startswith("clientdata_")
    }


def _selection_from_request(
    request: object, registered_ids: set[str]
) -> tuple[str, tuple[str, ...]]:
    if not isinstance(request, Mapping):
        raise ValueError("The bookmark request is not an object")
    request_id = request.get("requestId")
    include = request.get("include")
    version = request.get("version")
    if not isinstance(request_id, str) or not request_id or len(request_id) > 128:
        raise ValueError("The bookmark request ID is invalid")
    if version != PROTOCOL_VERSION:
        raise ValueError("The bookmark protocol version is unsupported")
    if not isinstance(include, Sequence) or isinstance(include, (str, bytes)):
        raise ValueError("The bookmark selection is invalid")
    selected: list[str] = []
    seen: set[str] = set()
    for raw_id in include:
        if (
            not isinstance(raw_id, str)
            or raw_id not in registered_ids
            or raw_id in seen
        ):
            raise ValueError("The bookmark selection contains an unknown field")
        selected.append(raw_id)
        seen.add(raw_id)
    if not selected:
        raise ValueError("Select at least one field")
    return request_id, tuple(selected)


async def _create_url(
    *,
    bookmark: _Bookmark,
    max_url_length: int,
) -> str:
    url = await bookmark.get_bookmark_url()
    if not isinstance(url, str) or not url:
        raise RuntimeError("Shiny did not return a bookmark URL")
    if len(url) > max_url_length:
        raise OverflowError("The bookmark URL exceeds the configured limit")
    return url


def _selection_exclusions(
    original: Sequence[str], known_inputs: set[str], selected: Sequence[str]
) -> list[str]:
    selected_ids = set(selected)
    return sorted(
        set(original)
        .difference(selected_ids)
        .union(
            known_inputs.difference(selected_ids),
            {REQUEST_INPUT_ID, DISCOVER_INPUT_ID, SYNC_ACK_INPUT_ID},
        )
    )


def register(
    *,
    session: Any,
    input: Any,
    fields: Mapping[str, Field | str] | Iterable[tuple[str, Field | str]],
    max_url_length: int = DEFAULT_MAX_URL_LENGTH,
    schema_version: int = 1,
    legacy_fields: Mapping[str, str] | None = None,
) -> Registration:
    """Expose selected Shiny inputs to ShinyHub's durable view-link controls.

    The app must use ``App(..., bookmark_store="url")`` and include
    :func:`bookmarking_dependency` in its UI. The browser-local ShinyHub
    switcher receives registered display values and generated URLs. Registered
    input changes keep the current address synchronized, while the ShinyHub
    server neither receives nor persists bookmark state.
    """

    bookmark = session.bookmark
    if getattr(bookmark, "store", None) != "url":
        raise ValueError('ShinyHub bookmarks require App(..., bookmark_store="url")')
    if getattr(bookmark, REGISTRATION_MARKER, None) is not None:
        raise RuntimeError("Register ShinyHub bookmarks only once per session")
    resolve = getattr(session, "ns", lambda value: value)
    namespace_probe = "__shinyhub_namespace_probe"
    if str(resolve(namespace_probe)) != namespace_probe:
        raise ValueError(
            "Register ShinyHub bookmarks from the top-level server session; "
            "register module inputs there by their resolved IDs"
        )
    if not isinstance(max_url_length, int) or max_url_length < 1_024:
        raise ValueError("max_url_length must be an integer of at least 1024")
    if (
        not isinstance(schema_version, int)
        or isinstance(schema_version, bool)
        or schema_version < 1
    ):
        raise ValueError("schema_version must be a positive integer")
    if legacy_fields is None:
        legacy_fields = {}
    if not isinstance(legacy_fields, Mapping):
        raise TypeError("legacy_fields must be a mapping")
    for old_id, old_label in legacy_fields.items():
        if not isinstance(old_id, str) or not old_id or old_id.startswith("."):
            raise ValueError("Legacy bookmark field IDs must be public input IDs")
        if not isinstance(old_label, str) or not old_label.strip():
            raise ValueError("Legacy bookmark field labels must be non-empty strings")

    registered = _normalise_fields(fields, resolve=lambda value: value)
    registration = Registration(registered, max_url_length, schema_version)
    setattr(bookmark, REGISTRATION_MARKER, registration)
    resolved_ids = {field.resolved_id for field in registered}
    lock = asyncio.Lock()
    restored_overrides: dict[str, Any] = {}
    selected_fields: ContextVar[tuple[str, ...] | None] = ContextVar(
        "shinyhub_bookmark_selected_fields", default=None
    )
    capability_revision = 0
    acknowledged_revision = 0
    initial_capabilities_observed = False

    from shiny import reactive
    from shiny.module import ResolvedId

    async def publish_capabilities() -> None:
        values = []
        for field in registered:
            current = input[ResolvedId(field.resolved_id)]()
            if field.input_id in restored_overrides:
                restored = restored_overrides[field.input_id]
                if _field_values_equal(field, current, restored):
                    restored_overrides.pop(field.input_id, None)
                else:
                    current = restored
            values.append(
                {
                    "id": field.resolved_id,
                    "label": field.label,
                    "value": _safe_display_value(current, field.formatter),
                }
            )
        await session.send_custom_message(
            CAPABILITIES_MESSAGE,
            {
                "version": PROTOCOL_VERSION,
                "store": "url",
                "autoSync": capability_revision > acknowledged_revision,
                "syncRevision": capability_revision,
                "schemaVersion": schema_version,
                "restoredSchemaVersion": registration.restored_schema_version,
                "fields": values,
                "adjustments": [item.as_message() for item in registration.adjustments],
            },
        )

    @bookmark.on_bookmark
    async def _write_bookmark_metadata(state: Any) -> None:
        state.values[BOOKMARK_METADATA_KEY] = {
            "version": BOOKMARK_METADATA_VERSION,
            "schema": schema_version,
        }
        selected = selected_fields.get()
        if selected is not None:
            state.exclude[:] = _selection_exclusions(
                state.exclude, _known_input_ids(input), selected
            )

    @bookmark.on_restore
    async def _inspect_restored_bookmark(state: Any) -> None:
        state_input = state.input if isinstance(state.input, Mapping) else {}
        state_values = state.values if isinstance(state.values, Mapping) else {}
        metadata = state_values.get(BOOKMARK_METADATA_KEY)
        registration.restored_schema_version = None
        if (
            isinstance(metadata, Mapping)
            and metadata.get("version") == BOOKMARK_METADATA_VERSION
            and isinstance(metadata.get("schema"), int)
            and not isinstance(metadata.get("schema"), bool)
            and metadata["schema"] > 0
        ):
            registration.restored_schema_version = metadata["schema"]
        current_values = {
            field.input_id: input[ResolvedId(field.resolved_id)]()
            for field in registered
        }
        adjustments, updates = _restore_adjustments(
            state_input=state_input,
            registered=registered,
            current_values=current_values,
            legacy_fields=legacy_fields,
        )
        registration.adjustments = adjustments
        restored_overrides.update(updates)
        for field in registered:
            if field.input_id in updates:
                _apply_choice_update(session, field, updates[field.input_id])
        await publish_capabilities()

    @reactive.effect
    async def _publish_capabilities() -> None:
        nonlocal capability_revision, initial_capabilities_observed
        if initial_capabilities_observed:
            capability_revision += 1
        else:
            initial_capabilities_observed = True
        await publish_capabilities()

    @reactive.effect
    @reactive.event(input[DISCOVER_INPUT_ID], ignore_none=True)
    async def _republish_capabilities() -> None:
        await publish_capabilities()

    @reactive.effect
    @reactive.event(input[SYNC_ACK_INPUT_ID], ignore_none=True)
    async def _acknowledge_url_sync() -> None:
        nonlocal acknowledged_revision
        raw_ack = input[SYNC_ACK_INPUT_ID]()
        if not isinstance(raw_ack, Mapping) or raw_ack.get("version") != PROTOCOL_VERSION:
            return
        revision = raw_ack.get("syncRevision")
        if (
            isinstance(revision, int)
            and not isinstance(revision, bool)
            and 0 <= revision <= capability_revision
        ):
            acknowledged_revision = max(acknowledged_revision, revision)

    @reactive.effect
    @reactive.event(input[REQUEST_INPUT_ID], ignore_none=True)
    async def _handle_request() -> None:
        raw_request = input[REQUEST_INPUT_ID]()
        automatic = isinstance(raw_request, Mapping) and raw_request.get("purpose") == "sync"
        requested_revision = (
            raw_request.get("syncRevision") if isinstance(raw_request, Mapping) else None
        )
        request_id = (
            raw_request.get("requestId", "") if isinstance(raw_request, Mapping) else ""
        )
        try:
            request_id, selected = _selection_from_request(raw_request, resolved_ids)
        except ValueError as error:
            await session.send_custom_message(
                ERROR_MESSAGE,
                {
                    "version": PROTOCOL_VERSION,
                    "requestId": request_id,
                    "code": "invalid_selection",
                    "message": str(error),
                },
            )
            return

        if lock.locked():
            await session.send_custom_message(
                ERROR_MESSAGE,
                {
                    "version": PROTOCOL_VERSION,
                    "requestId": request_id,
                    "code": "busy",
                    "message": "Another link is still being created.",
                },
            )
            return

        try:
            async with lock:
                token = selected_fields.set(tuple(selected))
                try:
                    create = _create_url(
                        bookmark=bookmark,
                        max_url_length=max_url_length,
                    )
                    url = await asyncio.wait_for(
                        create,
                        AUTO_SYNC_TIMEOUT_SECONDS
                        if automatic
                        else MANUAL_REQUEST_TIMEOUT_SECONDS,
                    )
                finally:
                    selected_fields.reset(token)
            result = {"version": PROTOCOL_VERSION, "requestId": request_id, "url": url}
            if automatic:
                result["purpose"] = "sync"
            if automatic and isinstance(requested_revision, int):
                result["syncRevision"] = requested_revision
            await session.send_custom_message(
                RESULT_MESSAGE,
                result,
            )
        except asyncio.TimeoutError:
            await session.send_custom_message(
                ERROR_MESSAGE,
                {
                    "version": PROTOCOL_VERSION,
                    "requestId": request_id,
                    "code": "sync_timeout" if automatic else "request_timeout",
                    "message": (
                        "The current view took too long to save in the URL."
                        if automatic
                        else "The app took too long to create this link. Try again."
                    ),
                    **({"purpose": "sync"} if automatic else {}),
                    **(
                        {"syncRevision": requested_revision}
                        if automatic and isinstance(requested_revision, int)
                        else {}
                    ),
                },
            )
        except OverflowError:
            await session.send_custom_message(
                ERROR_MESSAGE,
                {
                    "version": PROTOCOL_VERSION,
                    "requestId": request_id,
                    "code": "url_too_long",
                    "message": "This view contains too much state for a reliable URL. Exclude a few fields and try again.",
                    **({"purpose": "sync"} if automatic else {}),
                },
            )
        except Exception:
            logger.exception("ShinyHub bookmark URL serialization failed")
            await session.send_custom_message(
                ERROR_MESSAGE,
                {
                    "version": PROTOCOL_VERSION,
                    "requestId": request_id,
                    "code": "serialization_failed",
                    "message": "Shiny could not create this link. Try again.",
                    **({"purpose": "sync"} if automatic else {}),
                },
            )

    return registration

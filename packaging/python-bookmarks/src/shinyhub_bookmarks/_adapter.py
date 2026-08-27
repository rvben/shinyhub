from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import Any, Callable, Iterable, Mapping, Protocol, Sequence

PROTOCOL_VERSION = 1
REQUEST_INPUT_ID = ".shinyhub_bookmark_request"
DISCOVER_INPUT_ID = ".shinyhub_bookmark_discover"
CAPABILITIES_MESSAGE = "shinyhub-bookmark-capabilities"
RESULT_MESSAGE = "shinyhub-bookmark-result"
ERROR_MESSAGE = "shinyhub-bookmark-error"
DEFAULT_MAX_URL_LENGTH = 8_192

Formatter = Callable[[Any], str]


@dataclass(frozen=True, slots=True)
class Field:
    """A Shiny input that visitors may include in a bookmark."""

    label: str
    formatter: Formatter | None = None

    def __post_init__(self) -> None:
        if not isinstance(self.label, str) or not self.label.strip():
            raise ValueError("Field labels must be non-empty strings")


@dataclass(frozen=True, slots=True)
class _RegisteredField:
    input_id: str
    resolved_id: str
    label: str
    formatter: Formatter | None


class _Bookmark(Protocol):
    store: str
    exclude: list[str]

    async def get_bookmark_url(self) -> str | None: ...


class _Session(Protocol):
    bookmark: _Bookmark

    async def send_custom_message(self, message_type: str, message: object) -> None: ...


@dataclass(slots=True)
class Registration:
    """A live adapter registration, returned mainly for tests and diagnostics."""

    fields: tuple[_RegisteredField, ...]
    max_url_length: int


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
            raise TypeError(f"Bookmark field {input_id!r} must be a Field or label string")
        resolved_id = str(resolve(input_id))
        if resolved_id in seen:
            raise ValueError(f"Bookmark field {input_id!r} resolves to a duplicate input ID")
        seen.add(resolved_id)
        result.append(
            _RegisteredField(
                input_id=input_id,
                resolved_id=resolved_id,
                label=field.label.strip(),
                formatter=field.formatter,
            )
        )
    if not result:
        raise ValueError("Register at least one bookmark field")
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
        if not isinstance(raw_id, str) or raw_id not in registered_ids or raw_id in seen:
            raise ValueError("The bookmark selection contains an unknown field")
        selected.append(raw_id)
        seen.add(raw_id)
    if not selected:
        raise ValueError("Select at least one field")
    return request_id, tuple(selected)


async def _create_url(
    *,
    bookmark: _Bookmark,
    inputs: Any,
    selected: Sequence[str],
    max_url_length: int,
) -> str:
    original = list(bookmark.exclude)
    selected_ids = set(selected)
    try:
        bookmark.exclude[:] = sorted(
            set(original).difference(selected_ids).union(
                _known_input_ids(inputs).difference(selected_ids),
                {REQUEST_INPUT_ID, DISCOVER_INPUT_ID},
            )
        )
        url = await bookmark.get_bookmark_url()
    finally:
        bookmark.exclude[:] = original
    if not isinstance(url, str) or not url:
        raise RuntimeError("Shiny did not return a bookmark URL")
    if len(url) > max_url_length:
        raise OverflowError("The bookmark URL exceeds the configured limit")
    return url


def register(
    *,
    session: Any,
    input: Any,
    fields: Mapping[str, Field | str] | Iterable[tuple[str, Field | str]],
    max_url_length: int = DEFAULT_MAX_URL_LENGTH,
) -> Registration:
    """Expose selected Shiny inputs to ShinyHub's bookmarking control.

    The app must use ``App(..., bookmark_store="url")`` and include
    :func:`bookmarking_dependency` in its UI. The browser-local ShinyHub
    switcher receives registered display values and the generated URL, while
    the ShinyHub server neither receives nor persists bookmark state.
    """

    if getattr(session.bookmark, "store", None) != "url":
        raise ValueError('ShinyHub bookmarks require App(..., bookmark_store="url")')
    if not isinstance(max_url_length, int) or max_url_length < 1_024:
        raise ValueError("max_url_length must be an integer of at least 1024")

    resolve = getattr(session, "ns", lambda value: value)
    registered = _normalise_fields(fields, resolve=lambda value: str(resolve(value)))
    registration = Registration(registered, max_url_length)
    resolved_ids = {field.resolved_id for field in registered}
    lock = asyncio.Lock()

    from shiny import reactive

    async def publish_capabilities() -> None:
        values = []
        for field in registered:
            current = input[field.input_id]()
            values.append(
                {
                    "id": field.resolved_id,
                    "label": field.label,
                    "value": _display_value(current, field.formatter),
                }
            )
        await session.send_custom_message(
            CAPABILITIES_MESSAGE,
            {"version": PROTOCOL_VERSION, "store": "url", "fields": values},
        )

    @reactive.effect
    async def _publish_capabilities() -> None:
        await publish_capabilities()

    @reactive.effect
    @reactive.event(input[DISCOVER_INPUT_ID], ignore_none=True)
    async def _republish_capabilities() -> None:
        await publish_capabilities()

    @reactive.effect
    @reactive.event(input[REQUEST_INPUT_ID], ignore_none=True)
    async def _handle_request() -> None:
        raw_request = input[REQUEST_INPUT_ID]()
        request_id = raw_request.get("requestId", "") if isinstance(raw_request, Mapping) else ""
        try:
            request_id, selected = _selection_from_request(raw_request, resolved_ids)
        except ValueError as error:
            await session.send_custom_message(
                ERROR_MESSAGE,
                {"version": PROTOCOL_VERSION, "requestId": request_id, "code": "invalid_selection", "message": str(error)},
            )
            return

        if lock.locked():
            await session.send_custom_message(
                ERROR_MESSAGE,
                {"version": PROTOCOL_VERSION, "requestId": request_id, "code": "busy", "message": "Another bookmark is still being created."},
            )
            return

        try:
            async with lock:
                url = await _create_url(
                    bookmark=session.bookmark,
                    inputs=input,
                    selected=selected,
                    max_url_length=max_url_length,
                )
            await session.send_custom_message(
                RESULT_MESSAGE,
                {"version": PROTOCOL_VERSION, "requestId": request_id, "url": url},
            )
        except OverflowError:
            await session.send_custom_message(
                ERROR_MESSAGE,
                {"version": PROTOCOL_VERSION, "requestId": request_id, "code": "url_too_long", "message": "This view contains too much state for a reliable URL. Exclude a few fields and try again."},
            )
        except Exception:
            await session.send_custom_message(
                ERROR_MESSAGE,
                {"version": PROTOCOL_VERSION, "requestId": request_id, "code": "serialization_failed", "message": "Shiny could not create this bookmark. Try again."},
            )

    return registration

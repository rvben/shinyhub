from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from htmltools import Tag, TagChild

SelectChoices = (
    list[str] | tuple[str, ...] | Mapping[str, str] | Mapping[str, Mapping[str, str]]
)
_MISSING = object()


def input_selectize(
    id: str,
    label: TagChild,
    choices: SelectChoices,
    *,
    selected: str | list[str] | None = None,
    multiple: bool = False,
    width: str | None = None,
    remove_button: bool | None = None,
    options: dict[str, Any] | None = None,
) -> Tag:
    """Create a selectize input that preserves its choices when restoring a link.

    Use this in place of ``shiny.ui.input_selectize`` for both single and
    multiple selections. Shiny restores the selected value before rendering;
    this helper prevents affected Shiny releases from also replacing choices
    with that value. All other rendering is delegated to Shiny unchanged.

    This also works inside modules and dynamically rendered UI. It uses Shiny's
    private restore-context API only when this input has a saved value, and is
    tested against the supported Shiny range.
    ``ChoiceRestore(control="selectize")`` remains responsible for aliases and
    unavailable saved values after the session starts.
    """
    from shiny import ui
    from shiny.bookmark import restore_input
    from shiny.module import resolve_id

    restored = restore_input(resolve_id(id), _MISSING)
    kwargs: dict[str, Any] = dict(
        selected=selected if restored is _MISSING else restored,
        multiple=multiple,
        width=width,
        remove_button=remove_button,
        options=options,
    )
    if restored is _MISSING:
        return ui.input_selectize(id, label, choices, **kwargs)

    try:
        from shiny.bookmark._restore_state import RestoreContext, restore_context
    except ImportError as error:
        raise RuntimeError(
            "shinyhub_bookmarks.input_selectize requires Shiny's bookmark "
            "restore-context API. Upgrade shinyhub-bookmarks to a version "
            "compatible with your Shiny installation."
        ) from error

    # None would fall back to the session's restore context in dynamic UI.
    # An empty context masks both request- and session-backed restoration.
    with restore_context(RestoreContext()):
        return ui.input_selectize(id, label, choices, **kwargs)

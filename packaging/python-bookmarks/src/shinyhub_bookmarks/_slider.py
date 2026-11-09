from __future__ import annotations

from collections.abc import Iterable
from datetime import date, datetime, timedelta
from typing import TYPE_CHECKING, Any, TypeVar

from htmltools import Tag, TagChild

if TYPE_CHECKING:
    from shiny.ui import AnimationOptions

SliderValue = TypeVar("SliderValue", float, datetime, date)
_MISSING = object()


def input_slider(
    id: str,
    label: TagChild,
    min: SliderValue,
    max: SliderValue,
    value: SliderValue | Iterable[SliderValue],
    *,
    step: float | timedelta | None = None,
    ticks: bool = False,
    animate: bool | AnimationOptions = False,
    width: str | None = None,
    sep: str = ",",
    pre: str | None = None,
    post: str | None = None,
    time_format: str | None = None,
    timezone: str | None = None,
    drag_range: bool = True,
) -> Tag:
    """Create a slider that restores bookmarked date and datetime values.

    Replace ``shiny.ui.input_slider`` with this helper for date and datetime
    sliders, including ranges and module inputs. Numeric sliders and inputs
    without saved values delegate directly to Shiny. Invalid saved dates or
    mismatched single/range shapes fall back to the app's default value;
    ``register()`` can report the mismatch through its adjustment banner.

    Only bookmarked date sliders use Shiny's guarded private restore-context
    API. Rendering and UTC timestamp conversion remain Shiny's responsibility.
    The helper remains compatible when Shiny fixes native restoration.
    """
    from shiny import ui
    from shiny.bookmark import restore_input
    from shiny.module import resolve_id

    kwargs: dict[str, Any] = dict(
        step=step,
        ticks=ticks,
        animate=animate,
        width=width,
        sep=sep,
        pre=pre,
        post=post,
        time_format=time_format,
        timezone=timezone,
        drag_range=drag_range,
    )
    if not isinstance(min, date):
        return ui.input_slider(id, label, min, max, value, **kwargs)
    restored = restore_input(resolve_id(id), _MISSING)
    if restored is _MISSING:
        return ui.input_slider(id, label, min, max, value, **kwargs)

    parse = datetime.fromisoformat if isinstance(min, datetime) else date.fromisoformat
    ranged = isinstance(value, (list, tuple))
    saved_range = isinstance(restored, (list, tuple))
    if ranged != saved_range or (saved_range and len(restored) != 2):
        restored = value
    else:
        try:
            restored = [parse(item) for item in restored] if ranged else parse(restored)
        except (TypeError, ValueError):
            restored = value

    try:
        from shiny.bookmark._restore_state import RestoreContext, restore_context

        empty = RestoreContext()
    except (ImportError, TypeError, AttributeError) as error:
        raise RuntimeError(
            "shinyhub_bookmarks.input_slider requires Shiny's bookmark "
            "restore-context API. Upgrade shinyhub-bookmarks to a version "
            "compatible with your Shiny installation."
        ) from error

    # An empty context also masks session-backed restore in dynamic UI.
    with restore_context(empty):
        return ui.input_slider(id, label, min, max, restored, **kwargs)

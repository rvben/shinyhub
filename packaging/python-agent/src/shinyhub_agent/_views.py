"""An agent-composed page view: a declared catalog, a layout compiler and live tiles.

The model chooses a layout from measures, dimensions and filter values the app
declared. It never supplies data, markup or chart specifications, and composing
a layout reads no app data: tiles are resolved by the app's query callback
inside a Shiny output, so they follow the page like any other output.
"""

from __future__ import annotations

import copy
import inspect
import json
import logging
import math
import re
from collections.abc import Awaitable, Callable, Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any

from ._charts import check_temporal, instant, moment
from ._core import NAME, AgentTool, ToolError, ToolRegistry, _UnexpectedToolError

VIEW_TOOL = "build_view"
CLEAR_TOOL = "clear_view"
MAX_TILES = 6
MAX_TILE_ROWS = 500
# The renderer refuses a chart above 300,000 characters of JSON. The margin
# covers numbers that the browser writes out longer than Python does.
MAX_CHART_CHARS = 280_000
# The renderer's categorical palette has ten colors.
MAX_SERIES = 10
OUTPUT_ID = re.compile(r"^[A-Za-z][A-Za-z0-9_]{0,63}$")
UNAVAILABLE = "This tile could not load."
logger = logging.getLogger(__name__)
Query = Callable[[str, tuple[str, ...], dict[str, str]], Any]
Install = Callable[[Any, Callable[[], None]], Awaitable[None]]


class _TileProblem(Exception):
    """A data condition worth explaining to the visitor."""


def _labels(kind: str, value: Any, minimum: int) -> dict[str, str]:
    if not isinstance(value, Mapping) or not minimum <= len(value) <= 16:
        raise ValueError(f"Declare {minimum} to 16 {kind}s")
    for name, label in value.items():
        if not isinstance(name, str) or not NAME.fullmatch(name):
            raise ValueError(
                f"A {kind} id must use lower-case letters, digits and underscores"
            )
        if not isinstance(label, str) or not 1 <= len(label.strip()) <= 80:
            raise ValueError(f"A {kind} label must contain 1 to 80 characters")
    return {name: label.strip() for name, label in value.items()}


@dataclass(frozen=True)
class AgentView:
    """Declare what the chat agent may place in one generated page view.

    measures and dimensions map ids to visitor-facing labels. temporal names
    the dimensions holding ISO dates. filters maps a filter id to the values
    the agent may choose; a tile filter narrows the page's scope, never widens it.

    query(measure, group_by, filters) is a synchronous callable returning one
    dict per group, keyed by the group_by ids and the measure id. It runs
    inside the Shiny output, so reactive reads make the tiles follow the page.
    Apply the viewer's permissions and current page filters there. Return an
    empty list or None when there is no data, and None for an absent measure.
    """

    output_id: str
    measures: Mapping[str, str]
    dimensions: Mapping[str, str]
    query: Query
    temporal: Sequence[str] = ()
    filters: Mapping[str, Sequence[str]] = field(default_factory=dict)

    def __post_init__(self) -> None:
        if not isinstance(self.output_id, str) or not OUTPUT_ID.fullmatch(
            self.output_id
        ):
            raise ValueError(
                "output_id must start with a letter and use letters, digits and underscores"
            )
        measures = _labels("measure", self.measures, 1)
        dimensions = _labels("dimension", self.dimensions, 0)
        if set(measures) & set(dimensions):
            raise ValueError("Measure and dimension ids must be unique")
        labels = [*measures.values(), *dimensions.values()]
        if len(set(labels)) != len(labels):
            raise ValueError("Measure and dimension labels must be unique")
        if isinstance(self.temporal, str) or any(
            name not in dimensions for name in self.temporal
        ):
            raise ValueError("temporal must list declared dimension ids")
        if not isinstance(self.filters, Mapping) or len(self.filters) > 8:
            raise ValueError("Declare at most 8 filters")
        filters = {}
        for name, values in self.filters.items():
            if (
                not isinstance(name, str)
                or not NAME.fullmatch(name)
                or isinstance(values, str)
                or not isinstance(values, Sequence)
                or not 1 <= len(values) <= 50
                or any(
                    not isinstance(value, str) or not 1 <= len(value) <= 80
                    for value in values
                )
                or len(set(values)) != len(values)
            ):
                raise ValueError(
                    "A filter needs a lower-case id and 1 to 50 unique string values "
                    "of at most 80 characters"
                )
            filters[name] = tuple(values)
        if not callable(self.query):
            raise TypeError("query must be callable")
        if inspect.iscoroutinefunction(self.query):
            raise TypeError(
                "query must be synchronous: it runs inside the Shiny output"
            )
        object.__setattr__(self, "measures", measures)
        object.__setattr__(self, "dimensions", dimensions)
        object.__setattr__(self, "temporal", tuple(dict.fromkeys(self.temporal)))
        object.__setattr__(self, "filters", filters)

    def schema(self) -> dict[str, Any]:
        tile: dict[str, Any] = {
            "kind": {"type": "string", "enum": ["stat", "bar", "line", "table"]},
            "title": {"type": "string", "minLength": 1, "maxLength": 120},
            "measure": {
                "type": "string",
                "enum": list(self.measures),
                "description": "; ".join(
                    f"{name} = {label}" for name, label in self.measures.items()
                ),
            },
        }
        if self.dimensions:
            tile["by"] = {
                "type": "string",
                "enum": list(self.dimensions),
                "description": "; ".join(
                    f"{name} = {label}" + (" (date)" if name in self.temporal else "")
                    for name, label in self.dimensions.items()
                ),
            }
            # A date dimension is refused in series by the compiler, whose
            # message the model can act on; a schema failure says nothing.
            tile["series"] = {"type": "string", "enum": list(self.dimensions)}
        if self.filters:
            tile["filters"] = {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    name: {"type": "string", "enum": list(values)}
                    for name, values in self.filters.items()
                },
            }
        return {
            "type": "object",
            "additionalProperties": False,
            "properties": {
                "title": {"type": "string", "minLength": 1, "maxLength": 120},
                "tiles": {
                    "type": "array",
                    "minItems": 1,
                    "maxItems": MAX_TILES,
                    "items": {
                        "type": "object",
                        "additionalProperties": False,
                        "properties": tile,
                        "required": ["kind", "title", "measure"],
                    },
                },
            },
            "required": ["title", "tiles"],
        }


def _invalid(message: str) -> ToolError:
    return ToolError("invalid_view", message)


def compile_layout(view: AgentView, arguments: dict[str, Any]) -> dict[str, Any]:
    """Turn schema-valid arguments into a layout, rejecting undrawable tiles."""
    if not arguments["title"].strip():
        raise _invalid("Give the view a descriptive title.")
    tiles = []
    for index, tile in enumerate(arguments["tiles"], 1):
        kind, by, series = tile["kind"], tile.get("by"), tile.get("series")
        where = f"Tile {index} ({kind})"
        if not tile["title"].strip():
            raise _invalid(f"{where} needs a descriptive title.")
        if kind == "stat":
            if by or series:
                raise _invalid(
                    f"{where}: a stat shows one value, so remove by and series."
                )
        elif not by:
            raise _invalid(f"{where} needs a dimension in by.")
        elif kind == "line" and by not in view.temporal:
            raise _invalid(
                f"{where}: by must be a date dimension"
                + (f" ({', '.join(view.temporal)})." if view.temporal else "; none is declared.")
            )
        if series and series == by:
            raise _invalid(f"{where}: series must differ from by.")
        if series and series in view.temporal:
            raise _invalid(f"{where}: series must be a categorical dimension.")
        if kind == "bar" and series and by in view.temporal:
            raise _invalid(
                f"{where}: bars over a date dimension cannot be split by series. "
                "Use a line tile, or remove series."
            )
        tiles.append(
            {
                "id": f"tile_{index}",
                "kind": kind,
                "title": tile["title"].strip(),
                "measure": tile["measure"],
                "by": by,
                "series": series,
                "filters": dict(tile.get("filters", {})),
            }
        )
    return {"title": arguments["title"].strip(), "tiles": tiles}


def _rows(view: AgentView, tile: dict[str, Any], rows: Any) -> list[dict[str, Any]]:
    group = [name for name in (tile["by"], tile["series"]) if name]
    measure = tile["measure"]
    if not isinstance(rows, list):
        raise ValueError("query must return a list of rows or None")
    if len(rows) > MAX_TILE_ROWS:
        raise ValueError(f"query returned more than {MAX_TILE_ROWS} rows")
    expected, seen = {*group, measure}, set()
    placed = moment if tile["kind"] == "table" else instant
    for row in rows:
        if not isinstance(row, dict) or set(row) != expected:
            raise ValueError("rows must hold exactly the grouped ids and the measure")
        value = row[measure]
        if value is not None and (
            isinstance(value, bool)
            or not isinstance(value, (int, float))
            or not math.isfinite(value)
            or (isinstance(value, int) and abs(value) > 2**53 - 1)
        ):
            raise ValueError("measure values must be finite numbers or None")
        for name in group:
            if not isinstance(row[name], str) or not 1 <= len(row[name]) <= 300:
                raise ValueError("dimension values must be strings of 1 to 300 characters")
            if name in view.temporal:
                check_temporal(row[name])
        # Two spellings of one moment are one group. A chart also merges
        # moments it would plot at the same millisecond; a table lists them.
        key = tuple(
            placed(row[name]) if name in view.temporal else row[name]
            for name in group
        )
        if key in seen:
            raise ValueError("query returned more than one row for a group")
        seen.add(key)
    if tile["kind"] == "stat" and len(rows) > 1:
        raise ValueError("a stat needs exactly one row")
    return rows


def _data(view: AgentView, tile: dict[str, Any], rows: list[dict[str, Any]]) -> dict[str, Any]:
    measure, by, series = tile["measure"], tile["by"], tile["series"]
    label = view.measures[measure]
    if tile["kind"] == "stat":
        return {"state": "ok", "value": rows[0][measure], "label": label}
    columns = [*(name for name in (by, series) if name), measure]
    labels = [*(view.dimensions[name] for name in columns[:-1]), label]
    if tile["kind"] == "table":
        return {
            "state": "ok",
            "columns": labels,
            "rows": [[row[name] for name in columns] for row in rows],
        }
    if all(row[measure] is None for row in rows):
        return {"state": "empty"}
    if series and len({row[series] for row in rows}) > MAX_SERIES:
        raise _TileProblem(
            f"More than {MAX_SERIES} series. Narrow the page filters or ask for a simpler view."
        )
    fields = {
        view.dimensions[name]: "temporal" if name in view.temporal else "nominal"
        for name in columns[:-1]
    }
    fields[label] = "quantitative"
    chart = {
        "kind": tile["kind"],
        "x": view.dimensions[by],
        "y": label,
        "fields": fields,
        "rows": [dict(zip(labels, (row[name] for name in columns))) for row in rows],
    }
    if series:
        chart["color"] = view.dimensions[series]
    wire = json.dumps(chart, ensure_ascii=False, separators=(",", ":"))
    if len(wire.encode("utf-16-le")) // 2 > MAX_CHART_CHARS:
        raise _TileProblem(
            "Too much data to chart. Narrow the page filters or ask for a table."
        )
    return {"state": "ok", "chart": chart}


def _tile(view: AgentView, tile: dict[str, Any], silent: tuple[type, ...]) -> dict[str, Any]:
    shown = {"id": tile["id"], "kind": tile["kind"], "title": tile["title"]}
    if tile["filters"]:
        # A narrowed figure is shown with its scope, whatever the title says.
        shown["scope"] = [
            [view.dimensions.get(name, name), value]
            for name, value in tile["filters"].items()
        ]
    group = tuple(name for name in (tile["by"], tile["series"]) if name)
    try:
        rows = view.query(tile["measure"], group, dict(tile["filters"]))
        if inspect.isawaitable(rows):
            if inspect.iscoroutine(rows):
                rows.close()
            raise TypeError("query must be synchronous")
        if rows is None or rows == []:
            return {**shown, "state": "empty"}
        if isinstance(rows, list) and len(rows) <= MAX_TILE_ROWS:
            # The app may hand out rows it keeps; an oversized result is
            # refused below without paying for a copy of it.
            rows = copy.deepcopy(rows)
        return {**shown, **_data(view, tile, _rows(view, tile, rows))}
    except silent:
        # An unset input or req() means the page is not ready for this tile.
        return {**shown, "state": "empty"}
    except _TileProblem as problem:
        return {**shown, "state": "error", "message": str(problem)}
    except ToolError as error:
        if isinstance(error, _UnexpectedToolError):
            logger.exception("Agent view tile failed")
            return {**shown, "state": "error", "message": UNAVAILABLE}
        logger.info("Agent view tile refused: %s", error.code)
        return {**shown, "state": "error", "message": str(error)[:300]}
    except Exception:
        logger.exception("Agent view tile failed")
        return {**shown, "state": "error", "message": UNAVAILABLE}


def resolve(
    view: AgentView, layout: dict[str, Any] | None, *, silent: tuple[type, ...] = ()
) -> dict[str, Any]:
    """Query every tile of a layout. Call inside a tracked reactive context."""
    if layout is None:
        return {"version": 1, "view": None}
    return {
        "version": 1,
        "view": {
            "title": layout["title"],
            "tiles": [_tile(view, tile, silent) for tile in layout["tiles"]],
        },
    }


class ViewRegistry(ToolRegistry):
    """Chat-only registry overlay; never exposes view tools to WebMCP."""

    def __init__(self, base: ToolRegistry, view: AgentView, install: Install):
        for name in (VIEW_TOOL, CLEAR_TOOL):
            if base.get(name):
                raise ValueError(f"{name} is reserved when a view is enabled")
        self.base = base
        self.view = view
        self.install = install
        self.layout: dict[str, Any] | None = None
        self.changed = False
        self.mentioned = False
        try:
            build = AgentTool(
                VIEW_TOOL,
                "Replace the generated view on the app page with up to six live tiles "
                "built from the app's declared measures and dimensions. Tiles follow the "
                "page's current filters and refresh on their own. This returns no figures: "
                "read data with the app's other tools before stating any number. "
                "stat: one measure. bar, table: a measure by a dimension. "
                "line: a measure by a date dimension. series splits by a category. "
                "Tile filters only narrow the page's current scope.",
                view.schema(),
                self._build,
            )
        except ValueError as error:
            raise ValueError(
                "The view declares more measures, dimensions and filter values "
                "than fit in one tool schema"
            ) from error
        clear = AgentTool(
            CLEAR_TOOL,
            "Remove the generated view from the app page.",
            {"type": "object", "properties": {}, "additionalProperties": False},
            self._clear,
        )
        # Reuse core argument validation for the internal tools only.
        super().__init__([build, clear])

    def _own(self, name: Any) -> bool:
        return isinstance(name, str) and name in self._tools

    def get(self, name):
        return self._tools[name] if self._own(name) else self.base.get(name)

    def public_spec(self):
        result = self.base.public_spec()
        own = [tool.public_spec() for tool in self._tools.values()]
        return {**result, "tools": [*result["tools"], *own]}

    async def execute(self, name, arguments, *, timeout=8.0):
        if self._own(name):
            return await super().execute(name, arguments, timeout=timeout)
        return await self.base.execute(name, arguments, timeout=timeout)

    async def validate(self, name, arguments, *, timeout=8.0):
        if self._own(name):
            return await super().validate(name, arguments, timeout=timeout)
        return await self.base.validate(name, arguments, timeout=timeout)

    async def describe(self, name, arguments):
        return await self.base.describe(name, arguments)

    async def undo(self, name, arguments, result):
        return await self.base.undo(name, arguments, result)

    def begin_turn(self) -> None:
        self.changed = False

    def context(self) -> str:
        """The view on the page, as the model is told it."""
        if self.layout is None:
            return "Generated view on the page: none."
        tiles = [
            {
                key: value
                for key, value in tile.items()
                if key != "id" and value not in (None, {})
            }
            for tile in self.layout["tiles"]
        ]
        return "Generated view on the page (live tiles, no figures): " + json.dumps(
            {"title": self.layout["title"], "tiles": tiles}
        )

    def current(self) -> str:
        """The page's view as of now, once this chat has had one; else empty."""
        return self.context() if self.mentioned else ""

    async def _set(self, layout: dict[str, Any] | None) -> None:
        def commit() -> None:
            self.layout = layout
            self.changed = self.mentioned = True

        # install calls commit as it assigns, so a build that times out while
        # the page settles still leaves this record matching the page.
        await self.install(copy.deepcopy(layout), commit)

    async def _build(self, arguments):
        layout = compile_layout(self.view, arguments)
        await self._set(layout)
        return {
            "displayed": True,
            "tiles": len(layout["tiles"]),
            "note": "The tiles show live app data on the page. "
            "No figures are returned here.",
        }

    async def _clear(self, _arguments):
        await self._set(None)
        return {"cleared": True}

    async def reset(self, *, forget: bool = False) -> None:
        """Remove the view for a visitor's dismissal, or with forget for a new chat.

        This is not the agent's doing, so the turn's change flag is untouched.
        """

        def commit() -> None:
            self.layout = None

        try:
            await self.install(None, commit)
        except Exception:
            if self.layout is not None:
                raise
            # The view is off the page; only the update that followed failed.
            logger.exception("Agent view update failed after the view was removed")
        if forget:
            self.mentioned = False

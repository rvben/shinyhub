"""Session-owned chart data and a bounded, provider-independent chart tool."""

from __future__ import annotations

import copy
import json
import math
import re
import secrets
from collections import OrderedDict
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any

from ._core import MAX_RESULT_BYTES, AgentTool, ToolError, ToolRegistry

CHART_TOOL = "render_chart"
MAX_DATA_BYTES = 262_144
MAX_DATASETS = 8
MAX_CHARTS = 4
TEMPORAL = re.compile(
    r"\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}(?::\d{2}(?:\.\d{1,6})?)?(?:Z|[+-]\d{2}:\d{2})?)?"
)


def check_temporal(value: str) -> None:
    try:
        if not TEMPORAL.fullmatch(value):
            raise ValueError("Unsupported ISO representation")
        datetime.fromisoformat(
            value.replace("Z", "+00:00")  # noqa: FURB162 -- Python 3.10 compatibility
        )
    except ValueError as error:
        raise ValueError("Temporal values must be ISO dates or timestamps") from error


def moment(value: str) -> datetime:
    """The moment a valid temporal value names; zone-free values are UTC."""
    date = datetime.fromisoformat(
        value.replace("Z", "+00:00")  # noqa: FURB162 -- Python 3.10 compatibility
    )
    return (date if date.tzinfo else date.replace(tzinfo=timezone.utc)).astimezone(
        timezone.utc
    )


def instant(value: str) -> datetime:
    """The moment the renderer plots a valid temporal value at.

    Precision is the browser's millisecond.
    """
    date = moment(value)
    return date.replace(microsecond=(date.microsecond // 1000) * 1000)


@dataclass(frozen=True)
class ChartData:
    """Return from a read tool: typed, authorized rows available for inline charts.

    Field types are nominal (categories), quantitative (finite numbers), or
    temporal (ISO dates/timestamps). Values are snapshotted on construction.
    """

    rows: list[dict[str, Any]]
    fields: dict[str, str]
    title: str = "Chart data"

    def __post_init__(self):
        if (
            not isinstance(self.title, str)
            or not self.title.strip()
            or len(self.title) > 160
        ):
            raise ValueError("Chart data needs a title of 1–160 characters")
        if not isinstance(self.fields, dict) or not 1 <= len(self.fields) <= 16:
            raise ValueError("Chart data needs 1–16 typed fields")
        if any(
            not isinstance(k, str)
            or not k.strip()
            or len(k) > 80
            or v not in ("nominal", "quantitative", "temporal")
            for k, v in self.fields.items()
        ):
            raise ValueError("Invalid chart field name or type")
        if not isinstance(self.rows, list) or not 1 <= len(self.rows) <= 2000:
            raise ValueError(
                "Chart data needs 1–2000 rows; aggregate larger datasets first"
            )
        for row in self.rows:
            if not isinstance(row, dict) or set(row) != set(self.fields):
                raise ValueError("Chart rows must match the declared fields")
            for field, kind in self.fields.items():
                value = row[field]
                if value is None:
                    continue
                if kind == "quantitative":
                    if (
                        isinstance(value, bool)
                        or not isinstance(value, (int, float))
                        or (isinstance(value, int) and abs(value) > 2**53 - 1)
                        or not math.isfinite(value)
                    ):
                        raise ValueError(
                            "Quantitative values must be finite numbers; integers must fit the browser safe range"
                        )
                elif not isinstance(value, str) or len(value) > 300:
                    raise ValueError(
                        "Category and date values must be strings of at most 300 characters"
                    )
                elif kind == "temporal":
                    check_temporal(value)
        if len(json.dumps(self.rows, allow_nan=False).encode()) > MAX_DATA_BYTES:
            raise ValueError("Chart data is too large; aggregate it first")
        object.__setattr__(self, "rows", copy.deepcopy(self.rows))
        object.__setattr__(self, "fields", dict(self.fields))

    def as_result(self):
        # Browser tools retain the ordinary result-size bound. Only the chat
        # registry stores larger datasets, without sending rows to the model.
        return {"title": self.title, "fields": self.fields, "rows": self.rows}


class ChartRegistry(ToolRegistry):
    """Chat-only registry overlay; never exposes rendering tools to WebMCP."""

    def __init__(self, base: ToolRegistry):
        if base.get(CHART_TOOL):
            raise ValueError("render_chart is reserved when charts are enabled")
        self.base = base
        self.datasets: OrderedDict[str, ChartData] = OrderedDict()
        self.pending: list[dict[str, Any]] = []
        self.count = 0
        self.tool = AgentTool(
            CHART_TOOL,
            "Display an inline chart using a dataset_id returned by an app tool. "
            "Choose existing field names, never invent values. Supports bar, line, area, scatter. "
            "For follow-ups reuse the dataset_id from chart history; fetch again if expired. "
            "Charts are data snapshots, not live dashboard bindings. Explain the chart briefly.",
            {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    "dataset_id": {"type": "string", "maxLength": 80},
                    "kind": {
                        "type": "string",
                        "enum": ["bar", "line", "area", "scatter"],
                    },
                    "x": {"type": "string", "maxLength": 80},
                    "y": {"type": "string", "maxLength": 80},
                    "color": {"type": "string", "maxLength": 80},
                    "title": {"type": "string", "minLength": 1, "maxLength": 160},
                },
                "required": ["dataset_id", "kind", "x", "y", "title"],
            },
            self.render,
        )
        # Reuse core argument validation for the internal tool only.
        super().__init__([self.tool])

    def get(self, name):
        return self.tool if name == CHART_TOOL else self.base.get(name)

    def public_spec(self):
        result = self.base.public_spec()
        return {**result, "tools": [*result["tools"], self.tool.public_spec()]}

    async def execute(self, name, arguments, *, timeout=8.0):
        if name == CHART_TOOL:
            return await super().execute(name, arguments, timeout=timeout)
        # The base registry validates permissions, arguments and results, and
        # runs the app callback in the viewer's Shiny session.
        result = await self.base.execute(
            name, arguments, timeout=timeout, _chart_data=True
        )
        if not isinstance(result, ChartData):
            return result
        if not self.base.get(name).read_only:
            raise ToolError(
                "invalid_chart_data", "ChartData must come from a read-only tool."
            )
        dataset_id = "dataset_" + secrets.token_urlsafe(12)
        self.datasets[dataset_id] = result
        while len(self.datasets) > MAX_DATASETS:
            self.datasets.popitem(last=False)
        metadata = {
            "dataset_id": dataset_id,
            "title": result.title,
            "fields": dict(result.fields),
            "row_count": len(result.rows),
            "sample_rows": [],
            "statistics": {
                field: {"min": min(values), "max": max(values)}
                for field, kind in result.fields.items()
                if kind == "quantitative"
                and (
                    values := [
                        row[field] for row in result.rows if row[field] is not None
                    ]
                )
            },
            "hint": "Use render_chart with these field names. Full rows stay in the app session. "
            "Samples are incomplete; request app aggregates for further analysis.",
        }
        for row in result.rows[:3]:
            candidate = {**metadata, "sample_rows": [*metadata["sample_rows"], row]}
            if len(json.dumps(candidate, allow_nan=False).encode()) > MAX_RESULT_BYTES:
                break
            metadata = candidate
        return self._result(copy.deepcopy(metadata))

    async def validate(self, name, arguments, *, timeout=8.0):
        if name == CHART_TOOL:
            return await super().validate(name, arguments, timeout=timeout)
        return await self.base.validate(name, arguments, timeout=timeout)

    async def describe(self, name, arguments):
        return await self.base.describe(name, arguments)

    async def undo(self, name, arguments, result):
        return await self.base.undo(name, arguments, result)

    def begin_turn(self):
        self.pending.clear()
        self.count = 0

    def clear(self):
        self.begin_turn()
        self.datasets.clear()

    async def render(self, args):
        if not args["title"].strip():
            raise ToolError("invalid_chart", "Give the chart a descriptive title.")
        data = self.datasets.get(args["dataset_id"])
        if data is None:
            raise ToolError(
                "dataset_expired",
                "This chart dataset expired. Read the app data again.",
            )
        if self.count >= MAX_CHARTS:
            raise ToolError("chart_limit", "Show at most four charts per answer.")
        if any(
            args[key] not in data.fields for key in ("x", "y", "color") if key in args
        ):
            raise ToolError("invalid_chart", "Choose fields from the dataset.")
        if data.fields[args["y"]] != "quantitative" or (
            args["kind"] == "scatter" and data.fields[args["x"]] != "quantitative"
        ):
            raise ToolError("invalid_chart", "The selected chart needs numeric axes.")
        if "color" in args and data.fields[args["color"]] != "nominal":
            raise ToolError("invalid_chart", "Group charts by a categorical field.")
        if args["kind"] in ("line", "area") and data.fields[args["x"]] == "nominal":
            raise ToolError(
                "invalid_chart", "Line and area charts need a date or numeric x axis."
            )
        if not any(
            row[args["x"]] is not None and row[args["y"]] is not None
            for row in data.rows
        ):
            raise ToolError(
                "invalid_chart", "The selected axes have no complete data points."
            )
        if args["kind"] != "scatter":
            seen = set()
            for row in data.rows:
                x = row[args["x"]]
                if x is not None and data.fields[args["x"]] == "temporal":
                    x = instant(x)
                key = (x, row.get(args.get("color")))
                if key in seen:
                    raise ToolError(
                        "invalid_chart",
                        "Aggregate repeated x values per series in the app tool first.",
                    )
                seen.add(key)
        if "color" in args and len({row[args["color"]] for row in data.rows}) > 12:
            raise ToolError(
                "invalid_chart",
                "Choose at most twelve series; filter or aggregate in the app tool.",
            )
        # A small owned schema is sent to the client, never an arbitrary Vega spec.
        chart = {
            **args,
            "id": "chart_" + secrets.token_urlsafe(12),
            "fields": data.fields,
            "rows": data.rows,
            "source": data.title,
        }
        self.pending.append(copy.deepcopy(chart))
        self.count += 1
        return {
            "chart_id": chart["id"],
            "dataset_id": args["dataset_id"],
            "title": args["title"],
            "kind": args["kind"],
            "x": args["x"],
            "y": args["y"],
            **({"color": args["color"]} if "color" in args else {}),
            "row_count": len(data.rows),
            "displayed": True,
        }

import asyncio
import json

import pytest
from shinyhub_agent import AgentTool, ChartData, ToolError, ToolRegistry
from shinyhub_agent._charts import ChartRegistry
from shinyhub_agent._chat import ChatSession
from shinyhub_agent._runtime import ToolCall, Turn


def data():
    return ChartData(
        [
            {"date": "2026-01-01", "sales": 12, "region": "North"},
            {"date": "2026-02-01", "sales": 19, "region": "North"},
        ],
        {"date": "temporal", "sales": "quantitative", "region": "nominal"},
        "Authorized sales",
    )


def base(value=None):
    async def read(_):
        return value if value is not None else data()

    return ToolRegistry(
        [
            AgentTool(
                "sales",
                "Read authorized sales for charts",
                {
                    "type": "object",
                    "properties": {},
                    "additionalProperties": False,
                },
                read,
            )
        ]
    )


def args(dataset, **changes):
    return dict(
        dataset_id=dataset,
        kind="line",
        x="date",
        y="sales",
        title="Sales over time",
        **changes,
    )


@pytest.mark.parametrize(
    "rows,fields",
    [
        ([], {"x": "nominal"}),
        ([{"x": float("nan")}], {"x": "quantitative"}),
        ([{"x": float("inf")}], {"x": "quantitative"}),
        ([{"x": True}], {"x": "quantitative"}),
        ([{"x": "bad date"}], {"x": "temporal"}),
        ([{"x": 2, "secret": 3}], {"x": "quantitative"}),
        ([{"x": "a"}], {"x": "url"}),
    ],
)
def test_invalid_datasets(rows, fields):
    with pytest.raises(ValueError):
        ChartData(rows, fields)


def test_snapshot_and_browser_size_limit():
    rows = [{"x": "a", "y": 1}]
    value = ChartData(rows, {"x": "nominal", "y": "quantitative"})
    rows[0]["y"] = 99
    assert value.rows[0]["y"] == 1
    big = ChartData(
        [{"x": str(i) * 80, "y": i} for i in range(200)],
        {"x": "nominal", "y": "quantitative"},
    )

    async def run():
        with pytest.raises(ToolError, match="too much data"):
            await base(big).execute("sales", {})
        registry = ChartRegistry(base(big))
        result = await registry.execute("sales", {})
        assert result["row_count"] == 200
        assert "rows" not in result
        assert len(result["sample_rows"]) == 3
        assert result["statistics"]["y"] == {"min": 0, "max": 199}

    asyncio.run(run())


def test_fields_security_limits_and_session_isolation():
    async def run():
        a, b = ChartRegistry(base()), ChartRegistry(base())
        dataset = (await a.execute("sales", {}))["dataset_id"]
        with pytest.raises(ToolError, match="expired"):
            await b.execute("render_chart", args(dataset))
        invalid = [
            {"y": "secret"},
            {"y": "date"},
            {"kind": "scatter"},
            {"x": "region"},
            {"url": "https://example.com"},
            {"color": "sales"},
            {"kind": "javascript"},
        ]
        for changes in invalid:
            with pytest.raises(ToolError):
                await a.execute("render_chart", {**args(dataset), **changes})
        for _ in range(4):
            assert (await a.execute("render_chart", args(dataset)))["displayed"]
        with pytest.raises(ToolError, match="four charts"):
            await a.execute("render_chart", args(dataset))
        for _ in range(8):
            await a.execute("sales", {})
        assert len(a.datasets) == 8
        with pytest.raises(ToolError, match="expired"):
            await a.execute("render_chart", args(dataset))
        a.clear()
        assert not a.datasets and not a.pending
        assert base().get("render_chart") is None

    asyncio.run(run())


def test_chart_events_history_followups_reset_and_no_approval():
    events = []

    class Agent:
        async def run(self, message, history, tools, approve, *, thread_id):
            if history:
                assert "Chart snapshots" in history[-1]["content"]
                previous = json.loads(history[-1]["content"].splitlines()[-1])
                dataset = previous["dataset_id"]
            else:
                dataset = (await tools.execute("sales", {}))["dataset_id"]

            async def no_approval(*_):
                raise AssertionError("Charts must not request write approval")

            turn = Turn(2, 8)
            async for event in turn.execute(
                [ToolCall("chart-call", "render_chart", args(dataset))],
                tools,
                no_approval,
            ):
                if event["type"] != "tool_result":
                    yield event
            # A chart-only answer still succeeds and receives plain text fallback.

    async def run():
        async def send(event):
            events.append(event)

        chat = ChatSession(Agent(), base(), send, charts=True)
        for i in range(2):
            await chat.handle(
                {
                    "version": 1,
                    "session": chat.nonce,
                    "requestId": f"request_{i:08d}",
                    "message": "Graph sales",
                }
            )
            assert events[-1]["type"] == "done"
        charts = [e["chart"] for e in events if e["type"] == "chart"]
        assert len(charts) == 2 and charts[0]["dataset_id"] == charts[1]["dataset_id"]
        assert charts[0]["rows"] == data().rows
        assert '"rows"' not in chat.history[-1]["content"]
        await chat.handle(
            {
                "version": 1,
                "session": chat.nonce,
                "requestId": "reset_12345678",
                "action": "reset",
            }
        )
        assert not chat.history and not chat.charts.datasets

    asyncio.run(run())


def test_untrusted_custom_chart_event_is_ignored():
    events = []

    class Agent:
        async def run(self, *a, **kw):
            yield {"type": "chart", "chart": {"rows": [{"secret": 1}]}}
            yield {"type": "delta", "text": "Hello"}

    async def run():
        async def send(e):
            events.append(e)

        chat = ChatSession(Agent(), base(), send, charts=True)
        await chat.handle(
            {
                "version": 1,
                "session": chat.nonce,
                "requestId": "request_12345678",
                "message": "Hi",
            }
        )
        assert not any(e["type"] == "chart" for e in events)

    asyncio.run(run())


def test_duplicate_points_require_explicit_app_aggregation():
    async def run():
        value = ChartData(
            [{"x": "a", "y": 1}, {"x": "a", "y": 2}],
            {"x": "nominal", "y": "quantitative"},
        )
        registry = ChartRegistry(base(value))
        dataset = (await registry.execute("sales", {}))["dataset_id"]
        with pytest.raises(ToolError, match="Aggregate repeated"):
            await registry.execute(
                "render_chart", {**args(dataset), "kind": "bar", "x": "x", "y": "y"}
            )

    asyncio.run(run())


@pytest.mark.parametrize("backend", ["openai", "bedrock", "agui"])
def test_each_backend_can_read_data_render_and_answer(backend):
    from unittest.mock import patch

    from test_reliability import run_backend

    registry = ChartRegistry(base())

    async def no_approval(*_):
        raise AssertionError("Chart rendering is read-only")

    with patch("shinyhub_agent._charts.secrets.token_urlsafe", return_value="fixed"):
        events, requests = asyncio.run(
            run_backend(
                backend,
                [
                    {"calls": [("sales", {})]},
                    {"calls": [("render_chart", args("dataset_fixed"))]},
                    {"text": "Sales range from 12 to 19."},
                ],
                registry,
                no_approval,
            )
        )
    assert len(requests) == 3
    assert events[-1] == {"type": "delta", "text": "Sales range from 12 to 19."}
    assert len(registry.pending) == 1
    assert registry.pending[0]["rows"] == data().rows
    if backend == "openai":
        metadata = json.loads(requests[1]["input"][-1]["output"])
    elif backend == "agui":
        metadata = json.loads(requests[1]["messages"][-1]["content"])
    else:
        metadata = requests[1]["messages"][-1]["content"][0]["toolResult"]["content"][
            0
        ]["json"]
    assert metadata["sample_rows"] == data().rows
    assert "rows" not in metadata
    assert "render_chart" in json.dumps(requests[0])


def test_registration_keeps_chart_tool_out_of_browser_and_runs_reads_in_session(
    monkeypatch,
):
    from shiny import Inputs, reactive
    from shinyhub_agent import chat_dependency, register
    from shinyhub_agent._shiny import (
        CHAT_CAPABILITIES_MESSAGE,
        CHAT_EVENT_MESSAGE,
        CHAT_INPUT,
    )
    from test_shiny import LEGACY_REACTIVITY, Session, reset_graph_lock

    reset_graph_lock(monkeypatch)

    async def run():
        session, request, completed = Session(), reactive.Value(None), asyncio.Event()

        async def read(_):
            assert not LEGACY_REACTIVITY or reactive.lock().locked()
            return data()

        class Agent:
            async def run(self, message, history, tools, approve, *, thread_id):
                assert not LEGACY_REACTIVITY or not reactive.lock().locked()
                dataset = (await tools.execute("sales", {}))["dataset_id"]
                await tools.execute("render_chart", args(dataset))
                yield {"type": "delta", "text": "Here is the chart."}
                completed.set()

        tools = register(
            session=session,
            input=Inputs({CHAT_INPUT: request}),
            tools=[
                AgentTool(
                    "sales",
                    "Read sales",
                    {
                        "type": "object",
                        "properties": {},
                        "additionalProperties": False,
                    },
                    read,
                )
            ],
            chat=Agent(),
            charts=True,
        )
        assert tools.get("render_chart") is None
        await session.flushed[0]()
        nonce = next(
            v["session"] for k, v in session.messages if k == CHAT_CAPABILITIES_MESSAGE
        )
        request.set(
            {
                "version": 1,
                "session": nonce,
                "requestId": "request_12345678",
                "message": "Graph",
            }
        )
        await reactive.flush()
        await asyncio.wait_for(completed.wait(), 1)
        await asyncio.sleep(0)
        assert any(
            k == CHAT_EVENT_MESSAGE and v["type"] == "chart"
            for k, v in session.messages
        )
        assert not LEGACY_REACTIVITY or not reactive.lock().locked()
        for fn in session.ended:
            fn()

    asyncio.run(run())
    dep = chat_dependency()
    assert [script["src"] for script in dep.script] == ["charts.js", "chat.js"]


def test_chart_history_is_kept_if_later_answer_fails():
    events = []

    class Agent:
        async def run(self, message, history, tools, approve, *, thread_id):
            dataset = (await tools.execute("sales", {}))["dataset_id"]
            await tools.execute("render_chart", args(dataset))
            yield {"type": "delta", "text": "Starting"}
            yield {"type": "error", "message": "Interrupted"}

    async def run():
        async def send(e):
            events.append(e)

        chat = ChatSession(Agent(), base(), send, charts=True)
        await chat.handle(
            {
                "version": 1,
                "session": chat.nonce,
                "requestId": "request_12345678",
                "message": "Chart",
            }
        )
        assert "The reply was interrupted" in chat.history[-1]["content"]
        assert "dataset_id" in chat.history[-1]["content"]
        assert events[-1]["type"] == "error"

    asyncio.run(run())


def test_preview_respects_existing_model_result_limit_with_unicode():
    async def run():
        value = ChartData(
            [{f"field{i}": "測" * 300 for i in range(16)} for _ in range(6)],
            {f"field{i}": "nominal" for i in range(16)},
        )
        metadata = await ChartRegistry(base(value)).execute("sales", {})
        assert len(json.dumps(metadata).encode()) <= 32768
        assert len(metadata["sample_rows"]) == 1

    asyncio.run(run())


@pytest.mark.parametrize(
    "value,kind",
    [
        (10**1000, "quantitative"),
        (2**53, "quantitative"),
        ("20260101", "temporal"),
        ("2026-W01-1", "temporal"),
        ("2026-01-01 00:00", "temporal"),
    ],
)
def test_values_must_survive_browser_json_and_date_parsing(value, kind):
    with pytest.raises(ValueError):
        ChartData([{"x": value}], {"x": kind})


def test_empty_axes_and_equivalent_timestamp_duplicates_are_rejected():
    async def run():
        for rows, message in [
            ([{"date": "2026-01-01", "sales": None}], "no complete data"),
            (
                [
                    {"date": "2026-01-01", "sales": 1},
                    {"date": "2026-01-01T01:00:00+01:00", "sales": 2},
                ],
                "Aggregate repeated",
            ),
        ]:
            registry = ChartRegistry(
                base(ChartData(rows, {"date": "temporal", "sales": "quantitative"}))
            )
            dataset = (await registry.execute("sales", {}))["dataset_id"]
            with pytest.raises(ToolError, match=message):
                await registry.execute("render_chart", args(dataset))

    asyncio.run(run())


def test_whitespace_cannot_bypass_label_length_bounds():
    with pytest.raises(ValueError):
        ChartData([{"x": 1}], {"x": "quantitative"}, " " * 160 + "x")
    with pytest.raises(ValueError):
        ChartData([{" " * 80 + "x": 1}], {" " * 80 + "x": "quantitative"})

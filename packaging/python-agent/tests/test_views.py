import asyncio
import json

import pytest
from shiny import Inputs, reactive
from shiny.types import SilentException
from shinyhub_agent import AgentTool, AgentView, ChartData, ToolError, ToolRegistry
from shinyhub_agent._chat import ChatSession
from shinyhub_agent._shiny import VIEW_DISMISS_INPUT
from shinyhub_agent._views import ViewRegistry, compile_layout, resolve

from test_shiny import LEGACY_REACTIVITY, Session, graph_lock, reset_graph_lock

SCHEMA = {"type": "object", "properties": {}, "additionalProperties": False}
SALES = {
    ("North", "2026-01-01"): 10,
    ("North", "2026-02-01"): 14,
    ("South", "2026-01-01"): 7,
    ("South", "2026-02-01"): None,
}


def query(measure, group_by, filters):
    rows = {}
    for (region, month), value in SALES.items():
        if filters.get("region", region) != region:
            continue
        key = tuple({"region": region, "month": month}[name] for name in group_by)
        if value is not None:
            rows[key] = rows.get(key, 0) + value
        else:
            rows.setdefault(key, None)
    return [{**dict(zip(group_by, key)), measure: value} for key, value in rows.items()]


def view(fn=query, **changes):
    settings = dict(
        output_id="generated",
        measures={"revenue": "Revenue (EUR)", "orders": "Orders"},
        dimensions={"region": "Region", "month": "Month"},
        temporal=("month",),
        filters={"region": ("North", "South")},
        query=fn,
    )
    settings.update(changes)
    return AgentView(**settings)


def layout(*tiles, title="Overview"):
    return {"title": title, "tiles": list(tiles)}


STAT = {"kind": "stat", "title": "Total revenue", "measure": "revenue"}
BAR = {"kind": "bar", "title": "By region", "measure": "revenue", "by": "region"}
LINE = {
    "kind": "line",
    "title": "Trend",
    "measure": "revenue",
    "by": "month",
    "series": "region",
}
TABLE = {"kind": "table", "title": "Regions", "measure": "revenue", "by": "region"}


def base():
    async def read(_):
        return {"total": 31}

    return ToolRegistry([AgentTool("read_sales", "Read sales", SCHEMA, read)])


def registry(agent_view=None, inner=None):
    installed = []

    async def install(value, commit):
        installed.append(value)
        commit()

    return ViewRegistry(inner or base(), agent_view or view(), install), installed


def tiles(agent_view, *specs):
    compiled = compile_layout(agent_view, layout(*specs))
    return resolve(agent_view, compiled)["view"]["tiles"]


@pytest.mark.parametrize(
    "changes,message",
    [
        ({"output_id": "bad id"}, "output_id"),
        ({"measures": {}}, "1 to 16 measures"),
        ({"measures": {"Revenue": "Revenue"}}, "lower-case"),
        ({"measures": {"revenue": ""}}, "label"),
        ({"dimensions": {"revenue": "Region"}}, "unique"),
        ({"dimensions": {"region": "Revenue (EUR)"}, "temporal": ()}, "unique"),
        ({"temporal": ("week",)}, "temporal"),
        ({"filters": {"region": ()}}, "filter"),
        ({"filters": {"region": ("North", "North")}}, "filter"),
        ({"filters": {"region": (1,)}}, "filter"),
        ({"query": None}, "callable"),
    ],
)
def test_view_declaration_is_validated(changes, message):
    with pytest.raises((ValueError, TypeError), match=message):
        view(**changes)


def test_async_query_is_rejected_because_tiles_render_in_a_tracked_context():
    async def fetch(measure, group_by, filters):
        return []

    with pytest.raises(TypeError, match="synchronous"):
        view(fetch)


def test_view_tools_are_read_only_and_reserved():
    tools, _ = registry()
    assert tools.get("build_view").read_only
    assert tools.get("clear_view").read_only
    assert [spec["name"] for spec in tools.public_spec()["tools"]] == [
        "read_sales",
        "build_view",
        "clear_view",
    ]

    async def handler(_):
        return {}

    for name in ("build_view", "clear_view"):
        taken = ToolRegistry([AgentTool(name, "Taken", SCHEMA, handler)])
        with pytest.raises(ValueError, match="reserved"):
            registry(inner=taken)


@pytest.mark.parametrize(
    "tile",
    [
        {**STAT, "measure": "profit"},
        {**BAR, "by": "country"},
        {**LINE, "series": "channel"},
        {**STAT, "filters": {"country": "NL"}},
        {**STAT, "filters": {"region": "West"}},
        {**STAT, "kind": "pie"},
        {**STAT, "spec": {"mark": "bar"}},
        {**STAT, "title": ""},
    ],
)
def test_undeclared_measures_dimensions_filters_and_kinds_are_rejected(tile):
    async def run():
        tools, installed = registry()
        with pytest.raises(ToolError) as error:
            await tools.execute("build_view", layout(tile))
        assert error.value.code == "invalid_arguments"
        assert installed == []

    asyncio.run(run())


@pytest.mark.parametrize(
    "tile,message",
    [
        ({**STAT, "by": "region"}, "stat"),
        ({"kind": "bar", "title": "Bars", "measure": "revenue"}, "dimension"),
        ({**LINE, "by": "region", "series": "month"}, "date"),
        ({**BAR, "series": "region"}, "differ"),
        ({**BAR, "series": "month"}, "series"),
        ({**STAT, "title": "   "}, "title"),
    ],
)
def test_tiles_that_cannot_be_drawn_are_rejected_with_a_correctable_reason(
    tile, message
):
    async def run():
        tools, installed = registry()
        with pytest.raises(ToolError, match=message) as error:
            await tools.execute("build_view", layout(tile))
        assert error.value.code == "invalid_view"
        assert installed == []

    asyncio.run(run())


def test_more_than_six_tiles_and_blank_view_titles_are_rejected():
    async def run():
        tools, installed = registry()
        with pytest.raises(ToolError):
            await tools.execute("build_view", layout(*[STAT] * 7))
        with pytest.raises(ToolError) as error:
            await tools.execute("build_view", layout(STAT, title="  "))
        assert error.value.code == "invalid_view"
        assert installed == []

    asyncio.run(run())


def test_build_view_installs_a_layout_and_returns_no_figures():
    calls = []

    def counting(measure, group_by, filters):
        calls.append(measure)
        return query(measure, group_by, filters)

    async def run():
        tools, installed = registry(view(counting))
        result = await tools.execute(
            "build_view", layout(STAT, {**BAR, "filters": {"region": "North"}})
        )
        assert calls == [], "composing a layout must not read app data"
        assert result["displayed"] is True and result["tiles"] == 2
        assert "31" not in json.dumps(result) and "rows" not in result
        assert len(installed) == 1
        assert installed[0]["tiles"][1]["filters"] == {"region": "North"}
        assert tools.changed
        assert await tools.execute("clear_view", {}) == {"cleared": True}
        assert installed[-1] is None

    asyncio.run(run())


def test_other_tools_pass_through_the_view_registry_unchanged():
    async def run():
        tools, _ = registry()
        assert await tools.execute("read_sales", {}) == {"total": 31}
        assert await tools.validate("read_sales", {}) == {}
        with pytest.raises(ToolError) as error:
            await tools.execute("missing", {})
        assert error.value.code == "unknown_tool"

    asyncio.run(run())


def test_stat_bar_line_and_table_tiles_carry_only_declared_columns():
    stat, bar, line, table = tiles(view(), STAT, BAR, LINE, TABLE)
    assert stat["state"] == "ok"
    assert (stat["value"], stat["label"]) == (31, "Revenue (EUR)")
    assert bar["chart"]["fields"] == {
        "Region": "nominal",
        "Revenue (EUR)": "quantitative",
    }
    assert bar["chart"]["rows"] == [
        {"Region": "North", "Revenue (EUR)": 24},
        {"Region": "South", "Revenue (EUR)": 7},
    ]
    assert (bar["chart"]["x"], bar["chart"]["y"]) == ("Region", "Revenue (EUR)")
    assert line["chart"]["fields"]["Month"] == "temporal"
    assert line["chart"]["color"] == "Region"
    assert {"Month": "2026-02-01", "Region": "South", "Revenue (EUR)": None} in line[
        "chart"
    ]["rows"]
    assert table["columns"] == ["Region", "Revenue (EUR)"]
    assert table["rows"] == [["North", 24], ["South", 7]]


def test_tile_filters_reach_the_query_as_declared_values_only():
    seen = []

    def spy(measure, group_by, filters):
        seen.append((measure, group_by, filters))
        return query(measure, group_by, filters)

    (stat,) = tiles(view(spy), {**STAT, "filters": {"region": "South"}})
    assert seen == [("revenue", (), {"region": "South"})]
    assert stat["value"] == 7


def test_a_narrowed_tile_carries_its_scope_and_an_unfiltered_one_does_not():
    narrowed, whole = tiles(view(), {**STAT, "filters": {"region": "South"}}, STAT)
    assert narrowed["scope"] == [["Region", "South"]]
    assert "scope" not in whole
    # The scope survives the states that carry no figure.
    (failed,) = tiles(
        view(lambda *_: {"revenue": 1}), {**BAR, "filters": {"region": "South"}}
    )
    assert failed["state"] == "error" and failed["scope"] == [["Region", "South"]]


def test_the_tool_schema_tells_the_model_what_each_id_means():
    tile = view().schema()["properties"]["tiles"]["items"]["properties"]
    assert tile["measure"]["description"] == "revenue = Revenue (EUR); orders = Orders"
    assert tile["by"]["description"] == "region = Region; month = Month (date)"


def test_a_chart_too_large_for_the_renderer_is_refused_on_the_server():
    def split(width):
        rows = [
            {
                "region": f"{i:03d}" + "x" * width,
                "channel": f"{i % 10}" + "y" * width,
                "revenue": i,
            }
            for i in range(500)
        ]
        return view(
            lambda *_: rows,
            dimensions={"region": "Region", "channel": "Channel"},
            temporal=(),
        )

    grouped = {**BAR, "series": "channel"}
    (tile,) = tiles(split(100), grouped)
    assert tile["state"] == "ok"
    (tile,) = tiles(split(297), grouped)
    assert tile["state"] == "error" and "Too much data to chart" in tile["message"]
    # The same rows are fine as a table, which the browser does not size-check.
    (tile,) = tiles(split(297), {**TABLE, "series": "channel"})
    assert tile["state"] == "ok" and len(json.dumps(tile)) > 300_000


def test_an_oversized_result_is_refused_without_copying_it():
    copies = []

    class Row(dict):
        def __deepcopy__(self, memo):
            copies.append(1)
            return Row(self)

    def rows(count):
        return [Row(region=str(i), revenue=i) for i in range(count)]

    (tile,) = tiles(view(lambda *_: rows(2)), BAR)
    assert tile["state"] == "ok" and len(copies) == 2  # rows are copied when used
    del copies[:]
    (tile,) = tiles(view(lambda *_: rows(501)), BAR)
    assert tile["state"] == "error" and copies == []


@pytest.mark.parametrize("empty", [None, []])
def test_no_rows_is_an_explicit_empty_state_never_a_zero(empty):
    for spec in (STAT, BAR, LINE, TABLE):
        (tile,) = tiles(view(lambda *_: empty), spec)
        assert tile["state"] == "empty"
        assert "value" not in tile and "chart" not in tile and "rows" not in tile


def test_an_absent_measure_stays_absent():
    (stat,) = tiles(view(lambda m, g, f: [{m: None}]), STAT)
    assert stat["state"] == "ok" and stat["value"] is None
    (bar,) = tiles(view(lambda m, g, f: [{"region": "North", m: None}]), BAR)
    assert bar["state"] == "empty"
    (table,) = tiles(view(lambda m, g, f: [{"region": "North", m: None}]), TABLE)
    assert table["rows"] == [["North", None]]


def test_a_failing_query_is_an_error_state_distinct_from_no_data(caplog):
    def broken(*_):
        raise RuntimeError("database password=hunter2 unreachable")

    def refused(*_):
        raise ToolError("no_access", "You cannot see revenue.")

    (failed,) = tiles(view(broken), STAT)
    assert failed["state"] == "error"
    assert "hunter2" not in json.dumps(failed)
    (denied,) = tiles(view(refused), STAT)
    assert denied == {
        "id": denied["id"],
        "kind": "stat",
        "title": "Total revenue",
        "state": "error",
        "message": "You cannot see revenue.",
    }


@pytest.mark.parametrize(
    "spec,rows",
    [
        (STAT, [{"revenue": 1}, {"revenue": 2}]),
        (STAT, [{"revenue": "12"}]),
        (STAT, [{"revenue": float("nan")}]),
        (STAT, [{"revenue": True}]),
        (STAT, [{"revenue": 1, "margin": 2}]),
        (STAT, {"revenue": 1}),
        (BAR, [{"region": "North"}]),
        (BAR, [{"region": None, "revenue": 1}]),
        (BAR, [{"region": "North", "revenue": 1}, {"region": "North", "revenue": 2}]),
        (LINE, [{"month": "January", "region": "North", "revenue": 1}]),
        (
            {k: v for k, v in LINE.items() if k != "series"},
            [
                {"month": "2026-01-01T00:00:00Z", "revenue": 1},
                {"month": "2026-01-01T01:00:00+01:00", "revenue": 2},
            ],
        ),
        (
            {k: v for k, v in LINE.items() if k != "series"},
            # Python reads this offset as one hour; the browser cannot parse it.
            [{"month": "2026-01-01T00:00:00+00:60", "revenue": 1}],
        ),
        (BAR, [{"region": str(i), "revenue": i} for i in range(501)]),
    ],
)
def test_rows_outside_the_tile_contract_never_render_as_data(spec, rows):
    (tile,) = tiles(view(lambda *_: rows), spec)
    assert tile["state"] == "error"
    assert set(tile) == {"id", "kind", "title", "state", "message"}


def test_moments_closer_than_a_millisecond_are_separate_table_rows():
    rows = [
        {"month": "2026-01-01T09:00:00.123001Z", "revenue": 1},
        {"month": "2026-01-01T09:00:00.123002Z", "revenue": 2},
    ]
    by_moment = {"measure": "revenue", "by": "month"}
    (tile,) = tiles(
        view(lambda *_: rows), {"kind": "table", "title": "Readings", **by_moment}
    )
    assert tile["state"] == "ok"
    assert [row[0] for row in tile["rows"]] == [row["month"] for row in rows]
    # A chart plots at the browser's millisecond, where the two would overlap.
    (tile,) = tiles(
        view(lambda *_: rows), {"kind": "line", "title": "Readings", **by_moment}
    )
    assert tile["state"] == "error"
    # One moment written twice is still one group in a table.
    same = [
        {"month": "2026-01-01T09:00:00.123001Z", "revenue": 1},
        {"month": "2026-01-01T10:00:00.123001+01:00", "revenue": 2},
    ]
    (tile,) = tiles(
        view(lambda *_: same), {"kind": "table", "title": "Readings", **by_moment}
    )
    assert tile["state"] == "error"


def test_too_many_series_asks_for_a_narrower_view():
    def rows(count):
        return [
            {"month": "2026-01-01", "region": f"R{i}", "revenue": i}
            for i in range(count)
        ]

    # The renderer has ten distinct series colors; an eleventh would repeat one.
    (tile,) = tiles(view(lambda *_: rows(10)), LINE)
    assert tile["state"] == "ok" and len(tile["chart"]["rows"]) == 10
    (tile,) = tiles(view(lambda *_: rows(11)), LINE)
    assert tile["state"] == "error" and "10 series" in tile["message"]


def test_one_failing_tile_does_not_blank_the_others():
    def partial(measure, group_by, filters):
        if group_by:
            raise RuntimeError("boom")
        return [{measure: 5}]

    stat, bar = tiles(view(partial), STAT, BAR)
    assert stat["value"] == 5 and bar["state"] == "error"


def test_an_unready_input_is_no_data_for_that_tile():
    def unready(*_):
        raise SilentException()

    compiled = compile_layout(view(unready), layout(STAT))
    (tile,) = resolve(view(unready), compiled, silent=(SilentException,))["view"][
        "tiles"
    ]
    assert tile["state"] == "empty"


def test_no_layout_resolves_to_an_empty_view():
    assert resolve(view(), None) == {"version": 1, "view": None}


class Agent:
    def __init__(self, steps):
        self.steps = steps
        self.histories = []

    async def run(self, message, history, tools, approve, *, thread_id):
        self.histories.append(list(history))
        async for event in self.steps(message, tools, approve):
            yield event


def chat(steps, tools=None, **settings):
    events, installed = [], []

    async def send(event):
        events.append(event)

    async def install(value, commit):
        installed.append(value)
        commit()

    agent = Agent(steps)
    session = ChatSession(
        agent,
        tools or base(),
        send,
        view=view(),
        install_view=install,
        **settings,
    )
    return session, agent, events, installed


def ask(session, text, request="request_0001"):
    return session.handle(
        {
            "version": 1,
            "session": session.nonce,
            "requestId": request,
            "message": text,
        }
    )


def test_a_view_only_turn_is_acknowledged_without_approval_or_undo_changes():
    async def steps(message, tools, approve):
        assert tools.get("build_view").read_only
        await tools.execute("build_view", layout(STAT, BAR))
        return
        yield

    async def run():
        session, agent, events, installed = chat(steps)
        receipt = ("action_1", "set_period", {}, {}, 0.0)
        session.last_action = receipt
        await ask(session, "Build me an overview")
        kinds = [event["type"] for event in events]
        assert "approval_required" not in kinds and "action_applied" not in kinds
        assert kinds[-1] == "done"
        assert [e["text"] for e in events if e["type"] == "delta"] == [
            "View updated on the page."
        ]
        assert session.last_action is receipt
        assert len(installed) == 1 and len(installed[0]["tiles"]) == 2
        assert session.history[-1]["content"] == "View updated on the page."
        told = session._turn_history()[-1]["content"]
        assert '"measure": "revenue"' in told and "31" not in told

    asyncio.run(run())


def test_a_turn_without_answer_or_view_still_fails_as_empty():
    async def steps(message, tools, approve):
        return
        yield

    async def run():
        session, _, events, _ = chat(steps)
        await ask(session, "Hello")
        assert events[-1]["type"] == "error"

    asyncio.run(run())


def test_stop_keeps_a_committed_view_and_new_chat_clears_it():
    async def steps(message, tools, approve):
        await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Working"}
        await asyncio.Event().wait()

    async def run():
        session, _, events, installed = chat(steps)
        task = asyncio.create_task(ask(session, "Build"))
        while not any(event["type"] == "delta" for event in events):
            await asyncio.sleep(0)
        await session.handle(
            {
                "version": 1,
                "session": session.nonce,
                "requestId": "request_0002",
                "action": "cancel",
            }
        )
        await task
        assert events[-1]["message"].startswith("Stopped")
        assert len(installed) == 1 and installed[0] is not None
        assert "interrupted" in session.history[-1]["content"]
        assert session.views.current().startswith("Generated view on the page (")
        await session.handle(
            {
                "version": 1,
                "session": session.nonce,
                "requestId": "request_0003",
                "action": "reset",
            }
        )
        assert installed[-1] is None and len(installed) == 2
        assert events[-1]["type"] == "reset"

    asyncio.run(run())


def test_views_and_inline_charts_work_in_the_same_session():
    async def chart_rows(_):
        return ChartData(
            [{"region": "North", "sales": 12}],
            {"region": "nominal", "sales": "quantitative"},
            "Sales",
        )

    tools = ToolRegistry([AgentTool("sales", "Read sales", SCHEMA, chart_rows)])

    async def steps(message, registry, approve):
        metadata = await registry.execute("sales", {})
        assert "rows" not in metadata
        await registry.execute(
            "render_chart",
            {
                "dataset_id": metadata["dataset_id"],
                "kind": "bar",
                "x": "region",
                "y": "sales",
                "title": "Sales by region",
            },
        )
        await registry.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Done."}

    async def run():
        session, _, events, installed = chat(steps, tools, charts=True)
        names = [spec["name"] for spec in session.tools.public_spec()["tools"]]
        assert names == ["sales", "render_chart", "build_view", "clear_view"]
        await ask(session, "Chart and view")
        assert [e["type"] for e in events].count("chart") == 1
        assert events[-1]["type"] == "done" and len(installed) == 1
        content = session.history[-1]["content"]
        assert "Chart snapshots" in content and "Generated view" not in content
        assert session.views.current().startswith("Generated view on the page (")

    asyncio.run(run())


def test_view_settings_are_validated_by_the_chat_session():
    async def send(_):
        return None

    with pytest.raises(TypeError, match="AgentView"):
        ChatSession(Agent(None), base(), send, view=object(), install_view=send)
    with pytest.raises(TypeError, match="install_view"):
        ChatSession(Agent(None), base(), send, view=view())


class OutputSession(Session):
    def __init__(self):
        super().__init__()
        self.outputs = {}
        self.output_options = {}

    def output(self, *, id, suspend_when_hidden=True):
        def bind(renderer):
            self.outputs[id] = renderer
            self.output_options[id] = suspend_when_hidden
            return renderer

        return bind


def registered(monkeypatch, steps, agent_view, inputs=None):
    from shinyhub_agent import _shiny, register

    reset_graph_lock(monkeypatch)
    chats = []
    original = _shiny.ChatSession

    def capture(*args, **kwargs):
        chats.append(original(*args, **kwargs))
        return chats[-1]

    monkeypatch.setattr(_shiny, "ChatSession", capture)
    session = OutputSession()

    async def read(_):
        return {"total": 31}

    tools = register(
        session=session,
        input=inputs or Inputs({}),
        tools=[AgentTool("read_sales", "Read sales", SCHEMA, read)],
        chat=Agent(steps),
        view=agent_view,
    )
    return session, chats[0], tools


def test_register_requires_a_chat_agent_and_an_agent_view():
    from shinyhub_agent import register

    async def handler(_):
        return {}

    tool = AgentTool("read_sales", "Read sales", SCHEMA, handler)
    with pytest.raises(ValueError, match="view requires a chat agent"):
        register(session=OutputSession(), input=Inputs({}), tools=[tool], view=view())
    with pytest.raises(TypeError, match="AgentView"):
        register(
            session=OutputSession(),
            input=Inputs({}),
            tools=[tool],
            chat=Agent(None),
            view="generated",
        )


def test_view_tools_stay_out_of_browser_discovery(monkeypatch):
    async def run():
        async def steps(message, tools, approve):
            return
            yield

        session, _, tools = registered(monkeypatch, steps, view())
        try:
            assert tools.get("build_view") is None
            assert [spec["name"] for spec in tools.public_spec()["tools"]] == [
                "read_sales"
            ]
            with pytest.raises(ToolError) as error:
                await tools.execute("build_view", layout(STAT))
            assert error.value.code == "unknown_tool"
            assert VIEW_DISMISS_INPUT in session.bookmark.exclude
        finally:
            for fn in session.ended:
                fn()

    asyncio.run(run())


def test_tiles_recompute_when_app_state_changes_without_a_chat_request(monkeypatch):
    async def run():
        scale = reactive.Value(1)
        queries = []

        def live(measure, group_by, filters):
            queries.append(scale.get())
            return [{measure: 10 * scale.get()}]

        async def steps(message, tools, approve):
            await tools.execute("build_view", layout(STAT))
            yield {"type": "delta", "text": "Built."}

        session, chat_session, _ = registered(monkeypatch, steps, view(live))
        renderer = session.outputs["generated"]
        assert session.output_options["generated"] is False
        seen = []

        async with graph_lock():

            @reactive.effect(session=None)
            async def page():
                seen.append(await renderer.render())

            await reactive.flush()
        try:
            assert seen == [{"version": 1, "view": None}]
            assert queries == []

            from shinyhub_agent._shiny import CHAT_EVENT_MESSAGE

            await ask(chat_session, "Build")
            if not LEGACY_REACTIVITY:
                await reactive.flush()
            assert seen[-1]["view"]["tiles"][0]["value"] == 10
            chat_events = [m for m in session.messages if m[0] == CHAT_EVENT_MESSAGE]
            sent = len(chat_events)
            assert chat_events[-1][1]["type"] == "done"

            async with graph_lock():
                scale.set(3)
                await reactive.flush()
            assert seen[-1]["view"]["tiles"][0]["value"] == 30
            assert queries[-1] == 3
            assert len([m for m in session.messages if m[0] == CHAT_EVENT_MESSAGE]) == sent
            assert not chat_session.lock.locked()

            await chat_session.views.reset()
            if not LEGACY_REACTIVITY:
                await reactive.flush()
            assert seen[-1] == {"version": 1, "view": None}
        finally:
            page.destroy()
            for fn in session.ended:
                fn()

    asyncio.run(run())


def test_new_chat_refused_or_failed_is_answered_so_the_composer_is_released(caplog):
    async def steps(message, tools, approve):
        if message == "Build":
            await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Done."}

    async def run():
        events, broken = [], []

        async def send(event):
            events.append(event)

        async def install(value, commit):
            if broken:
                raise RuntimeError("session went away")
            commit()

        def reset(request):
            return session.handle(
                {
                    "version": 1,
                    "session": session.nonce,
                    "requestId": request,
                    "action": "reset",
                }
            )

        session = ChatSession(Agent(steps), base(), send, view=view(), install_view=install)
        await ask(session, "Build")
        thread, history = session.thread_id, list(session.history)
        async with session.lock:
            await reset("request_0002")
        assert events[-1]["type"] == "error" and events[-1]["requestId"] == "request_0002"
        broken.append(True)
        await reset("request_0003")
        assert events[-1]["type"] == "error" and events[-1]["requestId"] == "request_0003"
        assert "session went away" not in events[-1]["message"]
        # A new chat that did not start leaves the old one whole.
        assert session.thread_id == thread and session.history == history
        assert session.views.layout is not None and not session.lock.locked()
        broken.clear()
        await reset("request_0004")
        assert events[-1]["type"] == "reset" and session.history == []

    asyncio.run(run())


def test_new_chat_clears_the_conversation_when_the_view_is_gone_but_its_update_failed(
    caplog,
):
    async def steps(message, tools, approve):
        await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Done."}

    async def run():
        events, broken = [], []

        async def send(event):
            events.append(event)

        async def install(value, commit):
            commit()
            if broken:
                raise RuntimeError("an unrelated effect failed in the flush")

        session = ChatSession(Agent(steps), base(), send, view=view(), install_view=install)
        await ask(session, "Build")
        thread = session.thread_id
        broken.append(True)
        await session.handle(
            {
                "version": 1,
                "session": session.nonce,
                "requestId": "request_0002",
                "action": "reset",
            }
        )
        # The view left the page, so the conversation that built it goes too.
        assert session.views.layout is None
        assert events[-1] == {**events[-1], "type": "reset", "requestId": "request_0002"}
        assert session.history == [] and session.thread_id != thread
        assert "an unrelated effect failed" in caplog.text

    asyncio.run(run())


def test_a_build_that_times_out_after_reaching_the_page_can_still_be_removed():
    async def run():
        installed, settle = [], asyncio.Event()

        async def install(value, commit):
            installed.append(value)
            commit()
            if value is not None:
                await settle.wait()  # the page is still flushing

        tools = ViewRegistry(base(), view(), install)
        with pytest.raises(ToolError) as error:
            await tools.execute("build_view", layout(STAT), timeout=0.05)
        assert error.value.code == "timeout"
        assert tools.layout is not None and tools.changed
        assert '"measure": "revenue"' in tools.current()
        await tools.reset()
        assert installed[-1] is None and tools.layout is None

    asyncio.run(run())


def test_a_build_that_never_reached_the_page_is_not_recorded():
    async def run():
        async def install(value, commit):
            await asyncio.Event().wait()  # waiting for the session, nothing assigned

        tools = ViewRegistry(base(), view(), install)
        with pytest.raises(ToolError):
            await tools.execute("build_view", layout(STAT), timeout=0.05)
        assert tools.layout is None and not tools.changed and tools.current() == ""

    asyncio.run(run())


def test_bars_over_dates_cannot_be_split_by_series():
    async def run():
        tools, installed = registry()
        tile = {**BAR, "by": "month", "series": "region"}
        with pytest.raises(ToolError) as error:
            await tools.execute("build_view", layout(tile))
        assert error.value.code == "invalid_view"
        assert "line" in str(error.value) and installed == []
        await tools.execute("build_view", layout({**BAR, "by": "month"}))
        await tools.execute("build_view", layout({**tile, "kind": "line"}))
        assert len(installed) == 2

    asyncio.run(run())


def test_the_model_is_told_the_current_view_after_its_note_left_history():
    async def steps(message, tools, approve):
        if message == "Build":
            await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Done."}

    async def run():
        session, agent, _, _ = chat(steps, history_exchanges=1)
        await ask(session, "Build")
        await ask(session, "Thanks", "request_0002")
        assert "Generated view" not in session.history[-1]["content"]
        await ask(session, "Make the tile a table", "request_0003")
        seen = agent.histories[-1]
        assert len(seen) == 2 and seen[-1]["role"] == "assistant"
        assert seen[-1]["content"].startswith("Generated view on the page")
        assert seen[-1]["content"].endswith("Done.")
        assert '"measure": "revenue"' in seen[-1]["content"]
        assert "Generated view" not in session.history[-1]["content"]

    asyncio.run(run())


def test_a_long_answer_does_not_push_the_view_note_out_of_the_request():
    async def steps(message, tools, approve):
        if message == "Build":
            await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "x" * 32768 if message == "Build" else "Ok."}

    async def run():
        session, agent, _, _ = chat(steps, max_answer_chars=32768)
        await ask(session, "Build")
        await ask(session, "Next", "request_0002")
        said = agent.histories[-1][-1]["content"]
        assert said.startswith("Generated view on the page")
        assert said.count("Generated view on the page") == 1
        assert said.endswith("x")
        # The backends keep the first 32,768 characters of a message.
        assert "Total revenue" in said[:32768]

    asyncio.run(run())


def test_a_view_dismissed_right_after_it_was_built_is_not_described_as_shown():
    async def steps(message, tools, approve):
        if message == "Build":
            await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Done."}

    async def run():
        session, agent, _, _ = chat(steps)
        await ask(session, "Build")
        await session.views.reset()
        await ask(session, "And now?", "request_0002")
        said = agent.histories[-1][-1]["content"]
        assert said.startswith("Generated view on the page: none.")
        assert said.count("Generated view") == 1 and "Total revenue" not in said

    asyncio.run(run())


def test_the_model_learns_of_a_dismissal_and_a_new_chat_forgets_the_view():
    async def steps(message, tools, approve):
        if message == "Build":
            await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Done."}

    async def run():
        session, agent, _, _ = chat(steps)
        await ask(session, "Build")
        await ask(session, "Next", "request_0002")
        note = agent.histories[-1][-1]["content"]
        assert note.count("Generated view on the page") == 1  # not repeated
        assert note.startswith("Generated view on the page") and note.endswith("Done.")
        await session.views.reset()
        await ask(session, "And now?", "request_0003")
        assert agent.histories[-1][-1]["content"].startswith(
            "Generated view on the page: none."
        )
        await session.handle(
            {
                "version": 1,
                "session": session.nonce,
                "requestId": "request_0004",
                "action": "reset",
            }
        )
        await ask(session, "Hello", "request_0005")
        await ask(session, "Again", "request_0006")
        assert "Generated view" not in agent.histories[-1][-1]["content"]

    asyncio.run(run())


def test_a_question_cannot_start_while_new_chat_is_still_removing_the_view():
    async def steps(message, tools, approve):
        if message == "Build":
            await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Done."}

    async def run():
        events, removing, release = [], asyncio.Event(), asyncio.Event()

        async def send(event):
            events.append(event)

        async def install(value, commit):
            if value is None:
                removing.set()
                await release.wait()  # the session is busy with another callback
            commit()

        agent = Agent(steps)
        session = ChatSession(agent, base(), send, view=view(), install_view=install)
        await ask(session, "Build")
        thread = session.thread_id
        reset = asyncio.create_task(
            session.handle(
                {
                    "version": 1,
                    "session": session.nonce,
                    "requestId": "request_0002",
                    "action": "reset",
                }
            )
        )
        await removing.wait()
        await ask(session, "Too early", "request_0003")
        assert len(agent.histories) == 1  # the agent did not run on the old thread
        assert events[-1]["type"] == "error" and session.thread_id == thread
        release.set()
        await reset
        assert events[-1]["type"] == "reset" and session.thread_id != thread
        await ask(session, "Hello", "request_0004")
        assert agent.histories[-1] == []

    asyncio.run(run())


def test_the_current_view_is_stated_even_when_no_history_remains():
    async def steps(message, tools, approve):
        if message == "Build":
            await tools.execute("build_view", layout(STAT))
        yield {"type": "delta", "text": "Done."}

    async def run():
        session, agent, _, _ = chat(steps)
        await ask(session, "Build")
        session.history.clear()  # as a successful Undo leaves it
        await ask(session, "Add a table", "request_0002")
        seen = agent.histories[-1]
        assert [item["role"] for item in seen] == ["user", "assistant"]
        assert '"measure": "revenue"' in seen[-1]["content"]

    asyncio.run(run())

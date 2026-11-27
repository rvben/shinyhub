"""Local-only browser integration fixture; no external model calls."""

import asyncio
from importlib.metadata import distribution
from pathlib import Path

import shinyhub_agent
from shiny import App, reactive, render, ui
from shinyhub_agent import (
    AgentTool,
    AgentView,
    ToolError,
    agent_dependency,
    agent_view_output,
    chat_dependency,
    register,
)


installed_module = distribution("shinyhub-agent").locate_file("shinyhub_agent/__init__.py")
if Path(shinyhub_agent.__file__).resolve() != Path(installed_module).resolve():
    raise RuntimeError("The browser fixture requires an installed shinyhub-agent wheel")


class StubAgent:
    async def run(self, message, history, tools, approve, *, thread_id):
        if message == "wait":
            await asyncio.Event().wait()
        if "dashboard" in message.lower():
            # A view-only turn: the tiles are the answer, so no text follows.
            yield {
                "type": "tool_started",
                "name": "build_view",
                "description": "Build view",
                "readOnly": True,
            }
            await tools.execute(
                "build_view",
                {
                    "title": "Sales overview",
                    "tiles": [
                        {"kind": "stat", "title": "Total revenue", "measure": "revenue"},
                        {
                            "kind": "bar",
                            "title": "Revenue by region",
                            "measure": "revenue",
                            "by": "region",
                        },
                        {
                            "kind": "stat",
                            "title": "One region",
                            "measure": "revenue",
                            "filters": {"region": "North"},
                        },
                    ],
                },
            )
            yield {"type": "tool_finished", "name": "build_view", "ok": True}
        elif "year" in message.lower():
            yield {
                "type": "tool_started",
                "name": "set_period",
                "description": "Change period",
                "readOnly": False,
            }
            result = await approve("set_period", {"period": "year"})
            yield {"type": "action_applied", "name": "set_period", "result": result}
            yield {"type": "tool_finished", "name": "set_period", "ok": True}
            yield {"type": "delta", "text": "Showing year."}
        else:
            yield {
                "type": "tool_started",
                "name": "get_period",
                "description": "Read period",
                "readOnly": True,
            }
            result = await tools.execute("get_period", {})
            yield {"type": "tool_finished", "name": "get_period", "ok": True}
            yield {"type": "delta", "text": f"Current period: {result['period']}."}


app_ui = ui.page_fluid(
    agent_dependency(),
    chat_dependency(),
    ui.h1("Agent chat fixture"),
    ui.input_select("period", "Period", ["week", "year"]),
    ui.output_text("selected"),
    agent_view_output("generated"),
)

REVENUE = {"week": {"North": 4, "South": 6}, "year": {"North": 200, "South": 320}}


def server(input, output, session):
    period = reactive.value("week")

    @reactive.effect
    @reactive.event(input.period)
    def manual():
        period.set(input.period())

    async def get_period(_):
        return {"period": period.get()}

    async def set_period(args):
        previous_period = period.get()
        period.set(args["period"])
        ui.update_select("period", selected=args["period"])
        return {"period": period.get(), "previous_period": previous_period}

    async def undo_period(_args, result):
        if period.get() != result["period"]:
            raise ToolError("stale_view", "The view changed again.")
        period.set(result["previous_period"])
        ui.update_select("period", selected=result["previous_period"])
        return await get_period({})

    def sales(measure, group_by, filters):
        # Reading the reactive value here is what keeps the tiles live.
        regions = REVENUE[period.get()]
        if "region" in filters:
            regions = {filters["region"]: regions[filters["region"]]}
        if group_by:
            return [{"region": name, measure: value} for name, value in regions.items()]
        return [{measure: sum(regions.values())}]

    register(
        session=session,
        input=input,
        tools=[
            AgentTool(
                "get_period",
                "Read period",
                {
                    "type": "object",
                    "properties": {},
                    "additionalProperties": False,
                },
                get_period,
            ),
            AgentTool(
                "set_period",
                "Change period",
                {
                    "type": "object",
                    "properties": {
                        "period": {"type": "string", "enum": ["week", "year"]}
                    },
                    "required": ["period"],
                    "additionalProperties": False,
                },
                set_period,
                read_only=False,
                confirmation="Change period to year?",
                describe=lambda args: f"Reporting period = {args['period']}",
                receipt=lambda _args, result: f"View set to {result['period']}",
                undo=undo_period,
                undo_valid=lambda _args, result: period.get() == result["period"],
            ),
        ],
        chat=StubAgent(),
        view=AgentView(
            output_id="generated",
            measures={"revenue": "Revenue (EUR)"},
            dimensions={"region": "Region"},
            filters={"region": ["North", "South"]},
            query=sales,
        ),
    )

    @output
    @render.text
    def selected():
        return f"Selected: {period.get()}"


app = App(app_ui, server)

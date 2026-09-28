"""Local-only browser integration fixture; no external model calls."""

from shiny import App, reactive, render, ui
from shinyhub_agent import AgentTool, agent_dependency, chat_dependency, register


class StubAgent:
    async def run(self, message, history, tools, approve, *, thread_id):
        if "year" in message.lower():
            result = await approve("set_period", {"period": "year"})
            yield {"type": "action_applied", "name": "set_period", "result": result}
            yield {"type": "delta", "text": "Showing year."}
        else:
            result = await tools.execute("get_period", {})
            yield {"type": "delta", "text": f'Current period: {result["period"]}.'}


app_ui = ui.page_fluid(
    agent_dependency(), chat_dependency(),
    ui.h1("Agent chat fixture"),
    ui.input_select("period", "Period", ["week", "year"]),
    ui.output_text("selected"),
)


def server(input, output, session):
    period = reactive.value("week")

    @reactive.effect
    @reactive.event(input.period)
    def manual():
        period.set(input.period())

    async def get_period(_):
        return {"period": period.get()}

    async def set_period(args):
        period.set(args["period"])
        ui.update_select("period", selected=args["period"], session=session)
        return await get_period({})

    register(session=session, input=input, tools=[
        AgentTool("get_period", "Read period", {
            "type": "object", "properties": {}, "additionalProperties": False,
        }, get_period),
        AgentTool("set_period", "Change period", {
            "type": "object", "properties": {"period": {"type": "string", "enum": ["week", "year"]}},
            "required": ["period"], "additionalProperties": False,
        }, set_period, read_only=False, confirmation="Change period to year?"),
    ], chat=StubAgent())

    @output
    @render.text
    def selected():
        return f"Selected: {period.get()}"


app = App(app_ui, server)

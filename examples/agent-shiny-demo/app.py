"""Real Shiny session state exposed through explicit agent capabilities."""

import os
from pathlib import Path

from shiny import App, reactive, render, ui
from shinyhub_agent import AGUIChat, AgentTool, BedrockChat, OpenAIChat, ToolError, agent_dependency, chat_dependency, register

VIEWS = {
    "This week": {"requests": "1.42M", "p95_latency_ms": 148, "error_rate": "0.3%"},
    "Last week": {"requests": "1.16M", "p95_latency_ms": 164, "error_rate": "0.5%"},
    "This year": {"requests": "54.8M", "p95_latency_ms": 155, "error_rate": "0.4%"},
}
OPENAI_API_KEY = os.environ.get("OPENAI_API_KEY", "").strip()
AGUI_URL = os.environ.get("SHINYHUB_AGENT_AGUI_URL", "").strip()
AGUI_TOKEN = os.environ.get("SHINYHUB_AGENT_AGUI_TOKEN", "").strip()
BEDROCK_MODEL_ID = os.environ.get("SHINYHUB_AGENT_BEDROCK_MODEL_ID", "").strip()
INSTRUCTIONS = (
    "You assist with an illustrative operations dashboard. Use the app tools for "
    "any statement about the current view or metrics. The data is synthetic. "
    "When asked to change the period, call set_dashboard_period and wait for "
    "approval and the applied result before saying the view changed. "
    "Do not infer incidents, causes, or timelines that are not shown. "
    "Answer briefly and name the reporting period."
)
if AGUI_URL:
    CHAT = AGUIChat(AGUI_URL, AGUI_TOKEN)
elif BEDROCK_MODEL_ID:
    CHAT = BedrockChat(BEDROCK_MODEL_ID, INSTRUCTIONS, os.environ["AWS_REGION"])
elif OPENAI_API_KEY:
    CHAT = OpenAIChat(OPENAI_API_KEY, INSTRUCTIONS)
else:
    CHAT = None

STYLES = """
body { background:#0a101d; color:#edf3ff; }
.agent-demo { max-width:960px; margin:0 auto; padding:48px 24px; }
.agent-demo h1 { font-size:clamp(2rem,5vw,3.2rem); letter-spacing:-.04em; }
.agent-demo p { color:#abbdd3; max-width:660px; line-height:1.6; }
.agent-demo .form-select { max-width:240px; background:#172338; color:#edf3ff; border-color:#45617b; }
.metrics { display:grid; grid-template-columns:repeat(3,1fr); gap:12px; margin-top:30px; }
.metric { padding:24px; background:#142034; border:1px solid #314861; border-radius:14px; }
.metric span { display:block; color:#adc0d4; font-size:.87rem; }
.metric strong { display:block; margin-top:8px; font-size:2rem; letter-spacing:-.03em; }
@media(max-width:650px) { .metrics { grid-template-columns:1fr; } }
"""

app_ui = ui.page_fluid(
    agent_dependency(),
    *([chat_dependency()] if CHAT else []),
    ui.tags.style(STYLES),
    ui.include_js(Path(__file__).parent / "www" / "try-tool.js"),
    ui.div(
        ui.h1("Operations pulse"),
        ui.p("A live Shiny dashboard with synthetic metrics. Its period filter is owned by the Shiny session and exposed through two typed agent tools."),
        ui.input_select("period", "Reporting period", list(VIEWS)),
        ui.tags.button("Try agent tool: show this year", id="try-agent-tool", type="button"),
        ui.tags.p("", id="agent-tool-result", role="status"),
        ui.output_ui("metrics"),
        class_="agent-demo",
    ),
    title="Operations pulse · ShinyHub",
)


def server(input, output, session):
    selected_period = reactive.value("This week")

    @reactive.effect
    @reactive.event(input.period)
    def _manual_filter_change():
        selected_period.set(input.period())

    async def get_dashboard_state(_args):
        period = selected_period.get()
        return {"period": period, "synthetic": True, **VIEWS[period]}

    async def set_dashboard_period(args):
        period = args["period"]
        previous_period = selected_period.get()
        selected_period.set(period)
        ui.update_select("period", selected=period, session=session)
        return {**(await get_dashboard_state({})), "previous_period": previous_period}

    async def undo_dashboard_period(_args, result):
        if selected_period.get() != result["period"]:
            raise ToolError("stale_view", "The reporting period changed again.")
        previous_period = result["previous_period"]
        selected_period.set(previous_period)
        ui.update_select("period", selected=previous_period, session=session)
        return await get_dashboard_state({})

    register(session=session, input=input, tools=[
        AgentTool(
            "get_dashboard_state",
            "Read the selected period and synthetic dashboard metrics.",
            {"type": "object", "properties": {}, "additionalProperties": False},
            get_dashboard_state,
        ),
        AgentTool(
            "set_dashboard_period",
            "Change this viewer's reporting period and return the applied state.",
            {"type": "object", "properties": {"period": {"type": "string", "enum": list(VIEWS)}},
             "required": ["period"], "additionalProperties": False},
            set_dashboard_period,
            read_only=False,
            confirmation="Change this dashboard's reporting period?",
            receipt=lambda _args, result: f"View set to {result['period']}",
            undo=undo_dashboard_period,
        ),
    ], chat=CHAT)

    @output
    @render.ui
    def metrics():
        values = VIEWS[selected_period.get()]
        return ui.div(
            ui.div(ui.span("Requests"), ui.strong(values["requests"]), class_="metric"),
            ui.div(ui.span("P95 latency"), ui.strong(f'{values["p95_latency_ms"]} ms'), class_="metric"),
            ui.div(ui.span("Error rate"), ui.strong(values["error_rate"]), class_="metric"),
            class_="metrics",
        )


app = App(app_ui, server)

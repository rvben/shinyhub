"""Illustrative app chat and browser tool surface for a ShinyHub app."""

from __future__ import annotations

import json
import os
import re
import uuid
from pathlib import Path
from urllib.parse import urlsplit

import httpx
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import HTMLResponse, StreamingResponse
from pydantic import BaseModel, Field

app = FastAPI(docs_url=None, redoc_url=None)
DASHBOARD = json.loads(Path(__file__).with_name("dashboard.json").read_text())
PAGE = Path(__file__).with_name("index.html").read_text().replace(
    "__DASHBOARD_VIEWS__", json.dumps(DASHBOARD["views"])
)
AGENT_URL = os.environ.get("AGENT_DEMO_AGUI_URL", "").strip()
AGENT_TOKEN = os.environ.get("AGENT_DEMO_AGUI_TOKEN", "").strip()
OPENAI_API_KEY = os.environ.get("OPENAI_API_KEY", "").strip()
OPENAI_MODEL = os.environ.get("AGENT_DEMO_OPENAI_MODEL", "gpt-4.1-mini").strip()
SCRIPTED_ONLY = os.environ.get("AGENT_DEMO_SCRIPTED_ONLY", "false").lower() in ("true", "1", "yes")
CHAT_ENABLED = os.environ.get("AGENT_DEMO_ENABLE_CHAT", "true").lower() in ("true", "1", "yes")
WEBMCP_ENABLED = os.environ.get("AGENT_DEMO_ENABLE_WEBMCP", "true").lower() in ("true", "1", "yes")
MAX_EVENT_BYTES = 64 * 1024


class ChatRequest(BaseModel):
    message: str = Field(min_length=1, max_length=2000)
    thread_id: str = Field(min_length=1, max_length=100)
    messages: list[dict] = Field(default_factory=list, max_length=20)
    view: str = Field(default="This week", pattern=r"^(This week|Last week|This year)$")


def sse(data: dict) -> str:
    return "data: " + json.dumps(data, separators=(",", ":")) + "\n\n"


def demo_answer(message: str, view: str) -> str:
    period = view.lower()
    data = DASHBOARD["views"][view]
    requests = data["requests"].replace("M", " million")
    latency = data["latency"]
    errors = data["errors"]
    lower = message.lower()
    if "incident" in lower or "error" in lower:
        return (
            f"In the illustrative {period} view, the error rate is {errors}. "
            "This dashboard does not show an error timeline or incident records, "
            "so I cannot tell when errors occurred or what caused them."
        )
    if "latency" in lower or "slow" in lower or "response" in lower:
        return (
            f"For {period}, illustrative p95 latency is {latency}. "
            "The chart shows request volume, not latency over time. "
            "I would check traces before drawing a conclusion about slow requests."
        )
    if "request" in lower or "traffic" in lower:
        return (
            f"The illustrative {period} view shows {requests} requests. "
            f"{data['peak']} has the highest volume in the displayed series."
        )
    return (
        "I can explain the illustrative request volume, p95 latency, and "
        "error rate in the selected view. Try asking ‘What happened to latency?’ "
        "or ‘Were there any errors?’"
    )


def requested_view(message: str) -> str | None:
    if not re.search(r"\b(set|show|switch|change)\b", message, re.IGNORECASE):
        return None
    for period in DASHBOARD["views"]:
        if period.lower() in message.lower():
            return period
    return None


def parse_agui_event(data: str) -> dict | None:
    """Project AG-UI's text/lifecycle events onto this demo's read-only UI."""
    event = json.loads(data)
    kind = event.get("type")
    if kind in ("TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CHUNK"):
        delta = event.get("delta", "")
        if isinstance(delta, str) and delta:
            return {"type": "delta", "text": delta}
    if kind == "RUN_STARTED":
        return {"type": "status", "text": "Working on your answer"}
    if kind == "TOOL_CALL_START":
        return {"type": "status", "text": "Using agent tools"}
    if kind == "RUN_ERROR":
        return {"type": "error", "message": "The agent could not finish this reply."}
    if kind == "RUN_FINISHED":
        return {"type": "done"}
    return None


def agent_input(body: ChatRequest) -> dict:
    history = []
    for item in body.messages:
        role, content = item.get("role"), item.get("content")
        if role not in ("user", "assistant") or not isinstance(content, str):
            continue
        history.append({"id": str(uuid.uuid4()), "role": role, "content": content[:2000]})
    history.append({"id": str(uuid.uuid4()), "role": "user", "content": body.message})
    return {
        "threadId": body.thread_id,
        "runId": str(uuid.uuid4()),
        "state": {},
        "messages": history,
        "tools": [],
        "context": [{"description": "Selected dashboard view", "value": body.view}],
        "forwardedProps": {},
    }


OPENAI_TOOLS = [
    {
        "type": "function",
        "name": "get_dashboard_state",
        "description": "Read the synthetic metrics and labeled volume index for the visitor's selected dashboard period.",
        "parameters": {"type": "object", "properties": {}, "required": [], "additionalProperties": False},
        "strict": True,
    },
    {
        "type": "function",
        "name": "compare_dashboard_periods",
        "description": "Compare the synthetic metrics for This week and Last week, including computed differences.",
        "parameters": {"type": "object", "properties": {}, "required": [], "additionalProperties": False},
        "strict": True,
    },
    {
        "type": "function",
        "name": "set_dashboard_period",
        "description": "Change the visitor's visible dashboard view to This week, Last week, or This year. Use when asked to set, show, switch, or change the view/filter.",
        "parameters": {
            "type": "object",
            "properties": {"period": {"type": "string", "enum": ["This week", "Last week", "This year"]}},
            "required": ["period"],
            "additionalProperties": False,
        },
        "strict": True,
    },
]

OPENAI_INSTRUCTIONS = (
    "You are the assistant for an illustrative operations dashboard. "
    "Use the dashboard tools for any question about displayed values. The values are synthetic, "
    "not live operational data. Answer concisely and identify the selected period. "
    "Latency is the 95th percentile (p95), never an average or mean. "
    "When asked to change the view or period, use set_dashboard_period, then give one brief "
    "observation from the new view. Do not repeat the action receipt or end with an invitation. "
    "You may compare periods, but do not infer causes, incidents, or trends unsupported by the "
    "available metrics. The chart bars are a relative volume index, not request counts; "
    "the weekly views use days and This year uses illustrative months through September. "
    "If the user asks for data the tools do not provide, say what is missing. "
    "Only set_dashboard_period changes the dashboard; the other tools only read data."
)


def dashboard_tool(name: str, view: str) -> dict:
    views = DASHBOARD["views"]
    if name == "get_dashboard_state":
        data = views[view]
        return {
            "synthetic": True,
            "selected_period": view,
            "requests": data["requests"],
            "p95_latency": data["latency"],
            "error_rate": data["errors"],
            "volume_index": data["bars"],
            "volume_labels": data["labels"],
            "volume_peak": data["peak"],
        }
    if name == "compare_dashboard_periods":
        current, previous = views["This week"], views["Last week"]
        return {
            "synthetic": True,
            "selected_period": view,
            "periods": {period: dashboard_tool("get_dashboard_state", period) for period in ("This week", "Last week")},
            "difference_this_minus_last": {
                "requests_millions": round(float(current["requests"][:-1]) - float(previous["requests"][:-1]), 2),
                "p95_latency_ms": int(current["latency"].split()[0]) - int(previous["latency"].split()[0]),
                "error_rate_percentage_points": round(float(current["errors"][:-1]) - float(previous["errors"][:-1]), 2),
            },
        }
    raise ValueError("Unknown dashboard tool")


def response_text(output: list[dict]) -> str:
    return "\n".join(
        part["text"]
        for item in output
        if item.get("type") == "message"
        for part in item.get("content", [])
        if part.get("type") == "output_text" and isinstance(part.get("text"), str)
    ).strip()


async def openai_answer(body: ChatRequest) -> str:
    return "".join(event["text"] async for event in stream_openai(body) if event["type"] == "delta")


async def stream_openai(body: ChatRequest):
    inputs = [
        {"role": item["role"], "content": item["content"][:2000]}
        for item in body.messages[-12:]
        if item.get("role") in ("user", "assistant") and isinstance(item.get("content"), str)
    ]
    inputs.append({"role": "user", "content": body.message})
    headers = {"Authorization": f"Bearer {OPENAI_API_KEY}", "Content-Type": "application/json"}
    current_view = body.view
    async with httpx.AsyncClient(timeout=httpx.Timeout(60, connect=5)) as client:
        for _ in range(3):
            output = None
            text_seen = False
            async with client.stream(
                "POST",
                "https://api.openai.com/v1/responses",
                headers=headers,
                json={
                    "model": OPENAI_MODEL,
                    "instructions": OPENAI_INSTRUCTIONS,
                    "input": inputs,
                    "tools": OPENAI_TOOLS,
                    "parallel_tool_calls": False,
                    "max_output_tokens": 400,
                    "store": False,
                    "stream": True,
                },
            ) as response:
                response.raise_for_status()
                async for line in response.aiter_lines():
                    if not line.startswith("data:"):
                        continue
                    data = line[5:].strip()
                    if data == "[DONE]":
                        continue
                    event = json.loads(data)
                    kind = event.get("type")
                    if kind == "response.output_text.delta":
                        delta = event.get("delta", "")
                        if isinstance(delta, str) and delta:
                            text_seen = True
                            yield {"type": "delta", "text": delta}
                    elif kind == "response.output_item.added" and event.get("item", {}).get("type") == "function_call":
                        name = event["item"].get("name")
                        progress = {
                            "compare_dashboard_periods": "Comparing periods",
                            "get_dashboard_state": "Reading dashboard data",
                            "set_dashboard_period": "Changing dashboard view",
                        }
                        yield {"type": "status", "text": progress.get(name, "Using dashboard tool")}
                    elif kind == "response.completed":
                        output = event["response"].get("output", [])
                    elif kind in ("response.failed", "error"):
                        raise ValueError("The model could not finish")
            if output is None:
                raise ValueError("The model stream ended without a response")
            calls = [item for item in output if item.get("type") == "function_call"]
            if not calls:
                if not text_seen:
                    answer = response_text(output)
                    if answer:
                        yield {"type": "delta", "text": answer}
                        return
                    raise ValueError("The model returned no answer")
                return
            if len(calls) > 2:
                raise ValueError("The model requested too many tools")
            inputs.extend(output)
            for call in calls:
                arguments = json.loads(call["arguments"])
                if call["name"] == "set_dashboard_period":
                    if set(arguments) != {"period"} or arguments["period"] not in DASHBOARD["views"]:
                        raise ValueError("Invalid dashboard period")
                    current_view = arguments["period"]
                    result = dashboard_tool("get_dashboard_state", current_view)
                    yield {"type": "view_changed", "period": current_view}
                else:
                    if arguments != {}:
                        raise ValueError("Unexpected dashboard tool arguments")
                    result = dashboard_tool(call["name"], current_view)
                inputs.append(
                    {"type": "function_call_output", "call_id": call["call_id"], "output": json.dumps(result)}
                )
            yield {"type": "status", "text": "Writing answer"}
    raise ValueError("The model did not finish")


async def stream_agent(body: ChatRequest):
    if not SCRIPTED_ONLY and not AGENT_URL and OPENAI_API_KEY:
        try:
            yield sse({"type": "status", "text": "Reading your question"})
            async for event in stream_openai(body):
                yield sse(event)
        except (httpx.HTTPError, ValueError, KeyError, TypeError):
            yield sse({"type": "error", "message": "The model agent could not answer. Try again shortly."})
            return
        yield sse({"type": "done"})
        return

    if SCRIPTED_ONLY or not AGENT_URL:
        period = requested_view(body.message)
        if period:
            data = DASHBOARD["views"][period]
            yield sse({"type": "view_changed", "period": period})
            yield sse({
                "type": "delta",
                "text": (
                    f"The dashboard now shows {period}. The illustrative view has "
                    f"{data['requests'].replace('M', ' million')} requests, p95 latency "
                    f"{data['latency']}, and an error rate of {data['errors']}. "
                    f"{data['peak']} has the highest request volume in the displayed series."
                ),
            })
            yield sse({"type": "done"})
            return
        answer = demo_answer(body.message, body.view)
        yield sse({"type": "delta", "text": answer})
        yield sse({"type": "done"})
        return

    headers = {"Accept": "text/event-stream"}
    if AGENT_TOKEN:
        headers["Authorization"] = f"Bearer {AGENT_TOKEN}"
    try:
        async with httpx.AsyncClient(timeout=httpx.Timeout(30, connect=5)) as client:
            async with client.stream("POST", AGENT_URL, json=agent_input(body), headers=headers) as response:
                response.raise_for_status()
                data_lines: list[str] = []
                total = 0
                finished = False
                async for line in response.aiter_lines():
                    total += len(line)
                    if total > MAX_EVENT_BYTES:
                        yield sse({"type": "error", "message": "The agent sent too much data."})
                        return
                    if line.startswith("data:"):
                        data_lines.append(line[5:].lstrip())
                    elif not line and data_lines:
                        try:
                            projected = parse_agui_event("\n".join(data_lines))
                        except (ValueError, TypeError):
                            projected = None
                        data_lines.clear()
                        if projected:
                            yield sse(projected)
                            if projected["type"] in ("done", "error"):
                                finished = True
                                break
                if not finished:
                    yield sse({"type": "done"})
    except httpx.HTTPError:
        yield sse({"type": "error", "message": "The agent is unavailable. Try again shortly."})


@app.get("/", response_class=HTMLResponse)
async def index():
    return PAGE


@app.get("/agent-status")
async def agent_status():
    return {
        "chat_enabled": CHAT_ENABLED,
        "webmcp_enabled": WEBMCP_ENABLED,
        "mode": "Demo agent" if SCRIPTED_ONLY else "Connected AG-UI agent" if AGENT_URL else "OpenAI agent" if OPENAI_API_KEY else "Demo agent",
    }


@app.post("/chat")
async def chat(body: ChatRequest, request: Request):
    if not CHAT_ENABLED:
        raise HTTPException(404, "App chat is disabled")
    origin = request.headers.get("origin")
    external_host = request.headers.get("x-forwarded-host") or request.headers.get("host")
    if origin and urlsplit(origin).netloc != external_host:
        raise HTTPException(403, "Cross-origin chat is not allowed")
    if len(json.dumps(body.messages)) > 24_000:
        raise HTTPException(413, "Conversation is too long. Start a new chat.")
    return StreamingResponse(stream_agent(body), media_type="text/event-stream", headers={"Cache-Control": "no-store"})

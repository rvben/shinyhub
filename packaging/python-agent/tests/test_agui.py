import asyncio
import json
from unittest.mock import patch

import httpx
import pytest

from shinyhub_agent import AGUIChat, AgentTool, ToolRegistry


def sse(value):
    return "data: " + json.dumps(value) + "\n\n"


def test_agui_endpoint_resumes_after_frontend_tool_result():
    requests = []

    def endpoint(request):
        assert request.headers["authorization"] == "Bearer test-token"
        body = json.loads(request.content)
        requests.append(body)
        if len(requests) == 1:
            events = [
                {"type": "RUN_STARTED", "threadId": "thread-1", "runId": "run-1"},
                {"type": "TOOL_CALL_START", "toolCallId": "call-1", "toolCallName": "get_view"},
                {"type": "TOOL_CALL_ARGS", "toolCallId": "call-1", "delta": "{}"},
                {"type": "TOOL_CALL_END", "toolCallId": "call-1"},
                {"type": "RUN_FINISHED", "threadId": "thread-1", "runId": "run-1"},
            ]
        else:
            events = [
                {"type": "TEXT_MESSAGE_CONTENT", "messageId": "m", "delta": "Showing week."},
                {"type": "RUN_FINISHED", "threadId": "thread-1", "runId": "run-2"},
            ]
        return httpx.Response(200, headers={"content-type": "text/event-stream"},
                              text="".join(map(sse, events)))

    async def get_view(_):
        return {"period": "week"}

    tools = ToolRegistry([AgentTool("get_view", "Read view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, get_view)])
    real_client = httpx.AsyncClient

    def client_factory(*args, **kwargs):
        return real_client(*args, transport=httpx.MockTransport(endpoint), **kwargs)

    async def run():
        async def approve(*_):
            raise AssertionError("read-only tool needs no approval")
        agent = AGUIChat("https://agent.example/run", "test-token")
        return [event async for event in agent.run("What view?", [], tools, approve, thread_id="thread-1")]

    with patch("shinyhub_agent._agui.httpx.AsyncClient", side_effect=client_factory):
        events = asyncio.run(run())
    assert events[-1] == {"type": "delta", "text": "Showing week."}
    assert {"type": "tool_started", "name": "get_view", "description": "Read view",
            "readOnly": True} in events
    assert {"type": "tool_finished", "name": "get_view", "ok": True} in events
    assert requests[0]["threadId"] == requests[1]["threadId"] == "thread-1"
    assert requests[0]["tools"][0]["name"] == "get_view"
    assert requests[1]["messages"][-1]["role"] == "tool"
    assert json.loads(requests[1]["messages"][-1]["content"]) == {"period": "week"}
    assert "test-token" not in json.dumps(requests)


def test_agui_write_waits_for_approval_and_returns_applied_state():
    received = []

    def endpoint(request):
        body = json.loads(request.content)
        received.append(body)
        if len(received) == 1:
            events = [
                {"type": "TOOL_CALL_START", "toolCallId": "call-2", "toolCallName": "set_period"},
                {"type": "TOOL_CALL_ARGS", "toolCallId": "call-2", "delta": '{"period":"year"}'},
                {"type": "TOOL_CALL_END", "toolCallId": "call-2"},
                {"type": "RUN_FINISHED"},
            ]
        else:
            events = [
                {"type": "TEXT_MESSAGE_CONTENT", "delta": "Showing year."},
                {"type": "RUN_FINISHED"},
            ]
        return httpx.Response(200, text="".join(map(sse, events)))

    async def impossible(_):
        raise AssertionError("write handler runs only after approval")

    tools = ToolRegistry([AgentTool("set_period", "Change period", {
        "type": "object", "properties": {"period": {"type": "string", "enum": ["year"]}},
        "required": ["period"], "additionalProperties": False,
    }, impossible, read_only=False, confirmation="Change period?")])
    real_client = httpx.AsyncClient

    def client_factory(*args, **kwargs):
        return real_client(*args, transport=httpx.MockTransport(endpoint), **kwargs)

    async def run():
        async def approve(name, arguments):
            assert name == "set_period"
            assert arguments == {"period": "year"}
            return {"period": "year", "applied": True}
        return [event async for event in AGUIChat("https://agent.example/run").run(
            "Show year", [], tools, approve, thread_id="thread-2")]

    with patch("shinyhub_agent._agui.httpx.AsyncClient", side_effect=client_factory):
        events = asyncio.run(run())
    assert any(event["type"] == "action_applied" for event in events)
    assert json.loads(received[1]["messages"][-1]["content"]) == {"period": "year", "applied": True}


@pytest.mark.parametrize("url", ["http://agent.example/run", "https://user:pass@agent.example/run", "file:///run"])
def test_agui_requires_secure_endpoint(url):
    with pytest.raises(ValueError):
        AGUIChat(url)

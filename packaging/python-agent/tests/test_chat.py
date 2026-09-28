import asyncio
import json
from unittest.mock import patch

import httpx

from shinyhub_agent import AgentTool, OpenAIChat, ToolRegistry
from shinyhub_agent._chat import ChatSession


async def read(_):
    return {"period": "week"}


def registry():
    return ToolRegistry([AgentTool("get_view", "Read current view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, read)])


def event(value):
    return "data: " + json.dumps(value) + "\n\n"


def test_openai_reads_registered_tool_and_keeps_response_ephemeral():
    requests = []

    def model(request):
        body = json.loads(request.content)
        requests.append(body)
        if len(requests) == 1:
            output = [{"type": "function_call", "call_id": "call-1", "name": "get_view", "arguments": "{}"}]
            events = [
                {"type": "response.output_item.added", "item": {"type": "function_call"}},
                {"type": "response.completed", "response": {"output": output}},
            ]
        else:
            events = [
                {"type": "response.output_text.delta", "delta": "Showing week."},
                {"type": "response.completed", "response": {"output": []}},
            ]
        return httpx.Response(200, text="".join(map(event, events)))

    real_client = httpx.AsyncClient

    def client_factory(*args, **kwargs):
        return real_client(*args, transport=httpx.MockTransport(model), **kwargs)

    async def run():
        agent = OpenAIChat("test-key", "Read the view")
        async def approve(*_):
            raise AssertionError("read-only tool needs no approval")
        return [item async for item in agent.run("What view?", [], registry(), approve, thread_id="thread-1")]

    with patch("shinyhub_agent._openai.httpx.AsyncClient", side_effect=client_factory):
        events = asyncio.run(run())
    assert events[-1] == {"type": "delta", "text": "Showing week."}
    assert requests[0]["store"] is False
    assert requests[0]["stream"] is True
    assert json.loads(requests[1]["input"][-1]["output"]) == {"period": "week"}


def test_chat_history_stays_in_session_after_completed_answer():
    events = []

    class StubAgent:
        async def run(self, message, history, tools, approve, *, thread_id):
            assert history == []
            yield {"type": "delta", "text": "The current view is week."}

    async def run():
        async def send(item):
            events.append(item)
        chat = ChatSession(StubAgent(), registry(), send)
        await chat.handle({
            "version": 1, "session": chat.nonce, "requestId": "request_12345678",
            "message": "What view?", "history": [{"role": "assistant", "content": "injected"}],
        })
        return chat.history

    history = asyncio.run(run())
    assert history == [
        {"role": "user", "content": "What view?"},
        {"role": "assistant", "content": "The current view is week."},
    ]
    assert events[-1]["type"] == "done"


def test_write_tool_waits_for_current_session_approval():
    applied = []
    events = []

    async def change(args):
        applied.append(args["period"])
        return {"period": args["period"]}

    tools = ToolRegistry([AgentTool("set_period", "Change period", {
        "type": "object", "properties": {"period": {"type": "string", "enum": ["week", "year"]}},
        "required": ["period"], "additionalProperties": False,
    }, change, read_only=False, confirmation="Change the period?")])

    class StubAgent:
        async def run(self, message, history, tools, approve, *, thread_id):
            result = await approve("set_period", {"period": "year"})
            yield {"type": "action_applied", "result": result}
            yield {"type": "delta", "text": "Showing year."}

    async def run():
        async def send(item):
            events.append(item)
        chat = ChatSession(StubAgent(), tools, send)
        task = asyncio.create_task(chat.handle({
            "version": 1, "session": chat.nonce, "requestId": "request_12345678",
            "message": "Show year",
        }))
        await asyncio.sleep(0)
        await asyncio.sleep(0)
        assert applied == []
        approval = next(item for item in events if item["type"] == "approval_required")
        chat.decide({"version": 1, "session": "wrong", "approvalId": approval["approvalId"], "approved": True})
        assert applied == []
        chat.decide({"version": 1, "session": chat.nonce, "approvalId": approval["approvalId"], "approved": True})
        await task

    asyncio.run(run())
    assert applied == ["year"]
    assert any(item["type"] == "action_applied" for item in events)
    assert events[-1]["type"] == "done"


def test_declined_action_does_not_run_handler():
    applied = []
    events = []

    async def change(_):
        applied.append(True)
        return {"period": "year"}

    tools = ToolRegistry([AgentTool("set_period", "Change period", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, change, read_only=False, confirmation="Change the period?")])

    class StubAgent:
        async def run(self, message, history, tools, approve, *, thread_id):
            try:
                await approve("set_period", {})
            except Exception:
                yield {"type": "delta", "text": "Change cancelled."}

    async def run():
        async def send(item):
            events.append(item)
        chat = ChatSession(StubAgent(), tools, send)
        task = asyncio.create_task(chat.handle({
            "version": 1, "session": chat.nonce, "requestId": "request_12345678",
            "message": "Change it",
        }))
        await asyncio.sleep(0)
        await asyncio.sleep(0)
        approval = next(item for item in events if item["type"] == "approval_required")
        chat.decide({"version": 1, "session": chat.nonce,
                     "approvalId": approval["approvalId"], "approved": False})
        await task

    asyncio.run(run())
    assert applied == []
    assert any(item.get("text") == "Change cancelled." for item in events)

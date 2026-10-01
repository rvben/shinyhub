import asyncio
import json
import logging
from contextlib import contextmanager
from unittest.mock import patch

import httpx
import pytest
from shinyhub_agent import AgentTool, OpenAIChat, ToolRegistry
from shinyhub_agent._chat import ChatSession
from shinyhub_agent._usage import ModelCall, usage_context


def test_shared_adapter_keeps_viewer_metadata_out_of_prompts_and_separate_in_records(
    caplog,
):
    records, requests = [], []
    caplog.set_level(logging.INFO, logger="shinyhub_agent._usage")

    async def read(args):
        return {}

    tools = ToolRegistry(
        [
            AgentTool(
                "read_view",
                "Read view",
                {"type": "object", "properties": {}, "additionalProperties": False},
                read,
            )
        ]
    )

    def endpoint(request):
        requests.append(request)
        events = [
            {"type": "response.output_text.delta", "delta": "Hello."},
            {
                "type": "response.completed",
                "response": {
                    "output": [],
                    "usage": {"input_tokens": 11, "output_tokens": 5},
                },
            },
        ]
        return httpx.Response(
            200, text="".join("data: " + json.dumps(event) + "\n\n" for event in events)
        )

    real_client = httpx.AsyncClient

    def factory(*args, **kwargs):
        return real_client(*args, transport=httpx.MockTransport(endpoint), **kwargs)

    async def callback(record):
        await asyncio.sleep(0)
        records.append(record)

    async def run():
        async def send(event):
            pass

        agent = OpenAIChat("fake-key", "Use app facts", on_usage=callback)
        chats = [
            ChatSession(
                agent,
                tools,
                send,
                usage_metadata={"app": "cost-dashboard", "username": viewer},
            )
            for viewer in ("viewer-one", "viewer-two")
        ]
        await asyncio.gather(
            *(
                chat.handle(
                    {
                        "version": 1,
                        "session": chat.nonce,
                        "requestId": "request_12345678",
                        "message": "Hello",
                    }
                )
                for chat in chats
            )
        )
        assert usage_context.get() == {}
        assert all(len(chat.history) == 2 for chat in chats)

    with patch("httpx.AsyncClient", side_effect=factory):
        asyncio.run(run())
    assert {record.username for record in records} == {"viewer-one", "viewer-two"}
    assert len({record.turn_id for record in records}) == 2
    assert all(record.app == "cost-dashboard" for record in records)
    assert all(
        "viewer-one" not in request.content.decode()
        and "viewer-two" not in request.content.decode()
        for request in requests
    )
    assert all("viewer" not in request.headers["user-agent"] for request in requests)
    logs = [
        record.agent_usage
        for record in caplog.records
        if hasattr(record, "agent_usage")
    ]
    assert len(logs) == 2
    assert sum(record["input_tokens"] for record in logs) == 22
    assert all(record["timestamp"].endswith("+00:00") for record in logs)


def test_model_error_and_cancellation_still_report_observed_usage():
    records = []

    async def run():
        with pytest.raises(ValueError):
            async with ModelCall(
                "bedrock", "model", "turn", 1, False, records.append
            ) as call:
                call.tokens = {"input_tokens": 4, "output_tokens": 2}
                raise ValueError("private provider details")
        with pytest.raises(asyncio.CancelledError):
            async with ModelCall("bedrock", "model", "turn", 2, True, records.append):
                raise asyncio.CancelledError

    asyncio.run(run())
    assert [record.outcome for record in records] == ["error", "cancelled"]
    assert records[0].input_tokens == 4
    assert records[1].input_tokens is None


def test_usage_attaches_to_its_own_model_span_and_trace_failures_do_not_break_calls():
    trace = pytest.importorskip("opentelemetry.trace")
    attributes, records = {}, []

    class Span:
        def set_attribute(self, name, value):
            attributes[name] = value

    class Tracer:
        @contextmanager
        def start_as_current_span(self, name):
            assert name == "shinyhub.agent.model_call"
            yield Span()

    async def run():
        async with ModelCall(
            "openai", "model", "turn", 1, False, records.append
        ) as call:
            call.tokens = {"input_tokens": 11, "output_tokens": 5}
            call.tool_names = ("read_view",)

    with patch.object(trace, "get_tracer", return_value=Tracer()):
        asyncio.run(run())
    assert attributes["shinyhub.agent.input_tokens"] == 11
    assert attributes["shinyhub.agent.tool_names"] == ("read_view",)
    assert attributes["shinyhub.agent.turn_id"] == "turn"
    with patch.object(
        trace, "get_tracer", side_effect=RuntimeError("collector unavailable")
    ):
        asyncio.run(run())
    assert len(records) == 2

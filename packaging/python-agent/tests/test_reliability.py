import asyncio
import json
from copy import deepcopy
from unittest.mock import patch

import httpx
import pytest
from shinyhub_agent import (
    AgentTool,
    AGUIChat,
    BedrockChat,
    OpenAIChat,
    ToolError,
    ToolRegistry,
)

SCHEMA = {"type": "object", "properties": {}, "additionalProperties": False}


def registry(seen):
    async def read(args):
        seen.append("read")
        return {"total": 123}

    async def write(args):
        seen.append("write")
        return {"applied": True}

    return ToolRegistry(
        [
            AgentTool("read_view", "Read view", SCHEMA, read),
            AgentTool(
                "write_view",
                "Change view",
                SCHEMA,
                write,
                read_only=False,
                confirmation="Change view?",
            ),
        ]
    )


def model_reply(backend, reply):
    calls = reply.get("calls", [])
    text = reply.get("text")
    if backend == "bedrock":
        events = [{"messageStart": {"role": "assistant"}}]
        if text:
            events.append(
                {
                    "contentBlockDelta": {
                        "contentBlockIndex": len(calls),
                        "delta": {"text": text},
                    }
                }
            )
        for i, (name, args) in enumerate(calls):
            events.extend(
                [
                    {
                        "contentBlockStart": {
                            "contentBlockIndex": i,
                            "start": {
                                "toolUse": {"toolUseId": f"call-{i}", "name": name}
                            },
                        }
                    },
                    {
                        "contentBlockDelta": {
                            "contentBlockIndex": i,
                            "delta": {"toolUse": {"input": json.dumps(args)}},
                        }
                    },
                    {"contentBlockStop": {"contentBlockIndex": i}},
                ]
            )
        events.append(
            {"messageStop": {"stopReason": "tool_use" if calls else "end_turn"}}
        )
        events.append(
            {
                "metadata": {
                    "usage": {
                        "inputTokens": 11,
                        "outputTokens": 5,
                        "cacheReadInputTokens": 3,
                    }
                }
            }
        )
        if reply.get("fail"):
            events = events[:-2]
        return events
    if backend == "openai":
        events = []
        if text:
            events.append({"type": "response.output_text.delta", "delta": text})
        output = [
            {
                "type": "function_call",
                "call_id": f"call-{i}",
                "name": name,
                "arguments": json.dumps(args),
            }
            for i, (name, args) in enumerate(calls)
        ]
        events.append(
            {
                "type": "response.failed"
                if reply.get("fail")
                else "response.completed",
                "response": {
                    "output": output,
                    "usage": {
                        "input_tokens": 11,
                        "output_tokens": 5,
                        "input_tokens_details": {"cached_tokens": 3},
                    },
                },
            }
        )
    else:
        events = []
        if text:
            events.append({"type": "TEXT_MESSAGE_CONTENT", "delta": text})
        for i, (name, args) in enumerate(calls):
            events.extend(
                [
                    {
                        "type": "TOOL_CALL_START",
                        "toolCallId": f"call-{i}",
                        "toolCallName": name,
                    },
                    {
                        "type": "TOOL_CALL_ARGS",
                        "toolCallId": f"call-{i}",
                        "delta": json.dumps(args),
                    },
                    {"type": "TOOL_CALL_END", "toolCallId": f"call-{i}"},
                ]
            )
        events.append({"type": "RUN_ERROR" if reply.get("fail") else "RUN_FINISHED"})
    return "".join("data: " + json.dumps(event) + "\n\n" for event in events)


async def run_backend(backend, replies, tools, approve, *, history=(), **options):
    requests = []
    remaining = iter(replies)
    if backend == "bedrock":

        class Client:
            def converse_stream(self, **request):
                requests.append(deepcopy(request))
                return {"stream": iter(model_reply(backend, next(remaining)))}

        agent = BedrockChat(
            "test-model", "Use app facts", "eu-west-1", client=Client(), **options
        )
        events = [
            event
            async for event in agent.run(
                "Question", history, tools, approve, thread_id="thread"
            )
        ]
    else:
        real_client = httpx.AsyncClient

        def respond(request):
            requests.append(json.loads(request.content))
            return httpx.Response(200, text=model_reply(backend, next(remaining)))

        def factory(*args, **kwargs):
            return real_client(*args, transport=httpx.MockTransport(respond), **kwargs)

        agent = (
            OpenAIChat("fake-key", "Use app facts", **options)
            if backend == "openai"
            else AGUIChat("https://agent.example/run", **options)
        )
        with patch("httpx.AsyncClient", side_effect=factory):
            events = [
                event
                async for event in agent.run(
                    "Question", history, tools, approve, thread_id="thread"
                )
            ]
    return events, requests


BACKENDS = ["bedrock", "openai", "agui"]


@pytest.mark.parametrize("backend", BACKENDS)
def test_three_reads_are_correlated_and_deferred_without_losing_answer(backend):
    seen = []
    tools = registry(seen)

    async def approve(*_):
        raise AssertionError("No writes")

    events, requests = asyncio.run(
        run_backend(
            backend,
            [
                {"calls": [("read_view", {})] * 3},
                {"calls": [("read_view", {})]},
                {"text": "The total is 123."},
            ],
            tools,
            approve,
        )
    )
    assert seen == ["read"] * 3
    assert events[-1] == {"type": "delta", "text": "The total is 123."}
    encoded = json.dumps(requests[1])
    assert "tool_deferred" in encoded
    assert all(f"call-{i}" in encoded for i in range(3))


@pytest.mark.parametrize("backend", BACKENDS)
def test_deferred_write_is_a_barrier_for_later_reads(backend):
    seen = []
    tools = registry(seen)

    async def approve(name, args):
        return await tools.execute(name, args)

    _, requests = asyncio.run(
        run_backend(
            backend,
            [
                {"calls": [("write_view", {}), ("write_view", {}), ("read_view", {})]},
                {"text": "One change was applied."},
            ],
            tools,
            approve,
            max_tool_calls_per_step=4,
        )
    )
    assert seen == ["write"]
    assert json.dumps(requests[1]).count("tool_deferred") == 2


@pytest.mark.parametrize("backend", BACKENDS)
@pytest.mark.parametrize("budget", ["round", "call"])
def test_budget_reserves_one_final_call_with_tools_disabled(backend, budget):
    seen = []

    async def approve(*_):
        raise AssertionError("No writes")

    options = (
        {"max_tool_rounds": 1} if budget == "round" else {"max_tool_calls_per_turn": 1}
    )
    events, requests = asyncio.run(
        run_backend(
            backend,
            [
                {"calls": [("read_view", {})]},
                {"text": "Using the results: 123."},
            ],
            registry(seen),
            approve,
            **options,
        )
    )
    assert seen == ["read"]
    assert len(requests) == 2
    assert events[-1]["text"] == "Using the results: 123."
    final = requests[-1]
    if backend == "bedrock":
        assert "toolConfig" not in final
        assert all(
            set(block) == {"text"}
            for item in final["messages"]
            for block in item["content"]
        )
    elif backend == "openai":
        assert final["tool_choice"] == "none"
    else:
        assert final["tools"] == []
        assert final["context"]


@pytest.mark.parametrize("backend", BACKENDS)
def test_final_failure_discards_partial_text_and_retains_applied_actions(backend):
    seen = []
    tools = registry(seen)

    async def approve(name, args):
        return await tools.execute(name, args)

    events, _ = asyncio.run(
        run_backend(
            backend,
            [
                {"calls": [("write_view", {})]},
                {"text": "Unverified partial answer", "fail": True},
            ],
            tools,
            approve,
            max_tool_rounds=1,
        )
    )
    assert seen == ["write"]
    answer = "".join(event["text"] for event in events if event["type"] == "delta")
    assert "Unverified" not in answer
    assert "Applied actions: write_view" in answer


@pytest.mark.parametrize("backend", BACKENDS)
def test_final_call_cannot_apply_a_write_even_if_provider_ignores_contract(backend):
    seen = []
    tools = registry(seen)

    async def approve(name, args):
        return await tools.execute(name, args)

    events, _ = asyncio.run(
        run_backend(
            backend,
            [
                {"calls": [("read_view", {})]},
                {"calls": [("write_view", {})]},
            ],
            tools,
            approve,
            max_tool_rounds=1,
        )
    )
    assert seen == ["read"]
    assert "action limit" in events[-1]["text"]


@pytest.mark.parametrize("backend", BACKENDS)
def test_tool_rejection_is_a_structured_result_the_model_can_explain(backend):
    async def unavailable(_):
        raise ToolError("no_data", "October has no data yet.")

    tools = ToolRegistry([AgentTool("read_view", "Read view", SCHEMA, unavailable)])
    events, requests = asyncio.run(
        run_backend(
            backend,
            [
                {"calls": [("read_view", {})]},
                {"text": "October has no data yet."},
            ],
            tools,
            unavailable,
        )
    )
    assert events[-1]["text"] == "October has no data yet."
    assert "no_data" in json.dumps(requests[1])


@pytest.mark.parametrize("backend", BACKENDS)
def test_usage_includes_final_round_and_callback_failures_do_not_break_answers(
    backend, caplog
):
    records = []

    def callback(record):
        records.append(record)
        raise RuntimeError("An unavailable accounting sink")

    async def approve(*_):
        raise AssertionError("No writes")

    events, _ = asyncio.run(
        run_backend(
            backend,
            [
                {"calls": [("read_view", {})]},
                {"text": "123"},
            ],
            registry([]),
            approve,
            on_usage=callback,
            max_tool_rounds=1,
        )
    )
    assert events[-1]["text"] == "123"
    assert [record.round for record in records] == [1, 2]
    assert [record.final for record in records] == [False, True]
    assert records[0].turn_id == records[1].turn_id
    assert records[0].call_id != records[1].call_id
    assert records[0].tool_names == ("read_view",)
    assert all(
        record.duration_ms >= 0 and record.outcome == "completed" for record in records
    )
    assert all(record.username is None for record in records)
    if backend != "agui":
        assert sum(record.input_tokens for record in records) == 22
        assert sum(record.output_tokens for record in records) == 10
        assert records[0].cache_read_tokens == 3
    else:
        assert records[0].input_tokens is None
    assert "callback failed" in caplog.text


@pytest.mark.parametrize("backend", BACKENDS)
def test_registered_history_is_not_silently_recut_to_six_exchanges(backend):
    async def approve(*_):
        raise AssertionError("No writes")

    history = [
        {"role": "user" if i % 2 == 0 else "assistant", "content": f"Exchange {i}"}
        for i in range(16)
    ]
    history[1]["content"] += "a" * 4_000
    _, requests = asyncio.run(
        run_backend(backend, [{"text": "123"}], registry([]), approve, history=history)
    )
    assert "Exchange 0" in json.dumps(requests[0])
    assert history[1]["content"] in json.dumps(requests[0])


@pytest.mark.parametrize(
    "name,value",
    [
        ("max_tool_rounds", 0),
        ("max_tool_rounds", 9),
        ("max_tool_calls_per_step", 9),
        ("max_tool_calls_per_turn", 65),
        ("max_tool_calls_per_step", True),
    ],
)
@pytest.mark.parametrize("backend", BACKENDS)
def test_limits_reject_unbounded_or_noninteger_configuration(backend, name, value):
    with pytest.raises(ValueError, match=name):
        if backend == "bedrock":
            BedrockChat("model", "Instructions", "eu-west-1", **{name: value})
        elif backend == "openai":
            OpenAIChat("key", "Instructions", **{name: value})
        else:
            AGUIChat("https://agent.example/run", **{name: value})

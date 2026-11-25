import asyncio
import json
from copy import deepcopy

import pytest

from shinyhub_agent import AgentTool, BedrockChat, ToolRegistry
from shinyhub_agent._core import ToolError
from shinyhub_agent._formatting import ANSWER_FORMAT


def stream_tool(name, arguments):
    return [
        {"messageStart": {"role": "assistant"}},
        {"contentBlockStart": {"contentBlockIndex": 0, "start": {
            "toolUse": {"toolUseId": "use-1", "name": name}}}},
        {"contentBlockDelta": {"contentBlockIndex": 0, "delta": {
            "toolUse": {"input": arguments}}}},
        {"contentBlockStop": {"contentBlockIndex": 0}},
        {"messageStop": {"stopReason": "tool_use"}},
    ]


def stream_answer(text):
    return [
        {"messageStart": {"role": "assistant"}},
        {"contentBlockDelta": {"contentBlockIndex": 0, "delta": {"text": text}}},
        {"messageStop": {"stopReason": "end_turn"}},
    ]


class FakeBedrock:
    def __init__(self, *replies):
        self.replies = list(replies)
        self.requests = []

    def converse_stream(self, **request):
        self.requests.append(deepcopy(request))
        return {"stream": iter(self.replies.pop(0))}


def test_bedrock_streams_tool_result_with_app_scoped_history():
    async def read(_):
        return {"period": "year"}

    tools = ToolRegistry([AgentTool("get_view", "Read current view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, read)])
    client = FakeBedrock(stream_tool("get_view", "{}"), stream_answer("Showing year."))

    async def run():
        async def approve(*_):
            raise AssertionError("read-only tools do not request approval")
        return [event async for event in BedrockChat(
            "model-id", "Use app tools", "eu-west-1", client=client,
        ).run("What view?", [{"role": "user", "content": "Previous question"}],
              tools, approve, thread_id="viewer-thread")]

    events = asyncio.run(run())
    assert events[-1] == {"type": "delta", "text": "Showing year."}
    assert {"type": "tool_started", "name": "get_view", "description": "Read current view",
            "readOnly": True} in events
    assert {"type": "tool_finished", "name": "get_view", "ok": True} in events
    first, second = client.requests
    assert first["modelId"] == "model-id"
    assert first["system"] == [{"text": "Use app tools\n\n" + ANSWER_FORMAT}]
    assert second["system"] == first["system"]
    assert first["messages"][0] == {"role": "user", "content": [{"text": "Previous question"}]}
    assert first["toolConfig"]["tools"][0]["toolSpec"]["name"] == "get_view"
    assert second["messages"][-2]["content"][0]["toolUse"]["input"] == {}
    assert second["messages"][-1]["content"][0]["toolResult"] == {
        "toolUseId": "use-1", "content": [{"json": {"period": "year"}}], "status": "success",
    }


def test_bedrock_write_uses_visitor_approval_and_returns_applied_result():
    async def impossible(_):
        raise AssertionError("write handlers run only through the approval callback")

    tools = ToolRegistry([AgentTool("set_period", "Change dashboard period", {
        "type": "object", "properties": {"period": {"type": "string", "enum": ["year"]}},
        "required": ["period"], "additionalProperties": False,
    }, impossible, read_only=False, confirmation="Change period?")])
    client = FakeBedrock(stream_tool("set_period", '{"period":"year"}'),
                         stream_answer("Showing this year."))
    approved = []

    async def run():
        async def approve(name, arguments):
            approved.append((name, arguments))
            return {"period": "year", "applied": True}
        return [event async for event in BedrockChat(
            "model-id", "Help with the dashboard", "eu-west-1", client=client,
        ).run("Show year", [], tools, approve, thread_id="viewer-thread")]

    events = asyncio.run(run())
    assert approved == [("set_period", {"period": "year"})]
    assert {"type": "action_applied", "name": "set_period",
            "result": {"period": "year", "applied": True}} in events
    assert client.requests[1]["messages"][-1]["content"][0]["toolResult"]["status"] == "success"


def test_bedrock_rejects_incomplete_stream_and_never_calls_a_tool():
    called = []

    async def read(_):
        called.append(True)
        return {}

    tools = ToolRegistry([AgentTool("get_view", "Read view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, read)])
    client = FakeBedrock(stream_tool("get_view", "{}")[:-1])

    async def run():
        async def approve(*_):
            raise AssertionError("unexpected approval")
        return [event async for event in BedrockChat(
            "model-id", "Help", "eu-west-1", client=client,
        ).run("What view?", [], tools, approve, thread_id="viewer-thread")]

    with pytest.raises(ToolError):
        asyncio.run(run())
    assert called == []


def test_bedrock_requests_match_the_aws_converse_stream_contract():
    boto3 = pytest.importorskip("boto3")
    from botocore.validate import ParamValidator

    async def read(_):
        return {"period": "year"}

    tools = ToolRegistry([AgentTool("get_view", "Read current view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, read)])
    client = FakeBedrock(stream_tool("get_view", "{}"), stream_answer("Showing year."))

    async def run():
        async def approve(*_):
            raise AssertionError("unexpected approval")
        return [event async for event in BedrockChat(
            "model-id", "Help", "eu-west-1", client=client,
        ).run("What view?", [], tools, approve, thread_id="viewer-thread")]

    asyncio.run(run())
    sdk = boto3.client("bedrock-runtime", region_name="eu-west-1",
                       aws_access_key_id="unused", aws_secret_access_key="unused")
    shape = sdk.meta.service_model.operation_model("ConverseStream").input_shape
    for request in client.requests:
        validation = ParamValidator().validate(request, shape)
        assert not validation.has_errors(), validation.generate_report()


@pytest.mark.parametrize("max_tool_rounds", [1, 4])
def test_bedrock_policy_blocks_restricted_tool_results_before_inference(max_tool_rounds):
    async def read(_):
        return {"label": "restricted-value"}

    async def approve(*_):
        raise AssertionError("unexpected approval")

    tools = ToolRegistry([AgentTool("get_view", "Read current view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, read)])
    client = FakeBedrock(stream_tool("get_view", "{}"))
    inspected = []

    def policy(request):
        inspected.append(request)
        return "restricted-value" not in json.dumps(request)

    async def run():
        return [event async for event in BedrockChat(
            "model-id", "Help with the dashboard", "eu-west-1", client=client,
            request_policy=policy, max_tool_rounds=max_tool_rounds,
        ).run("What view?", [], tools, approve, thread_id="viewer-thread")]

    with pytest.raises(ToolError) as refused:
        asyncio.run(run())

    assert refused.value.code == "model_request_blocked"
    first, blocked = inspected
    assert client.requests == [first]
    assert "restricted-value" in json.dumps(blocked)


@pytest.mark.parametrize("denial", [False, 1, "allow", ValueError("restricted-value")])
def test_bedrock_policy_denial_never_calls_provider_or_records_usage(denial):
    client = FakeBedrock(stream_answer("Allowed response"))
    records = []

    async def read(_):
        return {"period": "year"}

    tools = ToolRegistry([AgentTool("get_view", "Read current view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, read)])

    def policy(request):
        if "restricted-value" in json.dumps(request):
            if isinstance(denial, Exception):
                raise denial
            return denial
        request["messages"].clear()
        return True

    async def run():
        async def approve(*_):
            raise AssertionError("unexpected approval")

        chat = BedrockChat(
            "model-id", "Help", "eu-west-1", client=client,
            request_policy=policy, on_usage=records.append,
        )
        with pytest.raises(ToolError, match="could not send") as refused:
            async for _ in chat.run("restricted-value", [], tools,
                                    approve, thread_id="viewer-thread"):
                pass
        assert refused.value.code == "model_request_blocked"
        assert "restricted-value" not in str(refused.value)
        if isinstance(denial, Exception):
            assert refused.value.__suppress_context__
        assert client.requests == []
        assert records == []
        return [event async for event in chat.run(
            "Allowed question", [], tools, approve, thread_id="viewer-thread",
        )]

    events = asyncio.run(run())
    assert events[-1] == {"type": "delta", "text": "Allowed response"}
    assert client.requests[0]["messages"] == [
        {"role": "user", "content": [{"text": "Allowed question"}]},
    ]
    assert len(records) == 1


def test_bedrock_policy_checks_suggestion_recovery_payload():
    async def read(_):
        raise AssertionError("suggestion recovery must not invoke app tools")

    async def approve(*_):
        raise AssertionError("unexpected approval")

    tools = ToolRegistry([AgentTool("get_view", "Read current view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, read)])
    client = FakeBedrock(
        stream_tool("suggest_followups", '{"questions":["Restricted value?"]}'),
    )
    inspected = []
    records, events = [], []

    def policy(request):
        inspected.append(request)
        return "Restricted value?" not in json.dumps(request)

    async def run():
        chat = BedrockChat(
            "model-id", "Help", "eu-west-1", client=client, request_policy=policy,
            on_usage=records.append,
        ).with_followups(2)
        async for event in chat.run(
            "What view?", [], tools, approve, thread_id="viewer-thread",
        ):
            events.append(event)

    with pytest.raises(ToolError) as refused:
        asyncio.run(run())
    assert refused.value.code == "model_request_blocked"
    first, blocked = inspected
    assert client.requests == [first]
    assert "Restricted value?" in json.dumps(blocked)
    assert len(records) == 1
    assert {"type": "status", "text": "Writing answer"} not in events


@pytest.mark.parametrize("model,instructions,region", [
    ("", "Help", "eu-west-1"), ("model", "", "eu-west-1"), ("model", "Help", ""),
])
def test_bedrock_requires_explicit_configuration(model, instructions, region):
    with pytest.raises(ValueError):
        BedrockChat(model, instructions, region)

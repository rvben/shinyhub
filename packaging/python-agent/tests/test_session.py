import asyncio

import pytest

from shinyhub_agent import AgentTool, ToolRegistry
from shinyhub_agent._session import SessionTools


async def read(_):
    return {"period": "week"}


def request(dispatcher, **changes):
    return {
        "version": 1,
        "session": dispatcher.nonce,
        "requestId": "request_12345678",
        "name": "get_view",
        "arguments": {},
        **changes,
    }


def make_dispatcher():
    tool = AgentTool("get_view", "Read current view", {
        "type": "object", "properties": {}, "additionalProperties": False,
    }, read)
    return SessionTools(ToolRegistry([tool]))


def test_session_response_correlates_and_contains_applied_result():
    dispatcher = make_dispatcher()
    response = asyncio.run(dispatcher.handle(request(dispatcher)))
    assert response == {
        "version": 1, "session": dispatcher.nonce, "requestId": "request_12345678",
        "ok": True, "result": {"period": "week"},
    }


def test_calls_cannot_cross_viewer_sessions():
    first = make_dispatcher()
    second = make_dispatcher()
    response = asyncio.run(second.handle(request(first)))
    assert response["code"] == "stale_session"
    assert "result" not in response


def test_request_budget_and_bad_request_id():
    dispatcher = make_dispatcher()

    async def run():
        results = [await dispatcher.handle(request(dispatcher)) for _ in range(21)]
        malformed = await dispatcher.handle(request(dispatcher, requestId="x"))
        return results, malformed

    results, malformed = asyncio.run(run())
    assert all(result["ok"] for result in results[:20])
    assert results[20]["code"] == "rate_limited"
    assert malformed is None


@pytest.mark.parametrize("action", ["execute", "prepare"])
def test_browser_cannot_bypass_write_policy_with_valid_nonce(action):
    calls = []

    async def write(args):
        calls.append(args)
        return {"changed": True}

    registry = ToolRegistry([
        AgentTool("get_view", "Read view", {"type": "object", "additionalProperties": False}, read),
        AgentTool("set_view", "Change view", {"type": "object", "additionalProperties": False},
                  write, read_only=False, confirmation="Change view?"),
    ])
    dispatcher = SessionTools(registry)
    assert [tool["name"] for tool in dispatcher.capabilities()["tools"]] == ["get_view"]
    response = asyncio.run(dispatcher.handle(request(dispatcher, name="set_view", action=action)))
    assert response["ok"] is False
    assert response["code"] == "write_not_allowed"
    assert "result" not in response
    assert calls == []
    # Chat still uses the full registry and its own server-side approval flow.
    assert registry.get("set_view") is not None
    assert asyncio.run(dispatcher.handle(request(dispatcher)))["ok"] is True


def test_browser_writes_require_explicit_opt_in():
    calls = []

    async def write(args):
        calls.append(args)
        return {"changed": True}

    registry = ToolRegistry([
        AgentTool("set_view", "Change view", {"type": "object", "additionalProperties": False},
                  write, read_only=False, confirmation="Change view?"),
    ])
    dispatcher = SessionTools(registry, allow_browser_writes=True)
    assert dispatcher.capabilities()["tools"][0]["readOnly"] is False
    response = asyncio.run(dispatcher.handle(request(dispatcher, name="set_view")))
    assert response["result"] == {"changed": True}
    assert calls == [{}]


def test_rejected_browser_writes_consume_request_budget():
    async def write(_):
        raise AssertionError("A rejected write must never reach the handler")

    registry = ToolRegistry([
        AgentTool("set_view", "Change view", {"type": "object", "additionalProperties": False},
                  write, read_only=False, confirmation="Change view?"),
    ])
    dispatcher = SessionTools(registry)

    async def run():
        return [await dispatcher.handle(request(dispatcher, name="set_view")) for _ in range(21)]

    responses = asyncio.run(run())
    assert all(response["code"] == "write_not_allowed" for response in responses[:20])
    assert responses[20]["code"] == "rate_limited"


def test_browser_write_policy_rejects_truthy_non_boolean_setting():
    with pytest.raises(TypeError, match="allow_browser_writes"):
        SessionTools(make_dispatcher().registry, allow_browser_writes="false")

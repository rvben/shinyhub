import asyncio

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

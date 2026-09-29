import asyncio
import importlib.util
import json
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

import httpx

spec = importlib.util.spec_from_file_location("agent_demo", Path(__file__).with_name("app.py"))
demo = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = demo
spec.loader.exec_module(demo)


class AgentChatTests(unittest.TestCase):
    def test_chat_and_webmcp_can_be_configured_independently(self):
        async def check():
            async with httpx.AsyncClient(
                transport=httpx.ASGITransport(app=demo.app), base_url="http://testserver"
            ) as client:
                status = await client.get("/agent-status")
                disabled_chat = await client.post(
                    "/chat", json={"message": "Hi", "thread_id": "t"}
                )
                return status.json(), disabled_chat.status_code

        with patch.object(demo, "CHAT_ENABLED", False), patch.object(demo, "WEBMCP_ENABLED", True):
            status, code = asyncio.run(check())
        self.assertFalse(status["chat_enabled"])
        self.assertTrue(status["webmcp_enabled"])
        self.assertEqual(code, 404)

    def test_chat_uses_external_host_set_by_shinyhub_proxy(self):
        async def check():
            async with httpx.AsyncClient(
                transport=httpx.ASGITransport(app=demo.app), base_url="http://backend.internal"
            ) as client:
                headers = {
                    "origin": "https://apps.shinyhub.am8.nl",
                    "x-forwarded-host": "apps.shinyhub.am8.nl",
                }
                accepted = await client.post(
                    "/chat", json={"message": "Hi", "thread_id": "t"}, headers=headers
                )
                headers["origin"] = "https://other.example"
                rejected = await client.post(
                    "/chat", json={"message": "Hi", "thread_id": "t"}, headers=headers
                )
                return accepted.status_code, rejected.status_code

        self.assertEqual(asyncio.run(check()), (200, 403))

    def test_scripted_answers_match_selected_view(self):
        self.assertIn("0.5%", demo.demo_answer("Were there errors?", "Last week"))
        self.assertIn("164 ms", demo.demo_answer("What was latency?", "Last week"))
        self.assertIn("1.16 million", demo.demo_answer("How many requests?", "Last week"))
        self.assertIn("August", demo.demo_answer("How many requests?", "This year"))

    def test_model_context_labels_latency_as_p95(self):
        state = demo.dashboard_tool("get_dashboard_state", "This week")
        self.assertEqual(state["p95_latency"], "182 ms")
        self.assertNotIn("latency", state)
        comparison = demo.dashboard_tool("compare_dashboard_periods", "This week")
        self.assertEqual(comparison["periods"]["Last week"]["p95_latency"], "164 ms")
        self.assertIn("never an average", demo.OPENAI_INSTRUCTIONS)

    def test_scripted_agent_can_change_to_year_view(self):
        async def collect():
            body = demo.ChatRequest(message="Can you set the view to this year?", thread_id="t")
            return [json.loads(event[6:]) async for event in demo.stream_agent(body)]

        with patch.object(demo, "OPENAI_API_KEY", ""), patch.object(demo, "AGENT_URL", ""):
            events = asyncio.run(collect())
        self.assertEqual([event["type"] for event in events], ["view_changed", "delta", "done"])
        self.assertEqual(events[0]["period"], "This year")

    def test_openai_period_tool_emits_view_change_and_uses_new_period(self):
        requests = []

        def model(request):
            requests.append(json.loads(request.content))
            if len(requests) == 1:
                output = [{"type": "function_call", "call_id": "call-1", "name": "set_dashboard_period", "arguments": '{"period":"This year"}'}]
                events = [
                    {"type": "response.output_item.added", "item": {"type": "function_call", "name": "set_dashboard_period"}},
                    {"type": "response.completed", "response": {"output": output}},
                ]
            else:
                events = [
                    {"type": "response.output_text.delta", "delta": "Showing this year."},
                    {"type": "response.completed", "response": {"output": []}},
                ]
            return httpx.Response(200, text="".join(demo.sse(event) for event in events))

        real_client = httpx.AsyncClient

        def client_factory(*args, **kwargs):
            return real_client(*args, transport=httpx.MockTransport(model), **kwargs)

        with patch.object(demo, "OPENAI_API_KEY", "test-model-key"), patch.object(
            demo.httpx, "AsyncClient", side_effect=client_factory
        ):
            events = asyncio.run(collect_events(demo.ChatRequest(message="Set view to this year", thread_id="t")))

        self.assertEqual(events[1], {"type": "view_changed", "period": "This year"})
        self.assertEqual(json.loads(requests[1]["input"][-1]["output"])["selected_period"], "This year")
        self.assertEqual(events[-1], {"type": "delta", "text": "Showing this year."})

    def test_input_sends_only_selected_context_and_chat(self):
        body = demo.ChatRequest(
            message="What happened?",
            thread_id="thread-1",
            view="Last week",
            messages=[{"role": "assistant", "content": "Earlier reply"}],
        )
        payload = demo.agent_input(body)
        self.assertEqual(payload["context"], [{"description": "Selected dashboard view", "value": "Last week"}])
        self.assertEqual([m["role"] for m in payload["messages"]], ["assistant", "user"])
        self.assertEqual(payload["tools"], [])
        self.assertNotIn("identity", json.dumps(payload).lower())

    def test_remote_agui_stream_is_projected_and_token_stays_server_side(self):
        received = {}

        def agent(request):
            received["authorization"] = request.headers.get("authorization")
            received["body"] = json.loads(request.content)
            events = "".join(
                demo.sse(event)
                for event in (
                    {"type": "RUN_STARTED", "threadId": "t", "runId": "r"},
                    {"type": "TEXT_MESSAGE_CONTENT", "messageId": "m", "delta": "Hello"},
                    {"type": "RUN_FINISHED"},
                )
            )
            return httpx.Response(200, headers={"content-type": "text/event-stream"}, text=events)

        real_client = httpx.AsyncClient

        def client_factory(*args, **kwargs):
            return real_client(*args, transport=httpx.MockTransport(agent), **kwargs)

        async def collect():
            body = demo.ChatRequest(message="Hi", thread_id="t")
            return [event async for event in demo.stream_agent(body)]

        with patch.object(demo, "AGENT_URL", "https://agent.example/run"), patch.object(
            demo, "AGENT_TOKEN", "test-secret"
        ), patch.object(demo.httpx, "AsyncClient", side_effect=client_factory):
            events = asyncio.run(collect())

        self.assertEqual(received["authorization"], "Bearer test-secret")
        self.assertEqual(received["body"]["messages"][-1]["content"], "Hi")
        self.assertEqual([json.loads(event[6:])["type"] for event in events], ["status", "delta", "done"])
        self.assertNotIn("test-secret", "".join(events))

    def test_openai_agent_calls_read_only_dashboard_tool_without_storing_response(self):
        requests = []

        def model(request):
            requests.append(json.loads(request.content))
            self.assertEqual(request.headers["authorization"], "Bearer test-model-key")
            if len(requests) == 1:
                return httpx.Response(
                    200,
                    text="".join(demo.sse(event) for event in (
                        {"type": "response.output_item.added", "item": {"type": "function_call", "name": "compare_dashboard_periods"}},
                        {"type": "response.completed", "response": {"output": [{"type": "function_call", "call_id": "call-1", "name": "compare_dashboard_periods", "arguments": "{}"}]}},
                    )),
                )
            return httpx.Response(
                200,
                text="".join(demo.sse(event) for event in (
                    {"type": "response.output_text.delta", "delta": "P95 latency rose "},
                    {"type": "response.output_text.delta", "delta": "by 18 ms in the synthetic data."},
                    {"type": "response.completed", "response": {"output": [{"type": "message", "content": [{"type": "output_text", "text": "P95 latency rose by 18 ms in the synthetic data."}]}]}},
                )),
            )

        real_client = httpx.AsyncClient

        def client_factory(*args, **kwargs):
            return real_client(*args, transport=httpx.MockTransport(model), **kwargs)

        with patch.object(demo, "OPENAI_API_KEY", "test-model-key"), patch.object(
            demo.httpx, "AsyncClient", side_effect=client_factory
        ):
            events = asyncio.run(
                collect_events(demo.ChatRequest(message="Compare latency", thread_id="t", view="Last week"))
            )

        answer = "".join(event["text"] for event in events if event["type"] == "delta")
        self.assertIn("18 ms", answer)
        self.assertEqual([event["type"] for event in events], ["status", "status", "delta", "delta"])
        self.assertEqual(events[0]["text"], "Comparing periods")
        self.assertEqual(len(requests), 2)
        self.assertTrue(all(request["store"] is False for request in requests))
        self.assertTrue(all(request["stream"] is True for request in requests))
        tool_result = requests[1]["input"][-1]
        self.assertEqual(tool_result["type"], "function_call_output")
        self.assertEqual(json.loads(tool_result["output"])["selected_period"], "Last week")
        self.assertEqual(json.loads(tool_result["output"])["difference_this_minus_last"]["p95_latency_ms"], 18)


async def collect_events(body):
    return [event async for event in demo.stream_openai(body)]


if __name__ == "__main__":
    unittest.main()

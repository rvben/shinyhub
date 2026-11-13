"""Exercise completed work spans while a real Shiny WebSocket stays open."""
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import httpx
from opentelemetry import trace
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from shiny import App, render, ui
from starlette.testclient import TestClient

exporter = InMemorySpanExporter()
trace.get_tracer_provider().add_span_processor(SimpleSpanProcessor(exporter))
tracer = trace.get_tracer("dashboard-test")


class DataHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ready")

    def log_message(self, *_):
        pass


def server(input, output, session):
    @render.text
    def result():
        value = input.n()
        with tracer.start_as_current_span("dashboard.load_data"):
            response = httpx.get(endpoint)
            response.raise_for_status()
        return str(value)


backend = ThreadingHTTPServer(("127.0.0.1", 0), DataHandler)
endpoint = f"http://127.0.0.1:{backend.server_port}/data"
thread = threading.Thread(target=backend.serve_forever, daemon=True)
thread.start()
app = App(ui.page_fluid(ui.input_numeric("n", "Count", 0), ui.output_text("result")), server)
try:
    with TestClient(app) as client, client.websocket_connect("/websocket/") as ws:
        assert "config" in ws.receive_json()
        assert client.get("/").status_code == 200
        prior = set()
        update_traces = set()
        session_ids = set()
        for value in range(3):
            method = "init" if value == 0 else "update"
            ws.send_json({"method": method, "data": {"n": value, ".clientdata_output_result_hidden": False}})
            got_output = got_idle = False
            for _ in range(100):
                message = ws.receive_json()
                got_output |= message.get("values", {}).get("result") == str(value)
                got_idle |= message.get("busy") == "idle"
                if got_output and got_idle:
                    break
            else:
                raise AssertionError("completed output never arrived")
            # Both work completion and output delivery precede inspection;
            # the socket remains connected throughout all three interactions.
            spans = {s.context.span_id: s for s in exporter.get_finished_spans()}
            fresh = [s for sid, s in spans.items() if sid not in prior]
            loads = [s for s in fresh if s.name == "dashboard.load_data"]
            assert len(loads) == 1, [s.name for s in fresh]
            load = loads[0]
            ancestors = []
            current = load
            while current.parent is not None:
                current = spans[current.parent.span_id]
                ancestors.append(current)
            updates = [s for s in ancestors if s.name == "reactive_update"]
            assert len(updates) == 1, [s.name for s in ancestors]
            update = updates[0]
            session_ids.add(update.attributes["session.id"])
            if value > 0:
                assert update.parent is None, "update attached to a session-length trace"
                assert update.context.trace_id not in update_traces
            update_traces.add(update.context.trace_id)
            assert any(s.name.startswith("output") for s in ancestors), [s.name for s in ancestors]
            assert any(s.kind == trace.SpanKind.CLIENT and s.parent and s.parent.span_id == load.context.span_id for s in fresh)
            assert not any(s.kind == trace.SpanKind.SERVER and "websocket" in s.name.lower() for s in spans.values())
            prior = set(spans)
        assert len(session_ids) == 1, "interactions lost their shared session identity"
    assert any(s.kind == trace.SpanKind.SERVER for s in exporter.get_finished_spans()), "HTTP instrumentation inactive"
    # An accidentally long-lived middleware span only ends at disconnect.
    assert not any(s.kind == trace.SpanKind.SERVER and "websocket" in s.name.lower()
                   for s in exporter.get_finished_spans())
finally:
    backend.shutdown()
    backend.server_close()
    thread.join()

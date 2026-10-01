"""Compatibility checks for the running interpreter, without external services."""

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
BOOTSTRAP = ROOT / "internal/pythontrace/bootstrap.py"
BINARY = None


class RuntimeCompatibility(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory(prefix="shinyhub-python-compat-")
        self.addCleanup(self.work.cleanup)
        self.directory = Path(self.work.name)

    def execute_bootstrap(self, mode, target, *args, asgi_events=False):
        # Install a real in-memory SDK before the exact shipped bootstrap runs.
        # Span JSON goes to stderr independently of the app's stdout/exit code.
        setup = """
import atexit, json
from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
provider = TracerProvider()
exporter = InMemorySpanExporter()
provider.add_span_processor(SimpleSpanProcessor(exporter))
trace.set_tracer_provider(provider)
@atexit.register
def report():
    import sys
    print('SPANS=' + json.dumps([{'name': s.name, 'attributes': dict(s.attributes), 'events': len(s.events), 'status': s.status.status_code.name} for s in exporter.get_finished_spans()]), file=sys.stderr)
"""
        return subprocess.run(
            [sys.executable, "-c", setup + BOOTSTRAP.read_text(encoding="utf-8"), mode, target, *args],
            cwd=self.directory, capture_output=True, text=True, timeout=30,
            env={**os.environ, "SHINYHUB_TRACING_ASGI_EVENTS": str(asgi_events).lower()},
        )

    def test_script_module_and_code_keep_arguments_and_execute_once(self):
        code = "import json,sys; print(json.dumps(sys.argv)); print('executed-once')"
        script = self.directory / "task.py"
        script.write_text(code, encoding="utf-8")
        for mode, target in [("job-script", str(script)), ("job-module", "task"), ("job-code", code)]:
            with self.subTest(mode=mode):
                result = self.execute_bootstrap(mode, target, "hello world", "--flag")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(result.stdout.splitlines()[0])[1:], ["hello world", "--flag"])
                self.assertEqual(result.stdout.count("executed-once"), 1)
                self.assertIn('"name": "process.run"', result.stderr)

    def test_failure_span_keeps_exit_code_and_excludes_exception_details(self):
        result = self.execute_bootstrap("job-code", "raise ValueError('private-test-value')")
        self.assertEqual(result.returncode, 1)
        spans = json.loads(next(line[6:] for line in result.stderr.splitlines() if line.startswith("SPANS=")))
        self.assertEqual(spans, [{"name": "process.run", "attributes": {"error.type": "ValueError"}, "events": 0, "status": "ERROR"}])
        self.assertNotIn("private-test-value", json.dumps(spans))
        exited = self.execute_bootstrap("job-code", "raise SystemExit(7)")
        self.assertEqual(exited.returncode, 7)

    def test_asgi_configuration_suppresses_transport_events(self):
        code = """import asyncio
from opentelemetry.instrumentation.asgi import OpenTelemetryMiddleware
async def app(scope, receive, send):
    await receive()
    await send({'type': 'http.response.start', 'status': 200})
    await send({'type': 'http.response.body', 'body': b'ok'})
async def receive(): return {'type': 'http.request', 'body': b''}
async def send(message): pass
scope = {'type': 'http', 'method': 'GET', 'path': '/', 'root_path': '', 'query_string': b'', 'headers': [], 'scheme': 'http', 'http_version': '1.1', 'server': ('localhost', 80), 'client': ('127.0.0.1', 1234)}
asyncio.run(OpenTelemetryMiddleware(app)(scope, receive, send))
"""
        result = self.execute_bootstrap("app-code", code)
        self.assertEqual(result.returncode, 0, result.stderr)
        spans = json.loads(next(line[6:] for line in result.stderr.splitlines() if line.startswith("SPANS=")))
        self.assertEqual([span["name"] for span in spans], ["GET /"])
        enabled = self.execute_bootstrap("app-code", code, asgi_events=True)
        self.assertEqual(enabled.returncode, 0, enabled.stderr)
        spans = json.loads(next(line[6:] for line in enabled.stderr.splitlines() if line.startswith("SPANS=")))
        self.assertEqual(len(spans), 4)

    def test_python_wheel_entrypoint_executes_embedded_binary(self):
        package = self.directory / "shinyhub"
        shutil.copytree(ROOT / "packaging/python/src/shinyhub", package, ignore=shutil.ignore_patterns("_binary", "__pycache__"))
        (package / "_binary").mkdir()
        shutil.copy2(BINARY, package / "_binary/shinyhub")
        result = subprocess.run([sys.executable, "-m", "shinyhub", "--version"], cwd=self.directory, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("shinyhub", result.stdout.lower())

    def test_real_native_app_launch_and_proxy_health(self):
        app = self.directory / "app"
        app.mkdir()
        minor = f"{sys.version_info.major}.{sys.version_info.minor}"
        (app / "pyproject.toml").write_text(f'[project]\nname="python-compat-app"\nversion="0.0.0"\nrequires-python="=={minor}.*"\ndependencies=["shiny>=1.8,<2", "pydantic"]\n', encoding="utf-8")
        (app / "app.py").write_text(f"""import sys
from shiny import App, ui
assert sys.version_info[:2] == {sys.version_info[:2]!r}
print('RUNTIME_CONFIRMED={minor}', flush=True)
app = App(ui.page_fluid(ui.h1('Python runtime compatibility')), lambda input, output, session: None)
""", encoding="utf-8")
        env = {key: value for key, value in os.environ.items() if not key.startswith("SHINYHUB_")}
        env.update(UV_PYTHON=sys.executable, UV_PYTHON_DOWNLOADS="never")
        result = subprocess.run([str(BINARY), "run", str(app), "--check", "--state-dir", str(self.directory / "state"), "--data-dir", str(self.directory / "data")], env=env, capture_output=True, text=True, timeout=180)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(f"RUNTIME_CONFIRMED={minor}", result.stdout + result.stderr)

    @unittest.skipUnless(sys.platform == "linux" and sys.version_info >= (3, 15), "live Tachyon attachment requires Linux and Python 3.15 in this test")
    def test_real_tachyon_profile_and_stack_dump(self):
        # Only this disposable child opts into inspection by its parent and
        # descendants. Do not alter the machine's ptrace policy or real apps.
        code = """import ctypes, os, time
libc = ctypes.CDLL(None)
assert libc.prctl(0x59616d61, os.getppid(), 0, 0, 0) == 0
print('ready', flush=True)
def work():
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        sum(i * i for i in range(10000))
work()
"""
        target = subprocess.Popen([sys.executable, "-c", code], stdout=subprocess.PIPE, text=True)
        try:
            self.assertEqual(target.stdout.readline().strip(), "ready")
            common = [str(BINARY), "diagnose", "python", str(target.pid), "--python", sys.executable, "--output", "json"]
            dump = subprocess.run(common, capture_output=True, text=True, timeout=30)
            self.assertEqual(dump.returncode, 0, dump.stderr)
            self.assertIn("<string>", json.loads(dump.stdout)["stacks"])
            destination = self.directory / "profile.html"
            profile = subprocess.run(common + ["--save", str(destination), "--duration", "1s"], capture_output=True, text=True, timeout=30)
            self.assertEqual(profile.returncode, 0, profile.stderr)
            self.assertEqual(json.loads(profile.stdout)["status"], "written")
            self.assertGreater(destination.stat().st_size, 100)
            self.assertEqual(destination.stat().st_mode & 0o777, 0o600)
            self.assertIsNone(target.poll(), "diagnostics do not stop the app")
        finally:
            target.terminate()
            target.wait(timeout=5)
            target.stdout.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    options = parser.parse_args()
    BINARY = options.binary.resolve(strict=True)
    print(f"Testing {sys.version} ({sys.executable})", flush=True)
    unittest.main(argv=[sys.argv[0]], verbosity=2)

"""Exercise the startup gate bundled in the image and native Worker entrypoint."""

import io
import contextlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch
import urllib.error


SCRIPT = Path(__file__).with_name("entrypoint.sh").read_text().split(
    "<<'PY_FLEET_READY'\n", 1
)[1].split("\nPY_FLEET_READY", 1)[0]


class StartupReadinessTest(unittest.TestCase):
    def run_gate(self, statuses, *, dead_server=False, manifest=None, body=b"real application", headers=None,
                 bootstrap_seconds=0):
        clock = [0]
        requests = []
        lock = threading.Lock()

        class Response:
            status = 200

            def __init__(self):
                self.headers = headers or {}

            def read(self, limit):
                return body

            def __enter__(self):
                return self

            def __exit__(self, *args):
                pass

        class Opener:
            def open(self, request, timeout):
                with lock:
                    requests.append(request)
                slug = request.full_url.split("/")[-2]
                status = statuses(slug, clock[0])
                if status != 200:
                    raise urllib.error.HTTPError(request.full_url, status, "pending", {}, None)
                return Response()

        def sleep(seconds):
            clock[0] += seconds

        manifest = manifest if manifest is not None else b'[[app]]\nslug="fast"\n[[app]]\nslug="slow"\n'
        output = io.StringIO()
        self.events = []
        with patch("sys.argv", ["-", "123", "fleet.toml"]), \
                patch.dict(os.environ, {"SHINYHUB_APP_ORIGIN": "https://apps.staging.example.com",
                                        "SHINYHUB_DEMO_BOOT_STARTED": str(-bootstrap_seconds)}), \
                patch("builtins.open", return_value=io.BytesIO(manifest)), \
                patch("os.kill", side_effect=ProcessLookupError if dead_server else None), \
                patch("time.monotonic", side_effect=lambda: clock[0]), \
                patch("time.sleep", side_effect=sleep), \
                patch("urllib.request.build_opener", return_value=Opener()), \
                contextlib.redirect_stdout(output):
            try:
                exec(compile(SCRIPT, "entrypoint.sh:readiness", "exec"), {})
            finally:
                self.events = [json.loads(line) for line in output.getvalue().splitlines()]
        return clock[0], requests

    def test_waits_for_slowest_application_and_rechecks_whole_fleet(self):
        elapsed, requests = self.run_gate(lambda slug, now: 503 if slug == "slow" and now < 2 else 200)
        self.assertEqual(elapsed, 2)
        self.assertEqual(len(requests), 6)
        for request in requests:
            self.assertEqual(request.get_header("Host"), "apps.staging.example.com")
            self.assertEqual(request.get_header("X-forwarded-proto"), "https")
            self.assertEqual(request.get_header("User-agent"), "demo-startup-check/1")
            self.assertEqual(request.get_header("Accept"), "application/octet-stream")

    def test_redirects_and_errors_never_admit_traffic(self):
        for status in (302, 401, 404, 503):
            with self.subTest(status=status), self.assertRaisesRegex(SystemExit, "slow"):
                self.run_gate(lambda slug, now: status if slug == "slow" else 200)

    def test_server_exit_fails_startup(self):
        with self.assertRaisesRegex(SystemExit, "ShinyHub exited"):
            self.run_gate(lambda slug, now: 200, dead_server=True)

    def test_http_200_wait_pages_and_rejection_headers_never_admit_traffic(self):
        with self.assertRaisesRegex(SystemExit, "fast, slow"):
            self.run_gate(lambda slug, now: 200, body=b'<div class="box" id="shinyhub-box">Starting app</div>')
        with self.assertRaisesRegex(SystemExit, "fast, slow"):
            self.run_gate(lambda slug, now: 200, headers={"X-Shinyhub-Reject": "replica-starting"})

    def test_empty_fleet_fails_startup(self):
        with self.assertRaisesRegex(SystemExit, "no applications"):
            self.run_gate(lambda slug, now: 200, manifest=b"app=[]\n")

    def test_ready_event_measures_bootstrap_and_slowest_app_once(self):
        self.run_gate(lambda slug, now: 503 if slug == "slow" and now < 2 else 200,
                      bootstrap_seconds=5)
        self.assertEqual(self.events, [{"event": "demo_fleet_ready", "elapsed_ms": 7000,
                                       "fleet_wait_ms": 2000, "application_count": 2}])

    def test_timeout_logs_only_unready_apps_and_never_ready(self):
        with self.assertRaises(SystemExit):
            self.run_gate(lambda slug, now: 503 if slug == "slow" else 200)
        self.assertEqual(self.events, [{"event": "demo_fleet_readiness_failed", "elapsed_ms": 60000,
                                       "fleet_wait_ms": 60000, "reason": "timeout", "pending": ["slow"]}])

    def test_server_exit_logs_failure(self):
        with self.assertRaises(SystemExit):
            self.run_gate(lambda slug, now: 200, dead_server=True)
        self.assertEqual(self.events[0]["event"], "demo_fleet_readiness_failed")
        self.assertEqual(self.events[0]["reason"], "server_exited")

    def run_boot_failure(self, command):
        # Run the actual entrypoint's trap before filesystem/bootstrap work so
        # early failure coverage does not need a container or modify /data.
        prefix = Path(__file__).with_name("entrypoint.sh").read_text().split("\nrandom_hex()", 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            os.symlink(sys.executable, Path(directory) / "python")
            result = subprocess.run(["/bin/sh", "-c", prefix + "\n" + command],
                                    env={**os.environ, "PATH": directory + os.pathsep + os.environ["PATH"],
                                         "SHINYHUB_DEPLOY_TOKEN": "private-token-must-not-be-logged"},
                                    capture_output=True, text=True, timeout=10)
        self.assertNotIn("private-token-must-not-be-logged", result.stdout + result.stderr)
        return result.returncode, [json.loads(line) for line in result.stdout.splitlines()]

    def test_early_failure_preserves_exit_code_and_logs_phase_once(self):
        code, events = self.run_boot_failure("boot_phase=fleet_apply\nexit 42")
        self.assertEqual(code, 42)
        self.assertEqual([event["event"] for event in events], ["demo_boot_started", "demo_boot_failed"])
        self.assertEqual(events[1]["phase"], "fleet_apply")
        self.assertEqual(events[1]["exit_code"], 42)
        self.assertGreaterEqual(events[1]["elapsed_ms"], 0)

    def test_interrupted_boot_logs_failure_and_exits(self):
        code, events = self.run_boot_failure("kill -TERM $$")
        self.assertEqual(code, 143)
        self.assertEqual(events[-1]["event"], "demo_boot_failed")
        self.assertEqual(events[-1]["reason"], "interrupted")


if __name__ == "__main__":
    unittest.main()

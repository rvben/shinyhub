"""Exercise the startup gate bundled in the image and native Worker entrypoint."""

import io
import os
from pathlib import Path
import threading
import unittest
from unittest.mock import patch
import urllib.error


SCRIPT = Path(__file__).with_name("entrypoint.sh").read_text().split(
    "<<'PY_FLEET_READY'\n", 1
)[1].split("\nPY_FLEET_READY", 1)[0]


class StartupReadinessTest(unittest.TestCase):
    def run_gate(self, statuses, *, dead_server=False, manifest=None, body=b"real application", headers=None):
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
        with patch("sys.argv", ["-", "123", "fleet.toml"]), \
                patch.dict(os.environ, {"SHINYHUB_APP_ORIGIN": "https://apps.staging.example.com"}), \
                patch("builtins.open", return_value=io.BytesIO(manifest)), \
                patch("os.kill", side_effect=ProcessLookupError if dead_server else None), \
                patch("time.monotonic", side_effect=lambda: clock[0]), \
                patch("time.sleep", side_effect=sleep), \
                patch("urllib.request.build_opener", return_value=Opener()):
            exec(compile(SCRIPT, "entrypoint.sh:readiness", "exec"), {})
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


if __name__ == "__main__":
    unittest.main()

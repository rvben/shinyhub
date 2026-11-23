"""Verify the previous ShinyHub release can serve the cached switcher."""

import hashlib
from http.client import HTTPConnection, HTTPException
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tempfile
import time

APP_HOST = "apps.example.test"
ASSET_PREFIX = "/app/.shinyhub/nav/"
SCRIPT = Path(__file__).resolve().parents[1] / "internal/appnav/assets/nav.min.js"


def fetch(port, path):
    connection = HTTPConnection("127.0.0.1", port, timeout=2)
    try:
        connection.request("GET", path, headers={"Host": APP_HOST})
        response = connection.getresponse()
        return response.status, response.headers.get("Cache-Control"), response.read()
    finally:
        connection.close()


def wait_ready(port, server):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline and server.poll() is None:
        try:
            if fetch(port, "/readyz")[0] == 200:
                return
        except (OSError, HTTPException):
            pass
        time.sleep(0.2)
    raise RuntimeError("rollback server did not become ready")


def check(binary):
    digest = hashlib.sha256(SCRIPT.read_bytes()).hexdigest()
    with tempfile.TemporaryDirectory(prefix="shinyhub-app-nav-rollback-") as name:
        work = Path(name)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        config = work / "shinyhub.yaml"
        config.write_text(
            f"server:\n  host: 127.0.0.1\n  port: {port}\n"
            f"  base_url: https://hub.example.test\n  app_origin: https://{APP_HOST}\n"
            f"auth:\n  secret: {secrets.token_hex(32)}\n"
        )
        log = work / "server.log"
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith("SHINYHUB_")}
        environment.update(HOME=name, XDG_CONFIG_HOME=name)
        password = work / "admin-password"
        password.touch(mode=0o600)
        password.write_text(secrets.token_hex(24))
        with log.open("w") as output:
            server = None
            try:
                subprocess.run(
                    [binary, "init", "--config", str(config), "--admin-user", "release-check",
                     "--admin-password-file", str(password)],
                    cwd=work,
                    env=environment,
                    stdout=output,
                    stderr=subprocess.STDOUT,
                    check=True,
                    timeout=30,
                )
                server = subprocess.Popen(
                    [binary, "serve", "--config", str(config), "--no-browser"],
                    cwd=work,
                    env=environment,
                    stdout=output,
                    stderr=subprocess.STDOUT,
                )
                wait_ready(port, server)
                missing = ASSET_PREFIX + "0" * 64 + ".js"
                status, cache, _ = fetch(port, missing)
                if (status, cache) != (404, "no-store"):
                    raise RuntimeError(f"unknown digest returned HTTP {status}, cache policy {cache!r}")
                asset = ASSET_PREFIX + digest + ".js"
                status, cache, content = fetch(port, asset)
                if status != 200:
                    raise RuntimeError(f"rollback server returned HTTP {status} for {asset}")
                if hashlib.sha256(content).hexdigest() != digest:
                    raise RuntimeError(f"rollback server returned incorrect bytes for {asset}")
                if cache != "private, max-age=31536000, immutable":
                    raise RuntimeError(f"rollback server returned cache policy {cache!r} for {asset}")
                print(f"Rollback server serves switcher SHA-256 {digest}")
            except (OSError, HTTPException, RuntimeError, subprocess.SubprocessError) as error:
                print("\n".join(log.read_text().splitlines()[-25:]), file=sys.stderr)
                raise SystemExit(f"App switcher rollback check failed: {error}") from error
            finally:
                if server is not None:
                    if server.poll() is None:
                        server.terminate()
                    try:
                        server.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        server.kill()
                        server.wait()


if __name__ == "__main__":
    binary = str(Path(sys.argv[1]).resolve()) if len(sys.argv) > 1 else "shinyhub"
    check(binary)

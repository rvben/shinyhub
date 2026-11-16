#!/usr/bin/env python3
"""Real Chromium acceptance for native OIDC behind isolated loopback HTTPS edges.

Uses the pinned provider/source builder from entra-oidc-e2e.py. Requires its
Python/Go prerequisites plus OpenSSL and Playwright 1.64.1 with Chromium.
"""

import argparse
import base64
import contextlib
import hashlib
import http.client
import http.server
import importlib.metadata
import importlib.util
import json
import os
import secrets
import signal
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from playwright.sync_api import expect, sync_playwright

SPEC = importlib.util.spec_from_file_location(
    "entra_acceptance", Path(__file__).with_name("entra-oidc-e2e.py")
)
fixture = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixture)
TENANT = "6f89cf12-978b-4d23-ac18-9ef0c127cf87"
ALICE = "df8ec5dd-1599-45ef-908b-4ae020cd1dbe"


class CheckFailure(RuntimeError):
    """A fixed check name safe to include in the public summary."""


class Edge(http.server.ThreadingHTTPServer):
    """Streaming HTTPS reverse proxy, bound only to loopback with no external targets."""

    daemon_threads = True
    block_on_close = False
    request_queue_size = 128

    def __init__(self, backend, certificate, key):
        super().__init__(("127.0.0.1", 0), Proxy)
        self.backend = backend
        self.available = True
        self.unavailable_paths = set()
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.load_cert_chain(certificate, key)
        self.socket = context.wrap_socket(
            self.socket, server_side=True, do_handshake_on_connect=False
        )
        self.thread = threading.Thread(target=self.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.shutdown()
        self.server_close()
        self.thread.join(timeout=5)

    def handle_error(self, *_):
        pass  # TLS probes and browser cancellation must not print sensitive URLs.


class Proxy(http.server.BaseHTTPRequestHandler):
    timeout = 15

    def setup(self):
        self.request.settimeout(self.timeout)
        self.request.do_handshake()
        super().setup()

    def log_message(self, *_):
        pass  # Never log callback codes, cookies, ID-token hints or state URLs.

    def do_GET(self):
        self.forward()

    def do_POST(self):
        self.forward()

    def do_DELETE(self):
        self.forward()

    do_PUT = do_POST
    do_PATCH = do_POST
    do_HEAD = do_GET

    def forward(self):
        if (
            not self.server.available
            or urllib.parse.urlsplit(self.path).path in self.server.unavailable_paths
        ):
            self.send_error(503, "Local test provider unavailable")
            return
        upstream = http.client.HTTPConnection(
            "127.0.0.1", self.server.backend, timeout=15
        )
        hop = {
            "connection",
            "keep-alive",
            "transfer-encoding",
            "upgrade",
            "proxy-connection",
        }
        sent = False
        try:
            body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            headers = {
                k: v
                for k, v in self.headers.items()
                if k.lower() not in hop and not k.lower().startswith("x-forwarded-")
            }
            headers.update(
                {"X-Forwarded-Proto": "https", "X-Forwarded-For": "127.0.0.1"}
            )
            upstream.request(
                self.command, self.path, body=body or None, headers=headers
            )
            response = upstream.getresponse()
            self.send_response(response.status)
            hop.update(
                value.strip().lower()
                for value in response.getheader("Connection", "").split(",")
            )
            for key, value in response.getheaders():
                if key.lower() not in hop | {"server", "date"}:
                    self.send_header(key, value)
            self.send_header("Connection", "close")
            self.end_headers()
            sent = True
            while chunk := response.read1(65536):
                self.wfile.write(chunk)
                self.wfile.flush()
        except (OSError, http.client.HTTPException):
            if not sent:
                self.send_error(502, "Local test upstream unavailable")
            self.close_connection = True
        finally:
            upstream.close()


def run(output):
    checks = []
    provenance = {}

    def check(name, condition):
        if not condition:
            raise CheckFailure(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def wait(url, process, opener):
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError("Fixture process exited before readiness")
            try:
                with opener.open(url, timeout=1) as response:
                    if response.status == 200:
                        return
            except OSError:
                pass
            time.sleep(0.2)
        raise RuntimeError("Fixture readiness timed out")

    try:
        installed_version = importlib.metadata.version("playwright")
        driver_package = json.loads(
            (fixture.REPO / "loadtest/render/driver/package.json").read_text()
        )
        if installed_version != driver_package["dependencies"]["playwright"]:
            raise RuntimeError("Python and browser-driver Playwright locks must match")
        with (
            tempfile.TemporaryDirectory(
                prefix="shinyhub-entra-browser-build-"
            ) as directory,
            contextlib.ExitStack() as resources,
        ):
            hub_binary, provider_binary, provenance = fixture.build_binaries(
                Path(directory), output
            )
            cert, key = output / "certificate.pem", output / "key.pem"
            resources.callback(key.unlink, missing_ok=True)
            fixture.command(
                [
                    "openssl",
                    "req",
                    "-x509",
                    "-newkey",
                    "rsa:2048",
                    "-nodes",
                    "-keyout",
                    str(key),
                    "-out",
                    str(cert),
                    "-subj",
                    "/CN=localhost",
                    "-days",
                    "1",
                    "-addext",
                    "subjectAltName=DNS:localhost,IP:127.0.0.1",
                    "-addext",
                    "basicConstraints=critical,CA:TRUE",
                    "-addext",
                    "keyUsage=critical,digitalSignature,keyCertSign",
                    "-addext",
                    "extendedKeyUsage=serverAuth",
                ],
                cwd=output,
                env={"PATH": os.environ["PATH"]},
                log=output / "certificate.log",
            )
            public = subprocess.run(
                ["openssl", "x509", "-in", str(cert), "-pubkey", "-noout"],
                check=True,
                capture_output=True,
                timeout=10,
            ).stdout
            der = subprocess.run(
                ["openssl", "pkey", "-pubin", "-outform", "DER"],
                input=public,
                check=True,
                capture_output=True,
                timeout=10,
            ).stdout
            spki = base64.b64encode(hashlib.sha256(der).digest()).decode()
            tls = ssl.create_default_context(cafile=cert)
            opener = urllib.request.build_opener(
                urllib.request.ProxyHandler({}),
                urllib.request.HTTPSHandler(context=tls),
            )
            ports = set()
            provider_edge = Edge(None, cert, key)
            resources.callback(provider_edge.close)
            hub_edge = Edge(None, cert, key)
            resources.callback(hub_edge.close)
            while len(ports) < 2:
                port = fixture.free_port()
                if port not in {provider_edge.server_port, hub_edge.server_port}:
                    ports.add(port)
            provider_port, hub_port = ports
            provider_edge.backend, hub_edge.backend = provider_port, hub_port
            provider = f"https://localhost:{provider_edge.server_port}"
            hub = f"https://127.0.0.1:{hub_edge.server_port}"
            issuer = provider + "/" + TENANT + "/v2.0"
            env = {
                k: v for k, v in os.environ.items() if k in {"PATH", "HOME", "TMPDIR"}
            }
            env.update(
                NO_PROXY="localhost,127.0.0.1",
                SSL_CERT_FILE=str(cert),
                # Go 1.27 supports process-local CA files on macOS too. The
                # project's older go directive otherwise retains platform roots.
                GODEBUG="x509sslcertoverrideplatform=1",
            )

            def launch(binary, args, name, extra):
                log = resources.enter_context((output / (name + ".log")).open("w"))
                process = subprocess.Popen(
                    [str(binary), *args],
                    cwd=output,
                    env=env | extra,
                    stdout=log,
                    stderr=log,
                    start_new_session=True,
                )
                resources.callback(fixture.stop, process)
                return process

            provider_process = launch(
                provider_binary,
                [],
                "provider",
                {
                    "HOST": "127.0.0.1",
                    "PORT": str(provider_port),
                    "TLS_ENABLED": "false",
                    "ORIGIN_MODE": "compat",
                    "PUBLIC_ORIGIN": provider,
                    "ENTRA_DATA_DIR": str(output / "provider"),
                },
            )
            wait(provider + "/health", provider_process, opener)

            def api(path, body=None, method=None, bearer=None, form=False):
                headers = {"User-Agent": "OIDC-HTTPS-Browser-Acceptance"}
                if bearer:
                    headers["Authorization"] = "Bearer " + bearer
                if body is not None:
                    headers["Content-Type"] = (
                        "application/x-www-form-urlencoded"
                        if form
                        else "application/json"
                    )
                data = (
                    (
                        urllib.parse.urlencode(body).encode()
                        if form
                        else json.dumps(body).encode()
                    )
                    if body is not None
                    else None
                )
                with opener.open(
                    urllib.request.Request(
                        provider + path, data=data, method=method, headers=headers
                    ),
                    timeout=15,
                ) as response:
                    content = response.read()
                    return json.loads(content) if content else None

            discovery = api("/" + TENANT + "/v2.0/.well-known/openid-configuration")
            check(
                "TLS-verified discovery advertises the HTTPS issuer",
                discovery["issuer"] == issuer,
            )
            app = api(
                "/admin/api/apps",
                {
                    "displayName": "HTTPS browser acceptance",
                    "isConfidential": True,
                    "redirectUris": [
                        {"uri": hub + "/api/auth/oidc/callback", "type": "web"},
                        {"uri": hub + "/?signed_out=1", "type": "web"},
                    ],
                    "frontchannelLogoutUri": hub + "/api/auth/oidc/frontchannel-logout",
                },
            )
            client = app["id"]
            secret = api(f"/admin/api/apps/{client}/secrets", {})["secretText"]
            token = api(
                f"/{TENANT}/oauth2/v2.0/token",
                {
                    "grant_type": "client_credentials",
                    "client_id": "00d88624-f0d7-46f6-a641-6232c2608928",
                    "client_secret": "daemon-app-secret",
                    "scope": "https://graph.microsoft.com/.default",
                },
                form=True,
            )["access_token"]
            roles = {
                value: api(
                    f"/admin/api/apps/{client}/roles",
                    {
                        "value": value,
                        "displayName": value,
                        "allowedMemberTypes": ["User"],
                        "isEnabled": True,
                    },
                )["id"]
                for value in ["admins", "viewers"]
            }

            def assign(value):
                return api(
                    f"/graph/v1.0/servicePrincipals/{client}/appRoleAssignedTo",
                    {
                        "principalId": ALICE,
                        "resourceId": client,
                        "appRoleId": roles[value],
                    },
                    bearer=token,
                )["id"]

            def remove(assignment):
                api(
                    f"/graph/v1.0/servicePrincipals/{client}/appRoleAssignedTo/{assignment}",
                    method="DELETE",
                    bearer=token,
                )

            assignment = assign("admins")
            password = secrets.token_urlsafe(32)
            password_file = output / "password"
            password_file.write_text(password)
            resources.callback(password_file.unlink, missing_ok=True)
            config = output / "hub.yaml"
            config_text = fixture.hub_config(
                output, issuer, client, hub, hub_port, max_age="15m"
            )
            for before, after in [
                (
                    "  shutdown_apps: stop",
                    "  shutdown_apps: stop\n  trusted_proxies: [127.0.0.1/32]",
                ),
                ("  session_ttl: 1m", "  session_ttl: 15m"),
            ]:
                if config_text.count(before) != 1:
                    raise RuntimeError("Shared fixture configuration changed")
                config_text = config_text.replace(before, after)
            config.write_text(config_text)
            hub_env = env | {
                "SHINYHUB_AUTH_SECRET": secrets.token_hex(32),
                "SHINYHUB_OIDC_CLIENT_SECRET": secret,
            }
            fixture.command(
                [
                    str(hub_binary),
                    "init",
                    "--config",
                    str(config),
                    "--admin-user",
                    "sandbox-recovery",
                    "--admin-password-file",
                    str(password_file),
                ],
                cwd=output,
                env=hub_env,
                log=output / "init.log",
            )
            hub_process = launch(
                hub_binary, ["serve", "--config", str(config)], "hub", hub_env
            )
            wait(hub + "/readyz", hub_process, opener)
            with (
                sync_playwright() as playwright,
                contextlib.ExitStack() as browser_resources,
            ):
                # Trust only this disposable certificate's public key, not arbitrary
                # invalid HTTPS certificates. No machine/browser trust store is changed.
                browser = playwright.chromium.launch(
                    args=["--ignore-certificate-errors-spki-list=" + spki]
                )
                browser_version = browser.version
                browser_resources.callback(browser.close)
                contexts = []
                unexpected = []
                errors = []
                framing_errors = []

                def private_log(name, message):
                    with (output / name).open("a") as stream:
                        stream.write(message + "\n")

                def new_page():
                    context = browser.new_context(
                        user_agent="OIDC-HTTPS-Browser-Acceptance",
                        service_workers="block",
                    )
                    contexts.append(context)

                    def guard(route):
                        parts = urllib.parse.urlsplit(route.request.url)
                        if parts.scheme == "https" and (parts.hostname, parts.port) in {
                            ("127.0.0.1", hub_edge.server_port),
                            ("localhost", provider_edge.server_port),
                        }:
                            route.continue_()
                        else:
                            unexpected.append(parts.scheme + ":" + str(parts.hostname))
                            route.abort()

                    context.route("**/*", guard)
                    page = context.new_page()

                    def page_error(error):
                        errors.append(str(error))
                        (output / "browser-errors.json").write_text(json.dumps(errors))

                    page.on("pageerror", page_error)

                    def console_error(message):
                        if message.type == "error":
                            private_log("console-errors.log", message.text)
                            if any(
                                term in message.text.lower()
                                for term in ["frame-ancestors", "x-frame-options"]
                            ):
                                framing_errors.append(message.text)

                    page.on("console", console_error)
                    page.on(
                        "requestfailed",
                        lambda request: private_log(
                            "request-errors.log",
                            urllib.parse.urlsplit(request.url).path
                            + ": "
                            + str(request.failure),
                        ),
                    )
                    page.set_default_timeout(15000)
                    return page

                def me(page):
                    return page.evaluate(
                        """async () => {const r=await fetch('/api/auth/me'); return {status:r.status, body:r.ok?await r.json():null};}"""
                    )

                def login(page, expected=200, token_outage=False):
                    page.goto(hub + "/?next=%2Fidentity")
                    try:
                        page.locator(".oidc-login").click()
                    except Exception:
                        with contextlib.suppress(Exception):
                            (output / "login-diagnostic.json").write_text(
                                json.dumps(
                                    page.evaluate(
                                        """async () => ({text:document.body.innerText, providers:await (await fetch('/api/auth/providers')).json()})"""
                                    )
                                )
                            )
                        raise
                    chooser = page.locator(
                        f'form:has(input[name="__ee_user"][value="{ALICE}"]) button'
                    )
                    expect(chooser).to_be_visible()
                    if (
                        "OIDC state cookie survives the cross-site HTTPS redirect"
                        not in checks
                    ):
                        state = [
                            c
                            for c in page.context.cookies(
                                hub + "/api/auth/oidc/callback"
                            )
                            if c["name"] == "shiny_oauth_state"
                        ]
                        check(
                            "OIDC state cookie survives the cross-site HTTPS redirect",
                            len(state) == 1
                            and state[0]["secure"]
                            and state[0]["httpOnly"]
                            and state[0]["sameSite"] == "Lax",
                        )
                    if token_outage:
                        provider_edge.unavailable_paths.add(
                            f"/{TENANT}/oauth2/v2.0/token"
                        )
                    with page.expect_response(
                        lambda r: (
                            urllib.parse.urlsplit(r.url).path
                            == "/api/auth/oidc/callback"
                        )
                    ) as response:
                        chooser.click()
                    if response.value.status != (302 if expected == 200 else expected):
                        raise RuntimeError("Unexpected browser OIDC callback status")
                    if expected == 200:
                        page.wait_for_url(hub + "/identity")
                        expect(page.locator("#identity-card")).to_be_visible()

                alice = new_page()
                login(alice)
                check(
                    "UI login provisions an administrator over HTTPS",
                    me(alice)["body"]["user"]["role"] == "admin",
                )
                session = next(
                    c
                    for c in alice.context.cookies(hub)
                    if c["name"] == "shiny_session"
                )
                check(
                    "Session cookie is Secure, HttpOnly, bound to the hub and SameSite Lax",
                    session["secure"]
                    and session["httpOnly"]
                    and session["sameSite"] == "Lax"
                    and session["domain"] == "127.0.0.1"
                    and session["path"] == "/",
                )
                check(
                    "JavaScript cannot read the session cookie",
                    "shiny_session=" not in alice.evaluate("document.cookie"),
                )
                independent = new_page()
                login(independent)
                check(
                    "Independent sessions are live before logout",
                    me(alice)["status"] == 200 and me(independent)["status"] == 200,
                )
                session = next(
                    c
                    for c in alice.context.cookies(hub)
                    if c["name"] == "shiny_session"
                )
                replay = new_page()
                replay.context.add_cookies([session])
                replay.goto(hub + "/")
                check(
                    "Captured cookie works before its session is revoked",
                    me(replay)["status"] == 200,
                )
                with alice.expect_response(
                    lambda r: r.url.startswith(discovery["end_session_endpoint"])
                ) as logout_response:
                    alice.locator("#identity-card").click()
                    alice.locator("#account-logout").click()
                check(
                    "UI logout reaches the real provider end-session endpoint",
                    logout_response.value.status == 200,
                )
                alice.wait_for_url(hub + "/?signed_out=1")
                check(
                    "RP logout returns to the registered signed-out page",
                    me(alice)["status"] == 401,
                )
                check(
                    "RP logout preserves an independent browser session",
                    me(independent)["status"] == 200,
                )
                check(
                    "Captured pre-logout cookie cannot recover its session",
                    me(replay)["status"] == 401,
                )

                # The browser loads the actual provider iframe across localhost /
                # 127.0.0.1 sites; the request must work without RP session cookies.
                with independent.expect_response(
                    lambda r: (
                        urllib.parse.urlsplit(r.url).path
                        == "/api/auth/oidc/frontchannel-logout"
                    )
                ) as frame:
                    independent.goto(discovery["end_session_endpoint"])
                (output / "logout-frame.json").write_text(
                    json.dumps(
                        {
                            "status": frame.value.status,
                            "headers": frame.value.all_headers(),
                            "failure": frame.value.request.failure,
                        }
                    )
                )
                check(
                    "Provider iframe finishes without a browser frame-policy block",
                    frame.value.request.failure is None
                    and frame.value.header_value("x-frame-options") is None
                    and not framing_errors
                    and any(
                        child.url == frame.value.url
                        and child.evaluate("location.href") == frame.value.url
                        for child in independent.frames
                    )
                    and not any(
                        child.url.startswith("chrome-error://")
                        for child in independent.frames
                    ),
                )
                check(
                    "Cross-site provider logout iframe succeeds without an RP cookie",
                    frame.value.status == 200
                    and "shiny_session="
                    not in frame.value.request.all_headers().get("cookie", ""),
                )
                independent.goto(hub + "/?signed_out=1")
                check(
                    "Provider-initiated iframe revokes the matching browser session",
                    me(independent)["status"] == 401,
                )
                role_page = new_page()
                login(role_page)
                check(
                    "Administrator can access a protected action before demotion",
                    role_page.evaluate("async () => (await fetch('/api/users')).status")
                    == 200,
                )
                remove(assignment)
                viewer_assignment = assign("viewers")
                viewer = new_page()
                login(viewer)
                check(
                    "Role change is observed by the new browser login",
                    me(viewer)["body"]["user"]["role"] == "viewer",
                )
                check(
                    "Demotion changes an existing browser session's authorization",
                    me(role_page)["body"]["user"]["role"] == "viewer"
                    and role_page.evaluate(
                        "async () => (await fetch('/api/users')).status"
                    )
                    == 403,
                )
                remove(viewer_assignment)
                check(
                    "Sessions remain live until verified authorization refusal",
                    me(viewer)["status"] == 200 and me(role_page)["status"] == 200,
                )
                denied = new_page()
                login(denied, 403)
                check(
                    "Missing roles revoke existing browser sessions",
                    me(viewer)["status"] == 401 and me(role_page)["status"] == 401,
                )
                check(
                    "Refused browser does not acquire a session",
                    me(denied)["status"] == 401,
                )
                assign("viewers")
                token_failure = new_page()
                login(token_failure, 502, token_outage=True)
                check(
                    "Token endpoint failure cannot create a browser session",
                    me(token_failure)["status"] == 401
                    and not [
                        c
                        for c in token_failure.context.cookies(hub)
                        if c["name"] == "shiny_session"
                    ],
                )
                provider_edge.available = False
                outage = new_page()
                outage.goto(hub + "/")
                with outage.expect_response(
                    lambda r: r.url.startswith(provider + "/")
                ) as unavailable:
                    outage.locator(".oidc-login").click()
                check(
                    "Browser observes the provider's unavailable sign-in endpoint",
                    unavailable.value.status == 503,
                )
                recovery = new_page()
                recovery.goto(hub + "/?signed_out=1")
                recovery.locator("#login-username").fill("sandbox-recovery")
                recovery.locator("#login-password").fill(password)
                recovery.locator('#login-form button[type="submit"]').click()
                expect(recovery.locator("#identity-card")).to_be_visible()
                check(
                    "Local recovery remains available during provider failure",
                    me(recovery)["body"]["user"]["role"] == "admin",
                )
                recovery.locator("#identity-card").click()
                recovery.locator("#account-logout").click()
                recovery.wait_for_url(hub + "/")
                check(
                    "Local recovery logout needs no provider",
                    me(recovery)["status"] == 401,
                )
                check(
                    "Browser traffic remains on the two isolated HTTPS origins",
                    not unexpected,
                )
                check("Browser scripts finish without uncaught errors", not errors)
                for context in contexts:
                    context.close()
            check(
                "Tested binaries remain unchanged",
                fixture.digest(hub_binary) == provenance["shinyhub_binary_sha256"]
                and fixture.digest(provider_binary)
                == provenance["provider_binary_sha256"],
            )
        report = provenance | {
            "fixture_harness_sha256": provenance["harness_sha256"],
            "status": "passed",
            "passed": checks,
            "browser": "chromium",
            "browser_version": browser_version,
            "playwright_version": importlib.metadata.version("playwright"),
            "harness_sha256": fixture.digest(__file__),
            "scope": "Chromium on two isolated loopback HTTPS origins; a pinned disposable certificate is trusted only by this browser and the fixture clients. UI login, cookie attributes, RP/front-channel logout, role demotion/refusal and local recovery. No Microsoft tenant, real Entra policy, deployment or machine trust-store changes.",
        }
        (output / "results.json").write_text(json.dumps(report, indent=2) + "\n")
        print(f"RESULT {len(checks)} HTTPS browser checks passed", flush=True)
    except Exception as error:
        (output / "failure.log").write_text(traceback.format_exc())
        (output / "results.json").write_text(
            json.dumps(
                provenance
                | {
                    "status": "failed",
                    "error_type": type(error).__name__,
                    "failed_check": str(error)
                    if isinstance(error, CheckFailure)
                    else None,
                    "passed": checks,
                },
                indent=2,
            )
            + "\n"
        )
        raise


def main():
    for sig in [signal.SIGTERM, signal.SIGHUP]:
        signal.signal(sig, lambda number, _: sys.exit(128 + number))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    output = (
        args.output.resolve()
        if args.output
        else Path(tempfile.mkdtemp(prefix="shinyhub-entra-browser-"))
    )
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    if any(output.iterdir()):
        parser.error("Output directory must be new or empty")
    output.chmod(0o700)
    print("Private output: " + str(output), flush=True)
    try:
        run(output)
    except Exception as error:  # noqa: BLE001 - sanitize private browser URLs at the CLI boundary
        print(
            f"FAILED ({type(error).__name__}); inspect the private output directory",
            file=sys.stderr,
        )
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()

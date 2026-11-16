#!/usr/bin/env python3
"""Run native ShinyHub/Entra-emulator compatibility checks without a Microsoft tenant.

Requires Python 3.14+, Git and Go 1.27.4. Builds a pinned provider in a temporary
checkout, binds both processes to loopback, and preserves a machine-readable
report. Does not use Docker, existing databases, cloud credentials or production.
"""

import argparse
import base64
import contextlib
import datetime
import errno
import hashlib
import html
import http.cookiejar
import json
import os
import re
import secrets
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
FIXTURE = REPO / "scripts/acceptance/fixtures/entra-emulator"


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def stop(process):
    """Stop our isolated process group, including any descendants."""
    if process is None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=20)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)


def command(args, *, cwd, env, log, capture=False):
    """Bound subprocesses and keep build diagnostics in the private output directory."""
    with log.open("ab") as stream:
        process = subprocess.Popen(
            args,
            cwd=cwd,
            env=env,
            stdout=subprocess.PIPE if capture else stream,
            stderr=stream,
            start_new_session=True,
        )
        try:
            stdout, _ = process.communicate(timeout=600)
            if process.returncode:
                raise subprocess.CalledProcessError(process.returncode, args)
        finally:
            stop(process)
    if capture == "bytes":
        return stdout
    return stdout.decode().strip() if capture else None


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def build_binaries(build, output):
    env = {
        k: v
        for k, v in os.environ.items()
        if k in {"PATH", "HOME", "TMPDIR", "GOPATH", "GOCACHE", "GOMODCACHE"}
    }
    env.update(
        GOWORK="off",
        GOFLAGS="-mod=readonly",
        GOENV="off",
        GOTOOLCHAIN="local",
        CGO_ENABLED="0",
        GOMAXPROCS="4",
        GOGC="50",
        GIT_CONFIG_GLOBAL=os.devnull,
        GIT_CONFIG_NOSYSTEM="1",
        GIT_TERMINAL_PROMPT="0",
    )
    log = output / "build.log"
    manifest = json.loads((FIXTURE / "source.json").read_text())
    version = command(
        ["go", "env", "GOVERSION"], cwd=REPO, env=env, log=log, capture=True
    )
    if version != "go" + manifest["go_version"]:
        raise RuntimeError("Use the exact Go toolchain pinned in the provider fixture")
    patch = FIXTURE / "compatibility.patch"
    if digest(patch) != manifest["patch_sha256"]:
        raise RuntimeError("Provider compatibility patch checksum mismatch")
    source = build / "provider-source"
    source.mkdir()
    run = lambda args, cwd=source, capture=False: command(
        args, cwd=cwd, env=env, log=log, capture=capture
    )
    print("Preparing pinned emulator source...", flush=True)
    run(["git", "init", "--quiet"])
    run(
        [
            "git",
            "-c",
            "http.userAgent=OIDC-Compatibility-Acceptance",
            "fetch",
            "--quiet",
            "--depth=1",
            manifest["repository"],
            manifest["baseline_commit"],
        ]
    )
    run(["git", "checkout", "--quiet", "--detach", "FETCH_HEAD"])
    if run(["git", "rev-parse", "HEAD"], capture=True) != manifest["baseline_commit"]:
        raise RuntimeError("Provider baseline revision mismatch")
    run(["git", "apply", "--check", str(patch)])
    run(["git", "apply", str(patch)])
    run(["git", "add", "--all"])
    if run(["git", "write-tree"], capture=True) != manifest["expected_tree"]:
        raise RuntimeError("Patched provider source tree mismatch")
    provider = build / "entra-emulator"
    hub = build / "shinyhub"
    print("Building the pinned emulator and current ShinyHub...", flush=True)
    run(
        [
            "go",
            "build",
            "-p=2",
            "-trimpath",
            "-buildvcs=false",
            "-o",
            str(provider),
            "./cmd/entra-emulator",
        ]
    )
    if (
        run(["git", "write-tree"], capture=True) != manifest["expected_tree"]
        or run(["git", "diff", "--name-only"], capture=True)
        or run(["git", "ls-files", "--others", "--exclude-standard"], capture=True)
    ):
        raise RuntimeError("Provider source changed during build")
    revision = run(["git", "rev-parse", "HEAD"], cwd=REPO, capture=True)

    def source_changes():
        diff = run(
            ["git", "diff", "--no-ext-diff", "--no-textconv", "--binary", "HEAD"],
            cwd=REPO,
            capture="bytes",
        )
        names = run(
            ["git", "ls-files", "-z", "--others", "--exclude-standard"],
            cwd=REPO,
            capture="bytes",
        ).split(b"\0")
        hashes = {
            os.fsdecode(name): digest(REPO / os.fsdecode(name))
            for name in names
            if name and (REPO / os.fsdecode(name)).is_file()
        }
        return diff, hashes

    dirty_diff, untracked_hashes = source_changes()
    run(
        ["go", "build", "-p=2", "-trimpath", "-o", str(hub), "./cmd/shinyhub"], cwd=REPO
    )
    hub_info = run(["go", "version", "-m", str(hub)], capture=True)
    fields = dict(re.findall("(vcs\\.revision|vcs\\.modified)=([^\\s]+)", hub_info))
    if (
        fields.get("vcs.revision") != revision
        or fields.get("vcs.modified") not in {"true", "false"}
        or revision != run(["git", "rev-parse", "HEAD"], cwd=REPO, capture=True)
    ):
        raise RuntimeError("ShinyHub build provenance mismatch")
    if os.environ.get("CI") and fields["vcs.modified"] != "false":
        raise RuntimeError("CI acceptance requires a clean ShinyHub source checkout")
    if (dirty_diff, untracked_hashes) != source_changes():
        raise RuntimeError("ShinyHub working source changed during build")
    provenance = {
        "provider_source": manifest,
        "provider_binary_sha256": digest(provider),
        "provider_build_info": run(
            ["go", "version", "-m", str(provider)], capture=True
        ),
        "shinyhub_commit": revision,
        "shinyhub_source_dirty": fields["vcs.modified"] == "true",
        "shinyhub_diff_sha256": hashlib.sha256(dirty_diff).hexdigest(),
        "shinyhub_untracked_sha256": untracked_hashes,
        "shinyhub_build_info": hub_info,
        "shinyhub_binary_sha256": digest(hub),
        "harness_sha256": digest(__file__),
    }
    return (hub, provider, provenance)


def hub_config(state, issuer, client, base, port, max_age="5m"):
    q = lambda value: json.dumps(str(value))
    return f"""database:
  dsn: {q(state / "hub.sqlite")}
server:
  host: 127.0.0.1
  port: {port}
  base_url: {q(base)}
  pid_file: {q(state / "hub.pid")}
  shutdown_apps: stop
auth:
  local_login: true
  oauth_default_role: viewer
  session_ttl: 1m
  session_max_age: {max_age}
  forward_auth: {{enabled: false}}
  group_role_mappings:
    - {{group: admins, role: admin}}
    - {{group: developers, role: developer}}
    - {{group: viewers, role: viewer}}
oauth:
  oidc:
    issuer_url: {q(issuer)}
    client_id: {q(client)}
    callback_url: {q(base + "/api/auth/oidc/callback")}
    display_name: Local Entra compatibility fixture
    username_claim: preferred_username
    username_strip_domain: true
    username_required: true
    groups_claim: roles
    missing_groups: reject
    require_groups: true
    require_valid_groups: true
    link_existing: off
    auto_redirect: true
    end_session: true
storage:
  apps_dir: {q(state / "apps")}
  app_data_dir: {q(state / "app-data")}
metrics: {{enabled: false}}
tracing: {{enabled: false}}
runtime: {{mode: native}}
"""


def exercise(output, hub_binary, provider_binary, provenance):
    hub_port = free_port()
    provider_port = free_port()
    while provider_port == hub_port:
        provider_port = free_port()
    ROOT = output
    BIN = hub_binary
    # Cookies are host-scoped, so use separate loopback hostnames for the two parties.
    PROVIDER = "http://localhost:" + str(provider_port)
    BASE = "http://127.0.0.1:" + str(hub_port)
    TENANT = "6f89cf12-978b-4d23-ac18-9ef0c127cf87"
    ALICE = "df8ec5dd-1599-45ef-908b-4ae020cd1dbe"
    BOB = "0d4ba1f9-cab1-4200-b516-d4cb8b340930"
    ISSUER = PROVIDER + "/" + TENANT + "/v2.0"
    checks = []

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None

    class Browser:
        def __init__(self):
            self.cookies = http.cookiejar.CookieJar()
            handlers = [
                urllib.request.ProxyHandler({}),
                urllib.request.HTTPCookieProcessor(self.cookies),
            ]
            self.follow = urllib.request.build_opener(*handlers)
            self.steps = urllib.request.build_opener(*handlers, NoRedirect())

        def request(
            self,
            url,
            body=None,
            follow=True,
            form=False,
            method=None,
            bearer=None,
            cookie=None,
        ):
            url = url if url.startswith("http") else BASE + url
            hostname = urllib.parse.urlsplit(url).hostname
            headers = {"User-Agent": "OIDC-Compatibility-Evaluation"}
            if bearer:
                headers["Authorization"] = "Bearer " + bearer
            if cookie:
                headers["Cookie"] = "shiny_session=" + cookie
            if body is not None:
                headers.update(
                    {
                        "Content-Type": "application/x-www-form-urlencoded"
                        if form
                        else "application/json",
                        "Origin": BASE,
                        "Referer": BASE + "/",
                    }
                )
                csrf = next(
                    (
                        c.value
                        for c in self.cookies
                        if c.name == "csrf_token"
                        and c.domain.lstrip(".") in {hostname, hostname + ".local"}
                    ),
                    None,
                )
                if csrf:
                    headers["X-CSRF-Token"] = csrf
            data = (
                (
                    urllib.parse.urlencode(body).encode()
                    if form
                    else json.dumps(body).encode()
                )
                if body is not None
                else None
            )
            req = urllib.request.Request(
                url,
                data=data,
                headers=headers,
                method=method,
            )
            try:
                with (self.follow if follow else self.steps).open(req, timeout=15) as r:
                    return (r.status, r.read(), dict(r.headers), r.url)
            except urllib.error.HTTPError as e:
                return (e.code, e.read(), dict(e.headers), e.url)

        def login(self, user=ALICE, tamper=None):
            s, _, h, _ = self.request(
                "/api/auth/oidc/login?next=%2Fidentity", follow=False
            )
            if s != 302:
                raise RuntimeError(f"Login start returned {s}")
            auth = h["Location"]
            if tamper == "nonce":
                p = urllib.parse.urlsplit(auth)
                q = dict(urllib.parse.parse_qsl(p.query))
                q["nonce"] = "wrong-nonce"
                auth = urllib.parse.urlunsplit(
                    p._replace(query=urllib.parse.urlencode(q))
                )
            s, b, h, _ = self.request(auth, follow=False)
            if s == 200:
                state = html.unescape(
                    re.search('name="__ee_state" value="([^"]+)"', b.decode())[1]
                )
                s, b, h, _ = self.request(
                    auth.split("?")[0],
                    {"__ee_state": state, "__ee_user": user},
                    form=True,
                    follow=False,
                )
            if s != 302:
                raise RuntimeError(f"Provider authorization returned {s}")
            callback = h["Location"]
            if tamper == "state":
                p = urllib.parse.urlsplit(callback)
                q = dict(urllib.parse.parse_qsl(p.query))
                q["state"] = "wrong-state"
                callback = urllib.parse.urlunsplit(
                    p._replace(query=urllib.parse.urlencode(q))
                )
            if tamper == "defer":
                return (None, callback)
            return (self.request(callback), callback)

        def me(self):
            s, b, _, _ = self.request("/api/auth/me")
            return (s, json.loads(b))

        def token(self):
            return next(c.value for c in self.cookies if c.name == "shiny_session")

    def check(name, ok):
        if not ok:
            raise RuntimeError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def ensure_free(port):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", port))
        if port == provider_port and socket.has_ipv6:
            with socket.socket(socket.AF_INET6) as sock:
                sock.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                try:
                    sock.bind(("::1", port))
                except OSError as error:
                    if error.errno not in {errno.EADDRNOTAVAIL, errno.EAFNOSUPPORT}:
                        raise

    def wait(url, p):
        for _ in range(120):
            if p.poll() is not None:
                raise RuntimeError("Process exited; inspect local log")
            try:
                with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
                    url, timeout=1
                ) as r:
                    if r.status == 200:
                        return
            except OSError:
                pass
            time.sleep(0.25)
        raise RuntimeError("Startup timed out")

    def rows(db, sql, args=()):
        with contextlib.closing(
            sqlite3.connect("file:" + str(db) + "?mode=ro", uri=True)
        ) as c:
            return c.execute(sql, args).fetchall()

    def api(path, body=None, method=None, bearer=None):
        s, b, _, _ = Browser().request(
            PROVIDER + path, body, method=method, bearer=bearer
        )
        if s not in (200, 201, 204):
            raise RuntimeError((path, s, b[:300]))
        return json.loads(b) if b else None

    os.umask(0o077)
    run = ROOT
    provider = None
    hub = None
    passwordfile = None
    env = {k: v for k, v in os.environ.items() if k in {"PATH", "HOME", "TMPDIR"}}
    env.update(NO_PROXY="localhost,127.0.0.1")
    try:
        pe = env | {
            "HOST": "127.0.0.1",
            "PORT": str(provider_port),
            "TLS_ENABLED": "false",
            "ORIGIN_MODE": "compat",
            "PUBLIC_ORIGIN": PROVIDER,
            "ENTRA_DATA_DIR": str(run / "provider"),
        }
        binary = str(provider_binary)
        provider_hash = provenance["provider_binary_sha256"]
        hub_hash = provenance["shinyhub_binary_sha256"]
        ensure_free(provider_port)
        ensure_free(hub_port)
        with (run / "provider.log").open("w") as log:
            provider = subprocess.Popen(
                [binary],
                env=pe,
                cwd=run,
                stdout=log,
                stderr=log,
                start_new_session=True,
            )
        wait(PROVIDER + "/health", provider)
        discovery = api("/" + TENANT + "/v2.0/.well-known/openid-configuration")
        check(
            "Discovery issuer matches configured authority",
            discovery["issuer"] == ISSUER,
        )
        check(
            "Discovery advertises correlated front-channel logout",
            discovery.get("frontchannel_logout_session_supported") is True
            and "sid" in discovery.get("claims_supported", []),
        )
        check(
            "RSA JWKS published",
            bool(api("/" + TENANT + "/discovery/v2.0/keys")["keys"]),
        )
        app = api(
            "/admin/api/apps",
            {
                "displayName": "ShinyHub acceptance",
                "isConfidential": True,
                "groupMembershipClaims": "All",
                "redirectUris": [
                    {"uri": BASE + "/api/auth/oidc/callback", "type": "web"},
                    {"uri": BASE + "/signed-out", "type": "web"},
                    {"uri": BASE + "/?signed_out=1", "type": "web"},
                ],
                "frontchannelLogoutUri": BASE + "/api/auth/oidc/frontchannel-logout",
            },
        )
        client = app["id"]
        secret = api("/admin/api/apps/" + client + "/secrets", {})["secretText"]
        check(
            "App creation preserves frontchannel logout registration",
            app.get("frontchannelLogoutUri")
            == BASE + "/api/auth/oidc/frontchannel-logout",
        )
        role = api(
            "/admin/api/apps/" + client + "/roles",
            {
                "value": "developers",
                "displayName": "Developers",
                "allowedMemberTypes": ["User"],
                "isEnabled": True,
            },
        )
        daemon = "00d88624-f0d7-46f6-a641-6232c2608928"
        s, b, _, _ = Browser().request(
            discovery["token_endpoint"],
            {
                "grant_type": "client_credentials",
                "client_id": daemon,
                "client_secret": "daemon-app-secret",
                "scope": "https://graph.microsoft.com/.default",
            },
            form=True,
        )
        check("Graph client credential flow works", s == 200)
        graph_token = json.loads(b)["access_token"]
        assignment = api(
            "/graph/v1.0/servicePrincipals/" + client + "/appRoleAssignedTo",
            {"principalId": ALICE, "resourceId": client, "appRoleId": role["id"]},
            bearer=graph_token,
        )
        viewer = api(
            "/admin/api/apps/" + client + "/roles",
            {
                "value": "viewers",
                "displayName": "Viewers",
                "allowedMemberTypes": ["User"],
                "isEnabled": True,
            },
        )
        adminrole = api(
            "/admin/api/apps/" + client + "/roles",
            {
                "value": "admins",
                "displayName": "Admins",
                "allowedMemberTypes": ["User"],
                "isEnabled": True,
            },
        )
        subject = (
            base64.urlsafe_b64encode(
                hashlib.sha256((ALICE + "|" + client + "|" + TENANT).encode()).digest()
            )
            .decode()
            .rstrip("=")
        )
        env.update(
            SHINYHUB_AUTH_SECRET=secrets.token_hex(32),
            SHINYHUB_OIDC_CLIENT_SECRET=secret,
        )
        passwordfile = run / "password"
        password = secrets.token_urlsafe(32)
        passwordfile.write_text(password)
        phase = "strict-roles-integration"
        state = run / phase
        state.mkdir()
        db = state / "hub.sqlite"
        cfg = state / "hub.yaml"
        text = hub_config(state, ISSUER, client, BASE, hub_port)
        cfg.write_text(text)
        command(
            [
                str(BIN),
                "init",
                "--config",
                str(cfg),
                "--admin-user",
                "sandbox-recovery",
                "--admin-password-file",
                str(passwordfile),
            ],
            env=env,
            cwd=state,
            log=state / "init.log",
        )
        with (state / "hub.log").open("w") as log:
            hub = subprocess.Popen(
                [str(BIN), "serve", "--config", str(cfg)],
                env=env,
                cwd=state,
                stdout=log,
                stderr=log,
                start_new_session=True,
            )
        wait(BASE + "/readyz", hub)
        admin = Browser()
        s, _, _, _ = admin.request(
            "/api/auth/session", {"username": "sandbox-recovery", "password": password}
        )
        check("Local recovery login", s == 200)
        s, body, _, _ = admin.request(
            "/api/users",
            {
                "username": "alice",
                "password": secrets.token_urlsafe(32),
                "role": "viewer",
            },
        )
        check("Legacy account created through recovery API", s == 201)
        uid = json.loads(body)["id"]
        s, _, _, _ = admin.request(
            "/api/apps", {"slug": "sentinel", "name": "Sentinel"}
        )
        check("Legacy app created through recovery API", s == 201)
        s, _, _, _ = admin.request("/api/apps/sentinel/owner", {"user_id": uid})
        check("Legacy app transferred to migration account", s == 200)
        # Seed only a hashed sentinel key; no usable credential is needed here.
        with contextlib.closing(sqlite3.connect(db)) as c, c:
            # Model a legacy external-auth account. The admin API deliberately
            # creates local-password users, which OIDC linking must refuse.
            c.execute("UPDATE users SET password_hash='!disabled' WHERE id=?", (uid,))
            c.execute(
                "INSERT INTO api_keys(user_id,key_hash,name) VALUES(?,?,'Sentinel')",
                (uid, secrets.token_hex(32)),
            )
        before = rows(db, "SELECT * FROM users")
        before_links = rows(db, "SELECT * FROM oauth_accounts")
        b = Browser()
        result, _ = b.login()
        check(
            "Automatic collision linking refused",
            result[0] == 409
            and b.me()[0] == 401
            and (before == rows(db, "SELECT * FROM users"))
            and (before_links == rows(db, "SELECT * FROM oauth_accounts")),
        )
        assets = lambda: (
            rows(db, "SELECT id,owner_id FROM apps"),
            rows(db, "SELECT id,user_id,key_hash FROM api_keys"),
        )
        before_assets = assets()
        before_links = rows(db, "SELECT * FROM oauth_accounts")
        payload = {
            "issuer": ISSUER,
            "client_id": client,
            "sub": subject,
            "dry_run": True,
        }
        s, _, _, _ = admin.request(f"/api/users/{uid}/link-oidc", payload)
        check(
            "Explicit migration dry run leaves assets and links intact",
            s == 200
            and assets() == before_assets
            and (rows(db, "SELECT * FROM oauth_accounts") == before_links),
        )
        payload["dry_run"] = False
        s, _, _, _ = admin.request(f"/api/users/{uid}/link-oidc", payload)
        check("Explicit migration links pairwise subject", s == 200)
        b = Browser()
        result, callback = b.login()
        s, me = b.me()
        check(
            "Signed code login preserves numeric ID, deep link and assets",
            result[0] == 200
            and result[3].endswith("/identity")
            and (s == 200)
            and (me["user"]["id"] == uid)
            and (assets() == before_assets),
        )
        check("Assigned app role grants developer", me["user"]["role"] == "developer")
        api(
            "/graph/v1.0/servicePrincipals/"
            + client
            + "/appRoleAssignedTo/"
            + assignment["id"],
            method="DELETE",
            bearer=graph_token,
        )
        unchanged = rows(db, "SELECT id,username,password_hash,role FROM users")
        links = rows(db, "SELECT * FROM oauth_accounts")
        previous_epoch = rows(db, "SELECT token_epoch FROM users WHERE id=?", (uid,))[
            0
        ][0]
        hints = rows(db, "SELECT jti FROM oidc_sessions WHERE user_id=?", (uid,))
        check("Linked logout hints exist before refusal", bool(hints))
        bad = Browser()
        result, _ = bad.login(tamper="nonce")
        check(
            "Unverified no-role token cannot revoke existing sessions",
            result[0] == 502
            and b.me()[0] == 200
            and (
                hints
                == rows(db, "SELECT jti FROM oidc_sessions WHERE user_id=?", (uid,))
            ),
        )
        denied = Browser()
        result, _ = denied.login()
        check(
            "Removed final app role refuses login without provisioning or relinking",
            result[0] == 403
            and b"authorization claim is absent" in result[1]
            and (denied.me()[0] == 401)
            and (
                unchanged
                == rows(db, "SELECT id,username,password_hash,role FROM users")
            )
            and (links == rows(db, "SELECT * FROM oauth_accounts"))
            and (assets() == before_assets),
        )
        check(
            "Refusal bumps the linked epoch and clears logout hints",
            rows(db, "SELECT token_epoch FROM users WHERE id=?", (uid,))[0][0]
            == previous_epoch + 1
            and (not rows(db, "SELECT jti FROM oidc_sessions WHERE user_id=?", (uid,))),
        )
        check(
            "Verified refusal is audited",
            any(
                json.loads(v[0]).get("reason") == "claim_absent"
                for v in rows(
                    db,
                    "SELECT detail FROM audit_events WHERE action='login_refused' AND user_id=?",
                    (uid,),
                )
            ),
        )
        check(
            "Verified authorization refusal revokes existing sessions", b.me()[0] == 401
        )
        epochs = rows(db, "SELECT id,token_epoch FROM users ORDER BY id")
        links_after_refusal = rows(db, "SELECT * FROM oauth_accounts")
        fresh = Browser()
        result, _ = fresh.login(user=BOB)
        check(
            "Unassigned new identity cannot provision an account",
            result[0] == 403
            and fresh.me()[0] == 401
            and (
                unchanged
                == rows(db, "SELECT id,username,password_hash,role FROM users")
            )
            and (epochs == rows(db, "SELECT id,token_epoch FROM users ORDER BY id")),
            # Existing identity links must also survive an unrelated refusal.
        )
        check(
            "Unknown identity refusal preserves existing identity links",
            links_after_refusal == rows(db, "SELECT * FROM oauth_accounts"),
        )
        viewassignment = api(
            "/graph/v1.0/servicePrincipals/" + client + "/appRoleAssignedTo",
            {"principalId": ALICE, "resourceId": client, "appRoleId": viewer["id"]},
            bearer=graph_token,
        )
        again = Browser()
        result, _ = again.login()
        check(
            "Role reassignment permits a viewer login and keeps old sessions revoked",
            result[0] == 200
            and again.me()[1]["user"]["role"] == "viewer"
            and (b.me()[0] == 401)
            and (assets() == before_assets),
        )
        b = Browser()
        result, _ = b.login()
        check(
            "New assigned role permits a fresh session after revocation",
            result[0] == 200 and b.me()[1]["user"]["role"] == "viewer",
        )
        elevated = api(
            "/graph/v1.0/servicePrincipals/" + client + "/appRoleAssignedTo",
            {
                "principalId": ALICE,
                "resourceId": client,
                "appRoleId": adminrole["id"],
            },
            bearer=graph_token,
        )
        privileged = Browser()
        result, _ = privileged.login()
        check(
            "Multiple app roles select the highest mapped role",
            result[0] == 200 and privileged.me()[1]["user"]["role"] == "admin",
        )
        api(
            "/graph/v1.0/servicePrincipals/"
            + client
            + "/appRoleAssignedTo/"
            + elevated["id"],
            method="DELETE",
            bearer=graph_token,
        )
        reduced = Browser()
        result, _ = reduced.login()
        check(
            "Admin role removal demotes all sessions after login",
            result[0] == 200
            and reduced.me()[1]["user"]["role"] == "viewer"
            and (privileged.me()[1]["user"]["role"] == "viewer"),
        )
        for tamper, expected in [("state", 400), ("nonce", 502)]:
            bad = Browser()
            result, _ = bad.login(tamper=tamper)
            check(
                "Reject tampered " + tamper,
                result[0] == expected and bad.me()[0] == 401,
            )
        replay = Browser()
        _, callback = replay.login(tamper="defer")
        callback_state = urllib.parse.parse_qs(urllib.parse.urlsplit(callback).query)[
            "state"
        ][0]
        statecookie = next(c for c in replay.cookies if c.name == "shiny_oauth_state")
        check(
            "Callback state exists before first use",
            rows(
                db,
                "SELECT COUNT(*) FROM oauth_states WHERE state=?",
                (callback_state,),
            )
            == [(1,)],
        )
        result = replay.request(callback)
        check(
            "First callback succeeds and consumes state",
            result[0] == 200
            and replay.me()[0] == 200
            and (
                rows(
                    db,
                    "SELECT COUNT(*) FROM oauth_states WHERE state=?",
                    (callback_state,),
                )
                == [(0,)]
            ),
        )
        replay.cookies.set_cookie(statecookie)
        check(
            "Reject consumed callback with original state cookie",
            replay.request(callback)[0] == 400 and replay.me()[0] == 200,
        )
        logout = Browser()
        logout.cookies = b.cookies
        logout.follow = b.follow
        logout.steps = b.steps
        target = (
            discovery["end_session_endpoint"]
            + "?"
            + urllib.parse.urlencode(
                {
                    "client_id": client,
                    "post_logout_redirect_uri": BASE + "/signed-out",
                    "state": "round trip + & ?",
                }
            )
        )
        s, body, _, _ = logout.request(target)
        text = html.unescape(body.decode())
        frames = re.findall('<iframe src="([^"]+)"', text)
        check(
            "Provider sends real front-channel notification for signed-in RP",
            s == 200
            and len(frames) == 1
            and (
                urllib.parse.parse_qs(urllib.parse.urlsplit(frames[0]).query)["iss"]
                == [ISSUER]
            ),
        )
        check(
            "Front-channel logout preserves caller state",
            "state=round+trip+%2B+%26+%3F" in text,
        )
        purl = urllib.parse.urlsplit(frames[0])
        original = dict(urllib.parse.parse_qsl(purl.query))
        for field, value in [
            ("iss", "https://wrong-issuer.invalid"),
            ("sid", "unknown-provider-session"),
        ]:
            q = original | {field: value}
            forged = urllib.parse.urlunsplit(
                purl._replace(query=urllib.parse.urlencode(q))
            )
            Browser().request(forged)
            check(
                "Forged logout " + field + " leaves sessions valid",
                all(br.me()[0] == 200 for br in [b, again, privileged, reduced]),
            )
        Browser().request(frames[0])
        check(
            "Cookieless provider iframe revokes corresponding Hub session",
            b.me()[0] == 401,
        )
        check(
            "Provider logout preserves independent sessions",
            all(br.me()[0] == 200 for br in [again, privileged, reduced]),
        )
        token = again.token()
        s, body, _, _ = again.request("/api/auth/logout", {})
        check(
            "Local logout revokes captured browser cookie",
            s == 200 and Browser().request("/api/auth/me", cookie=token)[0] == 401,
        )
        rp_url = json.loads(body)["logout_url"]
        check(
            "Hub RP logout uses registered post-logout return",
            urllib.parse.parse_qs(urllib.parse.urlsplit(rp_url).query).get(
                "post_logout_redirect_uri"
            )
            == [BASE + "/?signed_out=1"],
        )
        status, rpbody, _, _ = again.request(rp_url)
        check(
            "Provider accepts Hub-generated RP logout",
            status == 200 and BASE + "/?signed_out=1" in html.unescape(rpbody.decode()),
        )
        stop(hub)
        hub = None
        shortcfg = hub_config(state, ISSUER, client, BASE, hub_port, max_age="1m")
        cfg.write_text(shortcfg)
        with (state / "hub-expiry.log").open("w") as log:
            hub = subprocess.Popen(
                [str(BIN), "serve", "--config", str(cfg)],
                env=env,
                cwd=state,
                stdout=log,
                stderr=log,
                start_new_session=True,
            )
        wait(BASE + "/readyz", hub)
        bounded = Browser()
        expiry_start = time.monotonic()
        result, _ = bounded.login()
        check(
            "Accelerated expiry fixture starts with a valid role session",
            result[0] == 200 and bounded.me()[0] == 200,
        )
        api(
            "/graph/v1.0/servicePrincipals/"
            + client
            + "/appRoleAssignedTo/"
            + viewassignment["id"],
            method="DELETE",
            bearer=graph_token,
        )
        deadline = expiry_start + 65
        while time.monotonic() < deadline and bounded.me()[0] == 200:
            time.sleep(1)
        check(
            "Unobserved role removal remains bounded by session expiry",
            bounded.me()[0] == 401 and 55 <= time.monotonic() - expiry_start <= 65,
        )
        refused = Browser()
        result, _ = refused.login()
        check(
            "No-role reauthentication cannot renew authorization",
            result[0] == 403 and refused.me()[0] == 401,
        )
        stop(hub)
        hub = None
        passwordfile.unlink()
        report = provenance | {
            "status": "passed",
            "timestamp": datetime.datetime.now(datetime.UTC).isoformat(),
            "passed": checks,
            "scope": "Isolated native processes and fresh databases on HTTP loopback. Strict roles, migration and logout compatibility with the pinned patched emulator. Main session policy is 1m idle/5m absolute; supplemental expiry uses 1m/1m. No real Entra policy validation, directory polling or production changes. The emulator issues role-less tokens rather than enforcing assignment-required.",
        }
        check(
            "Tested binary files remain unchanged",
            provider_hash == hashlib.sha256(Path(binary).read_bytes()).hexdigest()
            and hub_hash == hashlib.sha256(BIN.read_bytes()).hexdigest(),
        )
        (run / "results.json").write_text(json.dumps(report, indent=2) + "\n")
        print(
            "RESULT " + str(len(checks)) + " checks passed; " + str(run),
            flush=True,
        )
    except Exception as error:
        (run / "results.json").write_text(
            json.dumps(
                provenance
                | {
                    "status": "failed",
                    "error_type": type(error).__name__,
                    "passed": checks,
                },
                indent=2,
            )
            + "\n"
        )
        raise
    finally:
        for p in [hub, provider]:
            stop(p)
        if passwordfile is not None:
            passwordfile.unlink(missing_ok=True)


def main():
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    signal.signal(signal.SIGHUP, lambda *_: sys.exit(129))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--output",
        type=Path,
        help="New/empty private directory for the report and logs",
    )
    args = parser.parse_args()
    if sys.version_info < (3, 14):
        parser.error("Python 3.14 or newer is required")
    os.umask(0o077)
    output = (
        args.output.resolve()
        if args.output
        else Path(tempfile.mkdtemp(prefix="shinyhub-entra-acceptance-"))
    )
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    if any(output.iterdir()):
        parser.error("--output must be empty; existing state is never reused")
    output.chmod(0o700)
    print("Output: " + str(output), flush=True)
    try:
        with tempfile.TemporaryDirectory(prefix="shinyhub-entra-build-") as directory:
            hub, provider, provenance = build_binaries(Path(directory), output)
            exercise(output, hub, provider, provenance)
    except Exception as error:
        if not (output / "results.json").exists():
            (output / "results.json").write_text(
                json.dumps(
                    {
                        "status": "failed",
                        "phase": "build",
                        "error_type": type(error).__name__,
                    },
                    indent=2,
                )
                + "\n"
            )
        print(
            "FAILED; inspect results.json and private logs in " + str(output),
            file=sys.stderr,
        )
        raise


if __name__ == "__main__":
    main()

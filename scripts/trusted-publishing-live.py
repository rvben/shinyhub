#!/usr/bin/env python3
"""Opt-in live OIDC test. An administrator revokes the marked test credential.

Only the fixed private fixture slug is mutated. No credentials are persisted,
uploaded as artifacts, or printed. The ten-minute expiry is tested in real time.
"""
import datetime
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

HOST = "https://shinyhub.am8.nl"
POLICY = "github-live-check"
SLUG = "ci-publishing-check"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


HTTP = urllib.request.build_opener(NoRedirect())


def request(url, method="GET", body=None, token=None):
    headers = {"User-Agent": "ShinyHub-Live-Verification"}
    if token:
        headers["Authorization"] = "Token " + token
    if body is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(body).encode()
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        response = HTTP.open(req, timeout=40)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read(65536)
        try:
            payload = json.loads(raw)
        except ValueError:
            payload = {}
        return response.code, payload


def identity():
    endpoint = urllib.parse.urlsplit(os.environ["ACTIONS_ID_TOKEN_REQUEST_URL"])
    assert endpoint.scheme == "https" and endpoint.hostname.endswith(".actions.githubusercontent.com")
    query = urllib.parse.parse_qs(endpoint.query)
    query["audience"] = [HOST]
    url = urllib.parse.urlunsplit(endpoint._replace(query=urllib.parse.urlencode(query, doseq=True)))
    req = urllib.request.Request(url, headers={
        "Authorization": "Bearer " + os.environ["ACTIONS_ID_TOKEN_REQUEST_TOKEN"],
        "User-Agent": "ShinyHub-Live-Verification",
    })
    with HTTP.open(req, timeout=30) as response:
        return json.load(response)["value"]


def exchange(assertion):
    return request(HOST + "/api/auth/trusted-publishing", "POST",
                   {"policy": POLICY, "identity_token": assertion})


def cli(*args):
    subprocess.run([os.environ["SHINYHUB_TEST_CLI"], "ci", "--host", HOST,
                    "--policy", POLICY, "--audience", HOST, "--", *args],
                   check=True, timeout=240)


def main():
    assertion = identity()
    status, credential = exchange(assertion)
    assert status == 201, f"identity exchange returned HTTP {status}"
    assert credential["apps"] == [SLUG] and credential["expires_in"] == 600
    assert exchange(assertion)[0] == 409, "replayed assertion was not rejected"
    token = credential["token"]
    assert request(HOST + "/api/auth/me", token=token)[0] == 200
    assert request(HOST + "/api/apps/outside-ci-publishing-scope", token=token)[0] in (403, 404)
    assert request(HOST + "/api/apps", "POST", {"slug": "outside-ci-publishing-scope", "name": "Scope rejection probe"}, token)[0] == 403
    print("PASS: workload exchange, assertion replay rejection, and app scope", flush=True)
    expiry = datetime.datetime.fromisoformat(credential["expires_at"].replace("Z", "+00:00")).timestamp()
    assert request(HOST + "/api/apps/" + SLUG, token=token)[0] == 404, "fixture already exists; inspect before retrying"
    try:
        with tempfile.TemporaryDirectory(prefix="shinyhub-ci-") as directory:
            app = Path(directory) / SLUG
            app.mkdir()
            (app / "app.py").write_text('from shiny import App, ui\napp = App(ui.page_fluid(ui.h2("CI publishing verified")), server=None)\n')
            (app / "requirements.txt").write_text("shiny>=1.6,<2\n")
            cli("deploy", str(app), "--slug", SLUG, "--visibility", "private", "--wait", "--wait-timeout", "180")
        status, deployed = request(HOST + "/api/apps/" + SLUG, token=token)
        assert status == 200 and deployed["app"]["status"] == "running"
        assert deployed["app"]["access"] == "private"
        print("PASS: real CLI ci publishing and healthy private app", flush=True)

        status, revocable = exchange(identity())
        assert status == 201
        # Only a credential ID is exposed. The operator uses their own admin
        # session to revoke this specific app-scoped credential.
        marker = "trusted-publishing-live:revoke:" + str(revocable["id"])
        assert request(HOST + "/api/apps/" + SLUG, "PATCH", {"description": marker}, token)[0] == 200
        print("Waiting for the administrator to revoke the fixture's marked credential.", flush=True)
        deadline = time.time() + 240
        while request(HOST + "/api/auth/me", token=revocable["token"])[0] != 401:
            assert time.time() < deadline, "administrator did not revoke the marked credential within four minutes"
            time.sleep(5)
        print("PASS: credential revocation before expiry", flush=True)

        print("Waiting for the original ten-minute credential to expire.", flush=True)
        while time.time() <= expiry + 2:
            time.sleep(min(30, expiry + 3 - time.time()))
        assert request(HOST + "/api/auth/me", token=token)[0] == 401, "expired credential accepted"
        assert exchange(assertion)[0] == 401, "expired workload assertion accepted"
        print("PASS: real credential and workload-assertion expiry", flush=True)
    finally:
        cli("apps", "delete", SLUG, "--yes")
    status, fresh = exchange(identity())
    assert status == 201
    assert request(HOST + "/api/apps/" + SLUG, token=fresh["token"])[0] == 404
    print("PASS: fixture deleted and absence verified", flush=True)


if __name__ == "__main__":
    main()

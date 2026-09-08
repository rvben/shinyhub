#!/usr/bin/env python3
"""Smoke-test the local extension in an isolated VS Code profile with a fake CLI."""
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile


def main():
    code = shutil.which("code")
    if not code or os.name != "posix":
        raise SystemExit("This smoke test requires VS Code and a POSIX shell.")
    extension = Path(__file__).resolve().parents[1] / "packaging" / "vscode"
    # On macOS the CLI launcher may return before the test window starts.
    # Launch the application executable directly and wait for its exit, as
    # VS Code's extension-test runner does.
    if sys.platform == "darwin":
        app = next((p for p in Path(code).resolve().parents if p.suffix == ".app"), None)
        if app is not None:
            code = str(app / "Contents" / "MacOS" / "Code")
    # Keep Electron's Unix-domain socket below macOS's 104-byte path limit.
    with tempfile.TemporaryDirectory(prefix="shinyhub-editor-", dir="/tmp") as directory:
        root = Path(directory).resolve()
        workspace = root / "workspace"
        workspace.mkdir()
        (workspace / "app.py").write_text("# disposable extension fixture\n")
        cli = root / "fake-shinyhub"
        cli.write_text('#!/bin/sh\nprintf \'%s\\n\' "$@" > "$0.args"\n')
        cli.chmod(0o700)
        settings = root / "user" / "User"
        settings.mkdir(parents=True)
        (settings / "settings.json").write_text(json.dumps({
            "telemetry.telemetryLevel": "off", "update.mode": "none",
            "extensions.autoUpdate": False, "security.workspace.trust.enabled": False,
        }))
        env = os.environ.copy()
        for name in ("VSCODE_IPC_HOOK_CLI", "SHINYHUB_TOKEN", "SHINYHUB_HOST", "ELECTRON_RUN_AS_NODE"):
            env.pop(name, None)
        env["SHINYHUB_TEST_CLI"] = str(cli)
        command = [code, f"--user-data-dir={root / 'user'}",
                   f"--extensions-dir={root / 'extensions'}", "--disable-extensions",
                   "--skip-welcome", "--skip-release-notes", "--disable-workspace-trust",
                   f"--extensionDevelopmentPath={extension}",
                   f"--extensionTestsPath={extension / 'test' / 'host.cjs'}", str(workspace)]
        process = subprocess.Popen(command, env=env, start_new_session=True)
        try:
            result = process.wait(timeout=90)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
            raise SystemExit("Extension test timed out after 90 seconds.")
        if result:
            raise SystemExit(result)
        args = cli.with_suffix(".args").read_text().splitlines()
        if args != ["dev", str(workspace), "--open"]:
            raise SystemExit(f"Unexpected task arguments: {args!r}")
        for log in (root / "user" / "logs").rglob("tasks.log"):
            if "no registered task type 'shinyhub'" in log.read_text():
                raise SystemExit("ShinyHub task type was not registered.")
        print("VS Code extension host smoke test passed (fake CLI, isolated profile).")


if __name__ == "__main__":
    main()

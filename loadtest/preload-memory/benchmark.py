"""Compare exec and pre-fork Shiny replica memory under real browser load.

Run this script with the target app's Python environment so the supervisor can
import the app. The browser driver is launched through the harness uv project.
Linux is required because the decision metric is /proc/*/smaps_rollup PSS.
"""

from __future__ import annotations

import argparse
import gc
import importlib.util
import json
import os
import signal
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent


def import_app(app_file: Path, object_name: str):
    spec = importlib.util.spec_from_file_location("shinyhub_preload_target", app_file)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import {app_file}")
    module = importlib.util.module_from_spec(spec)
    sys.path.insert(0, str(app_file.parent))
    try:
        spec.loader.exec_module(module)
    finally:
        sys.path.pop(0)
    return getattr(module, object_name)


def serve(app_file: Path, object_name: str, port: int) -> None:
    import uvicorn

    app = import_app(app_file, object_name)
    gc.enable()
    uvicorn.run(app, host="127.0.0.1", port=port, log_level="warning")


def supervise(mode: str, app_file: Path, object_name: str, ports: list[int], pid_file: Path) -> None:
    children: list[int] = []
    if mode == "fork":
        # No loop, thread pool, listener, or client connection may exist before
        # this point. Disable collection before import, freeze the stable import
        # graph, and give each child a fresh GC lifecycle.
        gc.disable()
        app = import_app(app_file, object_name)
        gc.collect()
        gc.freeze()
        if __import__("threading").active_count() != 1:
            raise RuntimeError("app import started threads; refusing unsafe fork")
        for port in ports:
            pid = os.fork()
            if pid == 0:
                gc.enable()
                import uvicorn

                uvicorn.run(app, host="127.0.0.1", port=port, log_level="warning")
                os._exit(0)
            children.append(pid)
    else:
        for port in ports:
            proc = subprocess.Popen([
                sys.executable, str(Path(__file__).resolve()), "_serve",
                "--app-file", str(app_file), "--object", object_name,
                "--port", str(port),
            ])
            children.append(proc.pid)

    pid_file.write_text(json.dumps({"supervisor": os.getpid(), "children": children}))

    def stop(_signum, _frame):
        for pid in children:
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    for pid in children:
        try:
            os.waitpid(pid, 0)
        except ChildProcessError:
            pass


def smaps(pid: int) -> dict[str, int]:
    wanted = {"Rss:": "rss_bytes", "Pss:": "pss_bytes", "Private_Clean:": "private_bytes",
              "Private_Dirty:": "private_bytes", "Private_Hugetlb:": "private_bytes", "SwapPss:": "swap_pss_bytes"}
    out = {"rss_bytes": 0, "pss_bytes": 0, "private_bytes": 0, "swap_pss_bytes": 0}
    with open(f"/proc/{pid}/smaps_rollup", encoding="utf-8") as fh:
        for line in fh:
            fields = line.split()
            if fields and fields[0] in wanted:
                out[wanted[fields[0]]] += int(fields[1]) * 1024
    return out


def sample(pids: list[int]) -> dict:
    totals = {"rss_bytes": 0, "pss_bytes": 0, "private_bytes": 0, "swap_pss_bytes": 0}
    rows = []
    for pid in pids:
        try:
            row = {"pid": pid, **smaps(pid)}
        except (FileNotFoundError, ProcessLookupError):
            continue
        rows.append(row)
        for key in totals:
            totals[key] += row[key]
    return {"pids": rows, "totals": totals}


def wait_ready(ports: list[int], timeout: float = 60) -> None:
    pending = set(ports)
    deadline = time.monotonic() + timeout
    while pending and time.monotonic() < deadline:
        for port in list(pending):
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/", timeout=1) as response:
                    if response.status == 200:
                        pending.remove(port)
            except Exception:
                pass
        time.sleep(0.2)
    if pending:
        raise RuntimeError(f"replicas did not become ready on ports {sorted(pending)}")


def run_mode(args, mode: str, base_port: int) -> dict:
    ports = list(range(base_port, base_port + args.replicas))
    with tempfile.TemporaryDirectory(prefix="shinyhub-preload-") as temp:
        pid_file = Path(temp) / "pids.json"
        supervisor = subprocess.Popen([
            sys.executable, str(Path(__file__).resolve()), "_supervise",
            "--mode", mode, "--app-file", str(args.app_file), "--object", args.object,
            "--ports", *map(str, ports), "--pid-file", str(pid_file),
        ])
        try:
            wait_ready(ports)
            deadline = time.monotonic() + 10
            while not pid_file.exists() and time.monotonic() < deadline:
                time.sleep(0.1)
            identities = json.loads(pid_file.read_text())
            pids = [identities["supervisor"], *identities["children"]]
            before = sample(pids)

            stats_file = Path(temp) / "driver.json"
            driver = [
                "uv", "run", "--quiet", "--project", str(HERE), "python", str(HERE / "browser_driver.py"),
                "--concurrency", str(args.replicas * args.sessions_per_replica),
                "--duration", str(args.duration), "--stats-out", str(stats_file),
            ]
            for port in ports:
                driver.extend(["--url", f"http://127.0.0.1:{port}"])
            result = subprocess.run(driver, text=True, capture_output=True)
            if result.returncode:
                raise RuntimeError(f"browser driver failed: {result.stderr[-1000:]}")
            time.sleep(args.settle_seconds)
            after = sample(pids)
            return {"mode": mode, "ports": ports, "before_load": before, "after_load": after,
                    "driver": json.loads(stats_file.read_text())}
        finally:
            supervisor.terminate()
            try:
                supervisor.wait(timeout=15)
            except subprocess.TimeoutExpired:
                supervisor.kill()
                supervisor.wait()


def benchmark(args) -> None:
    if sys.platform != "linux":
        raise SystemExit("preload_memory.py requires Linux /proc/smaps_rollup")
    runs = [run_mode(args, "exec", args.base_port), run_mode(args, "fork", args.base_port)]
    exec_pss = runs[0]["after_load"]["totals"]["pss_bytes"]
    fork_pss = runs[1]["after_load"]["totals"]["pss_bytes"]
    saving = (1 - fork_pss / exec_pss) * 100 if exec_pss else 0
    exec_p99 = runs[0]["driver"].get("p99_ms")
    fork_p99 = runs[1]["driver"].get("p99_ms")
    latency_ok = not exec_p99 or not fork_p99 or fork_p99 <= exec_p99 * (1 + args.max_p99_regression / 100)
    report = {
        "app_file": str(args.app_file), "replicas": args.replicas,
        "sessions_per_replica": args.sessions_per_replica, "runs": runs,
        "decision": {"pss_saving_percent": round(saving, 2), "minimum_saving_percent": args.min_pss_saving,
                     "p99_regression_within_limit": latency_ok,
                     "passes_gate": saving >= args.min_pss_saving and latency_ok},
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2))
    print(json.dumps(report["decision"], indent=2))


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="command", required=True)
    bench = sub.add_parser("benchmark")
    bench.add_argument("--app-file", type=Path, required=True)
    bench.add_argument("--object", default="app")
    bench.add_argument("--replicas", type=int, default=3)
    bench.add_argument("--sessions-per-replica", type=int, default=2)
    bench.add_argument("--duration", type=int, default=30)
    bench.add_argument("--settle-seconds", type=int, default=12)
    bench.add_argument("--base-port", type=int, default=8870)
    bench.add_argument("--min-pss-saving", type=float, default=20)
    bench.add_argument("--max-p99-regression", type=float, default=10)
    bench.add_argument("--output", type=Path, required=True)
    sup = sub.add_parser("_supervise")
    sup.add_argument("--mode", choices=("exec", "fork"), required=True)
    sup.add_argument("--app-file", type=Path, required=True)
    sup.add_argument("--object", required=True)
    sup.add_argument("--ports", type=int, nargs="+", required=True)
    sup.add_argument("--pid-file", type=Path, required=True)
    child = sub.add_parser("_serve")
    child.add_argument("--app-file", type=Path, required=True)
    child.add_argument("--object", required=True)
    child.add_argument("--port", type=int, required=True)
    return p


if __name__ == "__main__":
    parsed = parser().parse_args()
    if parsed.command == "benchmark":
        benchmark(parsed)
    elif parsed.command == "_supervise":
        supervise(parsed.mode, parsed.app_file, parsed.object, parsed.ports, parsed.pid_file)
    else:
        serve(parsed.app_file, parsed.object, parsed.port)

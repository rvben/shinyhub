"""Drive real Shiny sessions across several replica URLs."""

from __future__ import annotations

import argparse
import asyncio
import json
import time
from pathlib import Path

from playwright.async_api import async_playwright


async def run_session(url: str, duration: int, context) -> dict:
    page = await context.new_page()
    latencies: list[float] = []
    actions = errors = 0
    try:
        await page.goto(url, wait_until="domcontentloaded", timeout=120_000)
        await page.wait_for_function(
            "() => window.Shiny && window.Shiny.shinyapp && window.Shiny.shinyapp.$inputValues",
            timeout=120_000,
        )
        await page.wait_for_timeout(500)
        controls = await page.locator("select[id], input[id], button[id]").all()
        await page.evaluate("""() => {
          window.__preloadMutations = 0;
          const observer = new MutationObserver(() => { window.__preloadMutations += 1; });
          document.querySelectorAll('.shiny-bound-output').forEach(
            node => observer.observe(node, {childList: true, subtree: true, characterData: true, attributes: true})
          );
        }""")
        deadline = time.monotonic() + duration
        control_index = 0
        while time.monotonic() < deadline:
            if not controls:
                await page.wait_for_timeout(250)
                continue
            control = controls[control_index % len(controls)]
            control_index += 1
            started = time.perf_counter()
            try:
                tag = await control.evaluate("el => el.tagName")
                kind = await control.get_attribute("type")
                before = await page.evaluate("() => window.__preloadMutations")
                if tag == "SELECT":
                    options = await control.locator("option").all()
                    if options:
                        current = await control.evaluate("el => el.selectedIndex")
                        await control.select_option(index=(current + 1) % len(options))
                elif kind == "checkbox":
                    await control.click()
                elif tag == "BUTTON" or kind in ("button", "submit"):
                    await control.click()
                else:
                    await page.wait_for_timeout(100)
                    continue
                actions += 1
                await page.wait_for_function(
                    "before => window.__preloadMutations > before", before, timeout=10_000
                )
                latencies.append((time.perf_counter() - started) * 1000)
            except Exception:
                errors += 1
            await page.wait_for_timeout(150)
    except Exception:
        errors += 1
    finally:
        await page.close()
    return {"actions": actions, "errors": errors, "latencies_ms": latencies}


async def drive(args) -> dict:
    async with async_playwright() as playwright:
        browser = await playwright.chromium.launch(headless=True)
        contexts = [await browser.new_context() for _ in range(args.concurrency)]
        started = time.perf_counter()
        sessions = await asyncio.gather(*[
            run_session(args.url[i % len(args.url)], args.duration, contexts[i])
            for i in range(args.concurrency)
        ])
        for context in contexts:
            await context.close()
        await browser.close()
    latencies = sorted(value for session in sessions for value in session["latencies_ms"])
    percentile = lambda q: round(latencies[min(len(latencies) - 1, int(len(latencies) * q))], 1) if latencies else None
    return {
        "concurrency": args.concurrency,
        "duration_s": round(time.perf_counter() - started, 2),
        "interactions": sum(session["actions"] for session in sessions),
        "errors": sum(session["errors"] for session in sessions),
        "p50_ms": percentile(0.50), "p95_ms": percentile(0.95), "p99_ms": percentile(0.99),
        "latencies_count": len(latencies),
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", action="append", required=True)
    parser.add_argument("--concurrency", type=int, required=True)
    parser.add_argument("--duration", type=int, required=True)
    parser.add_argument("--stats-out", type=Path, required=True)
    options = parser.parse_args()
    result = asyncio.run(drive(options))
    options.stats_out.write_text(json.dumps(result, indent=2))
    print(json.dumps(result, indent=2))

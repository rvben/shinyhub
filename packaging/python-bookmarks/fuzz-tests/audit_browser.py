"""Audit a real date-slider bookmark round trip; exit 1 for changed state."""

import argparse
import json
import os
import time
from importlib.metadata import version
from pathlib import Path

from playwright.sync_api import sync_playwright
from shiny.playwright.controller import InputSelectize
from shiny.run import run_shiny_app


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--browser", choices=("chromium", "firefox", "webkit"), default="chromium"
    )
    parser.add_argument(
        "--drop-query",
        action="store_true",
        help="Negative control: discard the saved query before reopening",
    )
    parser.add_argument(
        "--native-sliders",
        action="store_true",
        help="Audit upstream sliders without the helper",
    )
    args = parser.parse_args()
    os.environ["SHINY_BOOKMARK_AUDIT_NATIVE_SLIDERS"] = (
        "1" if args.native_sliders else "0"
    )
    # run_shiny_app's child inherits this environment; pin both ends of the
    # bookmark round trip, including naive datetime conversion on the server.
    os.environ["TZ"] = "UTC"
    if hasattr(time, "tzset"):
        time.tzset()
    with run_shiny_app(
        Path(__file__).with_name("slider_app.py"), wait_for_start=True
    ) as app, sync_playwright() as p:
        browser = getattr(p, args.browser).launch(headless=True)
        try:
            page = browser.new_page(timezone_id="UTC")
            page.set_default_timeout(10000)
            errors = []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.goto(app.url)
            values = page.locator("#values")
            values.filter(has_text='"day"').wait_for()
            defaults = stable_values(page)
            for input_id in ("day", "moment"):
                page.locator(".shiny-input-container").filter(
                    has=page.locator("#" + input_id)
                ).locator(".irs-line").press("ArrowRight")
            InputSelectize(page, "single").set("c")
            InputSelectize(page, "multi").set(["b"])
            # Slider changes are debounced. An idle reactive graph can still
            # precede their arrival, so wait for each server value to change.
            page.wait_for_function(
                "defaults => { const values = JSON.parse(document.querySelector('#values').textContent); return Object.keys(defaults).every(key => JSON.stringify(values[key]) !== JSON.stringify(defaults[key])); }",
                arg=defaults,
            )
            before = stable_values(page)
            if not all(before[key] != defaults[key] for key in before):
                raise RuntimeError(
                    "Each field must differ from its default before saving"
                )
            page.get_by_role("button", name="Bookmark").click()
            bookmark = page.locator("#shiny-modal textarea")
            bookmark.wait_for()
            link = bookmark.input_value()
            response = page.goto(link.partition("?")[0] if args.drop_query else link)
            # Re-resolve the output after navigation and wait for a server value.
            values.filter(has_text='"day"').wait_for()
            after = stable_values(page)
            result = {
                "shiny": version("shiny"),
                "native_sliders": args.native_sliders,
                "browser": args.browser,
                "query": link.partition("?")[2],
                "status": response.status if response else None,
                "before": before,
                "after": after,
                "page_errors": errors,
                "preserved": before == after,
                "changed_fields": [key for key in before if before[key] != after[key]],
                "slider_dom": {
                    key: page.locator("#" + key).input_value()
                    for key in ("day", "moment")
                },
                "timezone": "UTC",
                "query_dropped": args.drop_query,
            }
        finally:
            browser.close()
    print(json.dumps(result, indent=2))
    return 0 if result["status"] == 200 and result["preserved"] and not errors else 1


def stable_values(page):
    page.wait_for_function(
        "() => window.Shiny?.shinyapp?.isConnected() && !document.documentElement.classList.contains('shiny-busy') && !document.querySelector('.recalculating')"
    )
    previous = None
    consecutive = 0
    for _ in range(10):
        current = json.loads(page.locator("#values").inner_text())
        if current == previous:
            consecutive += 1
            if consecutive >= 2:
                return current
        else:
            consecutive = 0
        previous = current
        page.wait_for_timeout(250)
    raise RuntimeError("Server values did not stabilize")


if __name__ == "__main__":
    raise SystemExit(main())

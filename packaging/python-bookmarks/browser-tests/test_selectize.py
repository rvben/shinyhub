from pathlib import Path
from urllib.parse import urlencode
import pytest
from shiny.run import run_shiny_app
from shiny.playwright.controller import InputSelectize, OutputCode


@pytest.mark.parametrize(
    "saved,selected", [('["Week"]', ["Week"]), ("null", []), ("[]", [])]
)
def test_restore_and_change(page, saved, selected):
    with run_shiny_app(Path(__file__).with_name("app.py"), wait_for_start=True) as app:
        query = urlencode({"res": '"Week"', "multi": saved, "m-late_res": '"Week"'})
        response = page.goto(app.url + "?_inputs_&" + query)
        assert response.status == 200
        single = InputSelectize(page, "res")
        multi = InputSelectize(page, "multi")
        dynamic = InputSelectize(page, "m-late_res")
        single.expect_selected(["Week"])
        multi.expect_selected(selected)
        dynamic.expect_selected(["Week"])
        single.expect_choices(["Day", "Week", "Month"])
        multi.expect_choices(
            [item for item in ["Day", "Week", "Month"] if item not in selected]
        )
        dynamic.expect_choices(["Day", "Week", "Month"])
        OutputCode(page, "values").expect_value(repr(("Week", tuple(selected), "Week")))
        single.set("Month")
        multi.set(["Month"])
        dynamic.set("Day")
        OutputCode(page, "values").expect_value(repr(("Month", ("Month",), "Day")))

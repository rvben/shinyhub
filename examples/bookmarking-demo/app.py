from __future__ import annotations

from htmltools import TagList
from shiny import App, reactive, render, ui
from shinyhub_bookmarks import ChoiceRestore, Field, bookmarking_dependency, register

REGIONS = {"Europe": 1.00, "Americas": 1.22, "Asia Pacific": 1.36}
PRODUCTS = {
    "All products": 1.00,
    "Analytics": 1.18,
    "Planning": 0.82,
    "Operations": 1.07,
}

APP_STYLES = """
:root {
  color-scheme: dark;
  --canvas: #060914;
  --surface: #0e1426;
  --surface-raised: #141b32;
  --surface-hover: #1b2444;
  --line: #2b3a63;
  --text: #e8eeff;
  --text-soft: #a8b4d4;
  --accent: #13b8a6;
  --accent-bright: #5eead4;
}
body { background: var(--canvas); color: var(--text); }
.demo-shell { max-width: 1120px; margin: 0 auto; padding: 34px 24px 56px; }
.demo-title { margin: 0; font-size: clamp(1.7rem, 4vw, 2.6rem); letter-spacing: -0.04em; font-weight: 750; }
.demo-lede { max-width: 700px; margin: 10px 0 26px; color: var(--text-soft); font-size: .875rem; line-height: 1.6; }
.demo-old-link { display: inline-flex; margin: -12px 0 24px; color: var(--accent-bright); font-size: .82rem; font-weight: 700; text-underline-offset: 3px; }
.demo-old-link:hover { color: var(--text); }
.demo-old-link:focus-visible { outline: 3px solid var(--accent); outline-offset: 3px; border-radius: 4px; }
.demo-layout { display: grid; grid-template-columns: 260px minmax(0, 1fr); gap: 22px; align-items: start; }
.demo-filters, .demo-result { background: var(--surface); border: 1px solid var(--line); border-radius: 14px; box-shadow: 0 18px 48px rgba(0, 0, 0, .24); }
.demo-filters { padding: 18px; }
.demo-filters h2, .demo-result h2 { margin: 0; font-size: 1.05rem; }
.demo-filters .form-label, .demo-filters .form-check-label { color: var(--text-soft); font-size: .82rem; font-weight: 650; }
.demo-filters .form-select, .demo-filters .form-control { color: var(--text); background-color: var(--surface-raised); border-color: var(--line); }
.demo-filters .form-select:hover, .demo-filters .form-control:hover { background-color: var(--surface-hover); }
.demo-filters .form-select:focus, .demo-filters .form-control:focus { border-color: var(--accent); box-shadow: 0 0 0 .2rem rgba(19, 184, 166, .2); }
.demo-filters .form-check-input { background-color: var(--surface-raised); border-color: var(--line); }
.demo-filters .form-check-input:checked { background-color: var(--accent); border-color: var(--accent); }
.demo-filters .irs--shiny .irs-bar, .demo-filters .irs--shiny .irs-single { background: var(--accent); border-color: var(--accent); }
.demo-filters .irs--shiny .irs-handle { background: var(--accent-bright); border-color: var(--accent); }
.demo-filters .irs--shiny .irs-line { background: var(--surface-raised); border-color: var(--line); }
.demo-result { overflow: hidden; }
.demo-result-head { padding: 18px 20px; border-bottom: 1px solid var(--line); display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.demo-badge { border-radius: 99px; padding: 5px 10px; background: var(--surface-raised); color: var(--accent-bright); font-size: .75rem; font-weight: 700; }
.metric-grid { display: grid; grid-template-columns: repeat(3, 1fr); }
.metric { padding: 22px 20px; border-right: 1px solid var(--line); }
.metric:last-child { border-right: 0; }
.metric-label { color: var(--text-soft); font-size: .78rem; font-weight: 650; }
.metric-value { margin-top: 8px; color: var(--text); font-size: clamp(1.7rem, 3vw, 2.6rem); font-weight: 750; letter-spacing: -.035em; }
.trend { padding: 22px 20px 24px; border-top: 1px solid var(--line); }
.trend-bars { height: 170px; display: flex; align-items: end; gap: 10px; }
.trend-bar { flex: 1; min-width: 10px; border-radius: 4px 4px 0 0; background: linear-gradient(180deg, var(--accent-bright), var(--accent)); }
.trend-axis { display: flex; justify-content: space-between; margin-top: 9px; color: var(--text-soft); font-size: .72rem; }
@media (max-width: 760px) { .demo-shell { padding: 24px 14px 40px; } .demo-layout { grid-template-columns: 1fr; } .metric-grid { grid-template-columns: 1fr; } .metric { border-right: 0; border-bottom: 1px solid var(--line); } .metric:last-child { border-bottom: 0; } }
"""


def app_ui(request):
    return ui.page_fluid(
        bookmarking_dependency(),
        ui.tags.style(APP_STYLES),
        ui.div(
            ui.h1("Portfolio pulse", class_="demo-title"),
            ui.p(
                "Change the filters and watch the page URL follow. Refresh or bookmark it to return to the same view; use the link icon to choose what to share.",
                class_="demo-lede",
            ),
            ui.a(
                "Open a deliberately outdated bookmark",
                href="?_inputs_&territory=%22Americas%22&product=%22Legacy%20planning%22&segment=%22Enterprise%22&imaginary-filter=%22Untrusted%20value%22",
                class_="demo-old-link",
            ),
            ui.div(
                ui.tags.aside(
                    ui.h2("View filters"),
                    ui.input_select(
                        "region", "Region", list(REGIONS), selected="Europe"
                    ),
                    ui.input_select(
                        "product", "Product", list(PRODUCTS), selected="All products"
                    ),
                    ui.input_slider(
                        "year", "Reporting year", min=2022, max=2026, value=2026, sep=""
                    ),
                    ui.input_checkbox("forecast", "Include forecast", value=True),
                    class_="demo-filters",
                ),
                ui.div(ui.output_ui("dashboard"), class_="demo-result"),
                class_="demo-layout",
            ),
            class_="demo-shell",
        ),
        title="Portfolio pulse",
    )


def server(input, output, session):
    register(
        session=session,
        input=input,
        fields={
            "region": Field(
                "Region",
                restore=ChoiceRestore(choices=REGIONS, default="Europe"),
                renamed_from={"territory": "Territory"},
            ),
            "product": Field(
                "Product",
                restore=ChoiceRestore(
                    choices=PRODUCTS,
                    default="All products",
                    aliases={"Legacy planning": "Planning"},
                ),
            ),
            "year": Field("Reporting year"),
            "forecast": Field(
                "Forecast",
                formatter=lambda value: "Included" if value else "Actuals only",
            ),
        },
        schema_version=2,
        legacy_fields={"segment": "Market segment"},
    )

    @reactive.calc
    def figures():
        scale = REGIONS[input.region()] * PRODUCTS[input.product()]
        year_factor = 1 + (input.year() - 2022) * 0.075
        revenue = round(
            4_250_000 * scale * year_factor * (1.09 if input.forecast() else 1)
        )
        return revenue, 0.214 + (scale - 1) * 0.025, round(780 * scale * year_factor)

    @output
    @render.ui
    def dashboard():
        revenue, margin, customers = figures()
        growth = [0.42, 0.57, 0.49, 0.70, 0.65, 0.81, 0.77, 0.92]
        if not input.forecast():
            growth = growth[:-2]
        bars = [
            ui.div(class_="trend-bar", style=f"height: {round(value * 100)}%")
            for value in growth
        ]
        return TagList(
            ui.div(
                ui.h2(f"{input.region()} · {input.product()}"),
                ui.span("Synthetic data", class_="demo-badge"),
                class_="demo-result-head",
            ),
            ui.div(
                ui.div(
                    ui.div("Revenue", class_="metric-label"),
                    ui.div(f"€{revenue / 1_000_000:.2f}M", class_="metric-value"),
                    class_="metric",
                ),
                ui.div(
                    ui.div("Gross margin", class_="metric-label"),
                    ui.div(f"{margin:.1%}", class_="metric-value"),
                    class_="metric",
                ),
                ui.div(
                    ui.div("Customers", class_="metric-label"),
                    ui.div(f"{customers:,}", class_="metric-value"),
                    class_="metric",
                ),
                class_="metric-grid",
            ),
            ui.div(
                ui.div(
                    *bars,
                    class_="trend-bars",
                    role="img",
                    aria_label="Synthetic revenue trend",
                ),
                ui.div(
                    ui.span(str(input.year() - len(growth) + 1)),
                    ui.span(str(input.year())),
                    class_="trend-axis",
                ),
                class_="trend",
            ),
        )


app = App(app_ui, server, bookmark_store="url")

from shiny import App, render, ui


# A deliberately small, bundled dataset keeps the first run fast and works
# offline after Shiny itself is installed. Values are illustrative GWh totals.
ENERGY = {
    "Amsterdam": {"Solar": 58, "Wind": 91, "Hydro": 28, "Storage": 43},
    "Berlin": {"Solar": 72, "Wind": 84, "Hydro": 22, "Storage": 36},
    "Copenhagen": {"Solar": 49, "Wind": 118, "Hydro": 34, "Storage": 41},
    "Lisbon": {"Solar": 109, "Wind": 67, "Hydro": 31, "Storage": 29},
}

STYLES = """
:root { color-scheme: dark; --ink:#f7f9ff; --muted:#a6b0ca; --panel:#10172a; --line:#263353; --accent:#65e6c4; --accent2:#8fa6ff; }
*,*::before,*::after { box-sizing:border-box; }
body { margin:0; color:var(--ink); background:radial-gradient(circle at 80% 0,#17254b 0,transparent 34rem),#070b15; font-family:Inter,system-ui,sans-serif; }
.container-fluid { padding:0; }
.shell { width:min(1080px,calc(100% - 36px)); margin:auto; padding:48px 0 64px; }
.eyebrow { margin:0 0 10px; color:var(--accent); font:700 .78rem/1.4 ui-monospace,SFMono-Regular,monospace; letter-spacing:.08em; text-transform:uppercase; }
h1 { max-width:15ch; margin:0; font-size:clamp(2.4rem,7vw,5.3rem); line-height:.98; letter-spacing:-.055em; text-wrap:balance; }
.lede { max-width:62ch; margin:18px 0 32px; color:var(--muted); font-size:1.04rem; line-height:1.65; }
.controls { display:grid; grid-template-columns:1fr 1fr; gap:18px; padding:20px; border:1px solid var(--line); border-radius:18px; background:color-mix(in srgb,var(--panel) 92%,transparent); }
.form-group { margin:0; } label { margin-bottom:8px; color:var(--muted); font-size:.82rem; font-weight:700; }
.irs--shiny .irs-bar,.irs--shiny .irs-single { background:var(--accent); border-color:var(--accent); }
.irs--shiny .irs-handle { border-color:var(--accent); }
.summary { display:grid; grid-template-columns:repeat(3,1fr); gap:14px; margin:14px 0; }
.metric { min-height:130px; padding:20px; border:1px solid var(--line); border-radius:16px; background:var(--panel); }
.metric-label { margin:0 0 20px; color:var(--muted); font-size:.78rem; font-weight:700; letter-spacing:.05em; text-transform:uppercase; }
.metric-value { margin:0; font-size:2rem; font-weight:750; letter-spacing:-.04em; }
.metric-note { margin:5px 0 0; color:var(--accent); font-size:.8rem; }
.chart { padding:22px; border:1px solid var(--line); border-radius:16px; background:var(--panel); }
.chart-head { display:flex; align-items:baseline; justify-content:space-between; gap:16px; margin-bottom:24px; }
.chart h2 { margin:0; font-size:1rem; }.chart small { color:var(--muted); }
.bar-row { display:grid; grid-template-columns:72px 1fr 54px; align-items:center; gap:14px; margin:16px 0; }
.bar-label,.bar-value { font-size:.82rem; }.bar-value { color:var(--muted); text-align:right; font-variant-numeric:tabular-nums; }
.bar-track { height:12px; overflow:hidden; border-radius:99px; background:#1b2540; }.bar-fill { height:100%; border-radius:99px; background:linear-gradient(90deg,var(--accent),var(--accent2)); transition:width .35s ease; }
.footnote { margin:18px 0 0; color:var(--muted); font-size:.78rem; }
@media(max-width:680px){.shell{padding-top:30px}.controls,.summary{grid-template-columns:1fr}.metric{min-height:auto}.bar-row{grid-template-columns:66px 1fr 48px}}
"""


app_ui = ui.page_fluid(
    ui.tags.head(ui.tags.style(STYLES)),
    ui.tags.main(
        ui.div(
            ui.p("Your first ShinyHub deployment", class_="eyebrow"),
            ui.h1("Explore a live energy forecast."),
            ui.p(
                "Change the city and growth assumption. Every value below is recalculated "
                "inside the Python Shiny process that ShinyHub built and launched for you.",
                class_="lede",
            ),
            ui.div(
                ui.input_select("city", "City", list(ENERGY), selected="Amsterdam"),
                ui.input_slider("growth", "Forecast growth", min=-10, max=30, value=12, post="%"),
                class_="controls",
            ),
            ui.output_ui("summary"),
            ui.output_ui("mix_chart"),
            class_="shell",
        )
    ),
)


def server(input, output, session):
    def forecast():
        multiplier = 1 + input.growth() / 100
        return {source: round(value * multiplier) for source, value in ENERGY[input.city()].items()}

    @output
    @render.ui
    def summary():
        values = forecast()
        total = sum(values.values())
        clean = sum(values[source] for source in ("Solar", "Wind", "Hydro"))
        leading = max(values, key=values.get)

        def metric(label, value, note):
            return ui.div(
                ui.p(label, class_="metric-label"),
                ui.p(value, class_="metric-value"),
                ui.p(note, class_="metric-note"),
                class_="metric",
            )

        return ui.div(
            metric("Forecast output", f"{total} GWh", f"{input.growth():+d}% scenario"),
            metric("Renewable share", f"{clean / total:.0%}", "Solar · wind · hydro"),
            metric("Leading source", leading, f"{values[leading]} GWh"),
            class_="summary",
        )

    @output
    @render.ui
    def mix_chart():
        values = forecast()
        maximum = max(values.values())
        bars = []
        for source, value in values.items():
            bars.append(
                ui.div(
                    ui.span(source, class_="bar-label"),
                    ui.div(
                        ui.div(class_="bar-fill", style=f"width:{value / maximum:.1%}"),
                        class_="bar-track",
                    ),
                    ui.span(f"{value}", class_="bar-value"),
                    class_="bar-row",
                )
            )
        return ui.section(
            ui.div(ui.h2(f"{input.city()} energy mix"), ui.tags.small("Illustrative bundled data · GWh"), class_="chart-head"),
            *bars,
            ui.p("This is an ordinary app: edit it, redeploy it, inspect its logs, or delete it whenever you like.", class_="footnote"),
            class_="chart",
        )


app = App(app_ui, server)

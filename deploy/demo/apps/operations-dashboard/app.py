import math
import random

from shiny import App, reactive, render, ui


app_ui = ui.page_fluid(
    ui.tags.style(
        """
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
          --warning: #fbbf24;
        }
        *, *::before, *::after { box-sizing: border-box; }
        body { margin: 0; background: var(--canvas); color: var(--text); font-family: Manrope, -apple-system, system-ui, "Segoe UI", sans-serif; }
        .shell { max-width: 1160px; margin: 0 auto; padding: 30px 18px 50px; }
        .eyebrow { color: var(--accent-bright); font: 700 12px/1.2 ui-monospace, monospace; letter-spacing: .12em; text-transform: uppercase; }
        h1 { max-width: 18ch; margin: 10px 0 12px; font-size: 3rem; line-height: 1.05; letter-spacing: -.035em; text-wrap: balance; }
        .lede { max-width: 65ch; color: var(--text-soft); font-size: 1.05rem; line-height: 1.55; text-wrap: pretty; }
        .controls, .metrics, .panels { display: grid; gap: 14px; }
        .controls { grid-template-columns: repeat(3, minmax(0, 1fr)); margin: 28px 0 14px; }
        .metrics { grid-template-columns: repeat(4, minmax(0, 1fr)); }
        .panels { grid-template-columns: 1.3fr .7fr; margin-top: 14px; }
        .card { padding: 18px; border: 1px solid var(--line); border-radius: 14px; background: var(--surface); color: var(--text); }
        .metric-label { color: var(--text-soft); font-size: .78rem; font-weight: 650; letter-spacing: .055em; text-transform: uppercase; }
        .metric-value { margin-top: 8px; color: var(--text); font-size: 2rem; font-weight: 760; letter-spacing: -.035em; }
        .healthy { color: var(--accent-bright); }
        .spark { display: flex; align-items: end; height: 180px; gap: 7px; margin-top: 18px; }
        .bar { flex: 1; min-width: 5px; border-radius: 5px 5px 2px 2px; background: var(--accent); opacity: .9; }
        .region { display:flex; justify-content:space-between; padding:12px 0; border-bottom:1px solid var(--line); color: var(--text); }
        .region:last-child { border: 0; }
        .dot { display:inline-block; width:8px; height:8px; margin-right:8px; border-radius:50%; background:var(--accent-bright); }
        label { color: var(--text-soft) !important; }
        select.form-select, select.form-control {
          min-height: 44px; border-color: var(--line); background-color: var(--surface-raised); color: var(--text);
        }
        select:focus, input:focus-visible { outline: 3px solid color-mix(in srgb, var(--accent) 42%, transparent); outline-offset: 2px; }
        .irs--shiny .irs-line { background: var(--surface-hover); border-color: var(--line); }
        .irs--shiny .irs-bar, .irs--shiny .irs-single { background: var(--accent); border-color: var(--accent); }
        .irs--shiny .irs-handle { border-color: var(--accent); background: var(--text); }
        .irs--shiny .irs-min, .irs--shiny .irs-max, .irs--shiny .irs-grid-text { color: var(--text-soft); }
        @media (max-width: 760px) { .controls, .metrics, .panels { grid-template-columns: 1fr 1fr; } .panels { grid-template-columns: 1fr; } }
        @media (max-width: 480px) { .shell { padding: 24px 14px 40px; } h1 { font-size: 2.25rem; } .controls, .metrics { grid-template-columns: 1fr; } }
        """
    ),
    ui.div(
        {"class": "shell"},
        ui.div("SHINYHUB · LIVE APPLICATION", class_="eyebrow"),
        ui.h1("Operations, without the noise."),
        ui.p("Tune a synthetic service fleet and watch capacity, latency, and throughput respond. This is a real Python Shiny application running on ShinyHub.", class_="lede"),
        ui.div(
            ui.div(ui.input_select("region", "Traffic region", {"eu": "Europe", "us": "North America", "ap": "Asia Pacific"}), class_="card"),
            ui.div(ui.input_slider("traffic", "Requests per minute", 200, 2400, 980, step=20), class_="card"),
            ui.div(ui.input_slider("replicas", "Active replicas", 1, 12, 5), class_="card"),
            class_="controls",
        ),
        ui.output_ui("metrics"),
        ui.div(
            ui.div(ui.div("THROUGHPUT · LAST 12 MINUTES", class_="metric-label"), ui.output_ui("sparkline"), class_="card"),
            ui.div(ui.div("REGION HEALTH", class_="metric-label"), ui.output_ui("regions"), class_="card"),
            class_="panels",
        ),
    ),
)


def server(input, output, session):
    @reactive.calc
    def model():
        traffic = input.traffic()
        replicas = input.replicas()
        capacity = replicas * 260
        load = traffic / capacity
        latency = round(31 + 62 * load**2)
        sessions = round(traffic / 36)
        headroom = max(0, round((capacity - traffic) / capacity * 100))
        return traffic, replicas, latency, sessions, headroom, load

    @output
    @render.ui
    def metrics():
        traffic, replicas, latency, sessions, headroom, load = model()
        status = "Healthy" if load < 0.85 else "At capacity"
        values = [
            (f"{traffic:,}", "Requests / min", ""),
            (f"{latency} ms", "P95 latency", ""),
            (str(sessions), "Active sessions", ""),
            (status, f"{headroom}% headroom", "healthy" if load < 0.85 else ""),
        ]
        return ui.div(*[
            ui.div(ui.div(label, class_="metric-label"), ui.div(value, class_=f"metric-value {tone}"), class_="card")
            for value, label, tone in values
        ], class_="metrics")

    @output
    @render.ui
    def sparkline():
        traffic, replicas, *_ = model()
        seed = traffic * 31 + replicas
        rng = random.Random(seed)
        heights = [max(18, min(100, 46 + math.sin(i / 2) * 17 + traffic / 45 + rng.randint(-9, 9))) for i in range(12)]
        return ui.div(*[ui.div(class_="bar", style=f"height:{height}%") for height in heights], class_="spark")

    @output
    @render.ui
    def regions():
        active = input.region()
        labels = [("eu", "Europe", "23 ms"), ("us", "North America", "48 ms"), ("ap", "Asia Pacific", "71 ms")]
        return ui.div(*[
            ui.div(ui.span(ui.span(class_="dot"), name), ui.strong(latency if code == active else "Ready"), class_="region")
            for code, name, latency in labels
        ])


app = App(app_ui, server)

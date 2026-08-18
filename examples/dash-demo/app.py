import argparse

import dash
from dash import Input, Output, dcc, html

parser = argparse.ArgumentParser()
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--host", default="127.0.0.1")
args = parser.parse_args()

app = dash.Dash(
    __name__,
    requests_pathname_prefix="/app/dash-demo/",
    title="Plotly Dash · ShinyHub Demo",
)
app.layout = html.Main(
    className="shell",
    children=[
        html.Header([
            html.P("Live application · Plotly Dash", className="context"),
            html.H1("Make a live metric move."),
            html.P(
                "Change the input and watch Dash recompute the result instantly. "
                "The application is running behind ShinyHub's proxy and lifecycle controls.",
                className="lede",
            ),
        ]),
        html.Section(
            className="workspace",
            **{"aria-labelledby": "control-heading"},
            children=[
                html.Div([
                    html.H2("Interactive calculation", id="control-heading"),
                    html.P("Choose any whole number from 0 to 100.", id="range-help"),
                    html.Label("Input value", htmlFor="n"),
                    html.Div(
                        className="range-row",
                        children=[
                            dcc.Input(
                                id="n",
                                type="range",
                                min=0,
                                max=100,
                                step=1,
                                value=25,
                            ),
                            html.Output("25", id="n-value", htmlFor="n"),
                        ],
                    ),
                ], className="controls"),
                html.Div([
                    html.P("Squared result"),
                    html.Strong("625", id="out"),
                    html.Span("Updates as you move the slider.", className="result-note"),
                ], className="result", **{"aria-live": "polite"}),
            ],
        ),
    ],
)


@app.callback(Output("out", "children"), Output("n-value", "children"), Input("n", "value"))
def update(n):
    value = int(n or 0)
    return f"{value**2:,}", str(value)


app.run(host=args.host, port=args.port)

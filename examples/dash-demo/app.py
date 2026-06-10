import argparse

import dash
from dash import dcc, html

parser = argparse.ArgumentParser()
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--host", default="127.0.0.1")
args = parser.parse_args()

app = dash.Dash(__name__, requests_pathname_prefix="/app/dash-demo/")
app.layout = html.Div([
    html.H1("ShinyHub Dash demo"),
    dcc.Slider(0, 100, value=25, id="n"),
    html.Div(id="out"),
])


@app.callback(dash.Output("out", "children"), dash.Input("n", "value"))
def update(n):
    return f"{n} squared is {n**2}"


app.run(host=args.host, port=args.port)

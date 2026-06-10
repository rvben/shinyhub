import os

import jwt
from shiny import App, render, ui

KEY = bytes.fromhex(os.environ.get("SHINYHUB_IDENTITY_KEY", "00"))
SLUG = os.environ.get("SHINYHUB_APP_SLUG", "")

app_ui = ui.page_fluid(
    ui.h2("ShinyHub identity demo"),
    ui.output_ui("who"),
    ui.output_ui("admin_panel"),
)


def server(input, output, session):
    headers = session.http_conn.headers

    def verified():
        token = headers.get("x-shinyhub-identity-token")
        if not token:
            return None
        try:
            return jwt.decode(token, KEY, algorithms=["HS256"],
                              audience=SLUG, issuer="shinyhub", leeway=30)
        except jwt.InvalidTokenError:
            return None

    @output
    @render.ui
    def who():
        user = verified()
        if user is None:
            return ui.p("Hello, anonymous visitor. Sign in to ShinyHub and reload.")
        groups = ", ".join(user.get("groups") or []) or "(none)"
        return ui.tags.dl(
            ui.tags.dt("User"), ui.tags.dd(user["preferred_username"]),
            ui.tags.dt("Role"), ui.tags.dd(user["role"]),
            ui.tags.dt("Groups (verified)"), ui.tags.dd(groups),
        )

    @output
    @render.ui
    def admin_panel():
        user = verified()
        if user and user["role"] == "admin":
            return ui.card(ui.h4("Admins only"), ui.p("You can see this because your verified role is admin."))
        return ui.HTML("")


app = App(app_ui, server)

# This demo decodes the ShinyHub identity token inline to stay dependency-light
# and show what the verification does under the hood. In your own app, prefer
# the one-call helper: `pip install shinyhub-identity`, then
# `from shinyhub_identity.shiny import session_identity`. See docs/identity.md.
import os

import jwt
from shiny import App, render, ui

KEY = bytes.fromhex(os.environ.get("SHINYHUB_IDENTITY_KEY", "00"))
SLUG = os.environ.get("SHINYHUB_APP_SLUG", "")

THEME_CSS = """
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
*, *::before, *::after { box-sizing: border-box; }
body { min-width:320px; margin:0; background:var(--canvas); color:var(--text); font-family:Manrope,-apple-system,system-ui,"Segoe UI",sans-serif; -webkit-font-smoothing:antialiased; }
.container-fluid { padding:0; }
.identity-shell { width:min(760px,calc(100% - 36px)); margin:0 auto; padding:48px 0 64px; }
.context { margin:0 0 10px; color:var(--accent-bright); font:650 .78rem/1.4 ui-monospace,SFMono-Regular,Menlo,monospace; }
h1 { max-width:18ch; margin:0; font-size:3rem; line-height:1.06; letter-spacing:-.035em; text-wrap:balance; }
.lede { max-width:65ch; margin:14px 0 28px; color:var(--text-soft); font-size:1.03rem; line-height:1.6; text-wrap:pretty; }
.identity-panel { padding:22px; border:1px solid var(--line); border-radius:14px; background:var(--surface); }
.verified { display:inline-flex; align-items:center; gap:8px; margin-bottom:18px; color:var(--accent-bright); font-size:.82rem; font-weight:650; }
.verified-dot { width:8px; height:8px; border-radius:99px; background:var(--accent); }
.identity-list { display:grid; grid-template-columns:minmax(120px,.45fr) minmax(0,1fr); margin:0; }
.identity-list dt, .identity-list dd { min-width:0; margin:0; padding:12px 0; border-bottom:1px solid var(--line); }
.identity-list dt { color:var(--text-soft); font-size:.82rem; font-weight:650; }
.identity-list dd { color:var(--text); overflow-wrap:anywhere; }
.identity-list dt:nth-last-of-type(1), .identity-list dd:nth-last-of-type(1) { border-bottom:0; }
.anonymous { margin:0; color:var(--text-soft); line-height:1.55; }
.admin-panel { margin-top:16px; padding:16px; border:1px solid var(--line); border-radius:14px; background:var(--surface-raised); }
.admin-panel h2 { margin:0 0 6px; font-size:1rem; }
.admin-panel p { margin:0; color:var(--text-soft); }
@media (max-width:560px) {
  .identity-shell { width:min(100% - 28px,760px); padding:28px 0 44px; }
  h1 { font-size:2.25rem; }
  .identity-panel { padding:18px; }
  .identity-list { grid-template-columns:1fr; }
  .identity-list dt { padding-bottom:3px; border-bottom:0; }
  .identity-list dd { padding-top:0; }
}
"""

app_ui = ui.page_fluid(
    ui.tags.head(ui.tags.style(THEME_CSS)),
    ui.tags.main(
        ui.div(
            ui.p("Live application · Trusted identity", class_="context"),
            ui.h1("See the identity your app receives."),
            ui.p(
                "ShinyHub signs a short-lived identity context for this application. "
                "The values below were verified inside the running Python process.",
                class_="lede",
            ),
            ui.div(
                ui.div(ui.span(class_="verified-dot"), "Verified identity context", class_="verified"),
                ui.output_ui("who"),
                class_="identity-panel",
            ),
            ui.output_ui("admin_panel"),
            class_="identity-shell",
        )
    ),
)


def server(input, output, session):
    # Verify once, at session start. Identity is bound at the WebSocket
    # handshake and the token forwarded there expires five minutes later, so
    # decoding it inside a reactive starts failing part-way through a long
    # session even though nothing about the user changed. `session_identity`
    # in the helper package does exactly this for you.
    token = session.http_conn.headers.get("x-shinyhub-identity-token")
    user, rejected = None, ""
    if token:
        try:
            user = jwt.decode(token, KEY, algorithms=["HS256"],
                              audience=SLUG, issuer="shinyhub", leeway=30,
                              options={"require": ["exp"]})
        except jwt.InvalidTokenError as err:
            # A token that is present but does not verify means a broken
            # deployment, not an anonymous visitor. Rendering it as "signed
            # out" would hide a misconfiguration behind an empty page.
            rejected = str(err)

    @output
    @render.ui
    def who():
        if rejected:
            return ui.p(
                f"An identity token arrived but failed verification ({rejected}). "
                "This deployment is misconfigured; nobody is signed out.",
                class_="anonymous",
            )
        if user is None:
            return ui.p("Sign in through ShinyHub to inspect a verified identity context.", class_="anonymous")
        groups = ", ".join(user.get("groups") or []) or "(none)"
        return ui.tags.dl(
            ui.tags.dt("User"), ui.tags.dd(user["preferred_username"]),
            ui.tags.dt("Name"), ui.tags.dd(user.get("name") or "(none)"),
            ui.tags.dt("Role"), ui.tags.dd(user["role"]),
            ui.tags.dt("Groups (verified)"), ui.tags.dd(groups),
            class_="identity-list",
        )

    @output
    @render.ui
    def admin_panel():
        if user and user["role"] == "admin":
            return ui.tags.section(
                ui.h2("Administrator context"),
                ui.p("This panel is visible because the verified role is admin."),
                class_="admin-panel",
            )
        return ui.HTML("")


app = App(app_ui, server)

---
description: "A live ShinyHub running a curated fleet of R and Python apps, so you can try deploys, scaling, and hibernation before installing anything."
---

# Live demo

The public demo is a real ShinyHub installation running a curated fleet of R and
Python applications.

[Open demo.shinyhub.dev](https://demo.shinyhub.dev/){ .shiny-button .shiny-button--primary }

## Explore anonymously

Public applications open without an account. They use synthetic bundled data
and run on an application origin isolated from the dashboard and API.

The fleet includes examples built with Python Shiny, R Shiny, Dash, and
Streamlit. Applications hibernate when idle, so the first request may briefly
show ShinyHub waking the process.

## Explore the control plane

Choose **Continue as Demo Viewer** for one-click access to the real control
plane. No account is created. The resulting session has the global `viewer`
role: it can inspect applications and viewer-safe operational state but cannot
deploy, change configuration, manage users, or create credentials. No signup or
third-party identity is required.

The collapsed manual-login fallback uses username `demo-viewer` and password
`explore-shinyhub-demo`. This credential is intentionally public and carries
the same read-only permissions.

The shared demo intentionally does not accept visitor-provided code. A writable
sandbox would require a separate, disposable tenant boundary rather than broad
permissions on this instance.

## Safety boundaries

- Every public identity starts as `viewer`.
- Application containers have fixed CPU, memory, session, and replica limits.
- The fleet is reconciled from version-controlled manifests.
- Demo data is synthetic and can be replaced during reconciliation.
- Administration uses a separate non-public credential.

Want to run the same stack yourself? Start with the
[Docker Compose installation](deployment/docker-compose.md).

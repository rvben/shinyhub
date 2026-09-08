---
description: "Publish FastAPI and Plumber APIs with managed launch commands, dependency preparation, OpenAPI readiness checks, and private deployment previews."
---

# Publish APIs

ShinyHub has managed launchers for FastAPI and Plumber. Declare the framework
in `shinyhub.toml`; deployment, local development, wake, replicas and rollback
read that declaration from the bundle. Existing apps retain their current
launch behavior when `framework` is omitted.

## FastAPI

Provide `app.py` exporting an ASGI application named `app`, and declare
`fastapi` and `uvicorn` in `requirements.txt` or `pyproject.toml`:

```toml
[app]
framework = "fastapi"
```

ShinyHub prepares Python dependencies through the existing uv build path and
starts `uvicorn app:app` on the replica's assigned host and port. Its external
root path is set to `/app/<slug>`, including the separate slug of a draft
preview. This keeps OpenAPI URLs and the `/docs` interface under the app's
route. See [FastAPI's proxy guide](https://fastapi.tiangolo.com/advanced/behind-a-proxy/)
for the role of ASGI `root_path`.

```bash
cd examples/fastapi-demo
shinyhub dev .
shinyhub deploy . --slug calculations --open
# GET /app/calculations/add?a=2&b=3 returns {"result":5}
# Interactive documentation: /app/calculations/docs
```

Pin dependencies with your usual lockfile workflow for production. Host builds
and off-host runtimes preserve the existing uv project/lockfile behavior.

## Plumber

Provide `plumber.R` and make the `plumber` package available in your R project
or runtime image:

```toml
[app]
framework = "plumber"
```

The launcher prepares R dependencies through the existing renv restore path,
then runs `plumber::pr_run(plumber::pr("plumber.R"), ...)` with the assigned
host and port. It preserves ShinyHub's R startup isolation and project-library
policy. The OpenAPI server URL points to the app's external route. Interactive
documentation is available at `/app/<slug>/__docs__/`.

```bash
cd examples/plumber-demo
# In R, initialize/snapshot the project's plumber dependency with renv.
shinyhub doctor .
shinyhub dev .
shinyhub deploy . --slug r-calculations --open
```

R execution requires R and the app dependencies on the selected runtime;
R apps remain unsupported by the Python-only reference Fargate runner.
See [Plumber's server interface](https://www.rplumber.io/reference/pr_run.html).

## Readiness, access and drafts

Both managed API launchers probe `/openapi.json` by default, so an API does
not need a `GET /` handler to become healthy. If OpenAPI is disabled or protected
by application-level authentication, declare an internal readiness route:

```toml
[app]
framework = "fastapi"
readiness_path = "/healthz"
readiness_status = 200
```

`deploy --open` opens the root app URL. Provide a root handler if you want that
flow, or open the documentation URL directly after deployment.

The normal public/shared/private access rules apply. Browser viewers sign in
through ShinyHub. Platform API keys and trusted CI credentials manage deployment;
they are not automatically credentials for the hosted API. Machine clients
must use an appropriate ShinyHub login session or the API's own authentication
on an intentionally public app. An app can use ShinyHub's verified forwarded
identity; see [Identity forwarding](identity.md) before making access decisions.

These APIs can use [deployment drafts](drafts.md) when their bundles contain no
hooks, schedules or access-group declarations. Reviewers receive access to a
separate preview; production data and credentials are not copied.

`framework` and a custom `command` are mutually exclusive. For different
entrypoints or server options, use the existing explicit-command workflow.
Session-based autoscaling remains session-based; HTTP request throughput does
not become a new autoscaling signal just because the content is an API.

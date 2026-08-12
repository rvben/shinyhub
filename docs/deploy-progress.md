# Deployment progress

`shinyhub deploy` reports the work the server is actually doing. Interactive
terminals show a compact spinner for the active phase and settle it into a
completed line. CI and redirected stderr receive durable, line-oriented phase
updates instead of terminal control characters.

Typical phases are:

1. Build and inspect the local bundle.
2. Upload and validate it on the server.
3. Build Python or R dependencies, with elapsed-time heartbeats for long builds.
4. Run manifest post-deploy hooks.
5. Start replicas and check readiness.
6. Record the deployment and apply manifest configuration.
7. Clean up superseded bundle files.

If a deployment fails, the error names the failed phase and keeps the server's
stable `failure_kind`. When a previous version exists, progress also says
whether recovery restored it, left the app stopped, or could not recover it.
Secrets, environment values, package-index credentials, and hook output are not
copied into the progress stream. App logs and `deploy-hooks.log` remain the
detailed diagnostic sources.

For the interactive end-to-end path, run `shinyhub deploy . --open`. It implies
`--start` and `--wait`, then verifies a public app through its actual routed URL
and opens it. Browser-launch failure is a convenience failure, not a deployment
failure: the URL remains visible and JSON reports `opened: false`. A route-check
failure is different—it exits non-zero while explicitly preserving the fact
that the deployment became healthy.

## Automation

The default for redirected stdout remains one JSON result document:

```sh
shinyhub deploy . --output json > deploy-result.json
```

Use NDJSON when automation should observe every phase in real time:

```sh
shinyhub deploy . --output ndjson | jq -c .
```

Every line is an independent JSON object. The stable top-level event types are:

- `phase`: a lifecycle update with `phase`, `status`, and `message`.
- `result`: the terminal success event; `result` contains the normal deploy API
  response.
- `error`: the terminal failure event with `phase`, `status_code`,
  `failure_kind`, and `message`.

Phase statuses are `started`, `progress`, `completed`, `warning`, and `failed`.
Additional optional fields include `elapsed_seconds`, `current`, `total`,
`replica`, `file_count`, `bytes`, and `digest`.

The CLI negotiates this stream through the deploy endpoint. Servers that do not
support it return their original JSON response; the CLI detects that response
and emits a compatible terminal NDJSON event without requiring a version flag
or separate command.

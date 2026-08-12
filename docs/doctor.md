# Doctor

`shinyhub doctor` is a read-only preflight for the complete developer journey:
run an app locally, connect to a ShinyHub, and deploy it. It gathers every
problem it can find instead of stopping at the first one, and every warning or
failure includes the action that resolves it.

## Recommended clean-room flow

From a new workstation and an app directory:

```bash
uv tool install shinyhub
cd ./my-app

shinyhub doctor . --local
shinyhub run . --check

shinyhub connect https://hub.example.com --name prod
shinyhub doctor .
shinyhub deploy . --wait
```

The first doctor invocation needs no configuration, credentials, or network.
The second checks both the local bundle and the current remote, including
permission for the slug that `deploy` will derive from the directory name.

## What it checks

Local checks:

- the app path exists and is a directory;
- the derived or explicit deployment slug is valid;
- `shinyhub.toml` parses under the same strict rules used by deploy;
- the production launch plan resolves to an inferred or custom command;
- the command's local executable, such as `uv` or `Rscript`, is on `PATH`.

Remote checks:

- a credential is selected without exposing its value;
- a saved credentials file is owner-only (`0600`);
- a non-loopback remote uses HTTPS;
- `/api/server-info` identifies a ready ShinyHub and reports its runtimes;
- the CLI and server API protocols are compatible, with the lagging side named;
- the credential authenticates and the server identifies its user and role;
- the credential type and lifecycle are known, warning 14 days before expiry;
- the identity may update the target app, or create it when it is new;
- the server reports the runtime required by the local app.

Checks that depend on a failed prerequisite are marked `skip`, not `fail`, so
one root cause does not masquerade as several unrelated problems. Warnings do
not make the command fail; blocking problems do.

An expiring saved credential points to `shinyhub connect --refresh`. For an
environment-provided credential, Doctor instead tells automation to replace
`SHINYHUB_TOKEN`; it never writes an environment credential into the local
store. Servers predating lifecycle metadata receive a non-blocking upgrade
warning rather than a guessed expiry.

Patch-level drift is compatible. Before 1.0, a minor-version difference is
shown as a warning; from 1.0 onward, that boundary is the major version. The
advertised API protocol is authoritative: a server with a newer protocol than
the CLI supports blocks remote checks before credentials are used. See
[CLI completion and compatibility](cli.md).

## Focused checks

Check local readiness without reading credentials or contacting a server:

```bash
shinyhub doctor ./my-app --local
```

Check only the selected remote:

```bash
shinyhub doctor --remote
shinyhub doctor --remote --slug sales
shinyhub doctor --remote --slug sales --host staging
```

The global `--host` accepts a saved alias or a full URL. `--remote --slug`
distinguishes permission to update an existing app from permission to create a
new one. Doctor performs no probe writes and never creates the app.

## Automation

Redirected output defaults to one JSON document; `--output json` makes that
choice explicit:

```bash
shinyhub doctor . --output json
```

The document contains stable `status`, `scope`, `checks`, `summary`, and
`next_steps` fields. Each check has a `name`, `status`, `detail`, and optional
`fix`. No token or password is included.

Exit codes:

| Code | Meaning |
|---:|---|
| `0` | Ready; warnings may still be present |
| `1` | Local app, manifest, runtime, configuration, or CLI/server compatibility blocker |
| `3` | Authentication, authorization, or network blocker |
| `6` | The host answered, but ShinyHub is not ready |

For CI without a credentials file:

```bash
SHINYHUB_HOST=https://hub.example.com \
SHINYHUB_TOKEN="$SHINYHUB_TOKEN" \
shinyhub doctor . --output json
```

Follow a successful doctor run with `shinyhub run . --check` for a real local
boot and `shinyhub deploy . --wait` for the real remote build and readiness
check. Doctor is deliberately fast and non-mutating; those two commands are the
end-to-end proof.

ShinyHub contributors can replay this entire journey against a fresh temporary
server with `make test-onboarding-e2e`. The test also performs an intentionally
broken redeploy and asserts that its app log is shown inline. Set `E2E_KEEP=1`
to retain its temporary configuration and logs after a failure.

Changes to the distributable or remote onboarding flow should additionally run
`make test-browser-onboarding-e2e`. That gate installs the built Python wheel,
completes sign-in and pairing in real headless Chrome, deploys, revokes the
credential in the dashboard, verifies the CLI's recovery guidance, and pairs
again through the still-authenticated browser session.

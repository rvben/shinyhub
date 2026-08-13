# CLI completion and compatibility

ShinyHub's CLI is designed to remain discoverable after the first successful
deploy and predictable when a workstation and remote server are upgraded at
different times.

## Go from deployment to the app

For an interactive deployment, use the complete success-to-use flow:

```bash
shinyhub deploy . --open
```

`--open` implies `--start` and `--wait`. ShinyHub waits for the deployed version
to become healthy, smoke-tests the actual `/app/<slug>/` route when the app is
public, then opens the canonical URL in the default browser. Private and shared
apps enter the normal browser sign-in flow; the CLI credential is never sent to
an app route.

A missing browser or graphical session does not turn a healthy deployment into
a failure. The command prints a copyable URL and structured JSON reports
`"opened": false`. A failed public route check does exit non-zero, but states
clearly that the deployment itself succeeded and points to the app logs.

Open an existing app without redeploying it:

```bash
shinyhub apps open sales
shinyhub apps open sales --no-browser  # SSH, containers, and scripts
```

Running, sleeping, waking, deploying, and degraded apps follow the same launch
behavior as the dashboard. Sleeping apps wake through the route. Stopped,
crashed, and never-deployed apps are not launched; the CLI gives the exact
start, log, or deploy command that resolves their state. `--output json` always
includes `url`, `opened`, and the observed `app_status`.

## Install shell completion

The installer detects zsh, bash, fish, or PowerShell from the current shell:

```bash
shinyhub completion install
```

Or name the shell explicitly, which is useful from setup scripts or an unusual
terminal environment:

```bash
shinyhub completion install zsh
shinyhub completion install bash
shinyhub completion install fish
shinyhub completion install powershell
```

Installation is per-user and safe to rerun. Bash, zsh, and PowerShell receive
one clearly marked source block in their normal startup file. Fish uses its
native completions directory and needs no startup-file edit. Existing file
permissions and all content outside ShinyHub's marked block are preserved.

Preview the exact paths without changing anything:

```bash
shinyhub completion install --dry-run
```

Start a new shell after installation, or follow the printed one-line reload
instruction. Completion includes commands and flags plus locally saved host
aliases and URLs for `shinyhub use` and `--host`. Host suggestions are read from
the credentials file without network access, and credentials are never emitted.

Remove only the files and marked block managed by ShinyHub:

```bash
shinyhub completion uninstall
```

Package maintainers can still generate a script without installing it:

```bash
shinyhub completion zsh > _shinyhub
shinyhub completion bash > shinyhub.bash
shinyhub completion fish > shinyhub.fish
shinyhub completion powershell > shinyhub.ps1
```

## Understand client/server compatibility

Every current server advertises an integer `protocol_version` from the
unauthenticated `/api/server-info` endpoint. The protocol changes only for an
incompatible API contract. Additive features use capability flags, allowing a
newer CLI or server to keep the common command set working safely.

`shinyhub connect` checks compatibility before it authorizes or verifies a
credential. `shinyhub doctor --remote` exposes the same decision as a dedicated
`version-compatibility` check.

| Situation | Behavior |
|---|---|
| Same release line, including patch drift | Compatible; no upgrade warning |
| Older CLI, compatible newer server | Continue; suggest upgrading the CLI |
| Newer CLI, compatible older server | Continue with capability-gated features; suggest upgrading the server |
| Server protocol newer than the CLI understands | Stop before an authenticated request; upgrade the CLI |
| Legacy server without a protocol field | Continue through capability negotiation and report the uncertainty |

Before ShinyHub 1.0, the minor version defines a release line because minor
releases may contain compatibility changes. Starting at 1.0, the major version
defines it. The explicit protocol remains authoritative in either case.

The connection's JSON result includes `cli_version`, `server_version`,
`protocol_version`, and `compatibility`, so automation can record the decision
without parsing prose.

Ordinary `shinyhub connect <url>` is idempotent. It validates an existing saved
credential first and returns `status: current` without browser authorization or
key rotation when that credential is usable. Explicit command-line credentials
take precedence over `SHINYHUB_TOKEN`, which takes precedence over the saved
credential. Only a rejected saved credential falls through to authorization;
network, rate-limit, and server failures remain errors. `--refresh` is the
explicit exception and always rotates through browser approval.

## Keep workstation credentials healthy

The server reports safe metadata about the credential used for each identity
request: its type, optional name, creation time, prior last use, and expiry. It
never returns the credential value or hash. Inspect it directly with:

```bash
shinyhub whoami
shinyhub whoami --output json
shinyhub doctor --remote
```

Doctor warns when 14 days or less remain. Rotate a saved workstation credential
through the same browser pairing flow without waiting for an outage:

```bash
shinyhub connect --refresh
# remote terminal:
shinyhub connect --refresh --no-browser
```

The new credential must authenticate before the owner-only credentials file is
atomically replaced. Other hosts and the current alias are preserved. ShinyHub
then revokes the old API key; if only that cleanup fails, the new credential is
kept and the command provides a manual revocation command. `--refresh` ignores
`SHINYHUB_TOKEN` by design so an inherited CI secret cannot be written into the
workstation store.

## Upgrade the side that is behind

For the Python-distributed CLI:

```bash
uv tool upgrade shinyhub
# or, when installed into a Python environment:
python -m pip install --upgrade shinyhub
```

For another installation method, upgrade through the same package manager or
release artifact used originally. Upgrade the server through its existing
deployment mechanism. Then verify the result without changing remote state:

```bash
shinyhub --version
shinyhub doctor --remote
```

Version warnings do not make Doctor fail when the advertised protocol is still
compatible. An unsupported protocol is a blocker because guessing across a
breaking API boundary would be less useful—and less safe—than stopping with the
exact upgrade command.

## Maintain the release contract

Before a release, run the complete long-lived CLI gate:

```bash
make test-cli-release-contract
```

It combines two independent journeys:

- `make test-cli-compatibility-e2e` downloads the exact checksum-pinned previous
  release. The current CLI connects to, diagnoses, deploys to, and exercises
  credential recovery against that server; then the released CLI logs in to,
  identifies itself against, and deploys to the current server.
- `make test-shell-completion-e2e` installs, loads, reinstalls, and uninstalls
  completion in real Bash, zsh, fish, and PowerShell processes. Set
  `SHINYHUB_COMPLETION_SHELLS="bash zsh"` for a smaller local subset.

The compatibility baseline lives in
`testdata/compatibility/previous-release.txt`, with the release's published
checksum manifest beside it. Advance both files to the newly published version
when that version becomes the baseline for the next release. Never regenerate
the old binary from a tag: the gate intentionally tests the artifact users
actually installed.

The required `/api/server-info` shape for each protocol is recorded in
`internal/protocol/testdata/server-info-vN.json`. Additive response fields do
not require a bump. Removing a required field or changing its JSON type does:
increment `protocol.CurrentVersion`, add the new fixture, and keep the previous
fixture as the historical contract. CI fails if the implementation and the
declared fixture diverge.

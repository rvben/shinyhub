# CLI completion and compatibility

ShinyHub's CLI is designed to remain discoverable after the first successful
deploy and predictable when a workstation and remote server are upgraded at
different times.

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

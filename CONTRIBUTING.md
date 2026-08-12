# Contributing to ShinyHub

Thanks for your interest in ShinyHub!

## Before you start

For non-trivial changes, please open a GitHub issue first to discuss the approach.
Small fixes and doc improvements can go straight to a PR.

## Dev setup

You need Go 1.26.5+ (see `go.mod` for the exact version) and Node 20+ for the
dashboard tests. Python app development additionally needs
[uv](https://docs.astral.sh/uv/); R app development needs R and renv.

```bash
git clone https://github.com/rvben/shinyhub.git
cd shinyhub
make bootstrap
make dev
```

`make bootstrap` downloads Go and Node dependencies and installs the pinned
live-reload tool under `tmp/tools`; it does not modify your global Go bin.
`make dev` serves the dashboard at <http://127.0.0.1:8080>, watches Go files,
and keeps the last healthy server running through compile errors. Log in with
`admin` / `admin`. Those credentials and the development auth secret are only
for local use.

To run without live reload, use `make run`. To archive the local database,
bundles, and app data and start fresh, use `make dev-reset`; it moves the old
state under `tmp/` rather than deleting it.

See [Local development](docs/local-development.md) for the full control-plane
and app-author workflow. If you use git worktrees, prefix raw Go commands with
`GOWORK=off` to avoid workspace-mode confusion:

```bash
GOWORK=off go test ./...
```

## Tests

Run the same fast gate expected of every PR:

```bash
make check
```

New features need tests. Bug fixes should include a regression test. The
specialized integration and security targets in the Makefile document their
extra Docker, uv, R, or cloud requirements.

## Commit style

We use [Conventional Commits](https://www.conventionalcommits.org/):

- `feat(scope): add X`
- `fix(scope): handle Y`
- `docs: update README`
- `test(scope): cover Z edge case`
- `refactor(scope): …`
- `chore: …` / `ci: …`

Keep commits focused — one logical change per commit.

## License

By submitting a pull request you agree that your contribution is licensed
under the project's MIT license (see `LICENSE`).

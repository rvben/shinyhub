# Contributing to ShinyHub

Thanks for your interest in ShinyHub!

## Before you start

For non-trivial changes, please open a GitHub issue first to discuss the approach.
Small fixes and doc improvements can go straight to a PR.

## Dev setup

ShinyHub is a pure-Go project. You need Go 1.23+ (see `go.mod` for the exact
minimum).

```bash
git clone https://github.com/rvben/shinyhub.git
cd shinyhub
go build ./...
go test ./...
```

If you use git worktrees, prefix Go commands with `GOWORK=off` to avoid
workspace-mode confusion:

```bash
GOWORK=off go test ./...
```

## Tests

All PRs must pass `make lint test`. New features need tests. Bug fixes should
include a regression test.

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

.PHONY: build clean test test-go test-js test-remote-e2e test-fargate-it test-handoff test-postgres test-ha lint fmt fmt-check run dev goreleaser-check build-runner-image skill-lint skill-smoke clispec-score

build:
	go build -o bin/shinyhub ./cmd/shinyhub

clean:
	rm -rf bin tmp

test: test-go test-js

test-go:
	go test ./... -count=1

# test-js runs the JSDOM tests for UI assets. Requires Node 20+. Installs
# devDependencies (jsdom) the first time it runs; afterwards it's a no-op.
test-js:
	@command -v node >/dev/null 2>&1 || { echo "node not found (Node 20+ required for UI tests)"; exit 1; }
	@if [ ! -d node_modules/jsdom ]; then npm install --no-audit --no-fund --silent; fi
	node --test 'internal/ui/jstests/*.test.js'

# test-remote-e2e launches a control plane and a real `shinyhub worker` against
# the local Docker daemon, deploys an app onto the remote tier with two
# replicas, and asserts the full data path plus recovery behaviors (bundle
# dedup, agent-tunnel routing, control-plane-restart re-adoption, worker-down
# lost-replica handling). Requires a working Docker daemon.
test-remote-e2e:
	./scripts/remote-e2e.sh

# test-handoff builds the binary and verifies a SIGHUP upgrade hands off both
# the main and metrics listeners with no connection-refused gap and a new
# process taking over (zero-downtime upgrade). No Docker required.
test-handoff:
	./scripts/handoff-e2e.sh

# test-postgres spins up a throwaway Postgres 16 in Docker and runs the full Go
# suite against it (SHINYHUB_TEST_POSTGRES_DSN). Each test gets an isolated
# database. Requires a working Docker daemon. SQLite remains the default backend
# for `make test`.
test-postgres:
	./scripts/postgres-test.sh

# test-ha builds the binary, spins up a throwaway Postgres, starts TWO shinyhub
# instances against it, kills the active with SIGKILL, and asserts the standby
# keeps serving the same live replica and acquires the control-plane lease.
# Requires Docker. Gated behind the `integration` build tag, so `make test` and
# `make test-postgres` never run it.
test-ha:
	./scripts/ha-failover-e2e.sh

# test-fargate-it runs the Fargate runtime's real-cluster smoke test (launch a
# task, assert routing + inventory, stop it). It is gated behind the `integration`
# build tag and skips unless SHINYHUB_FARGATE_IT_CLUSTER (and the related
# SHINYHUB_FARGATE_IT_* vars documented in internal/fargate/integration_test.go)
# are set. There is no open-source ECS emulator that supports the Fargate awsvpc
# RunTask path, so this requires a real ECS cluster and AWS credentials; running
# it launches a billed Fargate task and then stops it.
test-fargate-it:
	go test -tags=integration -run TestIntegration -count=1 -v ./internal/fargate/...

lint:
	go vet ./...

# skill-lint runs static checks on the deploy-shinyhub skill: frontmatter,
# example bundle present, referenced docs exist, no em/en dashes. No network or
# build, so it is safe to run anywhere; CI runs it on every push.
skill-lint:
	./scripts/skill-lint.sh

# skill-smoke stands up a server and deploys the skill's example app exactly as
# skills/deploy-shinyhub/SKILL.md documents, asserting the proxy serves it (200).
# The example app installs `shiny` via uv on first start, so this needs uv plus
# network; it SKIPS (exit 0) when uv is absent so offline runs stay green. CI
# installs uv and runs it for real.
skill-smoke:
	./scripts/skill-smoke.sh

# fmt rewrites all tracked Go files with gofmt (go 1.26 canonical formatting).
# Scoped to tracked files via git ls-files so nested worktrees under .claude/
# or .claire/ are never reformatted. Run as a standalone maintenance commit.
fmt:
	gofmt -w $$(git ls-files '*.go')

# fmt-check fails if any tracked Go file is not gofmt-clean. Wire into lint/CI
# once the repo has been swept clean with `make fmt`.
fmt-check:
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed (run make fmt):"; echo "$$unformatted"; exit 1; fi

run: build
	SHINYHUB_AUTH_SECRET=dev-secret-do-not-use-in-production ./bin/shinyhub serve

# dev runs the server with live reload via air. Go changes trigger a rebuild;
# edits to internal/ui/static/ are served from disk (no rebuild) thanks to
# SHINYHUB_DEV_STATIC. Install air with: go install github.com/air-verse/air@latest
dev:
	@command -v air >/dev/null 2>&1 || { echo "air not found. install: go install github.com/air-verse/air@latest"; exit 1; }
	air

goreleaser-check:
	goreleaser check

# build-runner-image builds the reference Python Fargate runner image. The
# image is not required for local development but is needed for ECS-based
# deployments. Requires Docker.
build-runner-image:
	docker build -t shinyhub-fargate-runner:latest build/fargate-runner/

# Release workflow:
#   make release-patch   (or release-minor / release-major)
#   vership bumps the version, commits changelog, creates a tag, and pushes.
#   GitHub Actions picks up the v* tag and runs GoReleaser.
#   Binaries are attached to the GitHub release automatically.
#   The install script at scripts/install.sh always pulls the latest release.

# clispec-score builds the binary and scores it against The CLI Spec v0.2.
# Requires clispec >= 0.2.0 installed (cargo install clispec --force).
# Exits 0 only when score == max. Note: the runtime probes execute a real
# list command, so full marks requires SHINYHUB_HOST/SHINYHUB_TOKEN pointing
# at a reachable server; unauthenticated runs lose the four probe checks.
# The authoritative conformance gate is the local test suite
# (internal/cli/schema_test.go, cmd/shinyhub/schema_conformance_test.go);
# this target is an outside-in floor. Pass the probe subcommand explicitly
# because nested subcommand discovery varies by clispec version.
clispec-score: build ## Score the binary against clispec (requires clispec >= 0.2.0)
	@clispec score ./bin/shinyhub apps list --json > /tmp/clispec-score.json || true
	@python3 -c "import json; d=json.load(open('/tmp/clispec-score.json')); s,m=d['score'],d['max']; print(f'clispec score: {s}/{m}'); raise SystemExit(0 if s==m else 1)"

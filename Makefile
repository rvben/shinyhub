.PHONY: build clean test test-go test-race vuln test-js test-remote-e2e test-fargate-it test-handoff test-postgres test-ha lint fmt fmt-check run dev goreleaser-check build-runner-image skill-lint skill-smoke load-test load-test-isolation iac-validate clispec-score test-identity test-py-identity test-r-identity test-identity-conformance

build:
	go build -o bin/shinyhub ./cmd/shinyhub

clean:
	rm -rf bin tmp

test: test-go test-js

test-go:
	go test ./... -count=1

# test-race runs the full Go suite under the race detector. Slower (~3-5x) than
# test-go, so it is its own target/CI job rather than folded into test-go. The
# control plane crosses many goroutine boundaries (watcher, proxy poolsync,
# autoscale, worker agent), so this is the primary guard against data races.
# A raised per-package -timeout accommodates the slowest packages under the
# race detector's overhead (the default 10m is exceeded by the api package).
test-race:
	go test -race ./... -count=1 -timeout 30m

# vuln scans the module (and its dependencies) against the Go vulnerability
# database. Run via `go run` so no separate install step is needed and it works
# the same locally and in CI.
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# test-js runs the JSDOM tests for UI assets. Requires Node 20+. Installs
# devDependencies (jsdom) the first time it runs; afterwards it's a no-op.
# The glob is left unquoted so the shell expands it to the matching files;
# node --test does not expand glob patterns itself before Node 21, so quoting
# would break on Node 20.
test-js:
	@command -v node >/dev/null 2>&1 || { echo "node not found (Node 20+ required for UI tests)"; exit 1; }
	@if [ ! -d node_modules/jsdom ]; then npm install --no-audit --no-fund --silent; fi
	node --test internal/ui/jstests/*.test.js

# test-identity runs the client-helper suites plus the cross-language
# conformance check. Kept out of the default `test` target so the core Go/JS
# gate does not require uv or R; wire it into CI as its own step.
test-identity: test-py-identity test-identity-conformance test-r-identity

# test-py-identity runs the shinyhub-identity Python helper's unit tests via uv.
test-py-identity:
	@command -v uv >/dev/null 2>&1 || { echo "uv not found (needed for the Python identity helper tests)"; exit 1; }
	cd packaging/python-identity && PYTHONPATH=src uv run --with pytest --with pyjwt --no-project python -m pytest tests/ -q

# test-r-identity runs the shinyhubidentity R helper's testthat suite. Needs R
# with jose, sodium and testthat; skips cleanly when Rscript is absent.
test-r-identity:
	@if command -v Rscript >/dev/null 2>&1; then \
		Rscript -e 'testthat::test_local("packaging/r-identity")'; \
	else \
		echo "Rscript not found; skipping R identity helper tests"; \
	fi

# test-identity-conformance verifies the shipped Python and R helpers against a
# token minted by the real production MintToken. Each language subtest skips
# when its toolchain (uv / Rscript) is absent.
test-identity-conformance:
	SHINYHUB_CONFORMANCE=1 go test ./internal/identity/ -run TestConformance -count=1 -v

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

# load-test runs k6 scenarios against a running ShinyHub server.
# LT_SLUG is required. LT_SCENARIO controls which scenario runs:
#   sessions    (default) - ramp to LT_SESSIONS concurrent WebSocket sessions
#   cold-start            - measure wake latency for a hibernated app
#   both                  - run cold-start then sessions
# Set ASSERT=1 to enable k6 thresholds (cold start p95<15s, session rate>99%).
# See docs/load-testing.md for the full parameter reference.
load-test: ## Run k6 load tests (LT_SLUG required; see docs/load-testing.md)
	@command -v k6 >/dev/null 2>&1 || { echo "k6 not found. Install: brew install k6 (https://k6.io/docs/get-started/installation/)"; exit 1; }
	@test -n "$(LT_SLUG)" || { echo "LT_SLUG is required, e.g. make load-test LT_SLUG=myapp"; exit 1; }
	@mkdir -p loadtest/results
	@K6_FLAGS="-e LT_HOST=$(or $(LT_HOST),http://127.0.0.1:8080)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_SLUG=$(LT_SLUG)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_SESSIONS=$(or $(LT_SESSIONS),100)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_RAMP=$(or $(LT_RAMP),30s)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_HOLD=$(or $(LT_HOLD),30)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_WS_PATH=$(or $(LT_WS_PATH),/websocket/)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_FIRST_MSG_TIMEOUT=$(or $(LT_FIRST_MSG_TIMEOUT),5)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_COLDSTART_TIMEOUT=$(or $(LT_COLDSTART_TIMEOUT),120)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_AUTH_COOKIE=$(or $(LT_AUTH_COOKIE),)"; \
	K6_FLAGS="$$K6_FLAGS -e ASSERT=$(or $(ASSERT),0)"; \
	scenario="$(or $(LT_SCENARIO),sessions)"; \
	if [ "$$scenario" = "cold-start" ] || [ "$$scenario" = "both" ]; then \
	  echo "==> cold-start scenario (slug=$(LT_SLUG))"; \
	  k6 run $$K6_FLAGS loadtest/coldstart.js; \
	fi; \
	if [ "$$scenario" = "sessions" ] || [ "$$scenario" = "both" ]; then \
	  echo "==> sessions scenario (slug=$(LT_SLUG), VUs=$(or $(LT_SESSIONS),100))"; \
	  k6 run $$K6_FLAGS loadtest/sessions.js; \
	fi

# load-test-isolation runs the HOL-elimination scenario (loadtest/hol.js) to
# prove that per_session isolation eliminates head-of-line blocking.
# At least one of LT_SLUG_MUX or LT_SLUG_ISO must be set. Provide both to
# record a side-by-side comparison in a single k6 run.
#   LT_SLUG_MUX  - slug of an app configured with worker_isolation=multiplex
#   LT_SLUG_ISO  - slug of an app configured with worker_isolation=per_session
#   ASSERT=1     - enable the threshold: hol_light_ms{mode:iso} p(95) < 3 s
# See docs/isolation.md for setup instructions and result interpretation.
load-test-isolation: ## HOL-elimination acceptance scenario (LT_SLUG_MUX and/or LT_SLUG_ISO required)
	@command -v k6 >/dev/null 2>&1 || { echo "k6 not found. Install: brew install k6 (https://k6.io/docs/get-started/installation/)"; exit 1; }
	@test -n "$(LT_SLUG_MUX)$(LT_SLUG_ISO)" || { echo "Set LT_SLUG_MUX and/or LT_SLUG_ISO, e.g. make load-test-isolation LT_SLUG_ISO=demo-iso"; exit 1; }
	@mkdir -p loadtest/results
	@K6_FLAGS="-e LT_HOST=$(or $(LT_HOST),http://127.0.0.1:8080)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_SLUG_MUX=$(or $(LT_SLUG_MUX),)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_SLUG_ISO=$(or $(LT_SLUG_ISO),)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_SESSIONS=$(or $(LT_SESSIONS),50)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_RAMP=$(or $(LT_RAMP),30s)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_HOLD=$(or $(LT_HOLD),60)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_WS_PATH=$(or $(LT_WS_PATH),/websocket/)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_FIRST_MSG_TIMEOUT=$(or $(LT_FIRST_MSG_TIMEOUT),5)"; \
	K6_FLAGS="$$K6_FLAGS -e LT_AUTH_COOKIE=$(or $(LT_AUTH_COOKIE),)"; \
	K6_FLAGS="$$K6_FLAGS -e ASSERT=$(or $(ASSERT),0)"; \
	echo "==> HOL isolation scenario (mux=$(or $(LT_SLUG_MUX),(none)), iso=$(or $(LT_SLUG_ISO),(none)), sessions=$(or $(LT_SESSIONS),50))"; \
	k6 run $$K6_FLAGS loadtest/hol.js

# iac-validate runs terraform fmt -check, init -backend=false, and validate on
# the aws-ecs module and its minimal example. No AWS credentials are required.
# All steps are also valid locally (make philosophy: CI runs make targets).
iac-validate:
	terraform -chdir=deploy/terraform/aws-ecs fmt -check -recursive
	terraform -chdir=deploy/terraform/aws-ecs init -backend=false
	terraform -chdir=deploy/terraform/aws-ecs validate
	terraform -chdir=deploy/terraform/aws-ecs/examples/minimal init -backend=false
	terraform -chdir=deploy/terraform/aws-ecs/examples/minimal validate

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

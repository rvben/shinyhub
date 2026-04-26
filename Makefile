.PHONY: build clean test lint run dev goreleaser-check

build:
	go build -o bin/shinyhub ./cmd/shinyhub

clean:
	rm -rf bin tmp

test:
	go test ./... -count=1

lint:
	go vet ./...

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

# Release workflow:
#   make release-patch   (or release-minor / release-major)
#   vership bumps the version, commits changelog, creates a tag, and pushes.
#   GitHub Actions picks up the v* tag and runs GoReleaser.
#   Binaries are attached to the GitHub release automatically.
#   The install script at scripts/install.sh always pulls the latest release.

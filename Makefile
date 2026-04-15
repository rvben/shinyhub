.PHONY: build test lint run goreleaser-check

build:
	go build -o bin/shinyhub ./cmd/shinyhub
	go build -o bin/shiny ./cmd/shiny

test:
	go test ./... -count=1

lint:
	go vet ./...

run: build
	SHINYHUB_AUTH_SECRET=dev-secret ./bin/shinyhub

goreleaser-check:
	goreleaser check

# Release workflow:
#   make release-patch   (or release-minor / release-major)
#   vership bumps the version, commits changelog, creates a tag, and pushes.
#   GitHub Actions picks up the v* tag and runs GoReleaser.
#   Binaries are attached to the GitHub release automatically.
#   The install script at scripts/install.sh always pulls the latest release.

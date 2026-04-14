.PHONY: build test lint run

build:
	go build -o bin/shinyhost ./cmd/shinyhost
	go build -o bin/shiny ./cmd/shiny

test:
	go test ./... -count=1

lint:
	go vet ./...

run: build
	SHINYHOST_AUTH_SECRET=dev-secret ./bin/shinyhost

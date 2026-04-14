.PHONY: build test lint run

build:
	go build -o bin/shinyhub ./cmd/shinyhub
	go build -o bin/shiny ./cmd/shiny

test:
	go test ./... -count=1

lint:
	go vet ./...

run: build
	SHINYHUB_AUTH_SECRET=dev-secret ./bin/shinyhub

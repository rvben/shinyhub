# syntax=docker/dockerfile:1.7

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/shinyhub ./cmd/shinyhub

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/rvben/shinyhub"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.description="Self-hosted platform for R Shiny apps"

COPY --from=builder /out/shinyhub /usr/local/bin/shinyhub
COPY shinyhub.yaml.example /etc/shinyhub/shinyhub.yaml.example

ENV SHINYHUB_CONFIG=/etc/shinyhub/shinyhub.yaml
EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/shinyhub"]

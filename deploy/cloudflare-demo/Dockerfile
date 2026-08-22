# syntax=docker/dockerfile:1.7

FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY package.json ./
COPY cmd ./cmd
COPY internal ./internal
RUN VERSION="$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' package.json)" \
    && test -n "$VERSION" \
    && CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/shinyhub ./cmd/shinyhub

FROM caddy:2.10.2-alpine AS caddy

FROM python:3.12-slim-bookworm

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    SHINYHUB_CONFIG=/opt/shinyhub-demo/shinyhub.yaml

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates r-base r-cran-shiny \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir \
    "dash>=4.4,<5" \
    "PyJWT>=2.13,<3" \
    "shiny>=1.6,<2" \
    "streamlit>=1.60,<2"

COPY --from=builder /out/shinyhub /usr/local/bin/shinyhub
COPY --from=caddy /usr/bin/caddy /usr/local/bin/caddy
COPY deploy/cloudflare-demo/shinyhub.yaml /opt/shinyhub-demo/shinyhub.yaml
COPY deploy/cloudflare-demo/fleet.toml /opt/shinyhub-demo/fleet.toml
COPY deploy/cloudflare-demo/Caddyfile /opt/shinyhub-demo/Caddyfile
COPY deploy/cloudflare-demo/bootstrap-viewer.py /opt/shinyhub-demo/bootstrap-viewer.py
COPY deploy/cloudflare-demo/entrypoint.sh /usr/local/bin/cloudflare-demo-entrypoint

COPY deploy/demo/apps/operations-dashboard/app.py /opt/shinyhub-demo/apps/operations-dashboard/app.py
COPY deploy/cloudflare-demo/manifests/operations-dashboard.toml /opt/shinyhub-demo/apps/operations-dashboard/shinyhub.toml
COPY deploy/demo/apps/r-shiny-gallery/app.R /opt/shinyhub-demo/apps/r-shiny-gallery/app.R
COPY deploy/cloudflare-demo/manifests/r-shiny-gallery.toml /opt/shinyhub-demo/apps/r-shiny-gallery/shinyhub.toml
COPY examples/dash-demo/app.py /opt/shinyhub-demo/apps/dash-demo/app.py
COPY examples/dash-demo/assets /opt/shinyhub-demo/apps/dash-demo/assets
COPY deploy/cloudflare-demo/manifests/dash-demo.toml /opt/shinyhub-demo/apps/dash-demo/shinyhub.toml
COPY examples/streamlit-demo/app.py /opt/shinyhub-demo/apps/streamlit-demo/app.py
COPY examples/streamlit-demo/.streamlit /opt/shinyhub-demo/apps/streamlit-demo/.streamlit
COPY deploy/cloudflare-demo/manifests/streamlit-demo.toml /opt/shinyhub-demo/apps/streamlit-demo/shinyhub.toml
COPY examples/identity-demo/app.py /opt/shinyhub-demo/apps/identity-demo/app.py
COPY deploy/cloudflare-demo/manifests/identity-demo.toml /opt/shinyhub-demo/apps/identity-demo/shinyhub.toml

RUN chmod 0755 /usr/local/bin/cloudflare-demo-entrypoint \
    && mkdir -p /data/apps /data/app-data \
    && useradd --system --uid 10001 --home-dir /data --shell /usr/sbin/nologin shinyhub \
    && chown -R shinyhub:shinyhub /data /opt/shinyhub-demo

USER shinyhub:shinyhub
WORKDIR /opt/shinyhub-demo
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cloudflare-demo-entrypoint"]

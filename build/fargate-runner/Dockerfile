# Reference Python Shiny runner for ShinyHub Fargate tasks.
# Based on the uv image that is also used by the Docker runtime's Python tier
# (runtime.docker.images.python), keeping the dep-prep semantics in sync.
# See entrypoint.sh for the fetch-verify-prep-exec sequence.
# R runner is a fast-follow (out of scope for this initial image).
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    unzip \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

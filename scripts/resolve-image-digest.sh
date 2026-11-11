#!/usr/bin/env bash
# resolve-image-digest.sh
#
# Appends digest=<manifest digest> of ghcr.io/rvben/shinyhub:<version> to
# $GITHUB_OUTPUT, where <version> is $GITHUB_REF_NAME without its leading v.
# Needs docker buildx and registry access.
set -e
ref="ghcr.io/rvben/shinyhub:${GITHUB_REF_NAME#v}"
digest=$(docker buildx imagetools inspect "$ref" --format '{{.Manifest.Digest}}')
echo "digest=$digest" >> "$GITHUB_OUTPUT"

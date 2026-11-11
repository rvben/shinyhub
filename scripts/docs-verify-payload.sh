#!/usr/bin/env bash
# docs-verify-payload.sh
#
# Checks the built site/ directory for the files the public documentation must
# carry and for internal material or stray standalone HTML it must not.
set -e
test -f site/index.html
test -f site/getting-started/installation/index.html
test -f site/demo/index.html
test -f site/images/favicon.png
test -f site/search.json
test -f site/stylesheets/extra.css
test ! -e site/superpowers
test ! -e site/reports
unexpected_html="$(find site -mindepth 1 -maxdepth 1 -type f -name '*.html' ! -name index.html ! -name 404.html -print)"
if [[ -n "$unexpected_html" ]]; then
  echo "Unexpected standalone HTML in the public documentation payload:" >&2
  echo "$unexpected_html" >&2
  exit 1
fi

# Documentation Pages Functions

## Documentation question search

The header's Ask docs dialog offers AI Search and the existing keyword engine.
Searches are submitted explicitly; typing alone never contacts the provider.
`api/docs-search.js` forwards a bounded question to the site's public Cloudflare
AI Search retrieval endpoint. Generation and query rewriting are disabled. The
provider endpoint has a 60 requests/minute limit per instance. Search responses
are no-store, and redirects, cross-origin browser requests, oversized bodies,
timeouts and provider errors are rejected. Keyword search remains available.

`DOCS_SEARCH_ANALYTICS` binds to the shared `docs_search_trial` Analytics Engine
dataset. Production canonical-host traffic records only fixed event categories
and numeric aggregates. Preview traffic is excluded. No question text, result
URLs, IP addresses, referrers, user agents, cookies or visitor identifiers are
written to the dataset. Analytics failures never break search.

Each data point uses `index1` for the site, `blob1` for the event (`open`, `click`,
`keyword`, `attempt`, or `complete`), `blob2` for outcome/method, and `double1..4`
for count, latency in milliseconds, raw result count and clicked rank. Completion
outcomes are `success`, `empty`, `limited`, `timeout`, or `error`. `attempt` counts
upstream requests; events do not dispatch provider queries. Use
`sum(_sample_interval)` for request/event totals and weighted latency sums when
querying aggregates. These are usage estimates, not billing records or unique
visitor counts. Direct requests to the public provider endpoint are outside the
website dataset; reconcile Cloudflare's billable usage before estimating a bill.

Run `node scripts/docs_search_test.mjs` to check request limits, privacy, preview
exclusion, provider errors and verified result links.

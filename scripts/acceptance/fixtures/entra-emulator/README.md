# Entra emulator compatibility fixture

This fixture builds [calvinchengx/entra-emulator](https://github.com/calvinchengx/entra-emulator)
for ShinyHub's native OIDC acceptance test. It runs only on loopback with synthetic
identities and fresh databases. It is not a production identity provider.

`source.json` pins the public upstream commit, the SHA-256 of
`compatibility.patch`, and the expected Git tree after applying that patch. The
runner checks all three before building. No private fork or prebuilt binary is
required. The patch raises Go directives; dependency checksums in `go.sum`
remain those of the pinned upstream source.

The temporary patch incorporates upstream PRs
[#217](https://github.com/calvinchengx/entra-emulator/pull/217) (delegated app
roles), [#263](https://github.com/calvinchengx/entra-emulator/pull/263) (Go security
minimum), and [#264](https://github.com/calvinchengx/entra-emulator/pull/264)
(correlated front-channel logout). The overlapping app DTO and ID-token changes
are combined, preserving both roles and session IDs. The patch also extends the
upstream correlation test to verify both claims in signed grants and refreshes.
Unrelated dependency/workflow changes from the app-role branch are excluded.
These are test-fixture changes; the original contributor PR remains intact.

The patched tree has no upstream commit of its own, so its binary is built with
`-buildvcs=false`. Reports identify it by the baseline commit, patch digest,
expected source tree, Go build information, and executable digest. They do not
claim it is an unmodified upstream release. ShinyHub's own executable reports its
Git revision and whether the working tree was dirty.

When upstream includes these features, pin a reviewed upstream commit, remove
the obsolete patch, update the expected tree, and rerun the entire acceptance
suite. Do not silently fall back to another provider or weaken the role policy.
Update the fixture's Go directives, `source.json` toolchain and CI's Go version
together when changing the pinned toolchain.

The upstream source and patch are covered by Apache-2.0. The upstream `LICENSE`
and `NOTICE` are retained here. The patch contains modifications to that source.

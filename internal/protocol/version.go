// Package protocol owns the version of the public CLI/server API contract.
// Increment CurrentVersion only for a compatibility break, not for additive
// fields or capability-gated features. Every value must have a corresponding
// testdata/server-info-vN.json contract fixture.
package protocol

const CurrentVersion = 1

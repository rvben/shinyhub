package deploy

import (
	"path/filepath"
	"strings"

	"github.com/rvben/shinyhub/internal/data"
)

// UsesPersistentData reports whether an app relies on its persistent data dir.
// It is the app-side signal for the durable-data guard: an app uses persistent
// data if EITHER its command template references the {data_dir} placeholder
// (authored to read/write its data dir) OR data has already been pushed for it
// (appDataDir/slug is non-empty, excluding the upload temp dir).
//
// An empty appDataDir skips the on-disk check, mirroring CheckAppQuota.
func UsesPersistentData(command []string, appDataDir, slug string) (bool, error) {
	for _, arg := range command {
		if strings.Contains(arg, placeholderDataDir) {
			return true, nil
		}
	}
	if appDataDir == "" {
		return false, nil
	}
	used, err := data.DirSize(filepath.Join(appDataDir, slug))
	if err != nil {
		return false, err
	}
	return used > 0, nil
}

// EphemeralDataBlockedTier decides whether a deploy must be blocked by the
// durable-data guard. usesData is the app-side signal (UsesPersistentData); ack
// is the operator's explicit acceptance of ephemeral storage; tierDurable
// reports whether a tier's app-data survives restart and is shared across
// replicas. It returns the first tier whose storage is not durable and true, or
// "" and false when the deploy is allowed. Fail-closed: any non-durable tier in
// a mixed placement blocks the whole deploy rather than silently dropping the
// replicas on the ephemeral tier.
func EphemeralDataBlockedTier(usesData, ack bool, tiers []string, tierDurable func(tier string) bool) (string, bool) {
	if ack || !usesData {
		return "", false
	}
	for _, t := range tiers {
		if !tierDurable(t) {
			return t, true
		}
	}
	return "", false
}

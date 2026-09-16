package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// EntitlementFingerprint binds an upgraded session to the exact effective set
// forwarded to its app. Empty sets have a nonempty fingerprint; "" means the
// connection predates entitlement resolution. Names are not logged here.
func EntitlementFingerprint(names []string) string {
	names = append([]string{}, names...)
	slices.Sort(names)
	names = slices.Compact(names)
	b, _ := json.Marshal(names)
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

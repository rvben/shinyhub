package api

import (
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
)

const (
	hdrIfContentDigest = "X-Shinyhub-If-Content-Digest"
	hdrIfManagedBy     = "X-Shinyhub-If-Managed-By"
)

// checkAppPreconditions returns true and writes a 409 if any present
// precondition header does not match the app's current state. Absent headers
// impose no condition (backward compatible). An If-Managed-By header present
// with an empty value asserts the app is currently unmanaged (NULL).
func checkAppPreconditions(w http.ResponseWriter, r *http.Request, app *db.App) (conflict bool) {
	// An empty value is treated as absent: a real content digest is always
	// non-empty, so there is no "assert empty digest" case to honor (unlike
	// If-Managed-By, where empty means "assert currently unmanaged").
	if want := r.Header.Get(hdrIfContentDigest); want != "" {
		if app.ContentDigest != want {
			writeError(w, http.StatusConflict,
				"precondition failed: content_digest changed (re-run plan)")
			return true
		}
	}
	if _, present := r.Header[hdrIfManagedBy]; present {
		want := r.Header.Get(hdrIfManagedBy)
		cur := ""
		if app.ManagedBy != nil {
			cur = *app.ManagedBy
		}
		if cur != want {
			writeError(w, http.StatusConflict,
				"precondition failed: managed_by changed (re-run plan)")
			return true
		}
	}
	return false
}

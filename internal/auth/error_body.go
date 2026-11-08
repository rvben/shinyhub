package auth

import (
	"encoding/json"
	"net/http"
)

// writeAPIError answers with the JSON error body every /api client parses,
// the same shape the API handlers write.
func writeAPIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{msg})
}

// writeRequestError answers a middleware that fronts every path: a page load
// gets the plain text a browser shows as is, any other caller gets JSON.
func writeRequestError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if requestWantsHTML(r) {
		http.Error(w, msg, status)
		return
	}
	writeAPIError(w, status, msg)
}

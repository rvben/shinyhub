package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

// getPaginatedList issues a GET that returns the standard list envelope
// {items,total,limit,offset}, forwarding the CLI's --limit/--offset so the
// server paginates. It returns the page items plus the server's full-set total
// (which drives the "showing X of Y" hint). action names the operation for
// error messages (e.g. "list deployments").
func getPaginatedList(cfg *cliConfig, action, path string, f *listFlags) ([]map[string]any, int, error) {
	items, total, _, err := getPaginatedListWithExtra(cfg, action, path, f)
	return items, total, err
}

// getPaginatedListWithExtra is getPaginatedList that also returns the envelope's
// command-specific keys (everything except items/total/limit/offset), for
// commands like data ls that carry quota_mb/used_bytes alongside the page.
func getPaginatedListWithExtra(cfg *cliConfig, action, path string, f *listFlags) ([]map[string]any, int, map[string]any, error) {
	// Reject invalid pagination before issuing the request, mirroring the
	// client-side sliceAndProject checks so switching to server-side pagination
	// does not silently drop input validation.
	if f.offset < 0 {
		return nil, 0, nil, validationErr("--offset must be >= 0", "")
	}
	if f.limit < 0 {
		return nil, 0, nil, validationErr("--limit must be >= 0", "")
	}
	res, err := doAPI(cfg, apiCall{Method: "GET", Path: path + paginationQuery(path, f), Op: action})
	if err != nil {
		return nil, 0, nil, err
	}
	out := res.Body
	var env struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, 0, nil, protocolFailure(cfg, &protocolError{op: "decode response", err: err})
	}
	// Capture command-specific envelope keys (all except the standard four).
	var all map[string]any
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, 0, nil, protocolFailure(cfg, &protocolError{op: "decode response", err: err})
	}
	extra := map[string]any{}
	for k, v := range all {
		switch k {
		case "items", "total", "limit", "offset":
		default:
			extra[k] = v
		}
	}
	return env.Items, env.Total, extra, nil
}

// paginationQuery renders the ?limit=&offset= suffix for the given path,
// choosing ? or & depending on whether path already carries a query string.
// Zero values are omitted (the server treats absent as "no bound").
func paginationQuery(path string, f *listFlags) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var b strings.Builder
	if f.limit > 0 {
		fmt.Fprintf(&b, "%slimit=%d", sep, f.limit)
		sep = "&"
	}
	if f.offset > 0 {
		fmt.Fprintf(&b, "%soffset=%d", sep, f.offset)
	}
	return b.String()
}

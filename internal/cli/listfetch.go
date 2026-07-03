package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// getPaginatedList issues a GET that returns the standard list envelope
// {items,total,limit,offset}, forwarding the CLI's --limit/--offset so the
// server paginates. It returns the page items plus the server's full-set total
// (which drives the "showing X of Y" hint). action names the operation for
// error messages (e.g. "list deployments").
func getPaginatedList(cfg *cliConfig, action, path string, f *listFlags) ([]map[string]any, int, error) {
	url := cfg.Host + path + paginationQuery(path, f)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, 0, httpError(cfg.Token, action, resp, out)
	}
	var env struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}
	return env.Items, env.Total, nil
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

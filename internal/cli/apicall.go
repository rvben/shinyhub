package cli

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// apiCall describes one buffered, authenticated request to the server.
type apiCall struct {
	Method string
	// Path is appended to the configured host and carries its own query string.
	Path string
	// Body is sent as JSON. A nil Body sends no payload and no Content-Type.
	Body []byte
	// Op names the action for the error message (e.g. "stop app").
	Op string
	// Accept lists the failure statuses (>= 400) the caller interprets itself,
	// such as a 404 that means "already absent". They come back as a result
	// instead of an error.
	Accept []int
}

// apiResult is a response whose body has been read in full and closed.
type apiResult struct {
	Status int
	Header http.Header
	Body   []byte
}

// doAPI sends c with the saved credential and returns the buffered response.
// A status of 400 or above becomes the httpError for c.Op unless c.Accept
// names it. It is for ordinary request/response calls only: streams, multipart
// uploads and retried requests keep their own handling.
func doAPI(cfg *cliConfig, c apiCall) (*apiResult, error) {
	var payload io.Reader
	if c.Body != nil {
		payload = bytes.NewReader(c.Body)
	}
	req, err := http.NewRequest(c.Method, cfg.Host+c.Path, payload)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	if c.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 && !slices.Contains(c.Accept, resp.StatusCode) {
		return nil, httpError(cfg.Token, c.Op, resp, body)
	}
	return &apiResult{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

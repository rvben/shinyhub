package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

// appRelativeURL strips the first two decoded path segments from the escaped
// path as well. Trimming a literal slug from RawPath corrupts escaped slugs.
func appRelativeURL(u *url.URL, slug string, target *url.URL) {
	prefix := "/app/" + slug
	path := strings.TrimPrefix(u.Path, prefix)
	if path == "" {
		path = "/"
	}
	escaped := u.EscapedPath()
	// Count decoded bytes rather than literal separators, because even the
	// mount's separators can be percent-encoded in a valid request URL.
	offset := 0
	for range len(prefix) {
		if escaped[offset] == '%' {
			offset += 3
		} else {
			offset++
		}
	}
	raw := escaped[offset:]
	if raw == "" {
		raw = "/"
	} else if len(raw) >= 3 && strings.EqualFold(raw[:3], "%2f") {
		// HTTP request targets need a literal leading slash. This is the
		// mount boundary, not an encoded slash inside an app path segment.
		raw = "/" + raw[3:]
	}
	u.Path = singleJoiningSlash(strings.TrimRight(target.Path, "/"), path)
	u.RawPath = singleJoiningSlash(strings.TrimRight(target.EscapedPath(), "/"), raw)
}

// rewriteAppRedirect keeps backend-local redirects on the public app mount.
// External redirects remain external; relative references resolve against the
// upstream document before the backend mount is replaced by the public mount.
func rewriteAppRedirect(slug string, target *url.URL) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || resp.Request == nil {
			return nil
		}
		raw := resp.Header.Get("Location")
		if raw == "" {
			return nil
		}
		ref, err := url.Parse(raw)
		if err != nil || ref.User != nil || ref.Opaque != "" {
			return nil
		}
		if ref.Host != "" && !strings.EqualFold(ref.Host, target.Host) ||
			ref.Scheme != "" && ref.Scheme != "http" && ref.Scheme != "https" {
			return nil
		}
		// Query-only and fragment-only references already resolve correctly
		// against the public document and must retain their exact spelling.
		if ref.Path == "" && ref.Host == "" && ref.Scheme == "" {
			return nil
		}
		// Worker tunnel prefixes are transport details, not part of the
		// document path the backend uses for parent-relative redirects.
		base := *resp.Request.URL
		backendPath := strings.TrimRight(target.Path, "/")
		if backendPath != "" && (base.Path == backendPath || strings.HasPrefix(base.Path, backendPath+"/")) {
			escapedBase := strings.TrimPrefix(base.EscapedPath(), strings.TrimRight(target.EscapedPath(), "/"))
			base.Path = strings.TrimPrefix(base.Path, backendPath)
			base.RawPath = escapedBase
		}
		resolved := base.ResolveReference(ref)
		path := resolved.EscapedPath()
		mount := "/app/" + slug
		if resolved.Path != mount && !strings.HasPrefix(resolved.Path, mount+"/") {
			backendMount := strings.TrimRight(target.EscapedPath(), "/")
			if backendMount != "" && (path == backendMount || strings.HasPrefix(path, backendMount+"/")) {
				path = strings.TrimPrefix(path, backendMount)
			}
			path = mount + "/" + strings.TrimPrefix(path, "/")
		}
		location := path
		if resolved.RawQuery != "" || resolved.ForceQuery {
			location += "?" + resolved.RawQuery
		}
		if resolved.Fragment != "" {
			location += "#" + resolved.EscapedFragment()
		}
		resp.Header.Set("Location", location)
		return nil
	}
}

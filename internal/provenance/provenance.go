// Package provenance defines the small, provider-neutral deployment source
// contract shared by the CLI, API, database, and dashboard.
package provenance

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const MaxEncodedBytes = 4096

var (
	runIDPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

type Link struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url,omitempty"`
}

type Revision struct {
	SHA string `json:"sha,omitempty"`
	Ref string `json:"ref,omitempty"`
	URL string `json:"url,omitempty"`
}

type Metadata struct {
	Provider string    `json:"provider,omitempty"`
	Source   *Link     `json:"source,omitempty"`
	Job      *Link     `json:"job,omitempty"`
	Revision *Revision `json:"revision,omitempty"`
	Change   *Link     `json:"change,omitempty"`
}

func ValidRunID(id string) bool { return runIDPattern.MatchString(id) }

// Validate rejects unsafe or misleading metadata before it becomes a durable,
// clickable attribution. Links are deliberately HTTPS-only and may not carry
// credentials or fragments.
func (m Metadata) Validate() error {
	if m.Provider != "" && !providerPattern.MatchString(m.Provider) {
		return fmt.Errorf("provider must match [a-z0-9][a-z0-9_-]{0,31}")
	}
	for name, link := range map[string]*Link{"source": m.Source, "job": m.Job, "change": m.Change} {
		if link == nil {
			continue
		}
		if err := validateText(name+" label", link.Label, 160); err != nil {
			return err
		}
		if err := validateURL(name+" URL", link.URL); err != nil {
			return err
		}
		if link.Label == "" && link.URL == "" {
			return fmt.Errorf("%s must contain a label or URL", name)
		}
	}
	if m.Revision != nil {
		if err := validateText("revision SHA", m.Revision.SHA, 128); err != nil {
			return err
		}
		if err := validateText("revision ref", m.Revision.Ref, 256); err != nil {
			return err
		}
		if err := validateURL("revision URL", m.Revision.URL); err != nil {
			return err
		}
		if m.Revision.SHA == "" && m.Revision.Ref == "" && m.Revision.URL == "" {
			return fmt.Errorf("revision must contain a SHA, ref, or URL")
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode provenance: %w", err)
	}
	if len(b) > MaxEncodedBytes {
		return fmt.Errorf("provenance exceeds %d bytes", MaxEncodedBytes)
	}
	return nil
}

func validateText(name, value string, max int) error {
	if len(value) > max {
		return fmt.Errorf("%s exceeds %d characters", name, max)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s contains control characters", name)
	}
	return nil
}

func validateURL(name, raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > 2048 {
		return fmt.Errorf("%s exceeds 2048 characters", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%s must be an HTTPS URL without credentials or a fragment", name)
	}
	return nil
}

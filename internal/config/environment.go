package config

import (
	"crypto/sha256"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// EnvironmentConfig is optional deployment identity. Absence means unmarked,
// not proof that an instance is production.
type EnvironmentConfig struct {
	Label         string `yaml:"label" json:"label"`
	Color         string `yaml:"color" json:"color"`
	Message       string `yaml:"message" json:"message,omitempty"`
	ProductionURL string `yaml:"production_url" json:"production_url,omitempty"`
}

func (e *EnvironmentConfig) LabelValue() string {
	if e == nil {
		return ""
	}
	return e.Label
}
func (e *EnvironmentConfig) Prefix() string {
	if e == nil {
		return ""
	}
	return "[" + e.Label + "] "
}
func (e *EnvironmentConfig) EffectiveColor() string {
	if e == nil || e.Color == "" {
		return "#f5b301"
	}
	return e.Color
}

// IconKey changes even when two environment labels share a color.
func (e *EnvironmentConfig) IconKey() string {
	sum := sha256.Sum256([]byte(e.LabelValue() + e.EffectiveColor()))
	return fmt.Sprintf("%x", sum[:8])
}

func validateEnvironment(e *EnvironmentConfig) error {
	if e == nil {
		return nil
	}
	if strings.IndexFunc(e.Label, unicode.IsControl) >= 0 {
		return fmt.Errorf("branding.environment.label must not contain control characters")
	}
	e.Label = strings.Join(strings.Fields(e.Label), " ")
	if e.Label == "" || utf8.RuneCountInString(e.Label) > 32 || strings.IndexFunc(e.Label, unicode.IsControl) >= 0 {
		return fmt.Errorf("branding.environment.label must contain 1-32 characters without control characters")
	}
	if utf8.RuneCountInString(e.Message) > 200 || strings.IndexFunc(e.Message, unicode.IsControl) >= 0 {
		return fmt.Errorf("branding.environment.message must contain at most 200 characters without control characters")
	}
	if e.Color == "" {
		e.Color = e.EffectiveColor()
	}
	if !hexColorRe.MatchString(e.Color) {
		return fmt.Errorf("branding.environment.color must be a CSS hex color")
	}
	if e.ProductionURL != "" {
		u, err := url.Parse(e.ProductionURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("branding.environment.production_url must be an HTTP(S) origin without credentials, query, fragment or subpath")
		}
		e.ProductionURL = u.Scheme + "://" + u.Host
	}
	return nil
}

// RGB and TextColor use the same validated color for icons and readable copy.
func (e *EnvironmentConfig) RGB() (uint8, uint8, uint8) {
	h := strings.TrimPrefix(e.EffectiveColor(), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	n, _ := strconv.ParseUint(h, 16, 24)
	return uint8(n >> 16), uint8(n >> 8), uint8(n)
}
func (e *EnvironmentConfig) TextColor() string {
	r, g, b := e.RGB()
	linear := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= .04045 {
			return x / 12.92
		}
		return math.Pow((x+.055)/1.055, 2.4)
	}
	l := .2126*linear(r) + .7152*linear(g) + .0722*linear(b)
	if (l+.05)/.05 >= 1.05/(l+.05) {
		return "#000000"
	}
	return "#ffffff"
}

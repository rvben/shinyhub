// Package availability resolves anonymous monitoring exposure independently of app access.
package availability

import "slices"

// Exposure is an authenticated explanation of stored intent and effective policy.
// It is never included in anonymous monitoring responses.
type Exposure struct {
	AppAllowed       bool   `json:"app_allowed"`
	Intent           string `json:"intent"`
	Exposed          bool   `json:"exposed"`
	Source           string `json:"source"`
	PolicySuppressed bool   `json:"policy_suppressed"`
}

func ValidIntent(v string) bool { return v == "inherit" || v == "off" || v == "public" }

// AppIntents returns the stored intents eligible under app-derived policy.
// The central allowlist is independent and always adds to this selection.
func AppIntents(allow, defaultPublic bool) []string {
	if !allow {
		return nil
	}
	if defaultPublic {
		return []string{"public", "inherit"}
	}
	return []string{"public"}
}

// Resolve gives the operator's allowlist precedence over app settings. Unknown
// intent (including records from servers predating this feature) fails closed.
func Resolve(intent string, allow, defaultPublic, allowlisted bool) Exposure {
	e := Exposure{Intent: intent, Source: "none", AppAllowed: allow}
	wants := intent == "public" || (intent == "inherit" && defaultPublic)
	if allowlisted {
		e.Exposed = true
		e.Source = "allowlist"
		return e
	}
	e.PolicySuppressed = wants && !allow
	e.Exposed = slices.Contains(AppIntents(allow, defaultPublic), intent)
	if !e.PolicySuppressed {
		if intent == "inherit" {
			e.Source = "default"
		} else if intent == "off" || intent == "public" {
			e.Source = "app"
		}
	}
	return e
}

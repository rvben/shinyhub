package config

import (
	"fmt"
	"time"
)

// BrowserSessionTTL is the validity of a browser token between renewals.
func (a *AuthConfig) BrowserSessionTTL() time.Duration {
	if a.SessionTTL == nil {
		return time.Hour
	}
	return *a.SessionTTL
}

// BrowserSessionMaxAge is the strict lifetime from the original login.
func (a *AuthConfig) BrowserSessionMaxAge() time.Duration {
	if a.SessionMaxAge == nil {
		return 12 * time.Hour
	}
	return *a.SessionMaxAge
}

func (a *AuthConfig) validateSessionLifetimes() error {
	for _, setting := range []struct {
		name  string
		value time.Duration
	}{
		{"session_ttl", a.BrowserSessionTTL()},
		{"session_max_age", a.BrowserSessionMaxAge()},
	} {
		if setting.value < time.Minute || setting.value > 30*24*time.Hour {
			return fmt.Errorf("auth.%s must be between 1m and 720h", setting.name)
		}
	}
	if a.BrowserSessionTTL() > a.BrowserSessionMaxAge() {
		return fmt.Errorf("auth.session_ttl must not exceed auth.session_max_age")
	}
	return nil
}

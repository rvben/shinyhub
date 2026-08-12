package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

const credentialExpiryWarningWindow = 14 * 24 * time.Hour

// remoteCredential mirrors the safe, non-secret credential metadata returned
// by /api/auth/me. Older servers omit it, which the CLI treats as unknown
// rather than inventing lifecycle guarantees.
type remoteCredential struct {
	Type       string     `json:"type"`
	ID         int64      `json:"id,omitempty"`
	Name       string     `json:"name,omitempty"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

type credentialLifecycle struct {
	Type             string     `json:"type"`
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	CreatedAt        *time.Time `json:"created_at"`
	LastUsedAt       *time.Time `json:"last_used_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	Status           string     `json:"status"`
	SecondsRemaining *int64     `json:"seconds_remaining"`
}

func decodeRemoteIdentity(body []byte) (remoteIdentity, error) {
	var payload struct {
		User struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
		CanCreateApps *bool             `json:"can_create_apps"`
		Credential    *remoteCredential `json:"credential,omitempty"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return remoteIdentity{}, err
	}
	if payload.User.Username == "" {
		return remoteIdentity{}, fmt.Errorf("authentication response did not identify a user")
	}
	identity := remoteIdentity{
		Username: payload.User.Username, Role: payload.User.Role,
		Credential: payload.Credential,
	}
	if payload.CanCreateApps != nil {
		identity.CanCreateApps = *payload.CanCreateApps
		identity.CanCreateAppsKnown = true
	}
	return identity, nil
}

func credentialLifecycleAt(credential *remoteCredential, now time.Time) credentialLifecycle {
	if credential == nil || credential.Type == "" {
		return credentialLifecycle{Type: "unknown", Status: "unknown"}
	}
	report := credentialLifecycle{
		Type: credential.Type, ID: credential.ID, Name: credential.Name,
		CreatedAt: credential.CreatedAt, LastUsedAt: credential.LastUsedAt,
		ExpiresAt: credential.ExpiresAt,
	}
	if credential.ExpiresAt == nil {
		report.Status = "non_expiring"
		return report
	}
	remaining := credential.ExpiresAt.Sub(now)
	seconds := int64(math.Ceil(remaining.Seconds()))
	report.SecondsRemaining = &seconds
	switch {
	case remaining <= 0:
		report.Status = "expired"
	case remaining <= credentialExpiryWarningWindow:
		report.Status = "expiring"
	default:
		report.Status = "healthy"
	}
	return report
}

func credentialTypeLabel(kind string) string {
	switch kind {
	case "api_key":
		return "API key"
	case "session_token":
		return "session token"
	case "browser_session":
		return "browser session"
	case "deploy_token":
		return "deploy token"
	default:
		return strings.ReplaceAll(kind, "_", " ")
	}
}

func credentialSummary(report credentialLifecycle) string {
	if report.Status == "unknown" {
		return "not reported by this server"
	}
	label := credentialTypeLabel(report.Type)
	if report.Name != "" {
		label += " “" + report.Name + "”"
	}
	return label
}

func credentialExpirySummary(report credentialLifecycle, now time.Time) string {
	if report.ExpiresAt == nil {
		if report.Status == "unknown" {
			return "unknown"
		}
		return "does not expire"
	}
	stamp := report.ExpiresAt.UTC().Format(time.RFC3339)
	remaining := report.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return stamp + " (expired)"
	}
	return stamp + " (" + remainingSummary(remaining) + " remaining)"
}

func remainingSummary(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d days", int(math.Ceil(d.Hours()/24)))
	}
	if d >= 2*time.Hour {
		return fmt.Sprintf("%d hours", int(math.Ceil(d.Hours())))
	}
	minutes := int(math.Ceil(d.Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("%d minutes", minutes)
}

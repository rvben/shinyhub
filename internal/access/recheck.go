package access

import (
	"errors"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// Principal is the identity a live upgraded (WebSocket) connection was admitted
// under, captured at the moment of the upgrade.
//
// An HTTP request passes through Middleware once. A WebSocket upgrade is one
// such request, and the connection it produces then outlives every subsequent
// authorization decision - typically for hours, and by design: the whole point
// of a Shiny session is that it persists. Revoking a user therefore had no
// effect on the session they already had open. This struct is what makes that
// connection findable again.
type Principal struct {
	// Slug is the app the connection is bound to.
	Slug                   string
	EntitlementAppID       int64
	EntitlementFingerprint string
	// UserID is 0 for a connection admitted anonymously to a public app.
	UserID int64
	// Role is the user's global role at the upgrade. It is compared against the
	// live role because the app was told this role (X-Shinyhub-Role and the
	// role claim of the identity token) and is entitled to act on it.
	Role string
	// SessionEpoch is users.token_epoch at the upgrade. An admin revoking a
	// user's sessions, or a password change, bumps it.
	SessionEpoch int64
	// JTI identifies the session token the connection was admitted with, so a
	// logout that revokes exactly that token also closes this connection. Empty
	// when the identity did not come from a ShinyHub session JWT.
	JTI string
	// Support fields preserve the separate actor/subject security boundary for
	// long-lived upgraded connections.
	ActorID           int64
	ActorRole         string
	ActorSessionEpoch int64
	SupportAppID      int64
	RoutedAppID       int64
	SupportExpiresAt  time.Time
}

// Recheck re-decides whether an already-open upgraded connection may stay open,
// reading the live database rather than anything cached on the connection.
//
// It reports revoked=true when access has lapsed, or a nonempty entitlement
// snapshot can no longer be verified. Other lookup failures return an error and
// preserve the existing connection's availability behavior.
//
// The checks below fall into two groups. The first re-runs admission through
// decide, so anything that would stop the user opening this connection now also
// closes the one they have. The second compares the identity ShinyHub bound to
// the connection against the live user, because that identity was forwarded to
// the app at the upgrade and the app's own authorization rests on it. Both
// matter: a demoted admin might still be admitted to a shared app while the app
// is still being told they are an admin.
func Recheck(st store, lookup auth.UserLookup, revoked auth.RevocationChecker, p Principal) (bool, string, error) {
	if !p.SupportExpiresAt.IsZero() && !time.Now().Before(p.SupportExpiresAt) {
		return true, "support session expired", nil
	}
	entRevoked, reason, entErr := RecheckEntitlements(st, p)
	if entRevoked {
		return true, reason, nil
	}
	// An unavailable entitlement snapshot must not mask a definitive logout,
	// epoch bump, role change, or admission revocation from the other checks.
	isRevoked, reason, err := recheckAccessAndIdentity(st, lookup, revoked, p)
	if isRevoked {
		return true, reason, nil
	}
	return false, "", errors.Join(entErr, err)
}

func recheckAccessAndIdentity(st store, lookup auth.UserLookup, revoked auth.RevocationChecker, p Principal) (bool, string, error) {
	if p.ActorID > 0 {
		if p.SupportAppID <= 0 || p.RoutedAppID <= 0 || p.RoutedAppID != p.SupportAppID {
			return true, "support session routed app replaced", nil
		}
		actor, aerr := lookup(p.ActorID)
		if aerr != nil {
			if errors.Is(aerr, db.ErrNotFound) {
				return true, "supporting administrator deleted", nil
			}
			return false, "", aerr
		}
		if actor == nil || actor.IsServiceAccount() || actor.Role != string(auth.RoleAdmin) {
			return true, "supporting administrator is no longer an admin", nil
		}
		if actor.Role != p.ActorRole || actor.TokenEpoch != p.ActorSessionEpoch {
			return true, "administrator sessions revoked", nil
		}
	}
	app, err := st.GetAppBySlug(p.Slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			if p.SupportAppID > 0 {
				return true, "support session app deleted", nil
			}
			// The request path passes an unknown slug straight through instead
			// of denying it, so a vanished app row is not a lapse of THIS
			// principal's access. Deleting an app stops its processes, which
			// drops the connection anyway.
			return false, "", nil
		}
		return false, "", err
	}
	if p.SupportAppID > 0 && app.ID != p.SupportAppID {
		return true, "support session app replaced", nil
	}
	if p.EntitlementAppID > 0 && app.ID != p.EntitlementAppID {
		return true, "entitlement app replaced", nil
	}

	// An anonymous connection carries no identity to revoke. The only thing
	// that can lapse is the app's own openness.
	if p.UserID <= 0 {
		if app.Access != "public" {
			return true, "app is no longer public", nil
		}
		return false, "", nil
	}

	user, err := lookup(p.UserID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return true, "user deleted", nil
		}
		return false, "", err
	}
	if user == nil {
		return true, "user deleted", nil
	}

	// Identity checks come before the admission re-run because they hold even
	// on a public app, where admission alone would never deny anyone. A public
	// app still receives the user's forwarded identity, and that identity is
	// exactly what a revocation invalidates.
	if user.TokenEpoch != p.SessionEpoch {
		return true, "sessions revoked", nil
	}
	if user.Role != p.Role {
		return true, "role changed", nil
	}
	if p.JTI != "" && revoked != nil {
		gone, rerr := revoked(p.JTI)
		if rerr != nil {
			return false, "", rerr
		}
		if gone {
			return true, "session signed out", nil
		}
	}

	status, err := decide(st, app, user)
	if err != nil {
		return false, "", err
	}
	if status != http.StatusOK {
		return true, "access to the app was removed", nil
	}
	return false, "", nil
}

// RecheckEntitlements verifies only the app-business permission snapshot. It
// remains active when the optional general session sweep is disabled.
func RecheckEntitlements(st store, p Principal) (bool, string, error) {
	if p.EntitlementFingerprint != "" {
		source, ok := st.(entitlementSource)
		var names []string
		var err error
		if !ok {
			err = errors.New("entitlement source unavailable")
		} else {
			names, err = source.AppEntitlementsForUser(p.EntitlementAppID, p.UserID)
		}
		if err != nil {
			// A connection holding business privileges must not retain them
			// indefinitely when the database cannot confirm they remain valid.
			if p.EntitlementFingerprint != auth.EntitlementFingerprint(nil) {
				return true, "app permissions could not be verified", nil
			}
			return false, "", err
		}
		if auth.EntitlementFingerprint(names) != p.EntitlementFingerprint {
			return true, "app entitlements changed", nil
		}
	}
	return false, "", nil
}

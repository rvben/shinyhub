package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestSupportTokenAuthenticatesOnlyThroughDedicatedAppCookie(t *testing.T) {
	secret := "01234567890123456789012345678901"
	actor := &auth.ContextUser{ID: 1, Username: "admin", Role: "admin", TokenEpoch: 4}
	subject := &auth.ContextUser{ID: 2, Username: "alice", Role: "developer", TokenEpoch: 7}
	issued := *subject
	issued.SupportSession = &auth.SupportSessionContext{
		ID: "support-id", ActorID: actor.ID, ActorUsername: actor.Username,
		ActorTokenEpoch: actor.TokenEpoch, AppID: 42, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute),
	}
	token, _, err := auth.IssueSessionTokenWithInfo(&issued, secret)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(id int64) (*auth.ContextUser, error) {
		switch id {
		case actor.ID:
			copy := *actor
			return &copy, nil
		case subject.ID:
			copy := *subject
			return &copy, nil
		default:
			return nil, errors.New("not found")
		}
	}

	appReq := httptest.NewRequest(http.MethodGet, "https://apps.example.com/app/sales/", nil)
	appReq.AddCookie(&http.Cookie{Name: auth.SupportSessionCookieName, Value: token})
	got, _, err := auth.AuthenticateSupportSession(appReq, secret, lookup, nil)
	if err != nil || got == nil || got.ID != subject.ID || got.SupportSession == nil || got.SupportSession.ActorID != actor.ID {
		t.Fatalf("dedicated cookie auth = %+v, err=%v", got, err)
	}

	apiReq := httptest.NewRequest(http.MethodGet, "https://hub.example.com/api/apps", nil)
	apiReq.Header.Set("Authorization", "Bearer "+token)
	if _, _, err := auth.AuthenticateRequest(apiReq, secret, nil, lookup, nil); !errors.Is(err, auth.ErrSupportSessionScope) {
		t.Fatalf("API support token error = %v, want scope error", err)
	}

	called := false
	handler := auth.BearerMiddleware(secret, nil, lookup, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, apiReq)
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("BearerMiddleware status=%d called=%v", rec.Code, called)
	}
}

func TestSupportTokenFailsWhenActorLosesAdmin(t *testing.T) {
	secret := "01234567890123456789012345678901"
	expires := time.Now().Add(15 * time.Minute)
	issued := &auth.ContextUser{
		ID: 2, Username: "alice", Role: "viewer", TokenEpoch: 1,
		SupportSession: &auth.SupportSessionContext{
			ID: "support-id", ActorID: 1, ActorUsername: "admin", ActorTokenEpoch: 2,
			AppID: 42, AppSlug: "sales", ExpiresAt: expires,
		},
	}
	token, _, err := auth.IssueSessionTokenWithInfo(issued, secret)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(id int64) (*auth.ContextUser, error) {
		if id == 1 {
			return &auth.ContextUser{ID: 1, Username: "admin", Role: "developer", TokenEpoch: 2}, nil
		}
		return &auth.ContextUser{ID: 2, Username: "alice", Role: "viewer", TokenEpoch: 1}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "https://apps.example.com/app/sales/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SupportSessionCookieName, Value: token})
	if _, _, err := auth.AuthenticateSupportSession(req, secret, lookup, nil); !errors.Is(err, auth.ErrSupportSessionInvalid) {
		t.Fatalf("error = %v, want invalid support session", err)
	}
}

func TestSupportTokenFailsWhenSubjectRoleChanges(t *testing.T) {
	secret := "01234567890123456789012345678901"
	issued := &auth.ContextUser{ID: 2, Username: "alice", Role: "viewer", TokenEpoch: 1,
		SupportSession: &auth.SupportSessionContext{ID: "support-id", ActorID: 1, ActorUsername: "admin",
			ActorTokenEpoch: 2, AppID: 42, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute)}}
	token, _, err := auth.IssueSessionTokenWithInfo(issued, secret)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(id int64) (*auth.ContextUser, error) {
		if id == 1 {
			return &auth.ContextUser{ID: 1, Username: "admin", Role: "admin", TokenEpoch: 2}, nil
		}
		return &auth.ContextUser{ID: 2, Username: "alice", Role: "developer", TokenEpoch: 1}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "https://apps.example.com/app/sales/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SupportSessionCookieName, Value: token})
	if _, _, err := auth.AuthenticateSupportSession(req, secret, lookup, nil); !errors.Is(err, auth.ErrSupportSessionInvalid) {
		t.Fatalf("role-change error = %v, want invalid support session", err)
	}
}

func TestSupportStopClaimsRemainUsableAfterLiveIdentityInvalidation(t *testing.T) {
	secret := "01234567890123456789012345678901"
	issued := &auth.ContextUser{ID: 2, Username: "alice", Role: "viewer", TokenEpoch: 1,
		SupportSession: &auth.SupportSessionContext{ID: "support-id", ActorID: 1, ActorUsername: "admin",
			ActorTokenEpoch: 2, AppID: 42, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute)}}
	token, _, err := auth.IssueSessionTokenWithInfo(issued, secret)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://apps.example.com/app/sales/.shinyhub/support-session/stop", nil)
	req.AddCookie(&http.Cookie{Name: auth.SupportSessionCookieName, Value: token})
	got, _, err := auth.AuthenticateSupportSessionForStop(req, secret)
	if err != nil || got.SupportSession == nil || got.SupportSession.ID != "support-id" || got.SupportSession.AppSlug != "sales" {
		t.Fatalf("stop claims = %+v, err=%v", got, err)
	}
}

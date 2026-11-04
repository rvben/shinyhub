package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/db"
)

type iconStateSnapshot struct {
	AppID    int64  `json:"app_id"`
	Mime     string `json:"mime"`
	Data     []byte `json:"data"`
	Emoji    string `json:"emoji"`
	Revision string `json:"revision"`
}

func newIconStateServer(t *testing.T) (*api.Server, *db.Store, string) {
	t.Helper()
	srv, store := newTestServer(t)
	token, ownerID := seedUserAndJWT(t, store, "icon-owner", "developer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "state", Name: "State", OwnerID: ownerID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	return srv, store, token
}

func readIconState(t *testing.T, srv *api.Server, token string) iconStateSnapshot {
	t.Helper()
	rec := serveIcon(srv, iconReq("GET", "/api/apps/state/icon/state", nil, "", token))
	if rec.Code != http.StatusOK {
		t.Fatalf("read icon state: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("snapshot must not be cached: %v", rec.Header())
	}
	var snapshot iconStateSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.AppID <= 0 || len(snapshot.Revision) != 64 {
		t.Fatalf("missing identity/revision: %+v", snapshot)
	}
	return snapshot
}

func writeIconState(t *testing.T, srv *api.Server, token string, state iconStateSnapshot, want int) map[string]any {
	t.Helper()
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	rec := serveIcon(srv, iconReq("PUT", "/api/apps/state/icon/state", body, "application/json", token))
	if rec.Code != want {
		t.Fatalf("write icon state: %d, want %d: %s", rec.Code, want, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAppIconStateRoundTripAndAtomicUndo(t *testing.T) {
	srv, store, token := newIconStateServer(t)
	// Manifest reconciliation can display an emoji while retaining image bytes.
	if err := store.SetAppIcon("state", "image/png", pngBytes); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppIconEmoji("state", "📈"); err != nil {
		t.Fatal(err)
	}
	before := readIconState(t, srv, token)
	if before.Mime != "image/png" || !bytes.Equal(before.Data, pngBytes) || before.Emoji != "📈" {
		t.Fatalf("snapshot lost the hidden image: %+v", before)
	}
	clear := iconStateSnapshot{AppID: before.AppID, Revision: before.Revision}
	result := writeIconState(t, srv, token, clear, http.StatusOK)
	if _, ok := result["data"]; ok {
		t.Fatal("write response should not repeat image bytes")
	}
	if result["icon_mime"] != "" || result["icon_emoji"] != "" || result["app_id"] != float64(before.AppID) {
		t.Fatalf("incorrect clear response: %v", result)
	}
	after := readIconState(t, srv, token)
	if after.Revision == before.Revision || after.Revision != result["revision"] || after.Mime != "" || after.Emoji != "" || len(after.Data) != 0 {
		t.Fatalf("clear was not reflected atomically: %+v / %v", after, result)
	}
	restore := before
	restore.Revision = after.Revision
	restored := writeIconState(t, srv, token, restore, http.StatusOK)
	got := readIconState(t, srv, token)
	if got.Revision != before.Revision || restored["revision"] != before.Revision || got.Mime != before.Mime || got.Emoji != before.Emoji || !bytes.Equal(got.Data, before.Data) {
		t.Fatalf("exact undo failed: %+v", got)
	}
	// Clearing just the emoji must still leave the exact restored image served.
	got.Emoji = ""
	writeIconState(t, srv, token, got, http.StatusOK)
	icon := serveIcon(srv, iconReq("GET", "/api/apps/state/icon", nil, "", token))
	if icon.Code != http.StatusOK || !bytes.Equal(icon.Body.Bytes(), pngBytes) {
		t.Fatalf("restored hidden image was lost: %d %s", icon.Code, icon.Body.String())
	}
	for _, action := range []string{db.AuditAppIconSet, db.AuditAppIconCleared} {
		events, err := store.ListAuditEvents(action, 20, 0)
		if err != nil || len(events) == 0 {
			t.Fatalf("missing %s audit: %v", action, err)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(events[0].Detail), &detail); err != nil || detail["restored_state"] != true {
			t.Fatalf("missing state-restore metadata: %s, %v", events[0].Detail, err)
		}
	}
}

func TestAppIconStateAuthorizesSnapshotsAndChanges(t *testing.T) {
	srv, store, ownerToken := newIconStateServer(t)
	viewerToken, _ := seedUserAndJWT(t, store, "icon-viewer", "viewer")
	snapshot := readIconState(t, srv, ownerToken)
	body, _ := json.Marshal(snapshot)
	for _, token := range []string{"", viewerToken} {
		for _, method := range []string{"GET", "PUT"} {
			rec := serveIcon(srv, iconReq(method, "/api/apps/state/icon/state", body, "application/json", token))
			want := http.StatusForbidden
			if token == "" {
				want = http.StatusUnauthorized
			}
			if rec.Code != want {
				t.Fatalf("%s authorization: %d, want %d: %s", method, rec.Code, want, rec.Body.String())
			}
		}
	}
	for _, method := range []string{"GET", "PUT"} {
		rec := serveIcon(srv, iconReq(method, "/api/apps/missing/icon/state", body, "application/json", ownerToken))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("missing app %s: %d %s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestAppIconStateRejectsStaleAndRecreatedApp(t *testing.T) {
	srv, store, token := newIconStateServer(t)
	old := readIconState(t, srv, token)
	if err := store.SetAppIconEmoji("state", "📈"); err != nil {
		t.Fatal(err)
	}
	old.Emoji = "📊"
	result := writeIconState(t, srv, token, old, http.StatusConflict)
	if !strings.Contains(result["error"].(string), "Reload") {
		t.Fatalf("conflict has no recovery advice: %v", result)
	}
	if current := readIconState(t, srv, token); current.Emoji != "📈" {
		t.Fatalf("stale restore overwrote the newer emoji: %+v", current)
	}
	owner, _ := store.GetUserByUsername("icon-owner")
	if err := store.DeleteApp("state"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "state", Name: "Replacement", OwnerID: owner.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	// Even supplying the replacement's fresh revision cannot reuse the old ID.
	replacement := readIconState(t, srv, token)
	old.Revision = replacement.Revision
	writeIconState(t, srv, token, old, http.StatusConflict)
	if current := readIconState(t, srv, token); current.AppID == old.AppID || current.Emoji != "" {
		t.Fatalf("recreated app was overwritten: %+v", current)
	}
}

func TestAppIconStateValidatesWholePayloadBeforeWriting(t *testing.T) {
	srv, _, token := newIconStateServer(t)
	before := readIconState(t, srv, token)
	valid, _ := json.Marshal(before)
	var fields map[string]any
	if err := json.Unmarshal(valid, &fields); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		key   string
		value any
		omit  bool
	}{
		{name: "missing identity", key: "app_id", value: 0},
		{name: "missing revision", key: "revision", value: ""},
		{name: "missing image state", key: "data", omit: true},
		{name: "null MIME", key: "mime", value: nil},
		{name: "missing emoji", key: "emoji", omit: true},
		{name: "null emoji", key: "emoji", value: nil},
		{name: "unknown key", key: "surprise", value: true},
		{name: "invalid base64", key: "data", value: "not base64!"},
		{name: "invalid emoji", key: "emoji", value: "not an emoji"},
		{name: "MIME without bytes", key: "mime", value: "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := make(map[string]any, len(fields))
			for key, value := range fields {
				request[key] = value
			}
			if tc.omit {
				delete(request, tc.key)
			} else {
				request[tc.key] = tc.value
			}
			body, _ := json.Marshal(request)
			rec := serveIcon(srv, iconReq("PUT", "/api/apps/state/icon/state", body, "application/json", token))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid state accepted: %d %s", rec.Code, rec.Body.String())
			}
			if after := readIconState(t, srv, token); after.Revision != before.Revision {
				t.Fatal("invalid payload changed the icon")
			}
		})
	}
	for _, suffix := range []string{` {}`, ` garbage`} {
		rec := serveIcon(srv, iconReq("PUT", "/api/apps/state/icon/state", append(append([]byte{}, valid...), []byte(suffix)...), "application/json", token))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("trailing JSON accepted: %d %s", rec.Code, rec.Body.String())
		}
	}
	for _, data := range [][]byte{[]byte("not an image"), []byte(`<svg onload="bad"><circle /></svg>`)} {
		request := before
		request.Mime, request.Data = "image/svg+xml", data
		writeIconState(t, srv, token, request, http.StatusBadRequest)
	}
}

func TestAppIconStateImageSizeBoundaryAndCanonicalMIME(t *testing.T) {
	srv, _, token := newIconStateServer(t)
	snapshot := readIconState(t, srv, token)
	request := snapshot
	request.Mime = "image/jpeg" // canonical sniffed MIME must win.
	request.Data = make([]byte, 512<<10)
	copy(request.Data, pngBytes)
	result := writeIconState(t, srv, token, request, http.StatusOK)
	if result["icon_mime"] != "image/png" {
		t.Fatalf("noncanonical MIME: %v", result)
	}
	stored := readIconState(t, srv, token)
	if len(stored.Data) != 512<<10 || stored.Mime != "image/png" {
		t.Fatalf("boundary image did not roundtrip: %d bytes, MIME %q", len(stored.Data), stored.Mime)
	}
	request.Revision = stored.Revision
	request.Data = append(request.Data, 0)
	writeIconState(t, srv, token, request, http.StatusRequestEntityTooLarge)
	// A chunked request cannot bypass the encoded-body limit with metadata.
	request.Data = nil
	request.Mime = ""
	request.Revision = strings.Repeat("x", 800<<10)
	body, _ := json.Marshal(request)
	req := iconReq("PUT", "/api/apps/state/icon/state", body, "application/json", token)
	req.ContentLength = -1
	rec := serveIcon(srv, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize encoded body accepted: %d %s", rec.Code, rec.Body.String())
	}
	if after := readIconState(t, srv, token); after.Revision != stored.Revision {
		t.Fatal("oversize request changed the saved icon")
	}
}

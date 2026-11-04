package db_test

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestAppIconStateRestoresHiddenImageAndEmojiTogether(t *testing.T) {
	store := newIconTestApp(t)
	empty, err := store.GetAppIconState("dash")
	if err != nil || empty.AppID <= 0 || empty.Mime != "" || len(empty.Data) != 0 || empty.Emoji != "" {
		t.Fatalf("empty snapshot: %+v, %v", empty, err)
	}
	pair := db.AppIconState{AppID: empty.AppID, Mime: "image/png", Data: []byte("hidden-image"), Emoji: "📊"}
	if err := store.CompareAndSwapAppIconState("dash", *empty, pair); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAppIconState("dash")
	if err != nil || got.Mime != pair.Mime || got.Emoji != pair.Emoji || !bytes.Equal(got.Data, pair.Data) {
		t.Fatalf("hidden image/emoji did not round trip: %+v, %v", got, err)
	}
	if err := store.CompareAndSwapAppIconState("dash", *got, *empty); err != nil {
		t.Fatal(err)
	}
	cleared, _ := store.GetAppIconState("dash")
	if cleared.Mime != "" || cleared.Emoji != "" || len(cleared.Data) != 0 {
		t.Fatalf("clear did not restore exact empty state: %+v", cleared)
	}
	// NULL and an empty BLOB have the same no-image meaning on both backends.
	if _, err := store.DB().Exec(`UPDATE apps SET icon_data = ? WHERE slug = ?`, []byte{}, "dash"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwapAppIconState("dash", *empty, pair); err != nil {
		t.Fatalf("empty bytes did not match the NULL snapshot: %v", err)
	}
	if _, err := store.GetAppIconState("missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing snapshot: %v", err)
	}
}

func TestAppIconStateRejectsEveryStaleComponent(t *testing.T) {
	for _, field := range []string{"app-id", "mime", "image", "emoji"} {
		t.Run(field, func(t *testing.T) {
			store := newIconTestApp(t)
			initial, _ := store.GetAppIconState("dash")
			pair := db.AppIconState{AppID: initial.AppID, Mime: "image/png", Data: []byte("original"), Emoji: "📊"}
			if err := store.CompareAndSwapAppIconState("dash", *initial, pair); err != nil {
				t.Fatal(err)
			}
			stale := pair
			switch field {
			case "app-id":
				stale.AppID++
			case "mime":
				stale.Mime = "image/jpeg"
			case "image":
				stale.Data = []byte("other bytes")
			case "emoji":
				stale.Emoji = "📈"
			}
			if err := store.CompareAndSwapAppIconState("dash", stale, db.AppIconState{AppID: stale.AppID}); !errors.Is(err, db.ErrAppIconConflict) {
				t.Fatalf("stale %s accepted: %v", field, err)
			}
			got, _ := store.GetAppIconState("dash")
			if got.Mime != pair.Mime || got.Emoji != pair.Emoji || !bytes.Equal(got.Data, pair.Data) {
				t.Fatalf("conflict changed stored icon: %+v", got)
			}
		})
	}
}

func TestAppIconStateConcurrentChangesHaveOneWinner(t *testing.T) {
	store := newIconTestApp(t)
	initial, _ := store.GetAppIconState("dash")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, emoji := range []string{"📊", "📈"} {
		wg.Add(1)
		go func(emoji string) {
			defer wg.Done()
			<-start
			results <- store.CompareAndSwapAppIconState("dash", *initial, db.AppIconState{AppID: initial.AppID, Emoji: emoji})
		}(emoji)
	}
	close(start)
	wg.Wait()
	close(results)
	winners, conflicts := 0, 0
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, db.ErrAppIconConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
}

func TestAppIconStateCannotChangeRecreatedSlug(t *testing.T) {
	store := newIconTestApp(t)
	old, _ := store.GetAppIconState("dash")
	owner, _ := store.GetUserByUsername("owner")
	if err := store.DeleteApp("dash"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "dash", Name: "Replacement", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwapAppIconState("dash", *old, db.AppIconState{AppID: old.AppID, Emoji: "📊"}); !errors.Is(err, db.ErrAppIconConflict) {
		t.Fatalf("old identity accepted for recreated slug: %v", err)
	}
	current, _ := store.GetAppIconState("dash")
	if current.AppID == old.AppID || current.Emoji != "" {
		t.Fatalf("replacement changed: %+v", current)
	}
}

package backup_test

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func sqliteTestURI(path, kind string) string {
	escaped := (&url.URL{Path: path}).EscapedPath()
	switch kind {
	case "mixed escapes":
		return "file:" + strings.ReplaceAll(escaped, "%25", "%") + "?cache=shared"
	case "localhost":
		return "file://localhost" + escaped + "?cache=shared"
	case "fragment":
		return "file:" + escaped + "#ignored"
	default:
		return "file:" + escaped + "?cache=shared"
	}
}

func TestRoundTrip_SQLiteURIPaths(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	for _, kind := range []string{"escaped", "mixed escapes", "localhost", "fragment"} {
		t.Run(kind, func(t *testing.T) {
			src := mkCfg(t)
			src.Database.DSN = filepath.Join(filepath.Dir(src.Database.DSN), "state + 100%.sqlite")
			seed(t, src)
			src.Database.DSN = sqliteTestURI(src.Database.DSN, kind)
			archive := filepath.Join(t.TempDir(), "backup.tar.gz")
			if err := backup.Create(src, "test", archive); err != nil {
				t.Fatal(err)
			}
			dst := mkCfg(t)
			path := filepath.Join(filepath.Dir(dst.Database.DSN), "state + 100%.sqlite")
			dst.Database.DSN = path
			seed(t, dst)
			old, err := db.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := old.DB().Exec("UPDATE users SET username='previous'"); err != nil {
				old.Close()
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			dst.Database.DSN = sqliteTestURI(path, kind)
			moved, err := backup.Restore(dst, archive)
			if err != nil {
				t.Fatal(err)
			}
			var preserved string
			for _, candidate := range moved {
				if strings.HasPrefix(candidate, path+".pre-restore-") {
					preserved = candidate
					break
				}
			}
			if preserved == "" {
				t.Fatalf("original database not preserved beside actual path: %v", moved)
			}
			restored, err := db.Open(dst.Database.DSN)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			var restoredName string
			if err := restored.DB().QueryRow("SELECT username FROM users").Scan(&restoredName); err != nil || restoredName != "alice" {
				t.Fatalf("restored database: username=%q err=%v", restoredName, err)
			}
			previous, err := db.Open(preserved)
			if err != nil {
				t.Fatal(err)
			}
			defer previous.Close()
			var previousName string
			if err := previous.DB().QueryRow("SELECT username FROM users").Scan(&previousName); err != nil || previousName != "previous" {
				t.Fatalf("preserved original database: username=%q err=%v", previousName, err)
			}
		})
	}
}

func TestRestore_SQLiteURIHonorsRuntimeMarker(t *testing.T) {
	for _, kind := range []string{"escaped", "mixed escapes", "localhost", "fragment"} {
		t.Run(kind, func(t *testing.T) {
			cfg := &config.Config{Database: config.DatabaseConfig{DSN: filepath.Join(t.TempDir(), "state + 100%.sqlite")}}
			withdraw, err := backup.PublishRuntimeMarker(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer withdraw()
			cfg.Database.DSN = sqliteTestURI(cfg.Database.DSN, kind)
			_, err = backup.Restore(cfg, filepath.Join(t.TempDir(), "does-not-exist.tar.gz"))
			var running *backup.ServerRunningError
			if !errors.As(err, &running) {
				t.Fatalf("URI must honor marker published through plain filename: %v", err)
			}
		})
	}
}

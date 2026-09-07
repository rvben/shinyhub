// Seed only a disposable, already-migrated load-test database.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func main() {
	dsn := flag.String("db", "/state/hub.db", "disposable SQLite path")
	count := flag.Int("sessions", 100000, "retained fixture sessions")
	password := flag.String("password-file", "/state/password", "fixture password file")
	flag.Parse()
	if err := seed(*dsn, *password, *count); err != nil {
		log.Fatal(err)
	}
}
func seed(dsn, passwordFile string, count int) error {
	if count < 0 || count > 1000000 {
		return fmt.Errorf("sessions must be between 0 and 1000000")
	}
	store, err := db.Open(dsn)
	if err != nil {
		return err
	}
	defer store.Close()
	password, err := os.ReadFile(passwordFile)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(strings.TrimSpace(string(password)))
	if err != nil {
		return err
	}
	if err = store.CreateUser(db.CreateUserParams{Username: "bench-viewer", PasswordHash: hash, Role: "viewer"}); err != nil {
		return err
	}
	app, err := store.GetAppBySlug("mixed")
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	// Keep every supported history size inside the seven-day report window.
	_, err = store.DB().Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < ?)
 INSERT INTO usage_sessions (id,app_id,principal_kind,identity_mode,policy_generation,instance_id,started_at,heartbeat_at,ended_at)
 SELECT printf('mixed-seed-%d',n),?,'anonymous','unattributed',1,'mixed-fixture',
 datetime('now',printf('-%d seconds',((?-n)*3 % 432000)+3602)),datetime('now',printf('-%d seconds',((?-n)*3 % 432000)+3601)),datetime('now',printf('-%d seconds',((?-n)*3 % 432000)+3601)) FROM seq`, count, app.ID, count, count, count)
	return err
}

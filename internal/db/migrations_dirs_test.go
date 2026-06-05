package db

import "testing"

// TestMigrationDirsAligned fails if a future migration lands in one dialect dir
// but not the other (once Postgres advances past its single baseline file).
func TestMigrationDirsAligned(t *testing.T) {
	sq, err := loadMigrations("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	pg, err := loadMigrations("postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(pg) == 0 {
		t.Fatal("no postgres migrations embedded")
	}
	pgMax := pg[len(pg)-1].version
	if pgMax == 1 {
		return // only the baseline exists yet; nothing to align
	}
	sqVersions := map[int]bool{}
	for _, m := range sq {
		sqVersions[m.version] = true
	}
	for _, m := range pg {
		if m.version == 1 {
			continue
		}
		if !sqVersions[m.version] {
			t.Errorf("postgres migration %d has no sqlite counterpart", m.version)
		}
	}
}

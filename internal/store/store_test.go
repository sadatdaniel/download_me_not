package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMigrateReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dmn.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs (id, kind, source, created, updated) VALUES ('a', 'web', 'https://x', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = Open(path) // already current: no migration, no backup
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs, err := ListJobs(db)
	if err != nil || len(jobs) != 1 || jobs[0].State != "queued" {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	if m, _ := filepath.Glob(path + ".v*.bak"); len(m) != 0 {
		t.Fatalf("unexpected backup %v", m)
	}

	// A database from a newer binary must not be touched.
	db.Exec("PRAGMA user_version = 99")
	db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("opened a newer database")
	}
	os.Remove(path)
}

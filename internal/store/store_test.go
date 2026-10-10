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
	if err != nil || len(jobs) != 1 || jobs[0].State != "queued" || jobs[0].Want != "running" || jobs[0].Total != -1 {
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

func TestLearnedTrackers(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	LearnTrackers(db, []string{"udp://a/announce", "udp://b/announce"}, true)
	LearnTrackers(db, []string{"udp://a/announce"}, true)
	LearnTrackers(db, []string{"udp://b/announce", "udp://c/announce"}, false)
	top, err := TopTrackers(db, 10)
	if err != nil || len(top) != 1 || top[0].URL != "udp://a/announce" || top[0].Success != 2 {
		t.Fatalf("%+v %v", top, err) // b: 1 success 1 failure (not better), c: never worked
	}
	ForgetTrackers(db)
	if top, _ := TopTrackers(db, 10); len(top) != 0 {
		t.Fatal("not forgotten")
	}
}

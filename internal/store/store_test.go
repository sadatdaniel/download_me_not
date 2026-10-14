package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMigrateReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "getaload.db")
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

func TestUsers(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	InsertJob(db, Job{ID: "old", Kind: "web", Want: "running", State: "done", Created: 1, Updated: 1})

	if _, err := CreateUser(db, "Sadat", "12a4"); err != ErrPINShape {
		t.Fatalf("bad PIN accepted: %v", err)
	}
	a, err := CreateUser(db, "Sadat", "1234")
	if err != nil || !a.Owner {
		t.Fatalf("first user: %+v %v", a, err)
	}
	if j, _ := GetJob(db, "old"); j.User != a.ID {
		t.Fatalf("first user did not inherit the old job: %q", j.User)
	}
	b, err := CreateUser(db, "Wife", "9876")
	if err != nil || b.Owner {
		t.Fatalf("second user: %+v %v", b, err)
	}
	if _, err := CreateUser(db, "wife", "1111"); err == nil {
		t.Fatal("duplicate name (case-insensitive) accepted")
	}
	if us, _ := ListUsers(db); len(us) != 2 || us[0].Name != "Sadat" {
		t.Fatalf("list: %+v", us)
	}

	if _, err := CheckPIN(db, b.ID, "9876"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := CheckPIN(db, b.ID, "0000"); err != ErrBadPIN {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if _, err := CheckPIN(db, b.ID, "0000"); !errors.As(err, new(ErrLocked)) {
		t.Fatalf("5th wrong PIN must lock: %v", err)
	}
	if _, err := CheckPIN(db, b.ID, "9876"); !errors.As(err, new(ErrLocked)) {
		t.Fatalf("locked user accepted the right PIN: %v", err)
	}
	if err := SetPIN(db, b.ID, "4321"); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckPIN(db, b.ID, "4321"); err != nil {
		t.Fatalf("new PIN after reset: %v", err)
	}

	BindDevice(db, "tokhash", b.ID)
	if u, _ := DeviceUser(db, "tokhash"); u != b.ID {
		t.Fatalf("bound user: %q", u)
	}
	BindDevice(db, "tokhash", a.ID)
	if u, _ := DeviceUser(db, "tokhash"); u != a.ID {
		t.Fatalf("rebind: %q", u)
	}
	if err := SetPref(db, b.ID, "fav", []byte(`["x"]`)); err != nil {
		t.Fatal(err)
	}
	SetPref(db, b.ID, "progress", []byte(`{"a":1}`))
	SetPref(db, b.ID, "fav", nil)
	if p, _ := Prefs(db, b.ID); p != `{"progress":{"a":1}}` {
		t.Fatalf("prefs: %s", p)
	}
	if err := SetPref(db, b.ID, "big", make([]byte, 1<<20)); err == nil {
		t.Fatal("oversized prefs accepted")
	}
	if err := SetPref(db, "nobody", "k", []byte(`1`)); err != ErrNoUser {
		t.Fatalf("unknown user: %v", err)
	}

	if err := SetUserSecret(db, a.ID, "gdrive.token", "sealed"); err != nil {
		t.Fatal(err)
	}
	if v, _ := UserSecret(db, a.ID, "gdrive.token"); v != "sealed" {
		t.Fatalf("secret: %q", v)
	}
	if v, _ := UserSecret(db, b.ID, "gdrive.token"); v != "" {
		t.Fatal("another user's secret leaked")
	}
	DeleteUser(db, a.ID)
	if v, _ := UserSecret(db, a.ID, "gdrive.token"); v != "" {
		t.Fatal("secret survived the user")
	}
	if u, _ := DeviceUser(db, "tokhash"); u != "" {
		t.Fatalf("binding survived the user: %q", u)
	}
}

// The lock grows to an hour and stays there: a long run of wrong PINs must never end it (the doubling once
// overflowed after 33 misses and unlocked the user).
func TestPINLockNeverExpiresEarly(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := CreateUser(db, "A", "1234")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE users SET failed = 40 WHERE id = ?`, u.ID)
	var l ErrLocked
	if _, err := CheckPIN(db, u.ID, "0000"); !errors.As(err, &l) || time.Until(l.Until) < 59*time.Minute {
		t.Fatalf("want an hour's lock, got %v", err)
	}
	if _, err := CheckPIN(db, u.ID, "1234"); !errors.As(err, &l) {
		t.Fatalf("the right PIN must wait for the lock too: %v", err)
	}
}

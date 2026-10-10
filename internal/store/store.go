// Package store is the SQLite job database (pure Go driver). The filesystem, not
// this database, is the source of truth for files (scope §4.2).
package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens (or creates) the database and applies pending migrations.
// Before migrating an existing file it copies it to <path>.v<N>.bak (scope §5.8: rollback needs old data).
func Open(path string) (*sql.DB, error) {
	_, statErr := os.Stat(path)
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // ponytail: single writer is plenty for one user; raise with a read pool if the API gets slow
	if err := migrate(db, path, statErr == nil); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB, path string, existed bool) error {
	names, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(names) {
		return fmt.Errorf("database version %d is newer than this binary (%d); refusing to run", v, len(names))
	}
	if v == len(names) {
		return nil
	}
	if existed {
		if err := backup(path, fmt.Sprintf("%s.v%d.bak", path, v)); err != nil {
			return fmt.Errorf("backup before migration: %w", err)
		}
	}
	for i := v; i < len(names); i++ {
		b, err := migrations.ReadFile("migrations/" + names[i].Name())
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(b)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", names[i].Name(), err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func backup(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type Job struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"` // web, torrent, video
	Source   string          `json:"source"`
	Mode     string          `json:"mode"`  // standard, stream
	Want     string          `json:"want"`  // running, paused (desired, set by the user)
	State    string          `json:"state"` // queued, probing, metadata, downloading, paused, done, failed
	Name     string          `json:"name"`
	Path     string          `json:"path,omitempty"` // final path inside the download folder
	InfoHash string          `json:"infohash,omitempty"`
	Done     int64           `json:"done"`
	Total    int64           `json:"total"`    // -1 = unknown
	Priority int             `json:"priority"` // higher starts first
	Error    string          `json:"error,omitempty"`
	Created  int64           `json:"created"`
	Updated  int64           `json:"updated"`
	Meta     json.RawMessage `json:"-"` // kind-specific resume state
}

const jobCols = `id, kind, source, mode, want, state, name, path, infohash, done, total, priority, error, meta, created, updated`

func scanJob(r interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var meta string
	err := r.Scan(&j.ID, &j.Kind, &j.Source, &j.Mode, &j.Want, &j.State, &j.Name, &j.Path, &j.InfoHash,
		&j.Done, &j.Total, &j.Priority, &j.Error, &meta, &j.Created, &j.Updated)
	j.Meta = json.RawMessage(meta)
	return j, err
}

func ListJobs(db *sql.DB) ([]Job, error) {
	rows, err := db.Query(`SELECT ` + jobCols + ` FROM jobs ORDER BY created DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func GetJob(db *sql.DB, id string) (Job, error) {
	return scanJob(db.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
}

func JobByInfoHash(db *sql.DB, ih string) (Job, error) {
	return scanJob(db.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE infohash = ? LIMIT 1`, ih))
}

func InsertJob(db *sql.DB, j Job) error {
	if len(j.Meta) == 0 {
		j.Meta = json.RawMessage("{}")
	}
	_, err := db.Exec(`INSERT INTO jobs (`+jobCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Kind, j.Source, j.Mode, j.Want, j.State, j.Name, j.Path, j.InfoHash, j.Done, j.Total, j.Priority, j.Error, string(j.Meta), j.Created, j.Updated)
	return err
}

// SetJob updates only the given columns, so concurrent writers (runner progress, API pause) never
// overwrite each other's fields.
func SetJob(db *sql.DB, id string, f map[string]any) error {
	keys := make([]string, 0, len(f))
	for k := range f {
		if !mutable[k] {
			return fmt.Errorf("store: column %q is not updatable", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	q, args := "UPDATE jobs SET ", []any{}
	for _, k := range keys {
		q += k + " = ?, "
		v := f[k]
		if m, ok := v.(json.RawMessage); ok {
			v = string(m)
		}
		args = append(args, v)
	}
	_, err := db.Exec(q+"updated = ? WHERE id = ?", append(args, time.Now().Unix(), id)...)
	return err
}

var mutable = map[string]bool{"mode": true, "want": true, "state": true, "name": true, "path": true,
	"infohash": true, "done": true, "total": true, "error": true, "meta": true, "priority": true}

func DeleteJob(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	return err
}

type LearnedTracker struct {
	URL     string `json:"url"`
	Success int    `json:"success"`
	Failure int    `json:"failure"`
	LastOK  int64  `json:"lastOk"`
}

// LearnTrackers records the outcome of a public torrent for each of its tracker URLs.
func LearnTrackers(db *sql.DB, urls []string, ok bool) error {
	now := time.Now().Unix()
	for _, u := range urls {
		s, f, last := 0, 1, int64(0)
		if ok {
			s, f, last = 1, 0, now
		}
		if _, err := db.Exec(`INSERT INTO learned_trackers (url, success, failure, last_ok, created) VALUES (?,?,?,?,?)
			ON CONFLICT(url) DO UPDATE SET success = success + ?, failure = failure + ?, last_ok = max(last_ok, ?)`,
			u, s, f, last, now, s, f, last); err != nil {
			return err
		}
	}
	return nil
}

// TopTrackers returns the best learned trackers: worked within 90 days, more successes than failures.
func TopTrackers(db *sql.DB, n int) ([]LearnedTracker, error) {
	rows, err := db.Query(`SELECT url, success, failure, last_ok FROM learned_trackers
		WHERE success > failure AND last_ok > ? ORDER BY success - failure DESC, last_ok DESC LIMIT ?`,
		time.Now().Add(-90*24*time.Hour).Unix(), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LearnedTracker{}
	for rows.Next() {
		var t LearnedTracker
		if err := rows.Scan(&t.URL, &t.Success, &t.Failure, &t.LastOK); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func ForgetTrackers(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM learned_trackers`)
	return err
}

package store

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// User is one person of the household. Users are strictly separate: own downloads, files and preferences.
type User struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Owner   bool   `json:"owner" doc:"the first user; may add and remove users (sees nobody else's files)"`
	Locked  int64  `json:"lockedUntil,omitempty" doc:"unix time until which PIN tries are refused"`
	Created int64  `json:"created"`
}

var (
	ErrBadPIN   = errors.New("wrong PIN")
	ErrPINShape = errors.New("the PIN must be exactly 4 digits")
	ErrNoUser   = errors.New("no such user")
)

// ErrLocked is returned while a user is locked after too many wrong PINs.
type ErrLocked struct{ Until time.Time }

func (e ErrLocked) Error() string {
	return fmt.Sprintf("too many wrong PINs; try again in %s", time.Until(e.Until).Round(time.Second))
}

func validPIN(pin string) bool {
	return len(pin) == 4 && strings.Trim(pin, "0123456789") == ""
}

// pinHash: PBKDF2-SHA256. 4 digits are weak on their own; the lockout and the device-token requirement do the
// heavy lifting, the slow hash only protects a stolen database a little.
func pinHash(pin string, salt []byte) (string, error) {
	k, err := pbkdf2.Key(sha256.New, pin, salt, 200_000, 32)
	return hex.EncodeToString(k), err
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateUser adds a user. The first user becomes the owner and inherits everything from before users existed
// (jobs without a user).
func CreateUser(db *sql.DB, name, pin string) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 40 {
		return User{}, errors.New("a name of 1 to 40 characters is required")
	}
	if !validPIN(pin) {
		return User{}, ErrPINShape
	}
	salt := randHex(16)
	h, err := pinHash(pin, []byte(salt))
	if err != nil {
		return User{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return User{}, err
	}
	u := User{ID: randHex(6), Name: name, Owner: n == 0, Created: time.Now().Unix()}
	if _, err := tx.Exec(`INSERT INTO users (id, name, pin, salt, owner, created) VALUES (?,?,?,?,?,?)`,
		u.ID, u.Name, h, salt, u.Owner, u.Created); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, errors.New("a user with that name already exists")
		}
		return User{}, err
	}
	if u.Owner {
		if _, err := tx.Exec(`UPDATE jobs SET user_id = ? WHERE user_id = ''`, u.ID); err != nil {
			return User{}, err
		}
	}
	return u, tx.Commit()
}

func scanUser(r interface{ Scan(...any) error }) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Name, &u.Owner, &u.Locked, &u.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNoUser
	}
	if u.Locked < time.Now().Unix() {
		u.Locked = 0
	}
	return u, err
}

const userCols = `id, name, owner, locked_until, created`

func ListUsers(db *sql.DB) ([]User, error) {
	rows, err := db.Query(`SELECT ` + userCols + ` FROM users ORDER BY created`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func GetUser(db *sql.DB, id string) (User, error) {
	return scanUser(db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// CheckPIN verifies a user's PIN. Five wrong tries in a row lock the user for a minute, doubling with each
// further miss up to an hour; a right PIN resets the count.
func CheckPIN(db *sql.DB, id, pin string) (User, error) {
	var h, salt string
	var failed int
	var locked int64
	err := db.QueryRow(`SELECT pin, salt, failed, locked_until FROM users WHERE id = ?`, id).Scan(&h, &salt, &failed, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoUser
	}
	if err != nil {
		return User{}, err
	}
	if now := time.Now(); locked > now.Unix() {
		return User{}, ErrLocked{time.Unix(locked, 0)}
	}
	got, err := pinHash(pin, []byte(salt))
	if err != nil {
		return User{}, err
	}
	if !validPIN(pin) || subtle.ConstantTimeCompare([]byte(got), []byte(h)) != 1 {
		failed++
		var until int64
		if failed >= 5 {
			wait := min(time.Minute<<(failed-5), time.Hour)
			until = time.Now().Add(wait).Unix()
		}
		db.Exec(`UPDATE users SET failed = ?, locked_until = ? WHERE id = ?`, failed, until, id)
		if until > 0 {
			return User{}, ErrLocked{time.Unix(until, 0)}
		}
		return User{}, ErrBadPIN
	}
	db.Exec(`UPDATE users SET failed = 0, locked_until = 0 WHERE id = ?`, id)
	return GetUser(db, id)
}

// SetPIN changes a PIN (and clears any lock).
func SetPIN(db *sql.DB, id, pin string) error {
	if !validPIN(pin) {
		return ErrPINShape
	}
	salt := randHex(16)
	h, err := pinHash(pin, []byte(salt))
	if err != nil {
		return err
	}
	r, err := db.Exec(`UPDATE users SET pin = ?, salt = ?, failed = 0, locked_until = 0 WHERE id = ?`, h, salt, id)
	if n, _ := r.RowsAffected(); err == nil && n == 0 {
		return ErrNoUser
	}
	return err
}

// DeleteUser removes a user and their device bindings. Their jobs and files are the caller's business.
func DeleteUser(db *sql.DB, id string) error {
	if _, err := db.Exec(`DELETE FROM device_users WHERE user_id = ?`, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// BindDevice makes a paired device (by token hash) act as a user from now on.
func BindDevice(db *sql.DB, tokenHash, userID string) error {
	_, err := db.Exec(`INSERT INTO device_users (token_hash, user_id, bound) VALUES (?,?,?)
		ON CONFLICT(token_hash) DO UPDATE SET user_id = excluded.user_id, bound = excluded.bound`,
		tokenHash, userID, time.Now().Unix())
	return err
}

func UnbindDevice(db *sql.DB, tokenHash string) error {
	_, err := db.Exec(`DELETE FROM device_users WHERE token_hash = ?`, tokenHash)
	return err
}

// DeviceUser returns the user a device acts as ("" if it has not chosen one).
func DeviceUser(db *sql.DB, tokenHash string) (string, error) {
	var id string
	err := db.QueryRow(`SELECT user_id FROM device_users WHERE token_hash = ?`, tokenHash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ListJobsOf lists one user's jobs.
func ListJobsOf(db *sql.DB, userID string) ([]Job, error) {
	rows, err := db.Query(`SELECT `+jobCols+` FROM jobs WHERE user_id = ? ORDER BY created DESC`, userID)
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

// Prefs and SetPrefs hold a user's own settings (JSON object), e.g. their add-ons.
func Prefs(db *sql.DB, id string) (string, error) {
	var p string
	err := db.QueryRow(`SELECT prefs FROM users WHERE id = ?`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoUser
	}
	return p, err
}

func SetPrefs(db *sql.DB, id, prefs string) error {
	_, err := db.Exec(`UPDATE users SET prefs = ? WHERE id = ?`, prefs, id)
	return err
}

func UserByName(db *sql.DB, name string) (User, error) {
	return scanUser(db.QueryRow(`SELECT `+userCols+` FROM users WHERE name = ?`, strings.TrimSpace(name)))
}

// HasUsers: once a user exists, every device must sign in as someone.
func HasUsers(db *sql.DB) (bool, error) {
	var ok bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users)`).Scan(&ok)
	return ok, err
}

// UserDevices lists the token hashes bound to a user.
func UserDevices(db *sql.DB, userID string) ([]string, error) {
	rows, err := db.Query(`SELECT token_hash FROM device_users WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ErrPrefsFull keeps one user's preferences from growing without bound.
var ErrPrefsFull = errors.New("preferences are full (1 MiB)")

// SetPref sets one key of a user's preferences to a JSON value; value nil removes the key.
// key must not contain '"' (the API restricts it to letters, digits, '_' and '-').
func SetPref(db *sql.DB, id, key string, value []byte) error {
	var r sql.Result
	var err error
	if value == nil {
		r, err = db.Exec(`UPDATE users SET prefs = json_remove(prefs, '$."'||?||'"') WHERE id = ?`, key, id)
	} else {
		r, err = db.Exec(`UPDATE users SET prefs = json_set(prefs, '$."'||?||'"', json(?)) WHERE id = ?
			AND length(prefs) + ? <= 1048576`, key, string(value), id, len(value))
	}
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		if _, err := GetUser(db, id); err != nil {
			return err
		}
		return ErrPrefsFull
	}
	return nil
}

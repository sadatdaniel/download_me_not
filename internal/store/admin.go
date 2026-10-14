package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// ErrBadCode is a wrong, used or expired admin code.
var ErrBadCode = errors.New("that code is wrong, used or expired; run `sudo getaload admin` for a new one")

// codeChars has no 0/O, 1/I/L: the code is read off a terminal and typed on a phone.
const codeChars = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func codeHash(code string) string {
	code = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// NewAdminCode makes a one-time code (shown as XXXX-XXXX) valid for ttl. 31^8 ≈ 8.5e11 codes; together with the
// sign-in rate limit and the short life, guessing one is out of reach.
func NewAdminCode(db *sql.DB, ttl time.Duration) (string, error) {
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = codeChars[int(b[i])%len(codeChars)] // ponytail: slight modulo bias (256 % 31 = 8); irrelevant at this size
	}
	code := string(b[:4]) + "-" + string(b[4:])
	db.Exec(`DELETE FROM admin_codes WHERE expires <= ?`, time.Now().Unix())
	_, err := db.Exec(`INSERT INTO admin_codes (hash, expires) VALUES (?, ?)`, codeHash(code), time.Now().Add(ttl).Unix())
	return code, err
}

// UseAdminCode accepts a code once: it is deleted in the same statement that checks it.
func UseAdminCode(db *sql.DB, code string) error {
	var h string
	err := db.QueryRow(`DELETE FROM admin_codes WHERE hash = ? AND expires > ? RETURNING hash`, codeHash(code), time.Now().Unix()).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBadCode
	}
	return err
}

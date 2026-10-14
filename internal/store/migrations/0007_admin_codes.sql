-- One-time codes from `getaload admin`: the app trades one for an admin key. Only the hash is kept.
CREATE TABLE admin_codes (
  hash    TEXT PRIMARY KEY,
  expires INTEGER NOT NULL
);

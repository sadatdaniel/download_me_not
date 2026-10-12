-- Household users (name + PIN). The first user created is the owner (manages users, nothing more).
CREATE TABLE users (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE COLLATE NOCASE,
  pin          TEXT NOT NULL,          -- PBKDF2-SHA256 hash, hex
  salt         TEXT NOT NULL,
  owner        INTEGER NOT NULL DEFAULT 0,
  failed       INTEGER NOT NULL DEFAULT 0,  -- wrong PINs in a row
  locked_until INTEGER NOT NULL DEFAULT 0,
  prefs        TEXT NOT NULL DEFAULT '{}',  -- per-user settings (add-ons, …) as JSON
  created      INTEGER NOT NULL
);

-- Which user a paired device (token hash) acts as. Bound once after a PIN check.
CREATE TABLE device_users (
  token_hash TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  bound      INTEGER NOT NULL
);

-- Every job belongs to one user ('' = from before users existed; given to the first user).
ALTER TABLE jobs ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX jobs_user ON jobs(user_id);

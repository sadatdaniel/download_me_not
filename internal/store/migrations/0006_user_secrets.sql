-- Per-user secrets (sealed by the server's key), e.g. each user's own Google Drive token.
CREATE TABLE user_secrets (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name    TEXT NOT NULL,
  value   TEXT NOT NULL,
  PRIMARY KEY (user_id, name)
);

CREATE TABLE jobs (
  id      TEXT PRIMARY KEY,
  kind    TEXT NOT NULL,
  source  TEXT NOT NULL,
  mode    TEXT NOT NULL DEFAULT 'standard',
  state   TEXT NOT NULL DEFAULT 'queued',
  name    TEXT NOT NULL DEFAULT '',
  error   TEXT NOT NULL DEFAULT '',
  meta    TEXT NOT NULL DEFAULT '{}',
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL
);

-- Swarm memory: trackers seen in completed public torrents, ranked by success.
CREATE TABLE learned_trackers (
  url      TEXT PRIMARY KEY,
  success  INTEGER NOT NULL DEFAULT 0,
  failure  INTEGER NOT NULL DEFAULT 0,
  last_ok  INTEGER NOT NULL DEFAULT 0,
  created  INTEGER NOT NULL
);

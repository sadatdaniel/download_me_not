-- want: desired state set by the user (running|paused); state: what the reconcile loop observed.
ALTER TABLE jobs ADD COLUMN want TEXT NOT NULL DEFAULT 'running';
ALTER TABLE jobs ADD COLUMN path TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN infohash TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN total INTEGER NOT NULL DEFAULT -1;
CREATE INDEX jobs_infohash ON jobs(infohash) WHERE infohash != '';

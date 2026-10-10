-- Higher runs first; ties run oldest first.
ALTER TABLE jobs ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;

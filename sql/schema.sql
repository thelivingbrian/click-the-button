CREATE TABLE IF NOT EXISTS counter_snapshots (
    ts    INTEGER PRIMARY KEY,
    clicks INTEGER NOT NULL,
    views INTEGER NOT NULL
);
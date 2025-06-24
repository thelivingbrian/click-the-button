CREATE TABLE IF NOT EXISTS counter_snapshots (
    ts    INTEGER PRIMARY KEY,
    clicksA INTEGER NOT NULL,
    clicksB INTEGER NOT NULL,
    views INTEGER NOT NULL
);
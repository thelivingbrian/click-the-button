# Click the Button

A shared station of small interactive polls, built with Go, Datastar, and SQLite.
This branch is a local prototype: guests and optional unverified handles work;
full accounts, creator authorization, and public moderation are not implemented.

## Run locally (macOS / Linux)

Use the Go toolchain specified by `go.mod`:

```sh
cp -n server/.env.example server/.env
go test -race ./...
cd server
go build -o server .
./server
```

Open http://localhost:8080. Start from `server/` so templates, assets and data
resolve correctly. The server binds to `127.0.0.1` by default. `PORT` defaults to
8080; `HOST` can override the bind address. Keep this prototype on loopback:
the studio intentionally has no account authorization. Browser scripts, charts,
and fonts currently load from external CDNs.

The prototype creates `server/data/station.db`, independently of the retired
`server/data/clicks.db`. Runtime files, `.env`, and the binary are ignored by Git.
New clicks are committed before success; stopping the new server does not require
waiting for a periodic counter snapshot. Restarting preserves results, browser
sessions, ballots, archives, and rematches.

## Try the prototype

- `/`: stable live board, recent actual activity, optional handle.
- `/poll/tug`: repeat clicks pull the cumulative balance left/right.
- `/poll/pulse`: exponential energy decay with a 30-second half-life and saved peak.
- `/poll/contest`: a repeat-click music contest.
- `/poll/scale`: 1–10 distribution and weighted average.
- `/poll/stars`: 1–5 histogram and weighted average.
- `/poll/heat`: keyboard-accessible 5×5 cumulative heat map.
- `/poll/one`: one ballot per browser session (clearing cookies bypasses this).
- `/studio`: close and archive a round, or start its rematch.
- `/archive`: preserved rounds, plus the original experiment.
- `/legacy/`: Dogs vs. Cats with its original stylesheet and frozen chart.

Each page uses at most one Datastar result stream. Cooldowns allow eight initial
clicks and replenish four clicks/second per browser session across polls. This
is a playability limit, not proof of humanity. Named activity is bounded to the
latest 30 actions and displayed for at most 24 hours. A handle is not reserved or
verified. No real users or visitor counts are fabricated.

Archive a round from the studio: its final state and accepted interaction history
are sealed together in a transaction. Frozen JSON is downloadable at
`/archive/{id}/export`; its ETag is the payload's SHA-256. Archiving again leaves
it unchanged. A rematch gets a new ID and empty results; retrying that rematch
action returns the same successor. Archived pages have no live subscription.
An already-open live page disables its controls when closure reaches it.

## Freeze the legacy experiment locally

Before running the new server, stop the old server and run from the repo root:

```sh
python3 scripts/archive-legacy.py
```

This expects an existing `server/data/clicks.db` containing saved snapshots. It
checks that port 8080 is free, makes a consistent SQLite backup, checks integrity,
exports chart rows, and records provenance, cutoff and checksums under
`server/data/legacy/`. The files are made read-only. Re-running verifies existing
checksums instead of replacing the archive. The original database stays intact.
The port check assumes the original local setup; ensure no legacy writer is
running on another port before executing it.

The cutoff is explicitly the **last persisted snapshot**, because the old server
has no atomic close/final shutdown flush. The archive is labeled synthetic local
activity, not production history. This script is for the local rehearsal only.
The legacy page reads only frozen JSON; it never opens the original SQLite file.
Old `/click/`, stream, and metric endpoints return 410 Gone.

`scripts/simulate-local.py` is retained for the old pre-retirement application;
it deliberately cannot seed this prototype through retired endpoints.

## Persistence and scope

SQLite WAL with a single application connection provides the local write order.
Poll updates, retry identifiers, one-vote constraints, and event history commit
together. Closure serializes with writes and atomically seals an immutable export;
there is no externally observable intermediate `closed` state in this version.
The database is the archive store; exports do not depend on a background worker.
Back up `station.db` using SQLite's backup API while running, or copy it only after
stopping and checkpointing the database. Include `data/legacy/` in local backups.

This is one-process development hosting. History is kept per interaction and
archive JSON is assembled in memory; sessions and deduplication records are not
pruned. Those choices need bounds before public scale. Full accounts, ephemeral
blurbs, custom poll creation/editing, scheduled closure, and distributed delivery
remain follow-up work. Presets and rematches make the full interaction/archive
loop available now without introducing a hosting migration.

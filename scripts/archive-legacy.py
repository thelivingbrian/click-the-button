#!/usr/bin/env python3
"""Freeze the stopped local legacy database at its persisted-snapshot cutoff."""
import datetime
import hashlib
import json
import pathlib
import socket
import sqlite3
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'server/data/clicks.db'
TARGET = ROOT / 'server/data/legacy'


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    if TARGET.exists():
        manifest = json.loads((TARGET / 'manifest.json').read_text())
        for filename, expected in manifest['sha256'].items():
            assert digest(TARGET / filename) == expected, filename + ' checksum mismatch'
        print('Existing archive verified; no files changed.')
        return
    with socket.socket() as sock:
        if sock.connect_ex(('127.0.0.1', 8080)) == 0:
            raise SystemExit('Stop the local server on port 8080 before freezing the database.')
    assert SOURCE.exists(), 'No legacy database exists'
    staging = TARGET.with_name('legacy-staging')
    staging.mkdir()  # Refuse to overwrite an incomplete previous attempt.
    with sqlite3.connect(SOURCE.as_uri() + '?mode=ro', uri=True) as source:
        with sqlite3.connect(staging / 'clicks.db') as backup:
            source.backup(backup)
            assert backup.execute('PRAGMA integrity_check').fetchone() == ('ok',)
            rows = backup.execute('SELECT ts, clicksA, clicksB, views FROM counter_snapshots ORDER BY ts').fetchall()
    assert rows, 'No persisted snapshots to archive'
    history = [dict(zip(('ts', 'clicksA', 'clicksB', 'views'), row)) for row in rows]
    (staging / 'history.json').write_text(json.dumps(history, indent=2) + '\n')
    manifest = {
        'title': 'Dogs vs. Cats', 'options': ['Dog', 'Cat'],
        'rules': 'Unlimited clicks; totals are clicks, not unique voters.',
        'provenance': 'Synthetic local development activity; not production results.',
        'cutoff': 'Last persisted snapshot. The legacy process has no atomic close/final flush.',
        'cutoffUnix': rows[-1][0], 'final': history[-1], 'snapshots': len(rows),
        'archivedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(),
        'sourceRevision': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
        'sha256': {name: digest(staging / name) for name in ('clicks.db', 'history.json')},
    }
    (staging / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    staging.rename(TARGET)
    for filename in TARGET.iterdir():
        filename.chmod(0o444)
    print(json.dumps(manifest, indent=2))


if __name__ == '__main__':
    main()

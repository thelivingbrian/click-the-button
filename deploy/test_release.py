import hashlib
import io
import json
import pathlib
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

import release


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)

    def bundle(self, name='server', kind=tarfile.REGTYPE):
        bundle = self.root / 'bundle.tar.gz'
        with tarfile.open(bundle, 'w:gz') as archive:
            entry = tarfile.TarInfo(name)
            entry.type = kind
            entry.linkname = '/etc/passwd' if kind == tarfile.SYMTYPE else ''
            entry.size = 2 if kind == tarfile.REGTYPE else 0
            archive.addfile(entry, io.BytesIO(b'ok'))
        return bundle

    def test_rejects_traversal_links_and_unexpected_files(self):
        destination = self.root / 'extract'
        destination.mkdir()
        for name, kind in [('../escape', tarfile.REGTYPE), ('/absolute', tarfile.REGTYPE), ('assets/link', tarfile.SYMTYPE), ('.env', tarfile.REGTYPE), ('data/station.db', tarfile.REGTYPE)]:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                release.extract(self.bundle(name, kind), destination)
        self.assertEqual(list(destination.iterdir()), [])

    def test_backup_restores_committed_wal_data(self):
        data = self.root / 'shared/data'
        (data / 'legacy').mkdir(parents=True)
        (data / 'legacy/history.json').write_text('[1,2,3]')
        with sqlite3.connect(data / 'station.db') as db:
            db.execute('PRAGMA journal_mode=WAL')
            db.execute('CREATE TABLE votes(value INTEGER)')
            db.execute('INSERT INTO votes VALUES(42)')
            db.commit()
            with mock.patch.object(release, 'ROOT', self.root):
                snapshot = release.backup()
            db.execute('INSERT INTO votes VALUES(43)')
            db.commit()
            with sqlite3.connect(snapshot / 'station.db') as restored:
                self.assertEqual(restored.execute('SELECT value FROM votes').fetchall(), [(42,)])
                self.assertEqual(restored.execute('PRAGMA integrity_check').fetchone(), ('ok',))
        for name, sha in json.loads((snapshot / 'checksums.json').read_text()).items():
            self.assertFalse(name.endswith(('-wal', '-shm')))
            self.assertEqual(hashlib.sha256((snapshot / name).read_bytes()).hexdigest(), sha)
        with sqlite3.connect(snapshot / 'station.db') as restored:
            self.assertEqual(restored.execute('PRAGMA journal_mode').fetchone(), ('delete',))

    def test_failed_health_rolls_back_binary_without_replacing_database(self):
        old, new = 'a' * 40, 'b' * 40
        for revision in (old, new):
            directory = self.root / 'releases' / revision
            directory.mkdir(parents=True)
            (directory / 'REVISION').write_text(revision)
        (self.root / 'current').symlink_to(self.root / 'releases' / old)
        with mock.patch.object(release, 'ROOT', self.root), mock.patch.object(release, 'backup') as backup, mock.patch.object(release, 'restart') as restart, mock.patch.object(release, 'healthy', side_effect=[False, True]):
            with self.assertRaisesRegex(RuntimeError, 'health'):
                release.install(self.root / 'unused', new)
        backup.assert_called_once()
        self.assertEqual(restart.call_count, 2)
        self.assertEqual((self.root / 'current').resolve().name, old)
        self.assertEqual((self.root / 'failed-revision').read_text().strip(), new)

    def test_production_archive_preserves_results_and_detects_tampering(self):
        source, target = self.root / 'clicks.db', self.root / 'legacy'
        with sqlite3.connect(source) as db:
            db.execute('CREATE TABLE counter_snapshots(ts INTEGER, clicksA INTEGER, clicksB INTEGER, views INTEGER)')
            db.executemany('INSERT INTO counter_snapshots VALUES(?,?,?,?)', [(100, 4, 7, 9), (200, 8, 12, 15)])
        script = pathlib.Path(__file__).resolve().parents[1] / 'scripts/archive-legacy.py'
        command = [sys.executable, str(script), '--source', str(source), '--target', str(target), '--port', '0', '--production', '--source-revision', 'a' * 40]
        subprocess.run(command, check=True, capture_output=True)
        manifest = json.loads((target / 'manifest.json').read_text())
        self.assertEqual(manifest['final'], {'ts': 200, 'clicksA': 8, 'clicksB': 12, 'views': 15})
        self.assertEqual(manifest['provenance'], 'Production results from click-the-button.com.')
        subprocess.run(command, check=True, capture_output=True)
        history = target / 'history.json'
        history.chmod(0o600)
        history.write_text('tampered')
        self.assertNotEqual(subprocess.run(command, capture_output=True).returncode, 0)


if __name__ == '__main__':
    unittest.main()

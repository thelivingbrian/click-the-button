#!/usr/bin/env python3
"""Install a local bundle or the latest production release. Run as the app user."""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
import pathlib
import re
import shutil
import sqlite3
import subprocess
import tarfile
import tempfile
import time
import urllib.request

ROOT = pathlib.Path('/srv/click-the-button')
REPOSITORY = 'thelivingbrian/click-the-button'
ORIGIN = 'click-the-button.com'


def request(url):
    req = urllib.request.Request(url, headers={'User-Agent': 'click-the-button-deployer', 'Host': ORIGIN} if url.startswith('http://127.0.0.1:') else {'User-Agent': 'click-the-button-deployer'})
    with urllib.request.urlopen(req, timeout=30) as response:
        return response.read()


def backup():
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S.%fZ')
    destination = ROOT / 'backups' / stamp
    destination.mkdir(parents=True)
    database = ROOT / 'shared/data/station.db'
    if database.exists():
        with sqlite3.connect(database.as_uri() + '?mode=ro', uri=True) as source:
            with sqlite3.connect(destination / 'station.db') as copy:
                source.backup(copy)
                if copy.execute('PRAGMA integrity_check').fetchone() != ('ok',):
                    raise RuntimeError('Backup integrity check failed')
    shutil.copytree(ROOT / 'shared/data/legacy', destination / 'legacy')
    current = ROOT / 'current'
    (destination / 'release.json').write_text(json.dumps({'release': str(current.resolve()) if current.exists() else None, 'created': stamp}) + '\n')
    hashes = {str(p.relative_to(destination)): hashlib.sha256(p.read_bytes()).hexdigest() for p in destination.rglob('*') if p.is_file()}
    (destination / 'checksums.json').write_text(json.dumps(hashes, indent=2) + '\n')
    print('Verified backup:', destination, flush=True)
    return destination


def switch(path):
    link = ROOT / 'next'
    link.unlink(missing_ok=True)
    link.symlink_to(path)
    link.replace(ROOT / 'current')


def restart():
    subprocess.run(['sudo', '-n', '/usr/bin/systemctl', 'restart', 'click-the-button.service'], check=True)


def healthy(revision):
    for _ in range(30):
        try:
            result = json.loads(request('http://127.0.0.1:14010/healthz'))
            if result == {'status': 'ok', 'revision': revision}:
                request('http://127.0.0.1:14010/legacy/')
                return True
        except Exception:
            pass
        time.sleep(1)
    return False


def extract(bundle, destination):
    with tarfile.open(bundle, 'r:gz') as archive:
        members = archive.getmembers()
        if sum(m.size for m in members) > 200 * 1024 * 1024:
            raise RuntimeError('Release exceeds 200 MiB')
        for member in members:
            path = pathlib.PurePosixPath(member.name)
            if path.is_absolute() or '..' in path.parts or not (member.isfile() or member.isdir()):
                raise RuntimeError('Unsafe archive entry')
            if path.parts and path.parts[0] not in ('server', 'assets', 'templates', 'sql', 'REVISION'):
                raise RuntimeError('Unexpected archive entry: ' + member.name)
        # All links and special files were rejected above; extract without owner/mode metadata.
        for member in members:
            target = destination / member.name
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with archive.extractfile(member) as source, target.open('xb') as output:
                    shutil.copyfileobj(source, output)
                target.chmod(0o750 if member.name.lstrip('./') == 'server' else 0o640)


def install(bundle, revision):
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise RuntimeError('Expected a full Git commit SHA')
    current = ROOT / 'current'
    previous = current.resolve() if current.exists() else None
    if previous and (previous / 'REVISION').read_text().strip() == revision:
        if not healthy(revision):
            raise RuntimeError('Installed release is unhealthy')
        print('Already deployed:', revision)
        return
    destination = ROOT / 'releases' / revision
    if not destination.exists():
        staging = pathlib.Path(tempfile.mkdtemp(prefix='.staging-', dir=ROOT / 'releases'))
        try:
            extract(bundle, staging)
            if (staging / 'REVISION').read_text().strip() != revision:
                raise RuntimeError('Bundle revision mismatch')
            for required in ('server', 'templates/station.html', 'templates/legacy.html', 'templates/home.tmpl.html', 'assets/station.css'):
                if not (staging / required).is_file():
                    raise RuntimeError('Missing release file: ' + required)
            (staging / 'data').symlink_to(ROOT / 'shared/data')
            staging.rename(destination)
        finally:
            if staging.exists():
                shutil.rmtree(staging)
    backup()
    switch(destination)
    try:
        restart()
        if not healthy(revision):
            raise RuntimeError('New release failed health verification')
    except Exception:
        (ROOT / 'failed-revision').write_text(revision + '\n')
        if previous:
            switch(previous)
            restart()
            if not healthy((previous / 'REVISION').read_text().strip()):
                print('Previous binary also failed health verification; operator recovery required.', flush=True)
        # Never restore a database automatically: new requests may already have committed.
        raise
    (ROOT / 'failed-revision').unlink(missing_ok=True)
    print('Deployed:', revision, flush=True)


def latest():
    release = json.loads(request(f'https://api.github.com/repos/{REPOSITORY}/releases/latest'))
    if release['draft'] or release['prerelease'] or not release['tag_name'].startswith('production-'):
        raise RuntimeError('Latest release is not a production release')
    assets = {a['name']: a for a in release['assets']}
    manifest = json.loads(request(assets['release.json']['browser_download_url']))
    revision = manifest['revision']
    if release['target_commitish'] != revision:
        raise RuntimeError('Release target does not match bundle revision')
    failed = ROOT / 'failed-revision'
    if failed.exists() and failed.read_text().strip() == revision:
        raise RuntimeError('This revision previously failed; publish a fix or retry explicitly')
    current = ROOT / 'current/REVISION'
    if current.exists() and current.read_text().strip() == revision:
        print('Already deployed:', revision)
        return
    payload = request(assets['click-the-button-linux-amd64.tar.gz']['browser_download_url'])
    if hashlib.sha256(payload).hexdigest() != manifest['sha256']:
        raise RuntimeError('Release checksum mismatch')
    with tempfile.TemporaryDirectory(dir=ROOT) as directory:
        bundle = pathlib.Path(directory) / 'release.tar.gz'
        bundle.write_bytes(payload)
        install(bundle, revision)


def main():
    os.umask(0o027)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', type=pathlib.Path)
    parser.add_argument('--revision')
    parser.add_argument('--backup', action='store_true')
    args = parser.parse_args()
    with (ROOT / 'deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.backup:
            backup()
        elif args.bundle and args.revision:
            install(args.bundle, args.revision)
        elif args.bundle or args.revision:
            parser.error('--bundle and --revision must be supplied together')
        else:
            latest()


if __name__ == '__main__':
    main()

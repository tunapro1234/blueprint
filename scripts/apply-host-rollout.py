#!/usr/bin/env python3
"""Apply a checksum-pinned bp bundle from an authorized HOST shell, not a sandbox.

Requires an operator-verified current server-main thread. Changes only bp's two
binaries, its agentbooks and the watchdog's two files; restarts blueprint.service.
Backups are retained. Never changes Codex's shared server, models or tmux options.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import uuid


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def replace_file(source, target, mode):
    fd, temporary = tempfile.mkstemp(prefix='.bp-rollout-', dir=target.parent)
    with os.fdopen(fd, 'wb') as f:
        f.write(source.read_bytes())
        os.fchmod(f.fileno(), mode)
        f.flush()
        os.fsync(f.fileno())
    os.replace(temporary, target)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', type=Path, required=True)
    parser.add_argument('--root-thread', required=True,
                        help='current server-main UUID, verified by its operator')
    parser.add_argument('--bind', action='append', default=[], metavar='AGENT=THREAD')
    parser.add_argument('--apply', action='store_true', help='perform the authorized host rollout')
    args = parser.parse_args()
    uuid.UUID(args.root_thread)
    bundle = args.bundle.resolve()
    run('sha256sum', '-c', 'checksums.txt', cwd=bundle)
    stamp = dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%S.%fZ')
    review = bundle / ('preflight-' + stamp)
    review.mkdir(mode=0o700)
    migration = [sys.executable, str(bundle / 'migrate-codex-book.py'),
                 '--thread', args.root_thread,
                 '--bind', 'blueprint=01a070c4-3f56-7213-b8ad-19fa7d6f5e4d',
                 '--bind', 'probot-egitim-astra=01a0712d-46d1-7223-b666-e44fb229c782']
    for binding in args.bind:
        migration += ['--bind', binding]
    run(*migration, '--output', str(review / 'books'))
    (review / 'config.json').write_text(json.dumps({
        'agentbooks': [str(review / 'books/0-agentbook.json'), str(review / 'books/1-agentbook.json')],
        'stateDir': str(review / 'state'), 'msgqRoot': '/srv/server-main/msgq',
        'usageHistory': '/srv/server-main/usage/history.jsonl', 'waBridge': False,
    }) + '\n')
    candidate = bundle / 'bp-linux-amd64'
    env = dict(os.environ, BP_HOME=str(review))
    report = json.loads(subprocess.check_output([str(candidate), 'status', '--json'], env=env))
    (review / 'status.json').write_text(json.dumps(report, indent=2) + '\n')
    root = next(a for a in report['agents'] if a['name'] == 'server-main')
    activity = root.get('activity', {})
    if (activity.get('thread_id') != args.root_thread or activity.get('source') != 'app-server'
            or activity.get('state') not in ('working', 'idle')):
        raise RuntimeError('root runtime is not verified/loaded; nothing installed: ' + json.dumps(activity))
    print('Verified root runtime. Review:', review, flush=True)
    if not args.apply:
        return
    if os.geteuid() != 0:
        raise RuntimeError('authorized host root shell required')
    unit = subprocess.check_output(['systemctl', 'show', 'blueprint.service', '-p', 'ExecStart', '--value'], text=True)
    if '/srv/blueprint/bp daemon' not in unit:
        raise RuntimeError('unexpected blueprint.service ExecStart; inspect before changing it')
    targets = [(candidate, Path('/srv/blueprint/bp'), 0o755),
               (candidate, Path('/usr/local/bin/bp'), 0o755),
               (bundle / 'bp_runtime_usage.py', Path('/srv/server-main/bin/bp_runtime_usage.py'), 0o644),
               (bundle / 'otonom-yakit-bekcisi.py', Path('/srv/server-main/bin/otonom-yakit-bekcisi.py'), 0o755)]
    backup = bundle / ('host-backup-' + stamp)
    backup.mkdir(mode=0o700)
    for index, (_, target, _) in enumerate(targets):
        if target.is_symlink() or (target.exists() and not target.is_file()):
            raise RuntimeError('unexpected target type: ' + str(target))
        if not os.access(target.parent, os.W_OK):
            raise RuntimeError('host target not writable: ' + str(target.parent))
        if target.exists():
            shutil.copy2(target, backup / (str(index) + '-' + target.name))
    (backup / 'targets.json').write_text(json.dumps([str(t) for _, t, _ in targets], indent=2) + '\n')
    print('Backups:', backup, flush=True)
    run('systemctl', 'stop', 'blueprint.service')
    try:
        run(*migration, '--apply')  # locks/reloads and backs up BOTH live books
        for source, target, mode in targets:
            replace_file(source, target, mode)
        run('systemctl', 'start', 'blueprint.service')
    except Exception:
        print('Rollout stopped: inspect preserved backups and both books before recovery. No other service was restarted.', file=sys.stderr)
        raise
    digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
    for _ in range(10):
        installed = json.loads(subprocess.check_output(['/usr/local/bin/bp', 'status', '--json']))
        if (installed.get('daemon_verification') == 'verified executable'
                and installed.get('daemon', {}).get('sha256') == digest):
            break
        time.sleep(.5)  # Type=simple can return before the startup identity write.
    (review / 'installed-status.json').write_text(json.dumps(installed, indent=2) + '\n')
    if (installed.get('schema_version') != 2 or installed.get('producer', {}).get('sha256') != digest
            or installed.get('daemon_verification') != 'verified executable'
            or installed.get('daemon', {}).get('sha256') != digest):
        raise RuntimeError('installed CLI/daemon verification incomplete; inspect installed-status.json')
    run(sys.executable, '/srv/server-main/bin/otonom-yakit-bekcisi.py', '--kuru')
    run('/usr/local/bin/bp', 'bar', 'probot-studio-astra')
    print('CLI and daemon verified:', digest, '\nEvidence:', review, flush=True)


if __name__ == '__main__':
    main()

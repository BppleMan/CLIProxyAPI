#!/usr/bin/env python3
"""Owner-only backup of persistent CPA OAuth/config state, never logs secrets."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context', default='admin@homelab-cluster')
    parser.add_argument('--namespace', default='cliproxyapi')
    parser.add_argument('--deployment', default='cliproxyapi')
    parser.add_argument('--destination', required=True, type=Path)
    args = parser.parse_args()
    dest = args.destination.expanduser().resolve()
    dest.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if dest.parent.stat().st_mode & 0o077:
        parser.error('destination directory must have mode 0700')
    temporary = dest.with_name(dest.name + '.partial')
    if dest.exists():
        parser.error('destination already exists')
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, 'wb') as out:
            subprocess.run([
                'kubectl', '--context', args.context, '-n', args.namespace,
                'exec', 'deploy/' + args.deployment, '-c', 'cliproxyapi', '--',
                'tar', 'cf', '-', '-C', '/data', 'auth', 'config.yaml', 'models.json',
            ], stdout=out, check=True)
        providers = []
        with tarfile.open(temporary) as archive:
            for member in archive.getmembers():
                parts = Path(member.name).parts
                if Path(member.name).is_absolute() or '..' in parts or member.issym() or member.islnk():
                    raise ValueError('unsafe archive member')
                if member.isfile() and member.name.startswith('auth/') and member.name.endswith('.json'):
                    credential = json.load(archive.extractfile(member))
                    if not isinstance(credential, dict) or not credential.get('type'):
                        raise ValueError('invalid credential JSON')
                    providers.append(credential['type'])
            # CPA already parsed this config; management may have rewritten it as YAML.
            if not archive.extractfile('config.yaml').read().strip():
                raise ValueError('empty runtime configuration')
            json.load(archive.extractfile('models.json'))
        if not providers:
            raise ValueError('no OAuth credential files found')
        os.link(temporary, dest)
        temporary.unlink()
        digest = hashlib.sha256(dest.read_bytes()).hexdigest()
        print(json.dumps({'archive': str(dest), 'credentials': len(providers),
                          'providers': providers, 'sha256': digest}))
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


if __name__ == '__main__':
    main()

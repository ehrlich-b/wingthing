"""Check the required Home baseline and candidate commit identities."""
import argparse
import os
from pathlib import Path
import subprocess

BASE = 'e509bea54ed562fe825defebba7be7a53144840a'
PREVIOUS = 'b067c1d06e5cf9e57c74256c60e89aa61f96da49'
ORIGINAL = '537b462e0ee577587af78061b4b4afe7b61eff90'
IDENTITY = 'Bryan Ehrlich <ehrlich.bryan@gmail.com>'

def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()

parser = argparse.ArgumentParser()
parser.add_argument('check', choices=['host', 'previous', 'base', 'original', 'identity'])
check = parser.parse_args().check
assert os.geteuid() != 0, 'Checks must run without root'
def baseline(destination, commit):
    directory = Path(destination)
    source = '/work/cache/git/' + commit + '.git'
    if not directory.exists():
        subprocess.run(['git', 'clone', source, str(directory)], check=True)
        subprocess.run(['git', '-C', str(directory), 'checkout', commit], check=True)
    actual = subprocess.check_output(['git', '-C', str(directory), 'rev-parse', 'HEAD'], text=True).strip()
    assert actual == commit, actual
    origin = subprocess.check_output(['git', '-C', str(directory), 'remote', 'get-url', 'origin'], text=True).strip()
    assert origin == source, origin
    print(directory, actual, origin)

if check == 'host':
    host = os.uname().nodename
    print('Host:', host, 'UID:', os.geteuid())
    print('Kernel tests blocked on forge.' if host == 'forge' else 'Ordinary registered gates permitted.')
    print('No root, kernel modules or block-device operations are used by this Home check.')
elif check == 'previous':
    baseline('../sources/previous', PREVIOUS)
    subprocess.run(['git', 'merge-base', '--is-ancestor', PREVIOUS, 'HEAD'], check=True)
    print(git('log', '--oneline', PREVIOUS + '..HEAD'))
elif check == 'base':
    baseline('../sources/one-shell', BASE)
    subprocess.run(['git', 'merge-base', '--is-ancestor', BASE, 'HEAD'], check=True)
    print(git('log', '--format=%H %s', BASE + '..HEAD'))
    print('Host:', os.uname().nodename)
elif check == 'original':
    baseline('../wingthing', ORIGINAL)
    subprocess.run(['git', 'merge-base', '--is-ancestor', ORIGINAL, BASE], check=True)
    print(git('show', '--no-patch', '--format=fuller', BASE, ORIGINAL))
else:
    rows = git('log', '--format=%H|%an <%ae>|%cn <%ce>', BASE + '..HEAD').splitlines()
    assert rows, 'The candidate must be committed'
    for row in rows:
        commit, author, committer = row.split('|')
        assert author == committer == IDENTITY, row
        print(commit, author, committer)
    print('Browser no-dialog assertions: test/web/look.mjs, WT_LOOK_SHELL=home')

"""Check the required Home baseline and candidate commit identities."""
import argparse
import os
import subprocess

BASE = 'e509bea54ed562fe825defebba7be7a53144840a'
ORIGINAL = '537b462e0ee577587af78061b4b4afe7b61eff90'
IDENTITY = 'Bryan Ehrlich <ehrlich.bryan@gmail.com>'

def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()

parser = argparse.ArgumentParser()
parser.add_argument('check', choices=['base', 'original', 'identity'])
check = parser.parse_args().check
assert os.geteuid() != 0, 'Checks must run without root'
if check == 'base':
    subprocess.run(['git', 'merge-base', '--is-ancestor', BASE, 'HEAD'], check=True)
    print(git('log', '--format=%H %s', BASE + '..HEAD'))
    print('Host:', os.uname().nodename)
elif check == 'original':
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

#!/usr/bin/env python3
import json, os, subprocess, sys
from pathlib import Path
root=Path(__file__).resolve().parents[3]
base='537b462e0ee577587af78061b4b4afe7b61eff90'
def git(*args):return subprocess.check_output(['git',*args],cwd=root,text=True).strip()
if sys.argv[1]=='source':
    assert git('remote','get-url','origin')=='/work/cache/git/'+base+'.git'
    assert git('merge-base',base,'HEAD')==base
    assert git('branch','--show-current')=='codex/wt-session-page-real-loop-1109-20261109'
    print('PASS: supplied checkout from required cache; candidate descends from',base)
else:
    assert os.geteuid()!=0
    rows=git('log',base+'..HEAD','--format=%an <%ae>|%cn <%ce>|%s').splitlines()
    assert rows
    for row in rows:
        author,committer,subject=row.split('|')
        assert author==committer=='Bryan Ehrlich <ehrlich.bryan@gmail.com>'
        assert subject.endswith('.') and '\n' not in subject
    for name in ['baseline','candidate']:
        data=json.loads((root.parent/'evidence'/name/'checks.json').read_text())
        assert data['dialogs']==[]
    print('PASS: non-root headless run, no browser dialogs, all candidate commits authored and committed by Bryan Ehrlich.')

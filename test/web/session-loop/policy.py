#!/usr/bin/env python3
import json, os, subprocess, sys
from pathlib import Path
root=Path(__file__).resolve().parents[3]
base='537b462e0ee577587af78061b4b4afe7b61eff90'
def git(*args):return subprocess.check_output(['git',*args],cwd=root,text=True).strip()
if sys.argv[1]=='source':
    assert git('remote','get-url','origin')=='/work/cache/git/'+base+'.git'
    assert git('merge-base',base,'HEAD')==base
    assert git('branch','--show-current')=='codex/wt-session-page-real-loop-1109-r11100019-20261110'
    previous = '771259dbaba4b39f405ce63344732b9d46d70375'
    for folder, head in [('sources/previous', previous), ('wingthing', base)]:
        checkout = root.parent/folder
        assert subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=checkout, text=True).strip() == head
        assert subprocess.check_output(['git', 'remote', 'get-url', 'origin'], cwd=checkout, text=True).strip() == '/work/cache/git/'+head+'.git'
    assert git('merge-base', previous, 'HEAD') == previous
    print('PASS: pinned previous and base clones; candidate builds on previous attempt.')
    print('PASS: supplied checkout from required cache; candidate descends from',base)
elif sys.argv[1]=='host':
    import socket
    print('Host:', socket.gethostname(), '; ordinary registered gates only; no privileged device operations.')
    assert os.geteuid()!=0
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

#!/usr/bin/env python3
"""Finite acceptance against builds of the pinned base and current checkout."""
import json, os, random, socket, subprocess
from pathlib import Path
ROOT = Path(__file__).resolve().parents[3]
LANE = ROOT.parent
BASE = '537b462e0ee577587af78061b4b4afe7b61eff90'
SCRATCH = LANE/'scratch'
EVIDENCE = LANE/'evidence'
for folder in [SCRATCH, EVIDENCE, SCRATCH/'t', SCRATCH/'npm']:
    folder.mkdir(parents=True, exist_ok=True)
ENV = dict(os.environ, TMPDIR=str(SCRATCH/'t'), npm_config_cache=str(SCRATCH/'npm'), PLAYWRIGHT_BROWSERS_PATH='/work/cache/ms-playwright')
def run(args, cwd=ROOT, env=ENV):
    print('+', ' '.join(map(str,args)), flush=True)
    subprocess.run(['nice','-n','10',*map(str,args)],cwd=cwd,env=env,check=True)
def ports():
    for _ in range(100):
        port=random.randrange(22000,39000)
        sockets=[]
        try:
            for n in range(3):
                s=socket.socket();s.bind(('127.0.0.1',port+n));sockets.append(s)
            return port
        except OSError: pass
        finally:
            for s in sockets:s.close()
    raise RuntimeError('no loopback fixture ports available')
def log_run(args, target, env=ENV):
    with target.open('w') as f:
        subprocess.run(['nice','-n','10',*map(str,args)],cwd=ROOT,env=env,stdout=f,stderr=subprocess.STDOUT,check=True)
    print(target.relative_to(LANE), flush=True)
base_src=SCRATCH/'baseline-src'
if not base_src.exists():
    run(['git','worktree','add','--detach',base_src,BASE])
assert subprocess.check_output(['git','rev-parse','HEAD'],cwd=base_src,text=True).strip()==BASE
# Both executables embed their own source tree's assets. No tunnel is mocked.
for source, name in [(base_src,'baseline'),(ROOT,'candidate')]:
    run(['npm','ci'],cwd=source/'web')
    run(['npm','run','build'],cwd=source/'web')
    binary=SCRATCH/('wt-'+name)
    run(['go','build','-p','2','-o',binary,'./cmd/wt'],cwd=source)
    env=dict(ENV, WT_LOOP_BIN=str(binary), WT_LOOP_OUT=str(EVIDENCE/name), WT_LOOP_SCRATCH=str(LANE), WT_LOOP_PORT=str(ports()), WT_LOOP_BASELINE='1' if name=='baseline' else '0')
    log_run(['node','test/web/session-loop.mjs'],EVIDENCE/(name+'-loop.log'),env)
    data=json.loads((EVIDENCE/name/'checks.json').read_text())
    if name=='baseline':
        assert len(data['failures'])==6, data
        assert [c['name'] for c in data['checks']]==['claude direct user and assistant within 3s'], data
    else:
        assert len(data['checks'])==7 and not data['failures'] and not data['dialogs'], data
run(['go','test','-p','2','./internal/egg','-run','^(TestCodexTranscript.*|TestLifecycleExactIdentityPartialReplayAndConcurrentReaders|TestCodexUnboundCaptureUnavailableWithoutGuessing)$','-count=1'])
run(['go','test','-p','2','./internal/ws','-run','^TestClientRegistersLatePTYAttachAfterAuthorization$','-count=1'])
run(['node','--test','web/test/codex-transcript.test.js'])
run(['go','build','-p','2','-o','.scratch/look-public','./test/web/public-server'])
look_env=dict(ENV, WT_LOOK_BIN=str(SCRATCH/'wt-candidate'), WT_LOOK_PORT=str(ports()), WT_LOOK_SHOTS=str(EVIDENCE/'look'), WT_LOOK_ONLY='03-session', PLAYWRIGHT_MODULE='../../web/node_modules/playwright/index.mjs')
log_run(['node','test/web/look.mjs'],EVIDENCE/'look.log',look_env)
look=json.loads((EVIDENCE/'look/capture.json').read_text())
assert look['complete'] and not look['overflow'], look
print('PASS: baseline reproduced; both native providers, PTY input order, shell fallback, fragment tests and existing look checks passed.')

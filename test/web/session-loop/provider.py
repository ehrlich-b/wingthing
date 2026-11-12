#!/usr/bin/env python3
"""Offline interactive providers: native transcript files and real installed hooks."""
import json, os, re, subprocess, sys, time, tty, uuid
from pathlib import Path
agent = Path(sys.argv[0]).name
args = sys.argv[1:]
if args == ['--version']:
    print('codex-cli 0.159.3' if agent == 'codex' else '2.1.150 (Claude Code)')
    sys.exit(0)
hooks = {}
if agent == 'claude':
    sid = args[args.index('--session-id')+1]
    settings = json.loads(Path(args[args.index('--settings')+1]).read_text())
    for event, definitions in settings['hooks'].items():
        hooks[event] = [h['command'] for d in definitions for h in d['hooks'] if h['type'] == 'command']
    transcript = Path.home()/'.claude/projects'/str(Path.cwd()).replace('/', '-')/(sid+'.jsonl')
else:
    sid = str(uuid.uuid4())
    for i,a in enumerate(args[:-1]):
        if a == '-c' and args[i+1].startswith('hooks.') and 'command=' in args[i+1]:
            value = args[i+1]
            hooks[value.split('=',1)[0].split('.',1)[1]] = [json.loads(re.search(r'command=("(?:\\.|[^"\\])*")', value).group(1))]
    transcript = Path.home()/'.codex/sessions/2026/11/09'/('rollout-2026-11-09-'+sid+'.jsonl')
transcript.parent.mkdir(parents=True, exist_ok=True)
def append(record):
    with transcript.open('a') as f: f.write(json.dumps(record)+'\n')
def hook(event, **extra):
    data = dict(hook_event_name=event, session_id=sid, **extra)
    for command in hooks.get(event, []):
        subprocess.run(['/bin/sh','-c',command], input=json.dumps(data).encode(), check=True, stdout=subprocess.DEVNULL)
if agent == 'codex': append(dict(type='session_meta', payload=dict(id=sid, cwd=str(Path.cwd()))))
tty.setraw(0)
hook('SessionStart', source='startup')
os.write(1, (agent+' ready: native transcript\r\n').encode())
buffer = b''
repeated = 0
while True:
    chunk = os.read(0,1)
    if not chunk: break
    if chunk not in (b'\r',b'\n'):
        buffer += chunk
        continue
    text = buffer.decode().removeprefix('\x1b[200~').removesuffix('\x1b[201~'); buffer=b''
    if not text: continue
    with (Path.cwd()/(agent+'-pty.jsonl')).open('a') as f: f.write(json.dumps(text)+'\n')
    turn_id = str(uuid.uuid4())
    hook('UserPromptSubmit', prompt=text, **(dict(turn_id=turn_id) if agent == 'claude' else {}))
    if agent == 'codex' and text == 'Repeat without a turn ID':
        repeated += 1
        deadline = time.monotonic() + 15
        release = Path.cwd()/'release-transcript'
        while not release.exists() or int(release.read_text()) < repeated:
            if time.monotonic() > deadline: raise TimeoutError('native transcript was not released')
            time.sleep(0.025)
    answer = 'Reply to '+text
    for role, content in [('user',text),('assistant',answer)]:
        if agent == 'claude': append(dict(type=role, sessionId=sid, message=dict(role=role, content=content, stop_reason='end_turn' if role=='assistant' else None)))
        else: append(dict(type='response_item', payload=dict(type='message', role=role, content=[dict(type='input_text' if role=='user' else 'output_text', text=content)])))
    os.write(1, ('INPUT '+text+'\r\n'+answer+'\r\n').encode())
    hook('Stop')

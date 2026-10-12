#!/usr/bin/env python3
"""Re-run unchanged storyboard cards; preserve baseline and fresh receipts."""
import hashlib, json, os, re, shutil, subprocess
from pathlib import Path
root = Path(__file__).resolve().parents[2]
out = root / 'evidence/fix-3'
baseline = json.loads((root/'evidence/run-1/results.json').read_text())['statuses']
env = dict(os.environ, GOFLAGS='-p=4', GOCACHE=str(root/'.scratch/northstar/go-build'), GOMODCACHE=str(root/'.scratch/northstar/go-mod'))
sources = sorted(list((root/'cmd/wt').glob('*storyboard*test.go')) + list((root/'cmd/wt').glob('northstar*test.go')) + list((root/'test/storyboard').glob('*.py')))
initial = {str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sources}
records = []
for n in range(1,4):
 run = out/f'run-{n}'
 run.mkdir(exist_ok=False)
 events = []
 with (run/'go-test.jsonl').open('w') as raw, (run/'output.txt').open('w') as readable:
  proc = subprocess.Popen(['nice','-n','15','go','test','-json','-count=1','-run','^TestStoryboard','-timeout','20m','./cmd/wt'], cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
  for line in proc.stdout:
   raw.write(line); raw.flush()
   try: event = json.loads(line)
   except json.JSONDecodeError: readable.write(line); continue
   events.append(event)
   readable.write(event.get('Output','')); readable.flush()
   if event.get('Action') in ['pass','fail','skip'] and event.get('Test','').startswith('TestStoryboard') and '/' not in event['Test']:
    print(f'run {n}: {event["Test"]}: {event["Action"]}',flush=True)
  code = proc.wait()
 statuses = {e['Test']:e['Action'].upper() for e in events if e.get('Test') and '/' not in e['Test'] and e.get('Action') in ['pass','fail','skip']}
 if set(statuses) != set(baseline) or 'SKIP' in statuses.values(): raise RuntimeError(f'incomplete run {n}: {statuses}')
 transcripts = {}
 for name in statuses:
  card = run/'cards'/f'{name}.txt'
  card.parent.mkdir(exist_ok=True)
  card.write_text(''.join(e.get('Output','') for e in events if e.get('Test','').split('/')[0] == name))
  transcripts[name] = [str(card.relative_to(root))]
 for e in events:
  match = re.search(r'PTY screen transcript: (.+)\.txt',e.get('Output',''))
  if not match: continue
  base = Path(match[1]); name = e.get('Test','').split('/')[0]
  for suffix in ['.txt','.ansi']:
   src = Path(str(base)+suffix)
   dst = run/'transcripts'/src.relative_to(root/'.scratch')
   dst.parent.mkdir(parents=True,exist_ok=True); shutil.copy2(src,dst)
   transcripts[name].append(str(dst.relative_to(root)))
 regressions = [t for t,s in baseline.items() if s=='PASS' and statuses[t]!='PASS']
 record = {'run':n,'exit_code':code,'statuses':statuses,'regressions':regressions,'transcripts':transcripts}
 (run/'results.json').write_text(json.dumps(record,indent=2)+'\n')
 records.append(record)
 print(f'run {n} totals: {list(statuses.values()).count("PASS")} PASS / {list(statuses.values()).count("FAIL")} FAIL; regressions: {regressions}',flush=True)
final = {str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sources}
if initial != final: raise RuntimeError('acceptance test sources changed during verification')
base_hashes = {str(p.relative_to(root)):subprocess.check_output(['git','show','580d4d9:'+str(p.relative_to(root))],cwd=root) for p in sources}
if any(hashlib.sha256(data).hexdigest()!=initial[path] for path,data in base_hashes.items()): raise RuntimeError('acceptance tests differ from base')
fixed = [t for t,s in baseline.items() if s=='FAIL' and all(r['statuses'][t]=='PASS' for r in records)]
summary = {'base':'580d4d9','runs':records,'fixed':fixed,'regressions':sorted({t for r in records for t in r['regressions']}),'unchanged_acceptance_sha256':initial}
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
print(json.dumps({'fixed':fixed,'regressions':summary['regressions']}),flush=True)

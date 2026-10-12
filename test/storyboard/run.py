#!/usr/bin/env python3
"""Run every storyboard journey three times, retain red results, report flakes."""
from pathlib import Path
import json, os, re, shutil, subprocess, sys
from catalog import ROOT, catalog

rows=catalog()
evidence=ROOT/'evidence'
evidence.mkdir(exist_ok=True)
(evidence/'cards.json').write_text(json.dumps(rows,indent=2)+'\n')
env=os.environ.copy()
env.update(GOFLAGS='-p=4',GOCACHE=str(ROOT/'.scratch/northstar/go-build'),GOMODCACHE=str(ROOT/'.scratch/northstar/go-mod'))
# Supported repo prerequisite for terminal-only acceptance; no browser assets
# or browser/iPhone acceptance is inferred from this embedding placeholder.
subprocess.run(['make','web/dist'],cwd=ROOT,env=env,check=True)
subprocess.run(['nice','-n','15','go','build','-buildvcs=false','-o',str(ROOT/'.scratch/storyboard-wt'),'./cmd/wt'],cwd=ROOT,env=env,check=True)
subprocess.run(['nice','-n','15','go','test','-run','^$','./cmd/wt'],cwd=ROOT,env=env,check=True)
results=[]
for n in range(1,4):
 run=evidence/f'run-{n}'
 if run.exists():raise SystemExit(f'refusing to overwrite evidence {run}')
 run.mkdir()
 events=[]
 with (run/'go-test.jsonl').open('w') as log, (run/'output.txt').open('w') as readable:
  proc=subprocess.Popen(['nice','-n','15','go','test','-json','-count=1','-run','^TestStoryboard','-timeout','20m','./cmd/wt'],cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
  for line in proc.stdout:
   log.write(line);log.flush()
   try:event=json.loads(line)
   except json.JSONDecodeError:readable.write(line);continue
   events.append(event)
   if 'Output' in event:readable.write(event['Output'])
   if event.get('Action') in ['pass','fail','skip'] and event.get('Test','').startswith('TestStoryboard') and '/' not in event['Test']:
    print(f'run {n}: {event["Test"]}: {event["Action"]}',flush=True)
  code=proc.wait()
 statuses={e['Test']:e['Action'].upper() for e in events if e.get('Test') and '/' not in e['Test'] and e.get('Action') in ['pass','fail','skip']}
 expected={r['test'] for r in rows if r['test']}
 if set(statuses)!=expected or 'SKIP' in statuses.values():raise SystemExit(f'incomplete run {n}: {set(statuses)^expected}; statuses={statuses}')
 transcripts={}
 for test in sorted(expected):
  observation=run/'cards'/f'{test}.txt'
  observation.parent.mkdir(exist_ok=True)
  observation.write_text(''.join(e.get('Output','') for e in events if e.get('Test','').split('/')[0]==test))
  transcripts[test]=[str(observation.relative_to(ROOT))]
 for event in events:
  match=re.search(r'PTY screen transcript: (.+)\.txt',event.get('Output',''))
  if match:
   path=Path(match[1]); top=event.get('Test','').split('/')[0]
   for suffix in ['.txt','.ansi']:
    src=Path(str(path)+suffix)
    if not src.exists():raise SystemExit(f'missing transcript {src}')
    rel=src.relative_to(ROOT/'.scratch')
    dst=run/'transcripts'/rel;dst.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(src,dst)
    transcripts.setdefault(top,[]).append(str(dst.relative_to(ROOT)))
 # Retain provider, timing and metadata receipts from unchanged Northstar
 # helpers too, but never build caches, binaries or disposable authority dirs.
 for directory in ['northstar/northstar-evidence','evidence/09/pty','storyboard-evidence']:
  src=ROOT/'.scratch'/directory
  if src.exists():shutil.copytree(src,run/'journey-receipts'/directory,dirs_exist_ok=True)
 record={'run':n,'exit_code':code,'statuses':statuses,'transcripts':transcripts}
 (run/'results.json').write_text(json.dumps(record,indent=2)+'\n');results.append(record)
 for directory in ['northstar/northstar-evidence','evidence/09/pty','storyboard-evidence']:
  src=ROOT/'.scratch'/directory
  if src.exists():shutil.rmtree(src)
summary={'base':'f8f9ebd96e1c72336ffd796c8d496ecda0a85619','runs':results,'cards':[]}
lines=['# Storyboard card → executable acceptance map','',
 'Stable card IDs include the flow and HTML article ID; ordinal links follow the storyboard display order. All 51 flow 1–6 cards execute with real kernel PTYs and a built `wt`. Flows 7–12 are PENDING-FEATURE only.', '',
 'FAIL includes missing product prerequisites. In particular Linux cannot prove physical iPhone screens, camera/Keychain, cellular, cryptography or live revocation; phone cards fail at the unavailable CLI prerequisite and do not claim downstream execution. Native fixture evidence does not establish real vendor authentication/readiness. The supported `make web/dist` placeholder is used only for Go embedding; no browser acceptance is claimed.', '',
 '| Card | Scenario | Test | Screen assertion | Observable state assertion | Runs 1/2/3 | Status |',
 '|---|---|---|---|---|---|---|']
for row in rows:
 if row['test']:
  statuses=[r['statuses'][row['test']] for r in results]
  status=statuses[0] if len(set(statuses))==1 else 'FLAKY'
  item={**row,'statuses':statuses,'status':status,'transcripts':[r['transcripts'].get(row['test'],[]) for r in results]}
 else:item={**row,'statuses':[]};status='PENDING-FEATURE'
 summary['cards'].append(item)
 vals=[f'[{row["card_id"]}]({row["source"]})',row['title'],row['test'] or '—',row['screen'] or 'Paid/deferred target',row['state'] or 'Not executed by this suite',' / '.join(item['statuses']) or '—',status]
 lines.append('| '+' | '.join(str(v).replace('|','\\|').replace('\n',' ') for v in vals)+' |')
summary['counts']={s:sum(r['status']==s for r in summary['cards']) for s in ['PASS','FAIL','FLAKY','PENDING-FEATURE']}
(evidence/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
(ROOT/'CARD-TEST-MAP.md').write_text('\n'.join(lines)+'\n')
(evidence/'CARD-TEST-MAP.md').write_text(('\n'.join(lines)+'\n').replace('](evidence/',']('))
flakes=[r['test'] for r in summary['cards'] if r['status']=='FLAKY']
(evidence/'FLAKY.md').write_text('# Flakiness across three complete runs\n\n'+('\n'.join(f'- {f}' for f in flakes) if flakes else 'No test changed PASS/FAIL outcome across the three runs.')+'\n')
from analyze import annotate_summary
summary=annotate_summary()
print(json.dumps({'counts':summary['counts'],'flaky_tests':summary['flaky_tests']}),flush=True)
# Product failures are expected, but preserve the conventional nonzero result.
sys.exit(1 if summary['counts']['FAIL'] or summary['flaky_tests'] else 0)

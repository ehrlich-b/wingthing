#!/usr/bin/env python3
"""Include subtest flakiness even when a stable failing parent masks it."""
from pathlib import Path
import json
from catalog import ROOT

def annotate_summary():
 path=ROOT/'evidence/summary.json'
 summary=json.loads(path.read_text())
 runs=[];durations=[]
 for record in summary['runs']:
  statuses={};elapsed=None
  for line in (ROOT/f'evidence/run-{record["run"]}/go-test.jsonl').read_text().splitlines():
   try:event=json.loads(line)
   except json.JSONDecodeError:continue
   if event.get('Action') in ['pass','fail','skip']:
    if event.get('Test','').startswith('TestStoryboard'):statuses[event['Test']]=event['Action'].upper()
    elif not event.get('Test'):elapsed=event.get('Elapsed')
  record['all_test_statuses']=statuses;runs.append(statuses);durations.append(elapsed)
 flakes=[]
 for name in sorted(set().union(*runs)):
  states=[r.get(name,'NOT-RUN') for r in runs]
  if len(set(states))>1:flakes.append({'test':name,'outcomes':states})
 summary['flaky_tests']=flakes
 summary['run_durations_seconds']=durations
 summary['counting']='Counts are one parent outcome per card. Flaky-tests list also includes subtests whose outcome or execution differed, even beneath a consistently failing card.'
 path.write_text(json.dumps(summary,indent=2)+'\n')
 text='# Flakiness across three complete runs\n\n'
 text+='Parent/card outcomes and subtest outcomes were both compared. A stable failing card cannot hide a flaky subcase.\n\n'
 if flakes:
  text+='| Test | Run 1 | Run 2 | Run 3 |\n|---|---|---|---|\n'
  text+=''.join('| '+r['test']+' | '+' | '.join(r['outcomes'])+' |\n' for r in flakes)
 else:text+='No parent or subtest changed outcome across the three complete runs.\n'
 (ROOT/'evidence/FLAKY.md').write_text(text)
 return summary

if __name__=='__main__':
 s=annotate_summary();print(json.dumps({'counts':s['counts'],'flaky_tests':s['flaky_tests'],'durations_seconds':s['run_durations_seconds']}))

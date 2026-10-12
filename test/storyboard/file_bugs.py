#!/usr/bin/env python3
"""File exactly one precise board bug for each observed failing card."""
from pathlib import Path
import json, os, subprocess
from catalog import ROOT

summary=json.loads((ROOT/'evidence/summary.json').read_text())
receipts=ROOT/'evidence/bug-receipts'
receipts.mkdir(exist_ok=True)
# Source the board's canonical environment once. Pass all issue text using
# argv/body files, never shell interpolation; this is explicitly authorized by
# the brief. Partial reruns are idempotent via the durable per-card receipt.
loaded=subprocess.check_output(['bash','-c','source /work/lanes/board/env.sh; env -0'])
env={k.decode():v.decode() for k,v in (item.split(b'=',1) for item in loaded.split(b'\0') if b'=' in item)}
symptoms={
 '01/card-1':'Empty navigator omits connection header and first-run terms',
 '01/card-4':'Bare wt omits local agent detection and no-agent chooser',
 '01/card-5':'New-session flow omits exact installer consent',
 '01/card-6':'Onboarding omits provider verification and native login handoff',
 '01/card-7':'Onboarding omits stage-specific install/PATH/auth recovery',
 '02/card-0':'Linux cold scoped Claude parent is refused before readiness',
 '02/card-1':'Linux explicit-cwd scoped parent is refused before readiness',
 '02/card-2':'Linux dashboard scoped parent cannot launch on local or forge',
 '02/card-4':'Native launch denial omits provider cause and selected policy',
 '02/card-5':'Concurrent Linux scoped parents and surviving waiter cannot reach readiness',
 '03/card-2':'Unacknowledged unread attention disappears after navigator reopen',
 '03/card-5':'Stale remote attachment retries SSH and reports unknown outcome',
 '04/card-3':'Offline forge attachment remains enabled instead of validation refusal',
 '06/card-1':'Linux scoped parent cannot obtain durable forge run receipts',
 '06/card-3':'Session details omit effective workspace/provider/network authority',
 '06/card-5':'Linux provider-policy denial loses phase-specific policy evidence',
}
bugs=[]
for card in summary['cards']:
 if not card['test'] or 'FAIL' not in card['statuses']:continue
 test=card['test']; existing=receipts/f'{test}.json'
 if existing.exists():
  bugs.append(json.loads(existing.read_text()));continue
 failing=card['statuses'].index('FAIL')
 events=[]
 for line in (ROOT/f'evidence/run-{failing+1}/go-test.jsonl').read_text().splitlines():
  try:e=json.loads(line)
  except json.JSONDecodeError:continue
  if e.get('Test','').split('/')[0]==test and e.get('Output'):events.append(e['Output'])
 actual=''.join(events)
 # Include the first assertion failure with its complete actual VT screen.
 start=next((actual.find(x) for x in ['screen timeout (','phone card blocked:','expected screen contains','legacy task did not complete:'] if x in actual),0)
 actual=actual[start:start+10000].replace('\r','')
 transcripts=[p for p in card['transcripts'][failing] if p.endswith('.txt')]
 if not transcripts:raise SystemExit(f'{test} has no screen transcript; do not file unsupported bug')
 body=(f'Card: {card["card_id"]}, ordinal #{card["ordinal"]}: {card["title"]}\n'
       f'Source: /work/lanes/wt-e2e/wingthing/{card["source"]}\n'
       f'Carrier: {summary["base"]}; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.\n\n'
       f'Expected screen: {card["screen"]}\nExpected observable state: {card["state"]}\n\n'
       f'Actual outcome across three runs: {", ".join(card["statuses"])}.\n'
       f'Actual assertion/screen and state evidence:\n{actual}\n\n'
       'Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):\n'+''.join(f'/work/lanes/wt-e2e/wingthing/{p}\n' for p in transcripts)+
       f'\nReproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^{test}$" ./cmd/wt\n')
 if card['flow']==5:
  body+='\nThe failure is a missing owner CLI prerequisite. Downstream iPhone camera/Keychain/crypto/RPC/route assertions are unexercised, not separately proven failures. A physical iPhone acceptance gate remains mandatory after that prerequisite ships.\n'
 bodyfile=receipts/f'{test}.md';bodyfile.write_text(body)
 # Explicitly estimated remaining implementation cost, not an observed cost.
 metadata={'V':6,'C':1.3,'vc_ratio':6/1.3,'bryan_minutes':0,'coordinator_touches':3,'tokens':300000,'codex_weekly_points':3,'score_basis':'Estimated remaining work: touches/3 + minutes/5 + weekly points/10 = 1.3. V=6: free storyboard acceptance gap; per-card scope, no claim of shipped security. Source cost data unavailable; coordinator review gated.'}
 symptom=symptoms.get(card['card_id'],f'Missing phone CLI prerequisite: {card["title"]}')
 result=subprocess.run(['bd','create',f'Wingthing {card["card_id"]}: {symptom}', '--type','bug','--body-file',str(bodyfile),'-l','lane:burn,project:wingthing,host:forge,score:estimated,needs:coordinator','--metadata',json.dumps(metadata),'--acceptance',f'{test} passes three times against the real built wt on Linux with the stated screen/state assertions; preserve existing Northstar tests. For physical-phone behavior record separate real-device evidence, never substitute fixture PTYs.','--priority','2','--json'],cwd='/work/lanes/board',env=env,text=True,capture_output=True)
 if result.returncode:raise SystemExit(f'bd create failed for {test}: {result.stderr}\n{result.stdout}')
 raw=json.loads(result.stdout)
 record={'card':card['card_id'],'test':test,'id':raw['id'],'statuses':card['statuses'],'body':str(bodyfile.relative_to(ROOT)),'transcripts':transcripts,'board_receipt':raw}
 existing.write_text(json.dumps(record,indent=2)+'\n');bugs.append(record)
 print(f'{card["card_id"]}: {raw["id"]}',flush=True)
(ROOT/'evidence/bugs.json').write_text(json.dumps(bugs,indent=2)+'\n')
lines=['# Observed storyboard bug beads','', '| Card | Failing acceptance test | Bead | Outcomes | Screen evidence |','|---|---|---|---|---|']
for b in bugs:
 lines.append(f'| {b["card"]} | {b["test"]} | {b["id"]} | {" / ".join(b["statuses"])} | [{Path(b["transcripts"][0]).name}]({b["transcripts"][0].removeprefix("evidence/")}) |')
(ROOT/'evidence/BUG-LIST.md').write_text('\n'.join(lines)+'\n')
print(f'{len(bugs)} bug beads filed',flush=True)

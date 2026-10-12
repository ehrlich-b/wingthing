#!/usr/bin/env python3
"""Summarize immutable card results separately from executable backend evidence."""
import hashlib, json, subprocess, sys
from pathlib import Path
root=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(root/'test/storyboard'))
from catalog import catalog
original=root
source_commit='580d4d9f8241fd37f56011bced9535872128eeda'
base='3a0b7fa8'
files=sorted((root/'cmd/wt').glob('storyboard*_pty_test.go'))
hashes={}
for p in files:
 data=subprocess.check_output(['git','-C',str(original),'show',f'{source_commit}:{p.relative_to(root)}'])
 assert p.read_bytes()==data,p
 hashes[str(p.relative_to(root))]=hashlib.sha256(data).hexdigest()
(root/'evidence/phone/imported-test-hashes.json').write_text(json.dumps(hashes,indent=2)+'\n')
measured=['cmd/wt/northstar_pty_test.go','cmd/wt/phone_storyboard_e2e_test.go','cmd/wt/pair.go','internal/phonepair/pairing.go','internal/phonepair/testpeer/main.go','internal/phonepair/testpeer/client.go','internal/wing/phone.go']
(root/'evidence/phone/tested-source-hashes.json').write_text(json.dumps({p:hashlib.sha256((root/p).read_bytes()).hexdigest() for p in measured},indent=2)+'\n')
summary=json.loads((root/'evidence/summary.json').read_text())
# run.py retains its historical e2e-base metadata; annotate actual execution
# without altering a measured PASS/FAIL or transcript.
summary['imported_suite_base']=subprocess.check_output(['git','-C',str(original),'rev-parse',source_commit+'^'],text=True).strip()
summary['base']=subprocess.check_output(['git','rev-parse',base],cwd=root,text=True).strip()
summary['test_source_commit']=source_commit
(root/'evidence/summary.json').write_text(json.dumps(summary,indent=2)+'\n')
prior=json.loads((root/'evidence/phone/original-baseline.json').read_text())
prior_cards={r['test']:r for r in prior['cards'] if r['test']}
regressions=[];improvements=[]
for c in summary['cards']:
 if not c['test']:continue
 old=prior_cards[c['test']]
 if old['status']=='PASS' and c['status']!='PASS':regressions.append(c['test'])
 if old['status']!='PASS' and c['status']=='PASS':improvements.append(c['test'])
# Compare named subcases as well as card parents.
prior_tests=prior['runs'][0].get('all_test_statuses',{})
subcase_regressions=[]
for name in sorted(prior_tests):
 states=[r.get('all_test_statuses',{}).get(name,'NOT-RUN') for r in summary['runs']]
 old=[r.get('all_test_statuses',{}).get(name,'NOT-RUN') for r in prior['runs']]
 if all(s=='PASS' for s in old) and not all(s=='PASS' for s in states):subcase_regressions.append({'test':name,'before':old,'after':states})
backend={}
for name in ['journey-complete-final-harness-three-runs','protocol-three-runs','relay-three-runs']:
 events=[json.loads(s) for s in (root/f'evidence/phone/{name}.jsonl').read_text().splitlines() if s.startswith('{')]
 outcomes={}
 for event in events:
  if event.get('Test') and event.get('Action') in ['pass','fail','skip']:
   outcomes.setdefault(event['Test'],[]).append(event['Action'].upper())
 assert outcomes and all(s==['PASS']*3 for s in outcomes.values()),(name,outcomes)
 backend[name]=outcomes
record={'gates':{'vet':'PASS','race phonepair/pake/relay/wing':'PASS','CLI journey race':'PASS','physical iPhone':'REQUIRED-UNRUN'},'base':summary['base'],'source':source_commit,'card_counts':summary['counts'],'card_regressions':regressions,'subcase_regressions':subcase_regressions,'card_improvements':improvements,'flaky_tests':summary['flaky_tests'],'backend':backend}
final_path=root/'evidence/phone/storyboard-final-after-harness.jsonl'
if final_path.exists():
 final=[json.loads(s) for s in final_path.read_text().splitlines() if s.startswith('{')]
 final_statuses={e['Test']:e['Action'].upper() for e in final if e.get('Test') and '/' not in e['Test'] and e.get('Action') in ['pass','fail','skip']}
 expected={r['test'] for r in summary['cards'] if r['test']}
 assert set(final_statuses)==expected,'Final full suite incomplete'
 assert all(final_statuses[c['test']]==c['status'] for c in summary['cards'] if c['test']),'Final suite changed outcome'
 record['final_full_suite_after_harness']=final_statuses
(root/'evidence/phone/results.json').write_text(json.dumps(record,indent=2)+'\n')
beads={r['test']:r['id'] for r in prior['bugs']}
physical=[
 'Real terminal QR camera scan and readable layout on the iPhone',
 'Camera scan, native screen and local-network permission',
 'Four-emoji presentation and deliberate approval on the phone',
 'Expiry presentation and fresh-QR rescan on the phone',
 'Used-invitation screen and return to scanner',
 'Changed-pin hard-stop screen and no automatic trust replacement',
 'Device-only Keychain / Secure Enclave storage and native success screen',
 'Cellular relay reconnection and acknowledgement-loss UI without replay',
 'Offline cached-state screen; no fabricated live status',
 'Phone authority and independently paired-wing UI',
 'Second physical machine pairing and separate Keychain credentials',
 'Real lost-phone/live-access revocation over network carriers',
 'Native session view, terminal rendering, input and delivery-unknown UI',
 'Native explicit stop confirmation and detach distinction',
 'Notification consent, quiet lock-screen copy, tap routing and background delivery',
 'Real Wi-Fi/Tailscale IP/MagicDNS carrier selection on both devices',
 'Denied camera/local-network Settings recovery and private manual invitation entry',
 'Native final-reply-loss recovery screen with the original grant',
]
coverage=[
 'CLI/testpeer invitation, identity, words, deadline, unchanged daemon',
 'CLI/testpeer PAKE proof and matching emoji; protocol pin tests',
 'CLI/testpeer both independent approvals; credential recovery tests',
 'Protocol expiry/used/cancel tests with deterministic clock',
 'Protocol single-use claims; CLI cancellation/new invitation',
 'Transcript pin and wrong-proof protocol tests',
 'CLI device listing; protocol registry hash, mode 0600 and persistence',
 'Opaque accountless relay routing tests; replay and revocation tests',
 'Relay unreachable/denied tests; no live iPhone cache evidence',
 'CLI/testpeer configuration denial and colliding remote untouched',
 'Protocol other-wing denial; registry bound to one wing',
 'CLI/testpeer revoke, closed live read and denied reconnect',
 'CLI/testpeer blocked inventory and attached read/input/detach',
 'CLI/testpeer exact-session stop; wing attachment/input/stop tests',
 'No physical/background notification execution; attention inventory only',
 'CLI/testpeer direct LAN; fixture Tailscale IP/MagicDNS and endpoint-order tests',
 'CLI hidden fallback link has endpoint/key/words; iOS Settings unrun',
 'CLI/testpeer recovered same grant after ignored final response; duplicate confirm',
]
lines=['# Phone acceptance results','',
 f"Base `{summary['base']}`; imported tests `{source_commit}`. All Go commands ran at nice level 15 with `GOFLAGS=-p=4`.",'',
 'Implemented fixes: preserve native needs-input attention after a wing restart; return cancelled for a cancelled invitation scan; diagnose an unpaired device in the revoke CLI. Added an in-memory Go testpeer client and a real CLI/wing/PTY backend journey.','',
 f"Full unchanged storyboard suite: {summary['counts']}. Card regressions against the earlier e2e base: {len(regressions)}; named subcase regressions: {len(subcase_regressions)}. Integration-base improvements: {', '.join(improvements) or 'none'}. Flaky tests: {len(summary['flaky_tests'])}.",'',
 'The full suite remains red, with no regressions against the imported baseline. Flow 5 has 18 FAIL/FAIL/FAIL card outcomes; the fixture does not start a main wing and every phone card deliberately ends in a physical-device fatal assertion. See ../../TEST-DISPUTES.md. Physical obligations below remain REQUIRED-UNRUN.','',
 'Backend gate: CLI/testpeer journey PASS ×3; every focused phonepair/PAKE/wing protocol case PASS ×3; phone relay cases PASS ×3. Full race on phonepair/pake/relay/wing PASS after building real web assets. go vet ./... PASS. The final CLI journey race also PASS; the final full-suite rerun after harness cleanup has identical card outcomes. Raw logs retain failed exploratory runs and the initial placeholder-assets race failure.','',
 'No bug bead was closed: each original card test is still red, so the requested three-pass condition has not been met. The backend three-pass evidence does not close a physical acceptance bead.','',
 '| Card | Bead (not closed in this task) | Card result | Backend evidence | Physical-only obligation |','|---|---|---|---|---|']
rows=[r for r in catalog() if r['flow']==5]
for row in rows:
 i=int(row['html_id'].split('-')[1])
 lines.append(f"| {row['card_id']} | {beads[row['test']]} | FAIL ×3 | {coverage[i]} | REQUIRED-UNRUN: {physical[i]} |")
lines+=['','Regression details are in results.json; raw full-suite evidence is ../run-{1,2,3}/. No push, deployment, real-device install or account/entitlement operation was performed.']
(root/'evidence/phone/RESULTS.md').write_text('\n'.join(lines)+'\n')
print(json.dumps({k:v for k,v in record.items() if k!='backend'},indent=2))

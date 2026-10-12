#!/usr/bin/env python3
"""Freeze stable HTML card IDs and concrete acceptance obligations."""
from html.parser import HTMLParser
from pathlib import Path
import json

ROOT = Path(__file__).resolve().parents[2]
FLOW_NAMES = ['Install','Start','Navigator','Machines','Phone','Runs']
# Screen and externally observable state; scenario-specific prerequisites live
# in Go. A blocked phone card fails at its real CLI prerequisite, never passes
# because the downstream physical screen cannot be rendered on Linux.
OBLIGATIONS = [
 [
 ('Verified installer succeeds; installed wt opens empty navigator', 'Installed bytes equal real Linux build; checksum verified; no daemon/session started'),
 ('Empty navigator, connection header, wing/roost explanation and useful keys', 'Zero sessions and no account, provider or daemon started'),
 ('Local agent detection and no-agent chooser/skip', 'PATH-only inspection; no provider, installer or account operation'),
 ('Exact selected vendor installer, machine, consent and Escape', 'Cancel leaves agent selection and local inventory unchanged; no installer execution'),
 ('Verify selected provider then native authentication handoff', 'Version check is not session readiness; explicit admission ID required before attach/detach/reattach'),
 ('Failed install/PATH/auth stage with fresh-consent retry, rescan and Back', 'Preserve selected provider and any admitted ID; no silent retry or policy change'),
 ('Failed release download diagnosis and successful-install exact PATH hint', 'Failure installs no binary; success installs verified artifact without Pro'),
 ('Local inventory and attached provider while installer transport fails', 'Local provider remains alive; no download request or daemon autostart'),
 ],
 [
 ('Cold Claude provider output then reattach same ID', 'Exactly one main local-only daemon and one parent at current cwd; detach preserves process/policy'),
 ('Explicit --cwd parent output; outside-scope refusal', 'Receiving machine stores chosen canonical cwd; no session admitted on denied path'),
 ('n prompts selected machine and cwd, attaches scoped Claude parent', 'Saved forge route, native parent/MCP registration, exact cwd and durable child receipts; generic route remains distinct'),
 ('wt egg codex shows native Codex composer', 'One generic Codex egg with requested name/cwd; detach leaves it alive'),
 ('Launch failure phase, named policy, actionable next step and admitted-ID recovery', 'Policy/provider preparation and readiness are distinct; no privileged fallback, secret exposure or duplicate admission'),
 ('Two concurrent Claude parent screens', 'Two distinct requested parents share one main local-only daemon; canceled readiness waiter cannot kill survivor; detach does not replace daemon'),
 ],
 [
 ('Grouped machine/project rows, machine filter and attention filter', 'Both exact colliding IDs remain separate; listing/filtering sends no mutation'),
 ('Enter attaches exact forge row; Ctrl+B q returns same selection', 'Remote attach receipt names exact ID; local never attached; neither process killed'),
 ('Blocked lifecycle, separate unread badge/count and acknowledgement', 'Hook cursors scoped by machine/session; completion not exit; unacknowledged unread cursor persists for same viewer across navigator reopen'),
 ('Native mouse, wheel, alternate screen, resize and reconnect output', 'Raw terminal modes restored/reset, provider gets wheel input; browser compatibility retained'),
 ('Recent runs, status/details/result, crash and stale/incomplete discovery', 'Qualified durable IDs retained; bounded pagination/result; no relaunch or fabricated liveness/output/cleanup'),
 ('Offline forge with last-success/stale rows; local still attachable', 'Stale attach refused; no fallback, exit inference or provider mutation'),
 ('Exact machine/ID/cwd details and Stop forge:same confirmation', 'Enter/Escape cancel; y kills only remote; colliding local remains alive'),
 ],
 [
 ('remote add and remote ls show saved forge alias', 'Saved SSH target/state/binary; add neither installs provider/workspace nor starts a wing'),
 ('Verified mcp connect add/ls names saved forge route', 'Authenticated handshake persists canonical state/socket/version and exact wing pin'),
 ('Qualified and generic exact forge attachment; plain name local', 'Interactive SSH argv/state/custom binary pinned to saved route; same ID elsewhere untouched'),
 ('Offline forge/stale inventory plus available local attachment', 'Cannot validate remote action; no same-name local fallback'),
 ('Changed-host-key and old receiver errors with reconnect uncertainty', 'No attached/killed session on either machine; refusal remains authoritative'),
 ],
 [
 ('Pair a phone, pinned identity/QR, six words and 5:00 waiting screen', 'Invitation uses existing main wing; creation+300s, single use, no second identity'),
 ('iPhone camera Scan to pair reads terminal invitation', 'Phone contacts exact endpoint/pin, no phone account or copied account token'),
 ('Same four emoji on phone/terminal; both confirmations', 'No credential before both approvals; cancellation burns invitation'),
 ('Saving this machine securely; private key stays on phone', 'P-256 device-only Keychain and reviewed SPAKE2 channel; atomic hashed wing registry'),
 ('Wing-bound sessions with status/attention and attach controls', 'Device has only sessions/attach/read/input/stop/detach/close; no config or other-wing authority'),
 ('Away from home connected through encrypted pinned relay', 'LAN then tailnet then entitled registered relay; phone never logs in'),
 ('Independent paired machine list and wt devices registry', 'Separate pins/credentials and revocation per wing'),
 ('Expired/used QR refused; Enter creates a fresh QR', 'Old invitation cannot grant; fresh secret/ID/deadline; memory-only single use'),
 ('Unreachable machine with free Wi-Fi/tailnet alternative', 'No credential sent on failed route; phone no account authority'),
 ('Identity mismatch hard stop with Cancel and no Continue', 'Full scanned pin checked before continuation; no grant, pin replacement or fallback'),
 ('Emoji mismatch Cancel; terminal no access granted and fresh QR', 'Both confirmations discarded and invitation burned'),
 ('wt devices revoke receipt and phone Access revoked', 'Persist revocation and close active streams before success; forge unaffected; no remote erasure claim'),
 ('Phone output/composer and explicit Send; delivery unknown on lost ack', 'Wing-bound encrypted input exactly once; reconnect reads, never replays'),
 ('Phone Stop pairing? names exact machine/session; Keep running', 'Detach preserves process; confirmed stop receipt targets exact execution, other sessions alive'),
 ('Explicit notification consent; generic lock-screen attention', 'No names/transcript/commands/secrets in push payload; background delivery requires physical-device evidence'),
 ('Connected tailnet Free, no relay/account prompt', 'Allowed LAN/tailnet endpoints, full pin validation; no Wingthing relay request'),
 ('Permission denial Settings/rescan/manual invitation path', 'Fallback includes endpoint, public pin and words; no private-key/credential clipboard export'),
 ('Finishing pairing; Check pairing result for dropped final reply', 'Original encrypted success recoverable before deadline; exactly one persisted grant, duplicate confirm idempotent'),
 ],
 [
 ('wt run submission, streamed result, timeline and status', 'One durable legacy task with selected cwd/agent/policy and completed output; sandbox default, not agent_run'),
 ('Scoped parent agent task receipt for selected forge', 'Allowed remote cwd; wing/run/session IDs saved, original idempotency key reconciled; accepted workers survive disconnect'),
 ('doctor launch-plan unverified and REQUIRED-UNRUN gates', 'Read-only report; no provider invocation, live wing probing or credential disclosure; success not readiness'),
 ('Launch policy workspace/provider home/denies/network review', 'Effective authority explicit; full OS-user mode opt-in; displayed policy alone not confinement evidence'),
 ('Offline completed_with_output, terminal_receipt_not_in_wing and explicit evidence state', 'Hash-verified completed snapshot unchanged; default omits output; modified copy refused; no wing/resume'),
 ('Policy/toolchain/provider-readiness failure phase/source/action', 'Denials authoritative; launch receipts retained; no policy broadening, secret exposure or duplicate retry'),
 ('Unknown admission, unavailable output and cleanup unknown recovery', 'Original qualified key and authorized inventory; incomplete/denied discovery fails closed, no replay/automatic resume'),
 ]
]

class Cards(HTMLParser):
 def __init__(self): super().__init__(); self.cards=[]; self.card=None; self.title=False
 def handle_starttag(self, tag, attrs):
  attrs=dict(attrs)
  if tag=='article' and 'card' in attrs.get('class','').split():
   self.card={'html_id':attrs['id'],'title':'','text':''};self.cards.append(self.card)
  if self.card and tag=='h2': self.title=True
 def handle_endtag(self,tag):
  if tag=='h2':self.title=False
  if tag=='article':self.card=None
 def handle_data(self,data):
  if self.card:
   self.card['text']+=data+'\n'
   if self.title:self.card['title']+=data

def catalog():
 source=ROOT/'evidence/storyboard-source'
 rows=[]
 for flow,file in enumerate(sorted(source.glob('[0-9][0-9]-*.html')),1):
  p=Cards();p.feed(file.read_text())
  for index,card in enumerate(p.cards):
   row={'flow':flow,'ordinal':index+1,'card_id':f'{flow:02d}/{card["html_id"]}',
        'source':f'evidence/storyboard-source/{file.name}#{index+1}',**card}
   if flow<=6:
    row['test']=f'TestStoryboard{FLOW_NAMES[flow-1]}Card{card["html_id"].split("-")[1]}'
    row['screen'],row['state']=OBLIGATIONS[flow-1][index]
   else:row.update(test=None,screen=None,state=None,status='PENDING-FEATURE')
   rows.append(row)
 assert len(rows)==100 and sum(r['flow']<=6 for r in rows)==51
 return rows

if __name__=='__main__':
 rows=catalog()
 (ROOT/'evidence/cards.json').write_text(json.dumps(rows,indent=2)+'\n')
 print(f'{len(rows)} cards; 51 executable acceptance journeys; 49 PENDING-FEATURE')

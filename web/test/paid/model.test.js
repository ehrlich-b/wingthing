import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createPaidState, applyPaidAction as act, quote, shouldOffer, fixtureDownload, TERMS } from '../../src/paid/model.js';
import { cards } from '../../src/paid/cards.js';
import { renderPaidView } from '../../src/paid/view.js';
const active = () => createPaidState('active');
const start = (s = active()) => act(s,{op:'launch',requestId:'launch-1'});
const ready = (s = start()) => { for(let i=0;i<4 && s.cloud.status!=='ready';i++) s=act(s,{op:'launch-step'}); return s; };
const stopped = (s = ready()) => act(act(s,{op:'stop'}),{op:'stop-confirm',eventId:`stop-${s.nextRun}`});
const payment = (s,outcome='confirmed',eventId='payment-1') => act(s,{op:'payment',eventId,checkoutId:'checkout-1',outcome});
const checkout = () => act(createPaidState(),{op:'checkout',requestId:'checkout-1'});
function rejects(s,a,pattern){ const before=JSON.stringify(s); assert.throws(()=>act(s,a),pattern); assert.equal(JSON.stringify(s),before,'failed action must be atomic'); }

test('07-1 contextual offers preserve exact intent and show once; direct reach is free',()=>{
 let s=createPaidState();
 for(const moment of ['reach','publish','cloud','share']) {
  assert.equal(shouldOffer(s,{moment},false),false); assert.equal(shouldOffer(s,{moment},true),true);
  s=act(s,{op:'intent',moment,task:'My exact task',route:'relay'});
  assert.equal(s.intent.task,'My exact task'); assert.equal(s.cloud,null); assert.equal(shouldOffer(s,{moment},true),false);
 }
 for(const route of ['lan','tailnet']) assert.equal(shouldOffer(createPaidState(),{moment:'reach',route},true),false);
 s=act(s,{op:'dismiss',moment:'cloud'}); assert.equal(s.intent.task,'My exact task');
});
test('07-2 complete price uses default zero spend, explicit overage and pooled size credits',()=>{
 const s=active();assert.equal(s.draft.overage,false); assert.equal(s.accountCapCents,0);
 assert.equal(quote(s).cents,0); assert.equal(quote(s).seconds,7320);
 const l=act(s,{op:'draft',size:'large'}); assert.equal(quote(l).seconds,14640);
 const empty={...s,consumedSeconds:TERMS.includedSeconds}; assert.equal(quote(empty).permitted,false);
 rejects(empty,{op:'launch',requestId:'no-credit'},/reservation/);
});
test('07-3 checkout has no card fields and success URL never grants Pro',()=>{
 let s=checkout(); s=act(s,{op:'checkout-return'}); assert.equal(s.pro,false);
 assert.equal(act(s,{op:'checkout',requestId:'new-checkout'}).checkout.id,'checkout-1');
 rejects(s,{op:'checkout',requestId:'bad',card:'4242'},/Unknown action field/);
 rejects(s,{op:'payment',eventId:'evt',checkoutId:'other',outcome:'confirmed'},/belong/);
});
test('07-4 confirmed event activates once, preserves device grants and never launches',()=>{
 const s=checkout(), p=payment(s); assert.equal(p.pro,true);assert.equal(p.entitlementVersion,1);assert.equal(p.cloud,null);
 assert.deepEqual(p.localDeviceGrants,s.localDeviceGrants);assert.equal(payment(p).entitlementVersion,1);
 assert.equal(payment(p,'confirmed','different-event').entitlementVersion,1);
 assert.equal(payment(p,'declined','late-decline').pro,true);
});
test('07-5 cancel renewal preserves entitlement through the precise paid period',()=>{
 const s=act(active(),{op:'cancel'}); assert.equal(s.pro,true);assert.equal(s.cancellation,true);assert.match(s.message,/2026-11-12T21:00:00Z/);
});
test('07-6 expiry requests confirmed shutdown, preserves local grants and dated export',()=>{
 const before=ready(), s=act(before,{op:'expire'});assert.equal(s.pro,false);assert.equal(s.cloud.status,'stopping');assert.ok(s.reservation);
 assert.deepEqual(s.localSessions,before.localSessions);assert.deepEqual(s.localDeviceGrants,before.localDeviceGrants);assert.equal(s.exportUntil,'2026-11-26T21:00:00Z');
 rejects(s,{op:'attach',wingId:s.cloud.wingId,sessionId:s.cloud.sessionId},/export-only|not ready/);
});
test('07-7 abandoned, declined and delayed payments preserve draft and no paid actions',()=>{
 for(const outcome of ['abandoned','declined','delayed']){
  const s=payment(checkout(),outcome);assert.equal(s.pro,false);assert.equal(s.cloud,null);assert.equal(s.draft.task,'Review pairing implementation');
  assert.match(act(s,{op:'payment-check'}).message, outcome==='delayed' ? /Payment pending/ : /checkout/);
 }
});
test('07-8 owner entitlement is exact-ID, operator-only, audited and revocable',()=>{
 rejects(active(),{op:'owner-grant',accountId:'fixture-account-1',reason:'dogfood'},/operator/);
 let s=createPaidState('operator'); rejects(s,{op:'owner-grant',accountId:'display-name',reason:'dogfood'},/Account ID/);
 s=act(s,{op:'owner-grant',accountId:s.accountId,reason:'dogfood'});assert.equal(s.audit.at(-1).reason,'dogfood');assert.equal(s.audit.at(-1).actor,'operator');
 s=act(s,{op:'owner-revoke'});assert.equal(s.pro,false);
});
test('10-1 terminal-origin draft is structured, explicit and never copies local paths',()=>{
 const s=act(active(),{op:'draft',task:'Test review',workspace:'docs'});assert.equal(s.draft.workspace,'docs');assert.equal(s.cloud,null);
 rejects(s,{op:'draft',cwd:'/private'},/Unknown action field/);
 const run=spawnSync(process.execPath,['../scripts/paid-fixture.mjs','--fixture-json'],{input:JSON.stringify({scenario:'active',actions:[{op:'draft',task:'JSON review'}]}),encoding:'utf8'});
 assert.equal(run.status,0,run.stderr);assert.equal(JSON.parse(run.stdout).state.draft.task,'JSON review');
});
test('10-2 dashboard draft keeps explicit project/task with no allocation',()=>{
 const s=act(active(),{op:'draft',task:'Dashboard review',workspace:'project'});assert.equal(s.draft.task,'Dashboard review');assert.equal(s.reservation,null);
});
test('10-3 paired phone queues only approved coordinator input and cannot raise spend',()=>{
 for(const scenario of ['phone','unapproved-phone']){
  const base=createPaidState(scenario),s=act(base,{op:'phone-request',templateId:base.template.id,task:'Phone review'});
  assert.equal(s.phoneRequest.status,scenario==='phone'?'queued-for-coordinator':'awaiting-owner-approval');assert.equal(s.cloud,null);
  for(const action of [{op:'draft',capCents:500},{op:'account-cap',capCents:500},{op:'repo-connect',repository:'sample/project',branch:'cloud/review'},{op:'vault-toggle',name:'DEPLOY_TOKEN',enabled:true},{op:'launch',requestId:'phone'}]) rejects(s,action,/Phone pairing/);
 }
});
test('10-4 size multiplier doubles credits and rate without changing CPU count',()=>{
 const s=act(active(),{op:'draft',size:'large'});assert.equal(quote(s).rateCents,70);assert.equal(quote(s).seconds,2*quote(active()).seconds);
 rejects(s,{op:'draft',size:'performance'},/Unknown size/);
});
test('10-5 worst-case cap uses shutdown buffer and refuses uncapped overage',()=>{
 let s=act({...active(),consumedSeconds:72000},{op:'draft',hours:2,overage:true,capCents:500});
 rejects(s,{op:'launch',requestId:'over-cap'},/reservation/);
 s=act(s,{op:'account-cap',capCents:500});assert.equal(quote(s).cents,72);assert.equal(start(s).reservation.cents,72);
 rejects(s,{op:'draft',hours:25},/Invalid hours/);rejects(s,{op:'draft',capCents:-1},/Invalid cap/);
});
test('10-6 reserve before boot, ready only after native check, idempotent request IDs',()=>{
 let s=start();assert.equal(s.cloud.status,'allocating');assert.ok(s.reservation);assert.equal(s.cloud.elapsedSeconds,0);
 assert.equal(act(s,{op:'launch',requestId:'launch-1'}).cloud.id,s.cloud.id);
 const changed={...s,draft:{...s.draft,task:'Different intent'}};rejects(changed,{op:'launch',requestId:'launch-1'},/conflicts/);
 s=ready(s);assert.equal(s.cloud.status,'ready');assert.equal(s.cloud.phase,5);assert.equal(s.cloud.elapsedSeconds,0);
});
test('10-7 confirmed stop retains volume and workspace but releases compute reservation',()=>{
 const s=stopped();assert.equal(s.cloud.status,'stopped');assert.equal(s.cloud.volumeId,'fixture-volume-1');assert.equal(s.reservation,null);assert.equal(s.cloud.checkpoint.secrets,'excluded');
});
test('10-8 selected GitHub installation rejects uninstalled repos and credential fields',()=>{
 const s=act(active(),{op:'repo-connect',repository:'sample/docs',branch:'cloud/docs'});assert.equal(s.draft.repoAuthorized,true);
 rejects(s,{op:'repo-connect',repository:'someone/private',branch:'cloud/review'},/outside/);
 rejects(s,{op:'repo-connect',repository:'sample/docs',branch:'main'},/review branch/);
 rejects(s,{op:'repo-connect',repository:'sample/docs',branch:'cloud/docs',token:'do-not-store'},/Unknown action field/);
});
test('10-9 clone then selected review branch only; no protected-default push',()=>{
 let s=active();rejects(s,{op:'git-push',branch:'cloud/review'},/Clone/);
 s=act(s,{op:'git-clone'});rejects(s,{op:'git-push',branch:'main'},/selected review/);
 s=act(s,{op:'git-push',branch:s.draft.branch});assert.match(s.draft.gitStatus,/fixture PR ready/);
});
test('10-10 vault toggles names only; values are outside typed contract and downloads',()=>{
 let s=act(active(),{op:'vault-toggle',name:'DEPLOY_TOKEN',enabled:true});assert.equal(s.draft.vault.DEPLOY_TOKEN,true);
 rejects(s,{op:'vault-toggle',name:'DEPLOY_TOKEN',enabled:true,value:'do-not-store'},/Unknown action field/);
 s=act(stopped(ready(start(s))),{op:'export'}); const exported=fixtureDownload(s,'export');assert.match(exported,/excludes/);assert.ok(!exported.includes('do-not-store'));
});
test('10-11 model mode requires context consent and BYO is separate from credits',()=>{
 const s=act(active(),{op:'model-select',agent:'codex',mode:'byo',consent:true});assert.equal(s.draft.agent,'codex');assert.equal(s.consumedCents,0);
 rejects(s,{op:'model-select',agent:'claude',mode:'wingthing',consent:true},/later scope/);
 rejects(s,{op:'model-select',agent:'claude',mode:'byo',consent:false},/consent/);
});
test('10-12 extra egress destination needs explicit owner review',()=>{
 let s=active();assert.ok(!s.draft.network.includes('api.example.com'));
 s=act(s,{op:'network-toggle',domain:'api.example.com',enabled:true});assert.ok(s.draft.network.includes('api.example.com'));
 rejects(s,{op:'network-toggle',domain:'other.example.com',enabled:true},/fixture extra/);
});
test('10-13 controlled image additions reject arbitrary system packages',()=>{
 const s=act(active(),{op:'tool-add',tool:'Rust (reviewed image step)'});assert.ok(s.draft.tools.includes('Rust (reviewed image step)'));
 rejects(s,{op:'tool-add',tool:'sudo arbitrary-install'},/Unreviewed/);
});
test('10-14 attach is qualified, Pro-only and phone cloud pairing is independent',()=>{
 let s=ready();rejects(s,{op:'attach',wingId:'wrong-wing',sessionId:s.cloud.sessionId},/identity/);
 s={...s,principal:'phone'};rejects(s,{op:'attach',wingId:s.cloud.wingId,sessionId:s.cloud.sessionId},/not paired/);
 let owner=act({...s,principal:'owner'},{op:'cloud-pair',wingId:s.cloud.wingId});
 assert.equal(act({...owner,principal:'phone'},{op:'attach',wingId:owner.cloud.wingId,sessionId:owner.cloud.sessionId}).cloud.attached,true);
});
test('10-15 only verified ready intervals count, duplicate events ignored, estimates block admission',()=>{
 rejects(start(),{op:'meter',eventId:'early',seconds:30,verified:true},/ready lifecycle/);
 let s=act(ready(),{op:'meter',eventId:'ready-interval',seconds:1080,verified:true});assert.equal(s.cloud.elapsedSeconds,1080);
 assert.equal(act(s,{op:'meter',eventId:'ready-interval',seconds:1080,verified:true}).cloud.elapsedSeconds,1080);
 s=act(s,{op:'meter',eventId:'estimate',seconds:60,verified:false});assert.equal(s.meterFresh,false);assert.equal(s.cloud.elapsedSeconds,1080);
});
test('10-16 active agent never idle-stops without viewers; idle warns then awaits shutdown',()=>{
 let s=act(ready(),{op:'activity',active:true,minutes:100});s=act(s,{op:'idle-check'});assert.equal(s.cloud.status,'ready');
 s=act(s,{op:'activity',active:false,minutes:13});s=act(s,{op:'idle-check'});assert.match(s.message,/warning/);
 const keep=act(s,{op:'keepalive'});assert.equal(keep.cloud.idleMinutes,0);
 s=act(s,{op:'activity',active:false,minutes:15});s=act(s,{op:'idle-check'});assert.equal(s.cloud.status,'stopping');assert.ok(s.reservation);
});
test('10-17 cold resume keeps volume and prior run ID, refreshes reservation without prompt replay',()=>{
 let s=stopped();rejects(s,{op:'resume',requestId:'resume',coldConsent:false},/consent/);
 const old=s.cloud.runId;s=act(s,{op:'resume',requestId:'resume',coldConsent:true});assert.equal(s.cloud.previousRunId,old);assert.notEqual(s.cloud.runId,old);assert.equal(s.cloud.volumeId,'fixture-volume-1');assert.equal(s.cloud.attached,false);assert.ok(s.reservation);
});
test('10-18 results grant only selected recipient; stop disables preview',()=>{
 let s=ready();assert.equal(s.cloud.previewRecipient,undefined);
 s=act(s,{op:'preview-share',recipient:'Alice'});assert.equal(s.cloud.previewRecipient,'Alice');
 assert.match(fixtureDownload(s,'artifacts'),/Dormant fixture/);
 s=act(s,{op:'stop'});assert.equal(s.cloud.preview,false);rejects(s,{op:'preview-share',recipient:'Alice'},/stopped/);
});
test('10-19 export excludes credentials; deletion requires stop, review, exact volume and revokes cloud only',()=>{
 let s=ready();rejects(s,{op:'delete-review'},/shutdown/);s=stopped(s);s=act(s,{op:'export'});assert.ok(s.exportManifest.excludes.includes('vault values'));
 rejects(s,{op:'delete',volumeId:s.cloud.volumeId},/Review/);s=act(s,{op:'delete-review'});rejects(s,{op:'delete',volumeId:'wrong-volume'},/Exact volume/);
 s=act(s,{op:'delete',volumeId:s.cloud.volumeId});assert.equal(s.cloud.status,'deleted');assert.deepEqual(s.localDeviceGrants,['fixture-local-wing']);
 rejects(s,{op:'resume',requestId:'resume',coldConsent:true},/retained stopped/);
});
test('10-20 all boot failures keep request/reservation and retry reconciles original VM',()=>{
 for(const phase of ['allocation','clone','credentials','boot']){
  const first=start(), failed=act(first,{op:'launch-fail',phase});assert.equal(failed.cloud.status,'failed');assert.equal(failed.cloud.elapsedSeconds,0);assert.ok(failed.reservation);
  const retry=act(failed,{op:'launch-retry'});assert.equal(retry.cloud.requestId,first.cloud.requestId);assert.equal(retry.cloud.id,first.cloud.id);
 }
});
test('10-21 bounded failures classify cap, disk, egress and incompatible restore explicitly',()=>{
 assert.equal(act(ready(),{op:'fault',kind:'cap'}).cloud.status,'stopping');
 assert.equal(act(ready(),{op:'fault',kind:'disk'}).cloud.diskGB,10);
 rejects(act(ready(),{op:'fault',kind:'disk'}),{op:'git-clone'},/writes paused/);
 assert.match(act(ready(),{op:'fault',kind:'network'}).message,/api.example.com/);
 const s=act(stopped(),{op:'fault',kind:'restore'});rejects(s,{op:'recover-cold',consent:false},/consent/);assert.equal(act(s,{op:'recover-cold',consent:true}).fault,null);
});
test('10-22 one live VM, retained/deleted volume and current entitlement gate every admission',()=>{
 rejects(start(),{op:'launch',requestId:'second-launch'},/resume|live VM/);
 const deleted=act(stopped(),{op:'fault',kind:'deleted-volume'});rejects(deleted,{op:'resume',requestId:'resume',coldConsent:true},/Deleted volume/);
 const expired=act(stopped(),{op:'expire'});rejects(expired,{op:'resume',requestId:'resume',coldConsent:true},/Paid cloud/);
});
test('11-1 cap changes cannot undercut outstanding reservations plus accrued usage',()=>{
 let s=act({...active(),consumedSeconds:72000},{op:'draft',capCents:500,overage:true});s=act(s,{op:'account-cap',capCents:500});s=start(s);
 rejects(s,{op:'account-cap',capCents:50},/outstanding reservation/);assert.equal(s.accountCapCents,500);
});
test('11-2 invoices finalize settled intervals only, itemize models separately and deduplicate',()=>{
 let s=ready();rejects(s,{op:'invoice-finalize',eventId:'invoice',invoiceId:s.invoice.id},/settled usage/);
 s=stopped(act(s,{op:'meter',eventId:'use-1',seconds:1080,verified:true}));
 s=act(s,{op:'invoice-finalize',eventId:'invoice',invoiceId:s.invoice.id});assert.equal(s.invoice.status,'finalized');assert.ok(s.invoice.events.includes('use-1'));
 assert.equal(s.invoice.lines.some(l=>l.label.includes('BYO')),false);assert.equal(s.invoice.lines.reduce((sum,l)=>sum+l.cents,0),s.invoice.cents);assert.equal(act(s,{op:'invoice-finalize',eventId:'invoice',invoiceId:s.invoice.id}).invoiceEvents.length,1);
 assert.match(fixtureDownload(s,'invoice'),/fixture-invoice-1/);
});
test('11-3 explicit preferences and metadata-only support omit task, transcripts and values',()=>{
 let s=act(ready(),{op:'notification',name:'attention',enabled:true});assert.equal(s.notifications.attention,true);s=act(s,{op:'support'});
 const report=fixtureDownload(s,'support');assert.ok(!report.includes(s.draft.task));assert.ok(!report.includes('DEPLOY_TOKEN'));assert.match(report,/usageEventIds/);
});
test('11-4 unpaid invoice blocks starts and stop policy preserves local work; recovery changes no cap',()=>{
 let s=ready();s=act(s,{op:'billing-fail',invoiceId:s.invoice.id});assert.equal(s.cloud.status,'stopping');assert.equal(s.billingBlocked,true);assert.equal(s.localSessions.length,1);
 s=act(s,{op:'billing-recover'});assert.equal(s.accountCapCents,0);assert.equal(s.cloud.status,'stopping');assert.equal(s.billingBlocked,false);
});
test('11-5 stale meter fails admission and invoice finalization; reconciliation preserves ledger',()=>{
 let s=act(stopped(),{op:'fault',kind:'stale-meter'});rejects(s,{op:'resume',requestId:'resume',coldConsent:true},/stale/);
 rejects(s,{op:'invoice-finalize',eventId:'invoice',invoiceId:s.invoice.id},/reconciled/);
 const events=[...s.meterEvents];s=act(s,{op:'reconcile',eventId:'reconcile-1'});assert.equal(s.meterFresh,true);assert.deepEqual(s.meterEvents,events);assert.equal(s.audit.at(-1).eventId,'reconcile-1');
});
test('12-1 later team permissions deny wrong tenant, revoked membership and phone admin',()=>{
 const cases=[['sample-tenant','member','shared-project',false,true],['wrong-tenant','admin','shared-project',false,false],['sample-tenant','admin','shared-project',true,false],['sample-tenant','phone','billing',false,false],['sample-tenant','member','billing',false,false],['sample-tenant','admin','billing',false,true]];
 for(const [tenant,role,resource,revoked,allowed] of cases) assert.equal(act(active(),{op:'team-check',tenant,role,resource,revoked}).teamDecision.startsWith('allowed'),allowed);
});
test('all 36 card routes render dormant UI with safe escaped user text and receipt context',()=>{
 assert.equal(cards.length,36);const counts={};
 for(const card of cards){counts[card.flow]=(counts[card.flow]||0)+1;const html=renderPaidView(card,createPaidState(card.key==='owner' ? 'operator' : 'active'));assert.ok(html.includes('Dormant · dev fixture'));assert.ok(html.includes(card.title.replaceAll('&','&amp;')));assert.ok(html.includes('fixture-account-1'));}
 assert.deepEqual(counts,{7:8,10:22,11:5,12:1});
 const s=act(active(),{op:'draft',task:'<img src=x onerror=alert(1)>'});assert.ok(!renderPaidView(cards.find(c=>c.key==='dashboard'),s).includes('<img src=x'));
});
test('runtime ceiling settles no higher than accepted reservation even on vendor overrun',()=>{
 let s=act({...active(),consumedSeconds:72000},{op:'draft',hours:0.25,capCents:500,overage:true});s=act(s,{op:'account-cap',capCents:500});s=ready(start(s));
 const reserved=s.reservation.cents;s=act(s,{op:'meter',eventId:'overrun',seconds:86400,verified:true});assert.equal(s.cloud.status,'stopping');
 s=act(s,{op:'stop-confirm',eventId:'vendor-stop'});assert.ok(s.consumedCents<=reserved);assert.equal(s.reservation,null);
});
test('resumed request reconciles the same execution after stop; changed size matches reservation',()=>{
 let s=stopped(ready(start(act(active(),{op:'draft',size:'large'}))));
 s=act(s,{op:'draft',size:'standard'});s=act(s,{op:'resume',requestId:'resume-once',coldConsent:true});
 assert.equal(s.cloud.size,'standard');assert.equal(s.reservation.seconds,7320);const run=s.cloud.runId;
 s=stopped(ready(s));s=act(s,{op:'resume',requestId:'resume-once',coldConsent:true});assert.equal(s.cloud.runId,run);assert.equal(s.cloud.status,'stopped');assert.equal(s.reservation,null);
 const changed=act(s,{op:'draft',size:'large'});rejects(changed,{op:'resume',requestId:'resume-once',coldConsent:true},/conflicts/);
});
test('expiry becomes export-only, grace has an exact deadline and repeated expiry cannot extend it',()=>{
 let s=act(stopped(),{op:'expire'}); const deadline=s.exportUntil;
 for(const a of [{op:'repo-connect',repository:'sample/docs',branch:'cloud/review'},{op:'git-clone'},{op:'vault-toggle',name:'DEPLOY_TOKEN',enabled:true},{op:'network-toggle',domain:'api.example.com',enabled:true}])rejects(s,a,/export-only/);
 assert.ok(act(s,{op:'export'}).exportManifest);s=act(s,{op:'advance-time',at:deadline});rejects(s,{op:'export'},/grace expired/);assert.equal(act(s,{op:'expire'}).exportUntil,deadline);
});
test('unpaid boot cannot become ready or grant a preview; phone controls need independent cloud pairing',()=>{
 let s=start();s=act(s,{op:'billing-fail',invoiceId:s.invoice.id});assert.equal(s.cloud.status,'stopping');rejects(s,{op:'launch-step'},/unpaid invoice/);
 rejects(s,{op:'preview-share',recipient:'Alice'},/export-only/);
 s={...ready(),principal:'phone'};
 for(const a of [{op:'stop'},{op:'activity',active:true,minutes:0},{op:'keepalive'}]) rejects(s,a,/not paired/);
 const paired={...s,phonePairedWings:[...s.phonePairedWings,s.cloud.wingId]};assert.equal(act(paired,{op:'stop'}).cloud.status,'stopping');
});
test('estimated interval survives shutdown, blocks billing and requires an explicit retained-event decision',()=>{
 let s=act(ready(),{op:'meter',eventId:'estimated-1',seconds:1080,verified:false});
 s=stopped(s);assert.equal(s.meterFresh,false);assert.equal(s.pendingIntervals['estimated-1'].status,'pending');
 rejects(s,{op:'invoice-finalize',eventId:'invoice',invoiceId:s.invoice.id},/reconciled/);
 rejects(s,{op:'reconcile',eventId:'recheck'},/Invalid fixture ID/);
 s=act(s,{op:'reconcile',eventId:'recheck',usageEventId:'estimated-1',decision:'confirmed'});assert.equal(s.meterFresh,true);assert.equal(s.consumedSeconds,1080);assert.ok(s.meterEvents.includes('estimated-1'));
 const repeat=act(s,{op:'reconcile',eventId:'recheck-2',usageEventId:'estimated-1',decision:'confirmed'});assert.equal(repeat.consumedSeconds,1080);
 const settled=act(repeat,{op:'stop-confirm',eventId:'stop-1'});assert.equal(settled.consumedSeconds,1080);assert.equal(settled.cloud.status,'stopped');
});
test('prepared export downloads stop after the exact grace deadline or workspace deletion',()=>{
 let s=act(act(stopped(),{op:'export'}),{op:'expire'});assert.match(fixtureDownload(s,'export'),/fixture-manifest/);
 s=act(s,{op:'advance-time',at:s.exportUntil});assert.throws(()=>fixtureDownload(s,'export'),/grace expired/);
 let deleted=act(act(stopped(),{op:'export'}),{op:'delete-review'});deleted=act(deleted,{op:'delete',volumeId:deleted.cloud.volumeId});assert.throws(()=>fixtureDownload(deleted,'export'),/deleted/);
});
test('fresh launch after deletion has unique VM/volume/run and UI shutdown event IDs',()=>{
 let s=stopped();const oldRun=s.cloud.runId;s=act(s,{op:'delete-review'});s=act(s,{op:'delete',volumeId:s.cloud.volumeId});
 s=ready(act(s,{op:'launch',requestId:'fresh-launch'}));assert.notEqual(s.cloud.runId,oldRun);assert.equal(s.cloud.volumeId,'fixture-volume-2');
 s=stopped(s);assert.equal(s.cloud.status,'stopped');assert.equal(s.reservation,null);assert.ok(s.stopEvents['stop-2']);
});
test('retained estimated usage can reconcile after workspace deletion without reviving compute',()=>{
 let s=stopped(act(ready(),{op:'meter',eventId:'estimate-delete',seconds:60,verified:false}));
 s=act(s,{op:'delete-review'});s=act(s,{op:'delete',volumeId:s.cloud.volumeId});
 s=act(s,{op:'reconcile',eventId:'fix-delete',usageEventId:'estimate-delete',decision:'confirmed'});assert.equal(s.consumedSeconds,60);assert.equal(s.cloud.status,'deleted');assert.equal(s.reservation,null);assert.equal(s.meterFresh,true);
});

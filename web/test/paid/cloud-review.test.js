import test from 'node:test';
import assert from 'node:assert/strict';
import { createPaidState, applyPaidAction } from '../../src/paid/model.js';
import { cards } from '../../src/paid/cards.js';
import { cardContent, repositoryAuthorizationSummary } from '../../src/paid/view.js';
const content=(key,s)=>cardContent(cards.find(c=>c.group==='cloud'&&c.key===key),s);
test('cloud launch lists exact authorized scope and operator visibility before create',()=>{
 const s=createPaidState('active');s.draft.task='Run pairing tests';s.draft.vault.DEPLOY_TOKEN=true;
 const html=content('launch',s).body;
 for(const text of ['project','sample/project','cloud/review','DEPLOY_TOKEN',...s.draft.network,'Who can see this workspace','hosting provider','memory','secrets','bills you separately','Cloud spend cap'])assert.ok(html.includes(text),text);
 assert.ok(html.indexOf('Who can see this workspace')<html.indexOf('Create fixture workspace'));
 assert.match(html,/Clone.*review branch.*pull request/);
});
test('budget uses dollar amounts and exposes model costs before review',()=>{
 const s=createPaidState('active');s.draft.capCents=500;s.draft.size='large';
 const html=content('budget',s).body;
 assert.match(html,/name="capUSD"[^>]*value="5\.00"/);assert.doesNotMatch(html,/integer cents/);
 assert.match(html,/Large/);assert.match(html,/billed separately/);assert.match(html,/outside.*cap/);
 assert.ok(html.indexOf('billed separately')<html.indexOf('Review launch'));
});
test('repository and secret consent identify account, target and operation',()=>{
 const s=createPaidState('active');s.draft.task='Run pairing tests';
 const repository=content('repository',s).body;
 for(const text of ['Sample GitHub account','sample/project','cloud/review','Clone','pull request','default branch','Currently authorized'])assert.ok(repository.includes(text),text);
 const vault=content('vault',s).body;
 for(const text of ['project','Run pairing tests','process','DEPLOY_TOKEN'])assert.ok(vault.includes(text),text);
 assert.doesNotMatch(vault,/value="(?:secret|token)/);
});
test('pending repository rights name the edited scope without changing the previous grant',()=>{
 const s=createPaidState('active'),before=structuredClone(s.draft);
 const preview=repositoryAuthorizationSummary('sample/docs','cloud/review-docs');
 assert.match(preview,/Clone sample\/docs/);assert.match(preview,/Push to cloud\/review-docs in sample\/docs/);
 assert.doesNotMatch(preview,/sample\/project|cloud\/review</);assert.match(preview,/protected branch/);
 assert.deepEqual(s.draft,before);
 assert.throws(()=>applyPaidAction(s,{op:'repo-connect',repository:'sample/docs',branch:'main'}),/review branch/);
 assert.throws(()=>applyPaidAction(s,{op:'repo-connect',repository:'other/private',branch:'cloud/review-docs'}),/installation/);
 assert.ok(!repositoryAuthorizationSummary('<script>','cloud/<img>').includes('<script>'));
});
test('running Large cost starts with time left and money used',()=>{
 let s=applyPaidAction(createPaidState('active'),{op:'draft',size:'large'});
 s=applyPaidAction(s,{op:'launch',requestId:'run-1'});for(let i=0;i<4;i++)s=applyPaidAction(s,{op:'launch-step'});
 s=applyPaidAction(s,{op:'meter',eventId:'interval-1',seconds:1080,verified:true});
 const html=content('meter',s).body;
 assert.match(html,/9 h 42 min.*Large/);assert.match(html,/\$0\.21/);assert.match(html,/18 min/);
 const size=content('size',s).body;assert.doesNotMatch(size,/ewr|wall hours|ready running hour/);
});
test('deletion names its workspace and starts with a disabled destructive confirmation',()=>{
 let s=applyPaidAction(createPaidState('active'),{op:'launch',requestId:'run-1'});for(let i=0;i<4;i++)s=applyPaidAction(s,{op:'launch-step'});
 s=applyPaidAction(applyPaidAction(s,{op:'stop'}),{op:'stop-confirm',eventId:'stop-1'});s=applyPaidAction(s,{op:'delete-review'});
 const html=content('cleanup',s).body;
 for(const text of ['project','sample/project',s.cloud.volumeId,'uncommitted edits'])assert.ok(html.includes(text),text);
 assert.match(html,/class="paid-button danger"[^>]*disabled/);
});

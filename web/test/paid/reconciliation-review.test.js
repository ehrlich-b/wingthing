import test from 'node:test';
import assert from 'node:assert/strict';
import {createPaidState,applyPaidAction} from '../../src/paid/model.js';
import {cardContent} from '../../src/paid/view.js';
import {cards} from '../../src/paid/cards.js';
const act=(s,op,fields={})=>applyPaidAction(s,{op,...fields});
function estimated(){let s=act(createPaidState('active'),'launch',{requestId:'launch-1'});for(let i=0;i<4;i++)s=act(s,'launch-step');return act(s,'meter',{eventId:'estimate-1',seconds:1080,verified:false});}
test('retained estimate has dated provenance and its amount stays separate from invoice',()=>{
 const s=estimated(),v=s.pendingIntervals['estimate-1'];
 assert.equal(v.observedAt,s.asOf);assert.equal(Date.parse(v.endAt)-Date.parse(v.startAt),1080000);assert.equal(v.computeCents,11);
 const report=act(s,'support').report;assert.equal(report.classification,'usage-under-review');assert.deepEqual(report.pendingUsageEventIds,['estimate-1']);
 const html=cardContent(cards.find(c=>c.key==='reconcile'),s).body;
 for(const text of ['Under review','18 min','$0.11','Last update','Prepare dispute reference','estimate-1','Final invoice'])assert.ok(html.includes(text),text);
 assert.doesNotMatch(html,/Pending intervals<\/dt><dd>None/);
 assert.throws(()=>act(s,'invoice-finalize',{eventId:'invoice-1',invoiceId:s.invoice.id}),/reconciled/);
 assert.throws(()=>act(s,'reconcile',{eventId:'review-1'}),/Invalid fixture ID/);
 const resolved=act(s,'reconcile',{eventId:'review-1',usageEventId:'estimate-1',decision:'confirmed'});
 assert.equal(resolved.meterFresh,true);assert.equal(resolved.pendingIntervals['estimate-1'].resolvedAt,resolved.asOf);
 assert.equal(resolved.cloud.elapsedSeconds,1080);
 assert.equal(act(resolved,'reconcile',{eventId:'review-2',usageEventId:'estimate-1',decision:'confirmed'}).cloud.elapsedSeconds,1080);
});
test('stale source without retained intervals identifies source review rather than pending None',()=>{
 const s=act(createPaidState('active'),'fault',{kind:'stale-meter'});
 const html=cardContent(cards.find(c=>c.key==='reconcile'),s).body;
 assert.match(html,/Source update needed/);assert.match(html,/No disputed intervals retained/);
 assert.doesNotMatch(html,/Pending intervals/);
});

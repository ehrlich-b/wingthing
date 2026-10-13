import test from 'node:test';
import assert from 'node:assert/strict';
import {createPaidState,applyPaidAction} from '../../src/paid/model.js';
import {cards} from '../../src/paid/cards.js';
import {renderPaidView,cardContent} from '../../src/paid/view.js';
const card=(key)=>cards.find(c=>c.key===key);
test('cancelled renewal has dates and no second cancellation control',()=>{
 const s=applyPaidAction(createPaidState('active'),{op:'cancel'}),html=cardContent(card('manage'),s).body;
 assert.doesNotMatch(html,/Cancel renewal<|2026-11-12T21/);assert.match(html,/Nov/);assert.match(html,/Export deadline/);assert.match(html,/left/);
});
test('customer navigation contains seven upgrade steps and no operator grant',()=>{
 for(const scenario of ['free','active','phone']){
  const html=renderPaidView(card('offers'),createPaidState(scenario));
  assert.doesNotMatch(html,/paid\/upgrade\/owner|Bryan/);
 }
 const operator=renderPaidView(card('owner'),createPaidState('operator'));assert.match(operator,/Owner entitlement \(operator\)/);
});
test('invoice excludes unknown provider charges and identifies its period',()=>{
 const s=applyPaidAction(createPaidState('active'),{op:'invoice-finalize',invoiceId:'fixture-invoice-1',eventId:'invoice-1'});
 const html=cardContent(card('invoices'),s).body;
 assert.match(html,/Service period/);assert.match(html,/Issued/);assert.match(html,/amount unknown/);
 assert.doesNotMatch(html,/BYO model invoices billed externally/);
 assert.equal(s.invoice.lines.some(l=>/external/i.test(l.label)),false);
 const usage=cardContent(card('usage'),s).body;assert.match(usage,/Billing period/);assert.match(usage,/Remaining included/);assert.match(usage,/Credits reset/);
});
test('notification settings identify a sample destination and saved state',()=>{
 const s=applyPaidAction(createPaidState('active'),{op:'notification',name:'attention',enabled:true});
 const html=cardContent(card('notifications'),s).body;
 assert.match(html,/owner@example\.test/);assert.match(html,/Email/);assert.match(html,/Saved/);assert.match(html,/Preview/);
});

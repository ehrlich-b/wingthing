import test from 'node:test';
import assert from 'node:assert/strict';
import { createPaidState, applyPaidAction } from '../../src/paid/model.js';
import { cards } from '../../src/paid/cards.js';
import { renderPaidView } from '../../src/paid/view.js';
const pending = () => applyPaidAction(createPaidState(),{op:'checkout',requestId:'checkout-1'});
const card = cards.find(c=>c.key==='payment-pending');
test('pending checkout only checks its original payment',()=>{
 const s=pending(),html=renderPaidView(card,s);
 assert.match(html,/>Payment pending</);
 assert.match(html,/>Check payment</);
 assert.doesNotMatch(html,/Retry \/ confirm|Reuse pending checkout|data-action="[^\"]*&quot;op&quot;:&quot;checkout&quot;/);
 assert.match(html,/Do not pay again/);
 const checked=applyPaidAction(s,{op:'payment-check'});
 assert.deepEqual(checked.checkout,s.checkout);assert.deepEqual(checked.paymentEvents,s.paymentEvents);assert.equal(checked.pro,false);
});
test('declined and abandoned payments each have one state-specific recovery',()=>{
 for(const [outcome,title,action] of [['declined','Payment declined','Update payment method'],['abandoned','Checkout unfinished','Resume checkout']]) {
  const s=applyPaidAction(pending(),{op:'payment',checkoutId:'checkout-1',eventId:outcome,outcome});
  const html=renderPaidView(card,s);
  assert.ok(html.includes('>'+title+'<'));assert.ok(html.includes('>'+action+'<'));
  assert.doesNotMatch(html,/Retry \/ confirm|Simulate declined|Simulate delayed|Simulate abandoned/);
 }
});
test('unpaid invoice shows amount and dated export deadline',()=>{
 const s=applyPaidAction(createPaidState('active'),{op:'billing-fail',invoiceId:'fixture-invoice-1'});
 const html=renderPaidView(cards.find(c=>c.key==='payment-failed'),s);
 assert.match(html,/Amount due/);assert.match(html,/\$29\.00/);assert.match(html,/Export deadline/);
 assert.match(html,/paid-feedback error/);assert.match(html,/role="alert"/);
});
test('payment and access outcomes carry a visible semantic tone',()=>{
 let s=pending();
 assert.equal(s.messageTone,'pending');
 for(const [outcome,tone] of [['delayed','pending'],['declined','error'],['abandoned','warning'],['confirmed','success']]) {
  const next=applyPaidAction(s,{op:'payment',eventId:outcome,checkoutId:'checkout-1',outcome});
  assert.equal(next.messageTone,tone);
  const html=renderPaidView(card,next);
  assert.ok(html.includes(`paid-feedback ${tone}`));assert.match(html,/paid-tone-icon/);
  if(tone==='error')assert.match(html,/role="alert"/);
 }
 const denied=applyPaidAction(createPaidState('active'),{op:'team-check',tenant:'sample-tenant',role:'phone',resource:'billing',revoked:false});
 assert.equal(denied.messageTone,'error');
 let running=applyPaidAction(createPaidState('active'),{op:'launch',requestId:'launch-1'});
 const failed=applyPaidAction(running,{op:'launch-fail',phase:'allocation'});
 assert.equal(failed.messageTone,'error');
 for(let i=0;i<4;i++)running=applyPaidAction(running,{op:'launch-step'});
 const idle=applyPaidAction(applyPaidAction(running,{op:'activity',active:false,minutes:13}),{op:'idle-check'});
 assert.equal(idle.messageTone,'warning');
});
test('an abandoned checkout is resumed under the same identity',()=>{
 const s=applyPaidAction(pending(),{op:'payment',eventId:'abandoned',checkoutId:'checkout-1',outcome:'abandoned'});
 const resumed=applyPaidAction(s,{op:'checkout',requestId:'another-checkout'});
 assert.deepEqual(resumed.checkout,s.checkout);assert.equal(resumed.entitlementVersion,s.entitlementVersion);
});

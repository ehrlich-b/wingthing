import test from 'node:test';
import assert from 'node:assert/strict';
import { accessReview, previewIdentity } from '../src/reach-share-state.js';
test('access review identifies the verified account, machine, resource and role',()=>{
 for(const provider of ['google','github']) {
  const identity=previewIdentity(provider);
  const app=accessReview({kind:'app',wingId:'computer-1',id:'preview-1',port:3000},identity,'Studio Mac');
  assert.ok(app.recipient.includes(provider==='google'?'alice@example.test':'@alice-example'));
  assert.ok(app.recipient.includes(identity.subject));
  assert.equal(app.resource,'Studio Mac · preview-1 · port 3000');assert.equal(app.role,'View app');
  const session=accessReview({kind:'session',wingId:'computer-1',id:'session-1',sessionId:'same'},identity,'Studio Mac','Review pairing');
  assert.equal(session.resource,'Studio Mac · Review pairing · same');assert.equal(session.role,'Observe session · no input');
 }
});
test('same names keep provider accounts and subjects separate',()=>{
 const google=previewIdentity('google'),github=previewIdentity('github');
 assert.notEqual(google.subject,github.subject);
 assert.throws(()=>previewIdentity('unknown'));
 assert.throws(()=>accessReview({kind:'app',port:3000},{provider:'google',name:'Alice',subject:'opaque'},'Studio Mac'),/account/);
});

test('matching account labels keep the full provider subject for disambiguation', () => {
 const resource = { kind: 'app', id: 'preview-1', port: 3000 };
 const first = previewIdentity('google');
 const second = { ...first, subject: 'sample-google-alice-43' };
 const a = accessReview(resource, first, 'Preview host');
 const b = accessReview(resource, second, 'Preview host');
 assert.ok(a.recipient.includes(first.email));
 assert.ok(b.recipient.includes(first.email));
 assert.notEqual(a.recipient, b.recipient);
 assert.ok(a.recipient.includes(first.subject));
 assert.ok(b.recipient.includes(second.subject));
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { filterSessionInventory, sessionInventoryState } from '../src/session-inventory.js';
import { sessionState } from '../src/paper-state.js';
const wing = { wing_id: 'local', online: true };
const rows = status => Array.from({length: 3}, (_, i) => ({ id: 'immutable-shell-' + i, wing_id: 'local', swept: true, agent: '', lifecycle: { status, state_source: 'shell_report' } }));
test('three shell tasks enter attention in one snapshot only while explicitly waiting', () => {
 for (const status of ['working','blocked','working','done']) {
  const sessions = rows(status);
  const attention = filterSessionInventory(sessions, [wing], {}, {status:'attention'});
  assert.equal(attention.length, status === 'blocked' ? 3 : 0);
  for (const s of sessions) {
   assert.match(sessionInventoryState(s, wing).agentLabel, /reported/);
   assert.match(sessionState(s, wing).label, /reported/);
  }
 }
 const sessions = rows('blocked');
 sessions[1].lifecycle.status = 'working';
 assert.deepEqual(filterSessionInventory(sessions,[wing],{},{status:'attention'}).map(s=>s.id), ['immutable-shell-0','immutable-shell-2']);
});
test('quiet and uninstrumented shell tasks stay unknown', () => {
 for (const extra of [{},{output:'quiet'},{output:'build finished'},{lifecycle:{state_source:'terminal_idle',status:'done'}}]) {
  const s={id:'quiet',wing_id:'local',agent:'',swept:true,...extra};
  assert.equal(sessionInventoryState(s,wing).status,'unknown');
  assert.equal(filterSessionInventory([s],[wing],{},{status:'attention'}).length,0);
 }
});

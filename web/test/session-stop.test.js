import test from 'node:test';
import assert from 'node:assert/strict';
import { stopSessionVerified } from '../src/session-stop.js';
const session = { id: 'same-id', wing_id: 'a' };
test('stop verifies exit and fresh inventory without repeating or changing the qualified mutation', async () => {
    const calls = []; let reads = 0;
    const result = await stopSessionVerified(session, async (wing, payload) => {
        calls.push({ wing, ...payload });
        return payload.type === 'pty.kill' ? { process_exited: true } : { sessions: ++reads === 1 ? [{ session_id: session.id }] : [] };
    }, { wait: async () => {} });
    assert.equal(result.process_exited, true);
    assert.deepEqual(calls, [{ wing: 'a', type: 'pty.kill', session_id: 'same-id' },
        { wing: 'a', type: 'sessions.list' }, { wing: 'a', type: 'sessions.list' }]);
});
test('signal ack, missing inventory, request denial and still-live inventory never report success', async () => {
    for (const mode of ['legacy', 'missing', 'denied', 'live']) {
        let kills = 0;
        await assert.rejects(stopSessionVerified(session, async (_wing, payload) => {
            if (payload.type === 'pty.kill') {
                kills++;
                if (mode === 'denied') throw new Error('not allowed');
                return mode === 'legacy' ? { ok: 'true' } : { process_exited: true };
            }
            return mode === 'missing' ? {} : { sessions: [{ session_id: session.id }] };
        }, { attempts: 2, wait: async () => {} }));
        assert.equal(kills, 1);
    }
});

import {checkStoppedSession,stopConfirmationText} from '../src/session-stop.js';
test('checking a stop is read-only and needs both exit receipt and fresh absence',async()=>{
 const calls=[];
 const request=async(wing,payload)=>{calls.push({wing,...payload});return {sessions:[]};};
 assert.deepEqual(await checkStoppedSession(session,request,false),{listed:false,verified:false});
 assert.deepEqual(await checkStoppedSession(session,request,true),{listed:false,verified:true});
 assert.ok(calls.every(call=>call.type==='sessions.list'&&call.wing==='a'));
 assert.deepEqual(await checkStoppedSession(session,async()=>({sessions:[{session_id:'same-id'}]}),true),{listed:true,verified:false});
 await assert.rejects(checkStoppedSession(session,async()=>({}),true));
});
test('failed inventory read retains the original exit receipt for later checking',async()=>{
 await assert.rejects(stopSessionVerified(session,async(_wing,payload)=>{if(payload.type==='pty.kill')return {process_exited:true};throw new Error('connection lost');}),error=>error.processExited===true);
});
test('stop confirmation uses name, computer and folder with ID outside the sentence',()=>{
 const text=stopConfirmationText({id:'same-id',name:'pairing'},'Studio Mac','~/pairing');
 assert.match(text,/pairing.*Studio Mac.*~\/pairing/);assert.doesNotMatch(text,/same-id|Click|\?/);
});

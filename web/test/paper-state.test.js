import test from 'node:test';
import assert from 'node:assert/strict';
import { machineState, sessionState, projectName, requestedCommand, paperRoute, awayLabel } from '../src/paper-state.js';

const row = (status, extra = {}) => ({ lifecycle: { status }, ...extra });
const online = { online: true };

test('each run state has its own label and key', () => {
    const keys = ['blocked', 'working', 'idle', 'completed', 'exited'].map(status => sessionState(row(status), online).key);
    assert.deepEqual(keys, ['need', 'work', 'idle', 'done', 'ended']);
    assert.equal(new Set(keys).size, keys.length);
    assert.equal(sessionState(row('blocked'), online).label, 'Needs you');
    assert.equal(sessionState(row('working'), online).label, 'Working');
    assert.equal(sessionState(row('mystery'), online).label, 'Unknown');
});

test('an attention signal reads as needs you', () => {
    assert.equal(sessionState(row('working', { needs_attention: true }), online).key, 'need');
});

test('a machine that is away overrides the last reported run state', () => {
    assert.deepEqual(sessionState(row('working'), { online: false }), { key: 'offline', label: 'Offline' });
    assert.equal(sessionState(row('blocked'), { online: true, tunnel_error: 'unreachable' }).label, 'Offline');
    assert.equal(machineState(undefined), 'unknown');
    assert.equal(machineState({}), 'checking');
});

test('rows name the project, not the path', () => {
    assert.equal(projectName('/Users/example/code/wingthing'), 'wingthing');
    assert.equal(projectName('C:\\work\\api\\'), 'api');
    assert.equal(projectName(''), '');
});

test('a blocked request names the command it will run', () => {
    assert.equal(requestedCommand('Run ./scripts/deploy.sh staging'), './scripts/deploy.sh staging');
    assert.equal(requestedCommand('Needs a decision'), '');
});

test('paper routes are separate from legacy routes', () => {
    assert.deepEqual(paperRoute(''), { view: 'wing' });
    assert.deepEqual(paperRoute('#approve/studio/a%2Fb'), { view: 'approve', wingId: 'studio', sessionId: 'a/b' });
    assert.equal(paperRoute('#chat/x?wing=y'), null);
    assert.equal(paperRoute('#w/abc'), null);
});

test('a machine that stopped reporting uses neutral wording and last contact time', () => {
    assert.equal(awayLabel('offline', '1:42 PM'), 'offline · last seen 1:42 PM');
    assert.equal(awayLabel('offline', ''), 'offline');
    assert.equal(awayLabel('unreachable', '1:42 PM'), 'offline · last seen 1:42 PM');
});

test('sleep, lost transport and disconnected machines never infer a sleep cause', () => {
    for (const wing of [{ online: false }, { online: false, presence: 'asleep' }, { online: true, tunnel_error: 'unreachable' }]) {
        assert.equal(machineState(wing), 'offline');
        for (const status of ['blocked', 'working', 'completed', 'idle', 'exited']) {
            assert.deepEqual(sessionState(row(status), wing), { key: 'offline', label: 'Offline' });
        }
    }
    assert.deepEqual(sessionState(row('working'), {}), { key: 'unknown', label: 'Unknown' });
});

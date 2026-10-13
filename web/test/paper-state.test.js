import test from 'node:test';
import assert from 'node:assert/strict';
import { machineState, sessionState, projectName, requestedCommand, paperRoute } from '../src/paper-state.js';

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
    assert.deepEqual(sessionState(row('working'), { online: false }), { key: 'asleep', label: 'Asleep' });
    assert.equal(sessionState(row('blocked'), { online: true, tunnel_error: 'unreachable' }).label, 'Unreachable');
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

import test from 'node:test';
import assert from 'node:assert/strict';
import { machineState, sessionState, projectName, requestedCommand, paperRoute, offlineLabel, machineLabel, machineGuidance } from '../src/paper-state.js';

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
    assert.equal(offlineLabel('1:42 PM'), 'offline · last seen 1:42 PM');
    assert.equal(offlineLabel(''), 'offline');
});

test('sleep, lost transport and disconnected machines never infer a sleep cause', () => {
    for (const wing of [{ online: false }, { online: false, presence: 'asleep' }, { online: true, tunnel_error: 'unreachable' }]) {
        assert.equal(machineState(wing), 'offline');
        for (const status of ['blocked', 'working', 'completed', 'idle', 'exited']) {
            assert.deepEqual(sessionState(row(status), wing), { key: 'offline', label: 'Offline' });
        }
    }
    assert.deepEqual(sessionState(row('working'), {}), { key: 'work', label: 'Working' });
});

test('online access failures have distinct labels and useful recovery guidance', () => {
    for (const [error, key, label, guidance] of [
        ['passkey_required', 'locked', 'Locked', /Unlock with your passkey/],
        ['passkey_failed', 'locked', 'Locked', /Unlock with your passkey/],
        ['no_passkeys_configured', 'locked', 'Locked', /Set up a passkey/],
        ['not_allowed', 'no-access', 'No access', /Ask the owner/],
        ['key_mismatch', 'key-mismatch', 'Key changed', /refresh this machine’s key/],
        ['other', 'unavailable', 'Unavailable', /check access/],
    ]) {
        const wing = { online: true, tunnel_error: error };
        assert.equal(machineState(wing), key);
        assert.equal(machineLabel(wing), label);
        assert.deepEqual(sessionState(row('working'), wing), { key, label });
        assert.match(machineGuidance(wing), guidance);
        assert.doesNotMatch(machineGuidance(wing), /Reconnect|Sessions kept/);
        assert.equal(machineState({ ...wing, online: false }), 'offline');
    }
});

test('checking presence preserves every last reported lifecycle label', () => {
    for (const status of ['blocked', 'working', 'completed', 'idle', 'exited', 'unknown']) {
        assert.deepEqual(sessionState(row(status), {}), sessionState(row(status), online));
    }
});

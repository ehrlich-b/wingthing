import test from 'node:test';
import assert from 'node:assert/strict';
import { signatureDER, loadDevice, saveDevice, forgetCandidate } from '../src/browser-device.js';
import { webcrypto } from 'node:crypto';

test('browser P-256 signature conversion verifies with DER-consuming native crypto', async () => {
    const { createPublicKey, verify } = await import('node:crypto');
    const keys = await webcrypto.subtle.generateKey({name:'ECDSA',namedCurve:'P-256'},false,['sign','verify']);
    assert.equal(keys.privateKey.extractable,false);
    const publicKey = createPublicKey({key:await webcrypto.subtle.exportKey('jwk',keys.publicKey),format:'jwk'});
    const message = new TextEncoder().encode('wing challenge with connection and sender identity');
    const raw = await webcrypto.subtle.sign({name:'ECDSA',hash:'SHA-256'},keys.privateKey,message);
    assert.equal(verify('sha256',message,publicKey,signatureDER(raw)),true);
    assert.equal(verify('sha256',new TextEncoder().encode('wrong challenge'),publicKey,signatureDER(raw)),false);
    assert.throws(()=>signatureDER(new Uint8Array(63)),/bad_proof/);
});

// A small asynchronous IDB fixture with transaction commit/abort semantics.
// Production storage and CryptoKey cloning are also exercised in Chromium.
function deviceStore(t, initial) {
    let rows = new Map(initial.map(row => [row.wing_id, structuredClone(row)]));
    const db = {
        close() {},
        transaction() {
            const staged = new Map(rows);
            let aborted = false;
            const tx = {
                abort() { aborted = true; queueMicrotask(() => tx.onabort?.()); },
                objectStore() {
                    return {
                        get(wing) {
                            const request = { transaction: tx };
                            queueMicrotask(() => {
                                request.result = structuredClone(staged.get(wing));
                                request.onsuccess?.();
                            });
                            return request;
                        },
                        put(row) { staged.set(row.wing_id, structuredClone(row)); },
                        delete(wing) { staged.delete(wing); },
                    };
                },
            };
            queueMicrotask(() => queueMicrotask(() => {
                if (!aborted) { rows = staged; tx.oncomplete?.(); }
            }));
            return tx;
        },
    };
    const originals = ['isSecureContext', 'indexedDB'].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]);
    t.after(() => {
        for (const [key, descriptor] of originals) {
            if (descriptor) Object.defineProperty(globalThis, key, descriptor);
            else delete globalThis[key];
        }
    });
    globalThis.isSecureContext = true;
    globalThis.indexedDB = {
        open() {
            const request = { result: db };
            queueMicrotask(() => request.onsuccess());
            return request;
        },
    };
}
async function candidate(overrides = {}) {
    const keys = await webcrypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']);
    return { wing_id: 'wing', key: 'pinned-key', device_id: 'candidate', tab_id: 'owner-tab', scope: 'inventory', ready: false, signingKey: keys.privateKey, ...overrides };
}

test('saveDevice stores and updates its own pending candidate', async t => {
    deviceStore(t, []);
    const device = await candidate();
    await saveDevice(device);
    assert.equal((await loadDevice('wing')).device_id, device.device_id);
    await saveDevice({ ...device, name: 'Updated browser' });
    const saved = await loadDevice('wing');
    assert.equal(saved.name, 'Updated browser');
    assert.equal(saved.signingKey.extractable, false);
});

for (const [name, changes] of [
    ['another tab', { tab_id: 'other-tab' }],
    ['another device in this tab', { device_id: 'other-device' }],
    ['a ready record', { ready: true }],
]) {
    test('saveDevice preserves ' + name + ' on failure', async t => {
        const device = await candidate(), saved = { ...device, ...changes };
        deviceStore(t, [saved]);
        await assert.rejects(saveDevice(device), /storage/);
        assert.deepEqual(await loadDevice('wing'), saved);
    });
}

test('forgetCandidate removes only its own pending record', async t => {
    const device = await candidate();
    deviceStore(t, [device, { ...device, wing_id: 'other-wing' }]);
    await forgetCandidate(device);
    assert.equal(await loadDevice('wing'), undefined);
    assert.equal((await loadDevice('other-wing')).device_id, device.device_id);
});

for (const [name, changes] of [
    ['another tab', { tab_id: 'other-tab' }],
    ['another device in this tab', { device_id: 'other-device' }],
    ['a ready record', { ready: true }],
]) {
    test('forgetCandidate preserves ' + name, async t => {
        const device = await candidate(), saved = { ...device, ...changes };
        deviceStore(t, [saved]);
        await forgetCandidate(device);
        assert.deepEqual(await loadDevice('wing'), saved);
    });
}

test('forgetCandidate without a tab identity cannot delete a record', async t => {
    const device = await candidate({ tab_id: undefined });
    deviceStore(t, [device]);
    await forgetCandidate(device);
    assert.deepEqual(await loadDevice('wing'), device);
});

test('forgetCandidate tolerates an already removed candidate', async t => {
    deviceStore(t, []);
    await forgetCandidate(await candidate());
    assert.equal(await loadDevice('wing'), undefined);
});

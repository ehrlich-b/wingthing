import test from 'node:test';
import assert from 'node:assert/strict';
import { x25519 } from '@noble/curves/ed25519.js';
import { bytesToB64 } from '../src/helpers.js';
import { S } from '../src/state.js';

const storage = () => {
    const values = new Map();
    return { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value) };
};
globalThis.sessionStorage = storage();
globalThis.localStorage = storage();
const { deriveE2EKey, e2eEncrypt, e2eDecrypt, identityKey } = await import('../src/crypto.js');

test('invalid wing keys fail before pinning and a valid key can follow', async () => {
    for (const key of [undefined, null, '', '%%%', 'YWJj', bytesToB64(new Uint8Array(32)), 42, {}]) {
        await assert.rejects(deriveE2EKey(key, 'invalid-wing'), /wing encryption key/);
        assert.equal(localStorage.getItem('wt_wing_identity_pins_v1'), null);
    }
    const publicKey = bytesToB64(x25519.getPublicKey(x25519.utils.randomSecretKey()));
    const key = await deriveE2EKey(publicKey, 'invalid-wing');
    assert.equal(key.length, 32);
    assert.equal(JSON.parse(localStorage.getItem('wt_wing_identity_pins_v1'))['invalid-wing'], publicKey);
});

test('unavailable browser identity fails key derivation', async () => {
    const privateKey = identityKey.priv;
    identityKey.priv = null;
    try {
        await assert.rejects(deriveE2EKey(bytesToB64(new Uint8Array(32)), 'wing'), /browser encryption identity unavailable/);
    } finally {
        identityKey.priv = privateKey;
    }
});

test('input encryption has no plaintext fallback and keyed Unicode input round trips', async () => {
    S.e2eKey = null;
    await assert.rejects(e2eEncrypt('private input'), /encryption key unavailable/);
    const publicKey = bytesToB64(x25519.getPublicKey(x25519.utils.randomSecretKey()));
    S.e2eKey = await deriveE2EKey(publicKey, 'roundtrip-wing');
    try {
        const ciphertext = await e2eEncrypt('private λ input\r');
        assert.notEqual(ciphertext, bytesToB64(new TextEncoder().encode('private λ input\r')));
        assert.equal(new TextDecoder().decode(await e2eDecrypt(ciphertext)), 'private λ input\r');
    } finally {
        S.e2eKey = null;
    }
});


test('output decryption rejects plaintext, absent keys and unauthenticated frames', async () => {
    const plaintext = bytesToB64(new TextEncoder().encode('injected terminal output'));
    S.e2eKey = null;
    await assert.rejects(e2eDecrypt(plaintext), { name: 'OutputDecryptionError', message: "Can't decrypt output" });
    S.e2eKey = new Uint8Array(32).fill(7);
    try {
        await assert.rejects(e2eDecrypt(plaintext), { name: 'OutputDecryptionError' });
        const encoded = await e2eEncrypt('authentic output');
        assert.equal(new TextDecoder().decode(await e2eDecrypt(encoded)), 'authentic output');
        S.e2eKey = new Uint8Array(32).fill(8);
        await assert.rejects(e2eDecrypt(encoded), { name: 'OutputDecryptionError' });
        for (const data of ['', '%%%', 'YWJj']) {
            await assert.rejects(e2eDecrypt(data), { name: 'OutputDecryptionError' });
        }
    } finally { S.e2eKey = null; }
});

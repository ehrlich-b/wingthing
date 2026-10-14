import test from 'node:test';
import assert from 'node:assert/strict';
import { signatureDER } from '../src/browser-device.js';
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

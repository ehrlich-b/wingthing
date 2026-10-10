import test from 'node:test';
import assert from 'node:assert/strict';
import { bytesToB64, b64ToBytes, bytesToB64url, b64urlToBytes } from '../src/helpers.js';

test('base64 helpers encode bytes independently of Unicode text and array offsets', () => {
    const fixtures = [new Uint8Array(), new Uint8Array([0]), new Uint8Array([255]),
        new Uint8Array([0, 128, 192, 255, 13, 10]), Uint8Array.from({ length: 256 }, (_, i) => i),
        new Uint8Array([99, 0, 128, 255, 99]).subarray(1, 4)];
    for (const bytes of fixtures) {
        const expected = Buffer.from(bytes).toString('base64');
        assert.equal(bytesToB64(bytes), expected);
        assert.deepEqual(b64ToBytes(expected), bytes);
        assert.equal(bytesToB64url(bytes), expected.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''));
        assert.deepEqual(b64urlToBytes(bytesToB64url(bytes)), bytes);
    }
});

test('bounded terminal-sized byte chunks retain exact output without argument spreading', () => {
    for (const length of [32768, 65536, 200000, 262144]) {
        const bytes = Uint8Array.from({ length }, (_, i) => (i * 31 + 17) % 256);
        assert.equal(bytesToB64(bytes), Buffer.from(bytes).toString('base64'));
        assert.deepEqual(b64ToBytes(bytesToB64(bytes)), bytes);
    }
});

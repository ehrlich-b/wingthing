import test from 'node:test';
import assert from 'node:assert/strict';

import {
    DEFAULT_SESSION_UPLOAD_CHUNK_BYTES,
    MAX_SESSION_UPLOAD_BYTES,
    uploadSessionFile,
} from '../src/upload.js';

function testFile(name, bytes) {
    return {
        name,
        size: bytes.length,
        slice(start, end) {
            var slice = bytes.slice(start, end);
            return { arrayBuffer: async function() { return slice.buffer.slice(slice.byteOffset, slice.byteOffset + slice.byteLength); } };
        },
    };
}

test('uploadSessionFile chunks bytes in order and finishes the upload', async function() {
    var bytes = new Uint8Array(DEFAULT_SESSION_UPLOAD_CHUNK_BYTES + 3);
    bytes[0] = 0;
    bytes[DEFAULT_SESSION_UPLOAD_CHUNK_BYTES] = 255;
    bytes[bytes.length - 1] = 7;
    var calls = [];
    var progress = [];
    var send = async function(wingId, message) {
        calls.push({ wingId, message });
        if (message.type === 'file.upload.begin') {
            return { upload_id: 'upload-1', chunk_size: DEFAULT_SESSION_UPLOAD_CHUNK_BYTES };
        }
        if (message.type === 'file.upload.chunk') {
            return { received: message.offset + Buffer.from(message.data, 'base64').length };
        }
        if (message.type === 'file.upload.finish') return { name: 'evidence.bin', size: bytes.length };
        throw new Error('unexpected call');
    };

    var result = await uploadSessionFile(send, 'wing-1', 'session-1', testFile('evidence.bin', bytes), function(done, total) {
        progress.push([done, total]);
    });

    assert.equal(result.name, 'evidence.bin');
    assert.deepEqual(calls.map(function(call) { return call.message.type; }), [
        'file.upload.begin',
        'file.upload.chunk',
        'file.upload.chunk',
        'file.upload.finish',
    ]);
    assert.equal(calls[1].message.offset, 0);
    assert.equal(calls[2].message.offset, DEFAULT_SESSION_UPLOAD_CHUNK_BYTES);
    assert.deepEqual(Buffer.from(calls[1].message.data, 'base64'), Buffer.from(bytes.slice(0, DEFAULT_SESSION_UPLOAD_CHUNK_BYTES)));
    assert.deepEqual(Buffer.from(calls[2].message.data, 'base64'), Buffer.from(bytes.slice(DEFAULT_SESSION_UPLOAD_CHUNK_BYTES)));
    assert.deepEqual(progress, [
        [DEFAULT_SESSION_UPLOAD_CHUNK_BYTES, bytes.length],
        [bytes.length, bytes.length],
    ]);
});

test('uploadSessionFile cancels an admitted upload after a chunk failure', async function() {
    var calls = [];
    var send = async function(_wingId, message) {
        calls.push(message.type);
        if (message.type === 'file.upload.begin') return { upload_id: 'upload-2', chunk_size: 8 };
        if (message.type === 'file.upload.cancel') return { ok: 'true' };
        throw new Error('connection lost');
    };

    await assert.rejects(
        uploadSessionFile(send, 'wing-1', 'session-1', testFile('notes.txt', new Uint8Array([1, 2, 3]))),
        /connection lost/
    );
    assert.deepEqual(calls, ['file.upload.begin', 'file.upload.chunk', 'file.upload.cancel']);
});

test('uploadSessionFile rejects an oversized file before contacting the wing', async function() {
    var called = false;
    await assert.rejects(
        uploadSessionFile(async function() { called = true; }, 'wing-1', 'session-1', {
            name: 'too-large.bin',
            size: MAX_SESSION_UPLOAD_BYTES + 1,
        }),
        /25 MiB/
    );
    assert.equal(called, false);
});

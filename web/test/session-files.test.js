import test from 'node:test';
import assert from 'node:assert/strict';

import { downloadSessionFile, uploadSessionFile, validSessionFilePath } from '../src/session-files.js';

test('session file paths accept relative and absolute policy-checked paths', function() {
    assert.equal(validSessionFilePath('reports/findings.md'), true);
    assert.equal(validSessionFilePath('/allowed/data/findings.md'), true);
    assert.equal(validSessionFilePath('../allowed-sibling/file'), true);
    assert.equal(validSessionFilePath(''), false);
    assert.equal(validSessionFilePath('bad\0path'), false);
});

test('upload sends ordered bounded chunks and finishes', async function() {
    var calls = [];
    var send = async function(_wingId, message) {
        calls.push(message);
        if (message.type === 'file.upload.begin') return { upload_id: 'upload-1', chunk_size: 3 };
        if (message.type === 'file.upload.chunk') return { received: message.offset + Buffer.from(message.data, 'base64').length };
        if (message.type === 'file.upload.finish') return {
            ok: true,
            name: 'note.txt',
            path: 'note.txt',
            size: 5,
            sha256: '2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824',
        };
        throw new Error('unexpected request');
    };
    var file = new File(['hello'], 'note.txt', { type: 'text/plain' });

    var result = await uploadSessionFile(send, 'wing-1', 'session-1', file);

    assert.equal(result.ok, true);
    assert.deepEqual(calls.map(function(call) { return call.type; }), [
        'file.upload.begin', 'file.upload.chunk', 'file.upload.chunk', 'file.upload.finish',
    ]);
    assert.deepEqual(calls.filter(function(call) { return call.type === 'file.upload.chunk'; }).map(function(call) { return call.offset; }), [0, 3]);
});

test('download verifies checksums without secure-origin browser crypto', async function() {
    var checksum = '2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824';
    var stream = async function(_wingId, _request, onChunk) {
        onChunk({ name: 'note.txt', size: 5, mime: 'text/plain' });
        onChunk({ data: Buffer.from('hello').toString('base64') });
        onChunk({ sha256: checksum });
    };

    var file = await downloadSessionFile(stream, 'wing-1', 'session-1', 'note.txt');

    assert.equal(file.name, 'note.txt');
    assert.equal(new TextDecoder().decode(file.bytes), 'hello');
    assert.equal(file.sha256, checksum);
});

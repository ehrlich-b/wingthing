import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { formatAuditTime } from '../src/helpers.js';

test('archive replay renders its plain base64 format without a live PTY key', async () => {
    const elements = new Map();
    const element = id => {
        if (!elements.has(id)) elements.set(id, { style: {}, textContent: '', innerHTML: '' });
        return elements.get(id);
    };
    const writes = [];
    class Terminal {
        loadAddon() {} open() {} dispose() {}
        write(data) { writes.push(data); }
    }
    const text = 'Plain archive session λ\r\n';
    const context = vm.createContext({
        Terminal, FitAddon: class {}, TextDecoder, Uint8Array, atob,
        document: { getElementById: element }, history: { pushState() {} },
        formatAuditTime,
        sendTunnelStream: async (wingId, request, onChunk) => {
            assert.equal(wingId, 'archive-wing');
            assert.equal(request.kind, 'pty');
            onChunk({ version: 2, width: 80, height: 24 });
            // Archive records are plain base64 inside the authenticated tunnel,
            // rather than individual live PTY AES-GCM envelopes.
            onChunk([0, 'o', Buffer.from(text).toString('base64')]);
        },
    });
    const source = readFileSync(new URL('../src/audit.js', import.meta.url), 'utf8')
        .replace(/^import .*;\n/gm, '').replace(/^export /gm, '');
    vm.runInContext(source, context, { filename: 'audit.js' });
    context.openAuditReplay('archive-wing', 'archive-session');
    await Promise.resolve();
    assert.equal(element('audit-play').disabled, false);
    element('audit-play').onclick();
    assert.deepEqual(writes, [text]);
});

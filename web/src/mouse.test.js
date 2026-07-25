// node --test web/src/mouse.test.js
//
// The payloads here are real bytes captured from Claude Code on a pty, not
// invented ones — the startup sequence is exactly what it emits today.
import test from 'node:test';
import assert from 'node:assert';
import { writeTerm, appMouseActive, wheelReport } from './mouse.js';

const CLAUDE_STARTUP = '\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h';
const dec = new TextDecoder();

function mkTerm() {
    const t = { out: '' };
    t.write = (bytes) => { t.out += dec.decode(bytes); };
    return t;
}

test('claude startup: mouse modes stripped, everything else survives', () => {
    const t = mkTerm();
    writeTerm(t, CLAUDE_STARTUP + 'hello');
    assert.equal(t.out, '\x1b[?1049hhello');
    assert.equal(appMouseActive(t), true);
    assert.equal(wheelReport(t, true, 10, 5), '\x1b[<64;10;5M');
    assert.equal(wheelReport(t, false, 10, 5), '\x1b[<65;10;5M');
});

test('combined params keep the modes that are not ours', () => {
    const t = mkTerm();
    writeTerm(t, 'a\x1b[?1002;1006hb\x1b[?1049;1003hc');
    assert.equal(t.out, 'ab\x1b[?1049hc');
    assert.equal(appMouseActive(t), true);
});

test('sequences split across chunk boundaries are still caught', () => {
    const t = mkTerm();
    for (const ch of 'x' + CLAUDE_STARTUP + 'y') writeTerm(t, ch);
    assert.equal(t.out, 'x\x1b[?1049hy');
    assert.equal(appMouseActive(t), true);
});

test('reset turns tracking back off', () => {
    const t = mkTerm();
    writeTerm(t, CLAUDE_STARTUP);
    writeTerm(t, '\x1b[?1000l\x1b[?1002l\x1b[?1003l');
    assert.equal(appMouseActive(t), false);
    assert.equal(wheelReport(t, true, 1, 1), null);
});

test('leaving the alt screen drops tracking even if the app never did', () => {
    const t = mkTerm();
    writeTerm(t, CLAUDE_STARTUP);
    writeTerm(t, '\x1b[?1049l');          // TUI exits/dies without disabling the mouse
    assert.equal(appMouseActive(t), false);
    assert.equal(t.out, '\x1b[?1049h\x1b[?1049l');
});

test('unrelated escape sequences pass through byte-identical', () => {
    const t = mkTerm();
    const other = 'plain \x1b[31mred\x1b[0m \x1b[?2004h \x1b[?1004h \x1b[?25l \x1b[2J\x1b[H done';
    writeTerm(t, other);
    assert.equal(t.out, other);
    assert.equal(appMouseActive(t), false);
});

test('a partial sequence is held, then completed on the next write', () => {
    const t = mkTerm();
    writeTerm(t, 'a\x1b');
    assert.equal(t.out, 'a');
    writeTerm(t, '[?1002h!');
    assert.equal(t.out, 'a!');
    assert.equal(appMouseActive(t), true);
});

test('a held escape that turns out not to be ours is re-emitted', () => {
    const t = mkTerm();
    writeTerm(t, 'a\x1b');
    writeTerm(t, '[31mred');
    assert.equal(t.out, 'a\x1b[31mred');
});

test('x10 encoding when the app never asks for sgr', () => {
    const t = mkTerm();
    writeTerm(t, '\x1b[?1000h');
    assert.equal(wheelReport(t, true, 10, 5), '\x1b[M' + String.fromCharCode(96, 42, 37));
});

test('utf-8 payloads survive the round trip', () => {
    const t = mkTerm();
    writeTerm(t, '▔▔ café ⏺ 日本語 \x1b[?1006h✓');
    assert.equal(t.out, '▔▔ café ⏺ 日本語 ✓');
});

test('write callbacks still fire when the whole chunk is held back', () => {
    const t = mkTerm();
    let called = false;
    t.write = (bytes, cb) => { t.out += dec.decode(bytes); if (cb) cb(); };
    writeTerm(t, '\x1b', () => { called = true; });
    assert.equal(called, true, 'replay overlay hangs forever if this callback is dropped');
});

test('terminals keep their own state', () => {
    const a = mkTerm(), b = mkTerm();
    writeTerm(a, CLAUDE_STARTUP);
    writeTerm(b, 'plain output');
    assert.equal(appMouseActive(a), true);
    assert.equal(appMouseActive(b), false);
});

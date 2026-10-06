import test from 'node:test';
import assert from 'node:assert/strict';
import { conversationViewport } from '../src/mobile-viewport.js';

test('composer fits the visible iOS viewport including keyboard scroll offset', () => {
    assert.deepEqual(conversationViewport({ height: 844, offsetTop: 0, scale: 1 }, 844, true), { height: 844, top: 0, keyboard: false });
    const keyboard = conversationViewport({ height: 430, offsetTop: 34, scale: 1 }, 844, true);
    assert.deepEqual(keyboard, { height: 430, top: 34, keyboard: true });
    assert.equal(keyboard.top + keyboard.height, 464, 'app bottom equals the visible viewport bottom');
    assert.deepEqual(conversationViewport({ height: 390, offsetTop: 0, scale: 1 }, 390, true), { height: 390, top: 0, keyboard: false });
});

test('desktop and terminal retain height fitting without phone translation or keyboard styling', () => {
    assert.deepEqual(conversationViewport({ height: 500, offsetTop: 70, scale: 1 }, 844, false), { height: 500, top: 0, keyboard: false });
    assert.equal(conversationViewport({ height: 300, offsetTop: 10, scale: 2 }, 844, true), null, 'pinch zoom must not move the app');
    assert.equal(conversationViewport(null, 844, true), null);
    assert.equal(conversationViewport({ height: NaN }, 844, true), null);
    assert.equal(conversationViewport({ height: 0 }, 844, true), null);
});

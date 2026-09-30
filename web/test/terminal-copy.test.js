import test from 'node:test';
import assert from 'node:assert/strict';

import { cleanTerminalSelection, copyTextWithFallback, terminalClipboardAvailable } from '../src/terminal-selection.js';

test('terminal copy removes screen padding while preserving meaningful layout', function() {
    assert.equal(
        cleanTerminalSelection('\n    SELECT *     \n      FROM devices;       \n\n'),
        '    SELECT *\n      FROM devices;'
    );
});

test('terminal copy keeps intentional internal blank lines', function() {
    assert.equal(cleanTerminalSelection('first   \n   \nsecond'), 'first\n\nsecond');
});

test('terminal copy uses the synchronous browser fallback on an insecure origin', async function() {
    var copied = '';
    var input;
    var doc = {
        activeElement: { focus() {} },
        body: { appendChild(node) { input = node; } },
        createElement() {
            return {
                value: '',
                style: {},
                setAttribute() {},
                focus() {},
                select() {},
                remove() {},
            };
        },
        execCommand(command) {
            if (command === 'copy') copied = input.value;
            return command === 'copy';
        },
    };

    assert.equal(terminalClipboardAvailable({}, doc), true);
    assert.equal(await copyTextWithFallback('clean text', {}, doc), true);
    assert.equal(copied, 'clean text');
});

test('terminal copy reports failure when no clipboard path is available', async function() {
    assert.equal(terminalClipboardAvailable({}, {}), false);
    assert.equal(await copyTextWithFallback('text', {}, {}), false);
});

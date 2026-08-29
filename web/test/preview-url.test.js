import test from 'node:test';
import assert from 'node:assert/strict';

import { DOM, S } from '../src/state.js';
import { activateURLPreview, closePreview, handlePreview } from '../src/preview.js';

function fakeElement() {
    return {
        style: {},
        attributes: {},
        classList: { add() {}, remove() {} },
        setAttribute(name, value) { this.attributes[name] = String(value); },
        removeAttribute(name) {
            delete this.attributes[name];
            if (name === 'src' || name === 'srcdoc' || name === 'href') delete this[name];
        },
    };
}

function installPreviewDOM() {
    DOM.previewPanel = fakeElement();
    // Treat the panel as already open so handlePreview updates synchronously.
    DOM.previewPanel.style.display = '';
    DOM.previewDivider = fakeElement();
    DOM.terminalSection = fakeElement();
    DOM.previewIframe = fakeElement();
    DOM.previewTitle = fakeElement();
    DOM.previewUrlBar = fakeElement();
    DOM.previewUrl = fakeElement();
    DOM.previewCopyBtn = fakeElement();
    DOM.previewLoadBtn = fakeElement();
    DOM.previewOpenBtn = fakeElement();
    DOM.previewDownloadBtn = fakeElement();
    S.fitAddon = null;
}

test('receiving an agent URL preview cannot make a browser network request', function() {
    installPreviewDOM();
    handlePreview({ mode: 'url', url: 'https://egress.example.test/leaked-secret' });

    assert.equal(DOM.previewIframe.src, undefined);
    assert.match(DOM.previewIframe.srcdoc, /Loading it will make a network request/);
    assert.equal(DOM.previewIframe.attributes.sandbox, '');
    assert.equal(DOM.previewLoadBtn.style.display, '');
    assert.equal(DOM.previewOpenBtn.href, 'https://egress.example.test/leaked-secret');

    assert.equal(activateURLPreview(), true);
    assert.equal(DOM.previewIframe.src, 'https://egress.example.test/leaked-secret');
    assert.equal(DOM.previewIframe.srcdoc, undefined);
    assert.equal(DOM.previewIframe.attributes.sandbox, 'allow-scripts');
    assert.equal(DOM.previewLoadBtn.style.display, 'none');

    closePreview();
});

test('a replacement URL requires a fresh explicit activation', function() {
    installPreviewDOM();
    handlePreview({ mode: 'url', url: 'https://first.example.test/' });
    assert.equal(activateURLPreview(), true);
    assert.equal(DOM.previewIframe.src, 'https://first.example.test/');

    handlePreview({ mode: 'url', url: 'https://second.example.test/' });
    assert.equal(DOM.previewIframe.src, undefined);
    assert.equal(DOM.previewOpenBtn.href, 'https://second.example.test/');
    assert.equal(DOM.previewLoadBtn.style.display, '');

    closePreview();
});

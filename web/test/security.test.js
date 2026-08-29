import test from 'node:test';
import assert from 'node:assert/strict';

import {
    escapeMarkup,
    renderSafeSimpleMarkdown,
    safePreviewURL,
    safeTerminalThumbnail,
} from '../src/security.js';

test('escapeMarkup is safe in text and quoted attribute contexts', function() {
    assert.equal(
        escapeMarkup('<img title="x" data-y=\'z\'>&'),
        '&lt;img title=&quot;x&quot; data-y=&#39;z&#39;&gt;&amp;'
    );
    assert.equal(escapeMarkup(null), '');
});

test('renderSafeSimpleMarkdown never passes agent HTML through', function() {
    assert.equal(
        renderSafeSimpleMarkdown('<img src=x onerror="alert(1)"> **safe**'),
        '&lt;img src=x onerror=&quot;alert(1)&quot;&gt; <strong>safe</strong>'
    );
    assert.equal(
        renderSafeSimpleMarkdown('`<b>**literal**</b>`\n```html\n<script>alert(1)</script>\n```'),
        '<code>&lt;b&gt;**literal**&lt;/b&gt;</code><br>' +
            '<pre class="cv-code"><code>&lt;script&gt;alert(1)&lt;/script&gt;</code></pre>'
    );
});

test('safePreviewURL accepts absolute HTTP and HTTPS URLs', function() {
    assert.equal(safePreviewURL('https://example.com/app?q=1'), 'https://example.com/app?q=1');
    assert.equal(safePreviewURL('http://127.0.0.1:3000/'), 'http://127.0.0.1:3000/');
});

test('safePreviewURL rejects executable, relative, credentialed, and oversized URLs', function() {
    assert.equal(safePreviewURL('javascript:alert(1)'), null);
    assert.equal(safePreviewURL('data:text/html,<script>alert(1)</script>'), null);
    assert.equal(safePreviewURL('//example.com/app'), null);
    assert.equal(safePreviewURL('/local/path'), null);
    assert.equal(safePreviewURL('https://user:secret@example.com/'), null);
    assert.equal(safePreviewURL('https://example.com/' + 'a'.repeat(8192)), null);
    assert.equal(safePreviewURL(null), null);
});

test('safeTerminalThumbnail accepts only generated WebP data URLs', function() {
    assert.equal(safeTerminalThumbnail('data:image/webp;base64,YWJjZA=='), 'data:image/webp;base64,YWJjZA==');
    assert.equal(safeTerminalThumbnail('data:image/svg+xml,<svg onload=alert(1)>'), '');
    assert.equal(safeTerminalThumbnail('javascript:alert(1)'), '');
    assert.equal(safeTerminalThumbnail('https://example.com/tracker.png'), '');
});

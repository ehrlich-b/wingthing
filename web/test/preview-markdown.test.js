import test from 'node:test';
import assert from 'node:assert/strict';

import { renderNetworkInertMarkdown } from '../src/preview-markdown.js';

test('agent-authored Markdown cannot create active HTML or navigable links', function() {
    var html = renderNetworkInertMarkdown(
        '<script>alert(1)</script> [click](javascript:alert(2)) [site](https://example.test/path)'
    );

    assert.doesNotMatch(html, /<script|<a\b|href=/i);
    assert.match(html, /&lt;script&gt;/);
    assert.match(html, /javascript:alert\(2\)/);
    assert.match(html, /https:\/\/example\.test\/path/);
});

test('agent-authored Markdown images never create browser fetches', function() {
    var html = renderNetworkInertMarkdown(
        '![loopback](http://127.0.0.1:8080/private) ![metadata](http://169.254.169.254/latest/meta-data/)'
    );

    assert.doesNotMatch(html, /<img\b|\bsrc=/i);
    assert.doesNotMatch(html, /127\.0\.0\.1|169\.254\.169\.254/);
    assert.match(html, /\[image: loopback\]/);
    assert.match(html, /\[image: metadata\]/);
});

test('ordinary Markdown structure and code remain readable', function() {
    var html = renderNetworkInertMarkdown('# Result\n\n- **done**\n\n```sh\necho ok\n```');

    assert.match(html, /<h1>Result<\/h1>/);
    assert.match(html, /<strong>done<\/strong>/);
    assert.match(html, /<code class="language-sh">echo ok/);
});

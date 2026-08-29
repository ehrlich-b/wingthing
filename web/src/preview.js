import { S, DOM } from './state.js';
import { safePreviewURL } from './security.js';
import { renderNetworkInertMarkdown } from './preview-markdown.js';

var SPLIT_KEY = 'wt_preview_split';

// Extensions rendered as markdown; everything else renders as plain source.
var MD_EXT = /\.(md|markdown|mdown|mkd)$/i;

// What the download button hands back. Populated in content mode, cleared in
// URL mode (where "open" already covers it) and on close.
var current = null;
var currentPreviewURL = null;

var URL_PREVIEW_DISCLOSURE = '<!DOCTYPE html><meta charset="utf-8"><style>'
    + 'body{font-family:system-ui,sans-serif;padding:24px;color:#333;background:#fff;line-height:1.5}'
    + '</style><p>This URL came from the agent. Loading it will make a network request from your browser. Review the address above, then choose <strong>load preview</strong>.</p>';

function escapeHTML(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function savedRatio() {
    try {
        var v = parseFloat(localStorage.getItem(SPLIT_KEY));
        if (v > 0.1 && v < 0.9) return v;
    } catch (e) {}
    return 0.5;
}

function applyWidth(ratio) {
    DOM.previewPanel.style.flexBasis = (ratio * 100) + '%';
    DOM.previewPanel.style.flexGrow = '0';
    DOM.previewPanel.style.flexShrink = '0';
}

function isOpen() {
    return DOM.previewPanel.style.display !== 'none';
}

function setContent(opts) {
    if (opts.mode === 'url') {
        current = null;
        currentPreviewURL = null;
        DOM.previewDownloadBtn.style.display = 'none';
        DOM.previewLoadBtn.style.display = 'none';
        DOM.previewOpenBtn.removeAttribute('href');
        DOM.previewOpenBtn.setAttribute('rel', 'noopener noreferrer');
        var previewURL = safePreviewURL(opts.url);
        if (!previewURL) {
            DOM.previewTitle.textContent = 'Preview blocked';
            DOM.previewIframe.removeAttribute('src');
            DOM.previewIframe.setAttribute('sandbox', '');
            DOM.previewIframe.srcdoc = '<!DOCTYPE html><meta charset="utf-8"><style>'
                + 'body{font-family:system-ui,sans-serif;padding:24px;color:#722;background:#fff}'
                + '</style><p>wingthing blocked an unsafe preview URL. Only absolute HTTP and HTTPS URLs without embedded credentials are allowed.</p>';
            DOM.previewUrlBar.style.display = 'none';
            return;
        }
        currentPreviewURL = previewURL;
        DOM.previewTitle.textContent = 'Preview';
        // Do not automatically fetch an agent-authored URL. Otherwise a
        // network-confined agent could put a secret in the URL and use the
        // attached browser as an egress proxy. The user must explicitly load
        // each new URL after reviewing it.
        DOM.previewIframe.removeAttribute('src');
        DOM.previewIframe.setAttribute('sandbox', '');
        DOM.previewIframe.srcdoc = URL_PREVIEW_DISCLOSURE;
        DOM.previewUrlBar.style.display = '';
        DOM.previewUrl.textContent = previewURL;
        DOM.previewCopyBtn.textContent = 'copy';
        DOM.previewLoadBtn.style.display = '';
        DOM.previewLoadBtn.textContent = 'load preview';
        DOM.previewOpenBtn.href = previewURL;
    } else {
        currentPreviewURL = null;
        DOM.previewLoadBtn.style.display = 'none';
        // Old wings send no filename/mime — markdown is the historical default.
        var content = opts.content || '';
        var filename = opts.filename || 'preview.md';
        current = {
            content: content,
            filename: filename,
            mime: opts.mime || 'text/markdown'
        };
        DOM.previewDownloadBtn.style.display = '';
        DOM.previewTitle.textContent = opts.filename ? filename : 'Preview';

        var body;
        if (MD_EXT.test(filename)) {
            body = renderNetworkInertMarkdown(content);
        } else {
            // Any other file type: show the source verbatim, never as markdown.
            body = '<pre class="src">' + escapeHTML(content) + '</pre>';
        }
        var doc = '<!DOCTYPE html><html><head>'
            + '<meta http-equiv="Content-Security-Policy" content="default-src &#39;none&#39;; style-src &#39;unsafe-inline&#39;">'
            + '<style>'
            + 'body{font-family:system-ui,sans-serif;font-size:14px;line-height:1.6;padding:16px;margin:0;background:#fff;color:#222;}'
            + 'pre{background:#f5f5f5;padding:12px;border-radius:4px;overflow-x:auto;}'
            + 'code{font-family:monospace;font-size:13px;}'
            + 'table{border-collapse:collapse;width:100%;margin:8px 0;}'
            + 'th,td{border:1px solid #ddd;padding:6px 10px;text-align:left;}'
            + 'th{background:#f5f5f5;font-weight:600;}'
            + '.preview-link-target,.preview-image-label{color:#555;}'
            + 'pre.src{font-family:monospace;font-size:13px;line-height:1.45;white-space:pre;}'
            + '</style></head><body>' + body + '</body></html>';
        DOM.previewIframe.removeAttribute('src');
        // Rendered agent content does not need scripts or a normal origin.
        DOM.previewIframe.setAttribute('sandbox', '');
        DOM.previewIframe.srcdoc = doc;
        DOM.previewUrlBar.style.display = 'none';
    }
}

export function handlePreview(opts) {
    if (!opts || !opts.mode) {
        closePreview();
        return;
    }

    if (isOpen()) {
        // Already open — just swap content
        setContent(opts);
        return;
    }

    // Open sequence — jank-free
    var ratio = savedRatio();
    DOM.previewDivider.style.display = '';
    DOM.previewPanel.style.display = '';
    DOM.previewPanel.style.background = 'var(--bg)';
    applyWidth(ratio);
    DOM.terminalSection.classList.add('has-preview');
    if (S.fitAddon) S.fitAddon.fit();

    requestAnimationFrame(function() {
        setContent(opts);
        DOM.previewPanel.style.background = '';
    });
}

export function closePreview() {
    current = null;
    currentPreviewURL = null;
    DOM.previewDownloadBtn.style.display = 'none';
    DOM.previewLoadBtn.style.display = 'none';
    DOM.previewPanel.style.display = 'none';
    DOM.previewDivider.style.display = 'none';
    DOM.terminalSection.classList.remove('has-preview');
    DOM.previewIframe.removeAttribute('src');
    DOM.previewIframe.removeAttribute('srcdoc');
    if (S.fitAddon) S.fitAddon.fit();
}

// Loading is deliberately separate from handlePreview: receiving an
// agent-authored message must never itself cause a browser network request.
export function activateURLPreview() {
    var previewURL = safePreviewURL(currentPreviewURL);
    if (!previewURL) return false;
    DOM.previewIframe.removeAttribute('srcdoc');
    // Agent-selected pages may run scripts, but receive an opaque origin.
    // Never combine allow-scripts with allow-same-origin here: a redirect to
    // this portal's origin could otherwise escape the iframe sandbox.
    DOM.previewIframe.setAttribute('sandbox', 'allow-scripts');
    DOM.previewIframe.src = previewURL;
    DOM.previewLoadBtn.style.display = 'none';
    return true;
}

// Copy button
function initCopyBtn() {
    DOM.previewCopyBtn.addEventListener('click', function() {
        var url = DOM.previewUrl.textContent;
        navigator.clipboard.writeText(url);
        DOM.previewCopyBtn.textContent = 'copied!';
        setTimeout(function() { DOM.previewCopyBtn.textContent = 'copy'; }, 1500);
    });
}

function initLoadBtn() {
    DOM.previewLoadBtn.addEventListener('click', function() {
        activateURLPreview();
    });
}

// Download button — saves the previewed content under the name and type the
// agent declared via the "file:" header.
function initDownloadBtn() {
    DOM.previewDownloadBtn.addEventListener('click', function() {
        if (!current) return;
        var blob = new Blob([current.content], { type: current.mime });
        var url = URL.createObjectURL(blob);
        var a = document.createElement('a');
        a.href = url;
        a.download = current.filename;
        a.click();
        setTimeout(function() { URL.revokeObjectURL(url); }, 0);
    });
}

// Close button
function initCloseBtn() {
    DOM.previewCloseBtn.addEventListener('click', function() {
        closePreview();
    });
}

// Divider drag
function initDividerDrag() {
    var dragging = false;

    DOM.previewDivider.addEventListener('mousedown', function(e) {
        e.preventDefault();
        dragging = true;
        document.body.style.cursor = 'col-resize';
        document.body.style.userSelect = 'none';
    });

    document.addEventListener('mousemove', function(e) {
        if (!dragging) return;
        var rect = DOM.terminalSection.getBoundingClientRect();
        var x = e.clientX - rect.left;
        var ratio = 1 - (x / rect.width);
        if (ratio < 0.15) ratio = 0.15;
        if (ratio > 0.85) ratio = 0.85;
        applyWidth(ratio);
        if (S.fitAddon) S.fitAddon.fit();
    });

    document.addEventListener('mouseup', function() {
        if (!dragging) return;
        dragging = false;
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        // Save ratio
        var rect = DOM.terminalSection.getBoundingClientRect();
        var panelRect = DOM.previewPanel.getBoundingClientRect();
        var ratio = panelRect.width / rect.width;
        try { localStorage.setItem(SPLIT_KEY, String(ratio)); } catch (e) {}
    });
}

export function initPreview() {
    initCopyBtn();
    initLoadBtn();
    initDownloadBtn();
    initCloseBtn();
    initDividerDrag();
}

import { marked } from 'marked';
import { S, DOM } from './state.js';

var SPLIT_KEY = 'wt_preview_split';

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
        DOM.previewIframe.removeAttribute('srcdoc');
        DOM.previewIframe.setAttribute('sandbox', 'allow-scripts allow-same-origin');
        DOM.previewIframe.src = opts.url;
        DOM.previewUrlBar.style.display = '';
        DOM.previewUrl.textContent = opts.url;
        DOM.previewCopyBtn.textContent = 'copy';
        DOM.previewOpenBtn.href = opts.url;
    } else {
        // markdown mode — strip HTML tags, render with marked
        var clean = (opts.content || '').replace(/<[^>]*>/g, '');
        var html = marked(clean);
        var doc = '<!DOCTYPE html><html><head><style>'
            + 'body{font-family:system-ui,sans-serif;font-size:14px;line-height:1.6;padding:16px;margin:0;background:#fff;color:#222;}'
            + 'pre{background:#f5f5f5;padding:12px;border-radius:4px;overflow-x:auto;}'
            + 'code{font-family:monospace;font-size:13px;}'
            + 'table{border-collapse:collapse;width:100%;margin:8px 0;}'
            + 'th,td{border:1px solid #ddd;padding:6px 10px;text-align:left;}'
            + 'th{background:#f5f5f5;font-weight:600;}'
            + 'img{max-width:100%;}'
            + 'a{color:#0066cc;}'
            + '</style></head><body>' + html + '</body></html>';
        DOM.previewIframe.removeAttribute('src');
        DOM.previewIframe.setAttribute('sandbox', 'allow-same-origin');
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
    DOM.previewPanel.style.display = 'none';
    DOM.previewDivider.style.display = 'none';
    DOM.terminalSection.classList.remove('has-preview');
    DOM.previewIframe.removeAttribute('src');
    DOM.previewIframe.removeAttribute('srcdoc');
    if (S.fitAddon) S.fitAddon.fit();
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
    initCloseBtn();
    initDividerDrag();
}

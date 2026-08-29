// Security helpers shared by browser code and dependency-free Node tests.

// This helper is intentionally safe in both text and quoted attribute
// contexts. DOM text-node serialization does not need to escape quotes, so it
// is not sufficient when the resulting string is later used in innerHTML.
export function escapeMarkup(raw) {
    return String(raw == null ? '' : raw)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

function renderEmphasis(raw) {
    return escapeMarkup(raw)
        .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
        .replace(/\*(.+?)\*/g, '<em>$1</em>')
        .replace(/\n/g, '<br>');
}

function renderInlineMarkdown(raw) {
    var result = '';
    var cursor = 0;
    var match;
    var inlineCode = /`([^`\n]+)`/g;
    while ((match = inlineCode.exec(raw)) !== null) {
        result += renderEmphasis(raw.slice(cursor, match.index));
        result += '<code>' + escapeMarkup(match[1]) + '</code>';
        cursor = match.index + match[0].length;
    }
    return result + renderEmphasis(raw.slice(cursor));
}

// Deliberately small Markdown renderer for agent-authored chat output. Escape
// every source byte before introducing the handful of tags that we own.
export function renderSafeSimpleMarkdown(raw) {
    var source = String(raw == null ? '' : raw);
    var result = '';
    var cursor = 0;
    var match;
    var codeBlock = /```\w*\n([\s\S]*?)```/g;
    while ((match = codeBlock.exec(source)) !== null) {
        result += renderInlineMarkdown(source.slice(cursor, match.index));
        result += '<pre class="cv-code"><code>' +
            escapeMarkup(match[1].replace(/\n$/, '')) +
            '</code></pre>';
        cursor = match.index + match[0].length;
    }
    return result + renderInlineMarkdown(source.slice(cursor));
}

// Preview URLs are supplied by an agent through .wt-preview. Require an
// absolute HTTP(S) URL and reject embedded credentials so the preview cannot
// turn a user click into javascript execution or credential disclosure.
export function safePreviewURL(raw) {
    if (typeof raw !== 'string' || raw.length > 8192) return null;
    var parsed;
    try {
        parsed = new URL(raw);
    } catch (e) {
        return null;
    }
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null;
    if (parsed.username || parsed.password) return null;
    return parsed.href;
}

// Terminal thumbnails are generated locally as WebP data URLs. localStorage
// is writable by any script running in this origin, so do not copy arbitrary
// values from it into an HTML src attribute.
export function safeTerminalThumbnail(raw) {
    if (typeof raw !== 'string') return '';
    return /^data:image\/webp;base64,[A-Za-z0-9+/]+={0,2}$/.test(raw) ? raw : '';
}

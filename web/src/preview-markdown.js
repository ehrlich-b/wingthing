import { marked, Renderer } from 'marked';
import { escapeMarkup } from './security.js';

// Markdown previews are agent-authored content. The srcdoc sandbox disables
// scripts, but normal Markdown images would still make the user's browser send
// requests to attacker-chosen public, intranet, or loopback URLs. Render the
// useful text formatting while making the document completely network-inert.
var renderer = new Renderer();

renderer.html = function(token) {
    return escapeMarkup(token.text);
};

renderer.link = function(token) {
    var label = this.parser.parseInline(token.tokens || []);
    return label + ' <code class="preview-link-target">' + escapeMarkup(token.href || '') + '</code>';
};

renderer.image = function(token) {
    var label = token.text ? ': ' + token.text : '';
    return '<span class="preview-image-label">[image' + escapeMarkup(label) + ']</span>';
};

export function renderNetworkInertMarkdown(content) {
    return marked(String(content == null ? '' : content), { renderer: renderer });
}

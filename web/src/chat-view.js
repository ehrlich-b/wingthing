// chat-view.js — Mobile chat view: renders JSONL as HTML, input types into PTY

import { S } from './state.js';
import { sendTunnelRequest } from './tunnel.js';
import { sendPTYInput } from './terminal.js';
import { parseJSONL } from './chat.js';

var pollTimer = null;
var pollOffset = 0;
var pollAgent = '';
var messages = [];
var container = null;
var inputEl = null;
var sendBtn = null;
var statusEl = null;
var thinking = false;

export function initChatView() {
    container = document.getElementById('chat-messages');
    inputEl = document.getElementById('chat-input');
    sendBtn = document.getElementById('chat-send');
    statusEl = document.getElementById('chat-view-status');

    sendBtn.addEventListener('click', submitInput);
    inputEl.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            submitInput();
        }
    });
    inputEl.addEventListener('input', autoGrow);
}

function autoGrow() {
    inputEl.style.height = 'auto';
    inputEl.style.height = Math.min(inputEl.scrollHeight, 120) + 'px';
}

function submitInput() {
    var text = inputEl.value.trim();
    if (!text || thinking) return;
    sendPTYInput(text + '\r');
    // Optimistically add user message
    messages.push({ type: 'user', content: text });
    renderMessages();
    inputEl.value = '';
    autoGrow();
    setThinking(true);
}

function setThinking(val) {
    thinking = val;
    inputEl.disabled = val;
    sendBtn.disabled = val;
    inputEl.placeholder = val ? 'thinking...' : 'send a message...';
    if (statusEl) statusEl.textContent = val ? 'thinking...' : '';
}

export function startChatPolling() {
    if (pollTimer) return;
    pollOffset = 0;
    messages = [];
    pollAgent = '';
    if (container) container.innerHTML = '';
    setThinking(false);
    doPoll();
}

export function stopChatPolling() {
    if (pollTimer) {
        clearTimeout(pollTimer);
        pollTimer = null;
    }
}

function schedulePoll() {
    pollTimer = setTimeout(doPoll, 3000);
}

function doPoll() {
    pollTimer = null;
    if (!S.ptySessionId || !S.ptyWingId) {
        schedulePoll();
        return;
    }
    sendTunnelRequest(S.ptyWingId, {
        type: 'chat.poll',
        session_id: S.ptySessionId,
        byte_offset: pollOffset,
    }).then(function(result) {
        if (result.agent) pollAgent = result.agent;
        if (result.offset !== undefined) pollOffset = result.offset;
        if (result.lines) {
            var newMsgs = parseJSONL(result.lines, pollAgent);
            if (newMsgs.length > 0) {
                // On first poll, replace everything. On subsequent, append.
                if (messages.length === 0) {
                    messages = newMsgs;
                } else {
                    // Check if last optimistic user msg is confirmed
                    for (var i = 0; i < newMsgs.length; i++) {
                        var nm = newMsgs[i];
                        // Skip duplicates of the last user message we optimistically added
                        if (nm.type === 'user' && messages.length > 0) {
                            var last = messages[messages.length - 1];
                            if (last.type === 'user' && last.content === nm.content) continue;
                        }
                        messages.push(nm);
                    }
                }
                renderMessages();
                // Got an assistant message — no longer thinking
                var lastMsg = messages[messages.length - 1];
                if (lastMsg && lastMsg.type === 'assistant') {
                    setThinking(false);
                } else if (lastMsg && lastMsg.type === 'user') {
                    setThinking(true);
                }
            }
        }
        schedulePoll();
    }).catch(function() {
        schedulePoll();
    });
}

function renderMessages() {
    if (!container) return;
    container.innerHTML = '';
    for (var i = 0; i < messages.length; i++) {
        var msg = messages[i];
        var el = document.createElement('div');
        el.className = 'cv-msg cv-' + msg.type;

        if (msg.type === 'user') {
            el.innerHTML = '<div class="cv-bubble cv-user-bubble">' + escapeHtml(msg.content) + '</div>';
        } else if (msg.type === 'assistant') {
            var html = '';
            if (msg.thinking) {
                html += '<details class="cv-thinking"><summary>thinking</summary><pre>' + escapeHtml(msg.thinking) + '</pre></details>';
            }
            if (msg.content) {
                html += '<div class="cv-text">' + renderMarkdown(msg.content) + '</div>';
            }
            if (msg.toolCalls) {
                for (var j = 0; j < msg.toolCalls.length; j++) {
                    var tc = msg.toolCalls[j];
                    if (tc.result !== undefined) {
                        var preview = tc.result.length > 200 ? tc.result.slice(0, 200) + '...' : tc.result;
                        html += '<details class="cv-tool"><summary>' + escapeHtml(tc.name || 'tool result') + '</summary><pre>' + escapeHtml(preview) + '</pre></details>';
                    } else {
                        var inputPreview = '';
                        if (tc.input) {
                            try {
                                var s = typeof tc.input === 'string' ? tc.input : JSON.stringify(tc.input, null, 2);
                                inputPreview = s.length > 200 ? s.slice(0, 200) + '...' : s;
                            } catch(e) { inputPreview = '...'; }
                        }
                        html += '<details class="cv-tool"><summary>' + escapeHtml(tc.name) + '</summary><pre>' + escapeHtml(inputPreview) + '</pre></details>';
                    }
                }
            }
            el.innerHTML = html;
        } else if (msg.type === 'tool_result') {
            var preview = msg.content.length > 200 ? msg.content.slice(0, 200) + '...' : msg.content;
            el.innerHTML = '<details class="cv-tool"><summary>tool result</summary><pre>' + escapeHtml(preview) + '</pre></details>';
        }
        container.appendChild(el);
    }

    if (thinking) {
        var dot = document.createElement('div');
        dot.className = 'cv-msg cv-assistant cv-thinking-indicator';
        dot.textContent = 'thinking...';
        container.appendChild(dot);
    }

    container.scrollTop = container.scrollHeight;
}

function escapeHtml(text) {
    return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// Simple markdown: code blocks, inline code, bold, italic
function renderMarkdown(text) {
    // Code blocks: ```...```
    text = text.replace(/```(\w*)\n([\s\S]*?)```/g, function(_, lang, code) {
        return '<pre class="cv-code"><code>' + escapeHtml(code.replace(/\n$/, '')) + '</code></pre>';
    });
    // Inline code
    text = text.replace(/`([^`]+)`/g, function(_, code) {
        return '<code>' + escapeHtml(code) + '</code>';
    });
    // Bold
    text = text.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
    // Italic
    text = text.replace(/\*(.+?)\*/g, '<em>$1</em>');
    // Line breaks (but not inside pre)
    var parts = text.split(/(<pre[\s\S]*?<\/pre>)/);
    for (var i = 0; i < parts.length; i++) {
        if (i % 2 === 0) { // not inside pre
            parts[i] = parts[i].replace(/\n/g, '<br>');
        }
    }
    return parts.join('');
}

export function isMobileChatDefault() {
    // Check sessionStorage preference first
    var pref = sessionStorage.getItem('wt_view_mode');
    if (pref === 'chat') return true;
    if (pref === 'terminal') return false;
    // Auto-detect mobile: touch-primary device or narrow viewport
    return (window.matchMedia('(hover: none) and (pointer: coarse)').matches ||
            window.innerWidth < 600);
}

export function setViewPreference(mode) {
    sessionStorage.setItem('wt_view_mode', mode);
}

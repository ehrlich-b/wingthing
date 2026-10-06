// chat-view.js — Conversation reader: renders the exact execution's native
// transcript, lifecycle evidence and unresolved input. Opening or reopening
// only reads; Send, Check receipt and Stop are explicit user actions.

import { S } from './state.js';
import { sendTunnelRequest, randomUUID } from './tunnel.js';
import { escapeMarkup, renderSafeSimpleMarkdown } from './security.js';
import { createConversationReader } from './conversation-recovery.js';
import { coordinatorComposer, chatEnterSubmits, coordinatorStatus } from './coordinator-state.js';
import { trackSessionCompletions, sessionCompletionObservation, acknowledgeSessionCompletions } from './session-completion.js';

var reader = null;
var selectionVersion = 0;
var snapshot = null;
var listeners = [];
var container = null;
var inputEl = null;
var sendBtn = null;
var statusEl = null;
var detailEl = null;
var pendingEl = null;
var renderedSignature = '';
var pendingSignature = '';
var followsLatest = true;

function browserStorage(kind) {
    try { return window[kind]; } catch (e) { return null; }
}

function ensureReader() {
    if (reader) return reader;
    reader = createConversationReader({
        storage: browserStorage('localStorage'),
        legacyStorage: browserStorage('sessionStorage'),
        request: function(wingId, payload) { return sendTunnelRequest(wingId, payload); },
        randomId: randomUUID,
        onContinuationStarted: function(source, result) {
            window.dispatchEvent(new CustomEvent('wingthing:continuation-started', { detail: { source: source, result: result } }));
        },
        onChange: function(next) {
            snapshot = next;
            if (next.target && next.target.userId === (S.currentUser && S.currentUser.id)) {
                trackSessionCompletions(browserStorage('localStorage'), next.target.userId, [sessionCompletionObservation({ id: next.target.sessionId, wing_id: next.target.wingId, lifecycle: next.lifecycle },
                    coordinatorStatus(next.lifecycle, next), S.activeView === 'terminal' && document.visibilityState === 'visible')]);
            }
            render();
            listeners.forEach(function(listener) { try { listener(next); } catch (e) {} });
        },
    });
    return reader;
}

export function initChatView() {
    container = document.getElementById('chat-messages');
    inputEl = document.getElementById('chat-input');
    sendBtn = document.getElementById('chat-send');
    statusEl = document.getElementById('chat-view-status');
    detailEl = document.getElementById('chat-view-detail');
    document.getElementById('chat-state-details').open = window.innerWidth > 600;
    statusEl.setAttribute('aria-live', 'polite');
    pendingEl = document.createElement('div');
    pendingEl.id = 'chat-pending';
    pendingEl.className = 'chat-pending';
    pendingEl.hidden = true;
    var stateDetails = document.getElementById('chat-state-details');
    stateDetails.parentNode.insertBefore(pendingEl, stateDetails);

    sendBtn.addEventListener('click', submitInput);
    inputEl.addEventListener('keydown', function(e) {
        if (chatEnterSubmits(e, window.matchMedia('(hover: none) and (pointer: coarse)').matches)) {
            e.preventDefault();
            submitInput();
        }
    });
    inputEl.addEventListener('input', autoGrow);
    pendingEl.setAttribute('role', 'region');
    pendingEl.setAttribute('aria-label', 'Unresolved input');
    pendingEl.addEventListener('click', function(event) {
        var button = event.target.closest('[data-check-request]');
        if (button && reader) reader.checkReceipt(button.dataset.checkRequest).then(restoreDraft);
    });
    container.addEventListener('scroll', function() { followsLatest = container.scrollHeight - container.scrollTop - container.clientHeight < 48; });
    if ('ResizeObserver' in window) new ResizeObserver(function() { if (followsLatest) container.scrollTop = container.scrollHeight; }).observe(container);
}

function autoGrow() {
    inputEl.style.height = 'auto';
    inputEl.style.height = Math.min(inputEl.scrollHeight, 120) + 'px';
}

// A definite not-sent receipt for this execution's own request hands its
// text back to the empty composer, whether it came from Send or Check receipt.
function restoreDraft() {
    if (!reader || !inputEl || inputEl.value) return;
    var draft = reader.takeDraft();
    if (draft !== null) { inputEl.value = draft; autoGrow(); }
}

function submitInput() {
    if (!reader || !snapshot || !snapshot.inputReady || S.spectating) return;
    var text = inputEl.value;
    if (!text.trim()) return;
    var sent = reader.send(text);
    // The reserved request now owns this text (shown in the pending card); a
    // definite not-sent receipt is the only thing that hands it back.
    if (snapshot && snapshot.pending && snapshot.pending.input === text.trim()) { inputEl.value = ''; autoGrow(); }
    if (snapshot && snapshot.continuation && snapshot.continuation.input === text) { inputEl.value = ''; autoGrow(); }
    sent.then(restoreDraft);
}

export function onChatSnapshot(listener) {
    listeners.push(listener);
    return function() { listeners = listeners.filter(function(item) { return item !== listener; }); };
}

export function chatSnapshot() { return snapshot; }
export function stopChatExecution() { return reader ? reader.stopExecution() : Promise.resolve(false); }
export function refreshChat() { if (reader) reader.refresh(); }

// target: { sessionId, wingId, conversationId?, providerSessionId? }. Opening
// never prompts, checks receipts, checkpoints, or attaches a terminal writer.
export function startChatPolling(target) {
    selectionVersion++;
    followsLatest = true;
    renderedSignature = '';
    pendingSignature = '';
    if (container) container.innerHTML = '';
    var t = target || { sessionId: S.ptySessionId, wingId: S.ptyWingId };
    if (document.visibilityState === 'visible') acknowledgeSessionCompletions(browserStorage('localStorage'), S.currentUser && S.currentUser.id, [{ id: t.sessionId, wing_id: t.wingId }]);
    ensureReader().open({ userId: S.currentUser ? S.currentUser.id : '', wingId: t.wingId, sessionId: t.sessionId, conversationId: t.conversationId || '', providerSessionId: t.providerSessionId || '' });
}

export function stopChatPolling() {
    selectionVersion++;
    if (reader) reader.close();
    snapshot = null;
    if (pendingEl) { pendingEl.hidden = true; pendingEl.innerHTML = ''; }
    pendingSignature = '';
}

export function chatSelectionVersion() { return selectionVersion; }

function timeLabel(ms) {
    if (!ms) return 'unknown time';
    try { return new Date(ms).toLocaleTimeString(); } catch (e) { return String(ms); }
}

function render() {
    if (!snapshot) return;
    updateInput();
    renderPending();
    var thinking = !!snapshot.lifecycle && snapshot.presentation.live && snapshot.lifecycle.state === 'working';
    var signature = snapshot.messages.length + ':' + thinking + ':' + snapshot.cached + ':' + snapshot.trimmed;
    if (signature !== renderedSignature) {
        renderMessages(thinking);
        renderedSignature = signature;
    }
}

function updateInput() {
    var composer = coordinatorComposer(snapshot, S.spectating);
    var ready = composer.ready;
    document.getElementById('chat-input-bar').dataset.mode = composer.mode;
    if (inputEl) {
        inputEl.disabled = !ready;
        inputEl.placeholder = composer.placeholder;
    }
    if (sendBtn) {
        sendBtn.textContent = 'Send';
        sendBtn.disabled = !ready;
    }
    if (!statusEl) return;
    var shown = snapshot.presentation;
    var parts = [shown.label + (shown.detail ? ' · ' + shown.detail : '')];
    if (snapshot.readError && snapshot.readError !== shown.detail) parts.push((snapshot.halted ? '' : 'Reconnecting · ') + snapshot.readError + (snapshot.observedAt ? ' · last native observation ' + timeLabel(snapshot.observedAt) : ''));
    if (snapshot.storageError) parts.push(snapshot.storageError);
    if (snapshot.notice) parts.push(snapshot.notice);
    // A polite live region: rewrite only on change so polls do not re-announce.
    var text = parts.join(' · ');
    var label = composer.label || shown.label;
    if (snapshot.pending || snapshot.continuation) label = 'Unconfirmed input · Check receipt';
    else if (snapshot.readError) label = 'Connection interrupted · Details';
    if (statusEl.textContent !== label) statusEl.textContent = label;
    if (detailEl && detailEl.textContent !== text) detailEl.textContent = text;
    statusEl.dataset.state = shown.state;
}

function pendingCard(pending, current) {
    var target = pending.wingId + ' · ' + pending.sessionId + (pending.providerSessionId ? ' · provider ' + pending.providerSessionId : ' · provider not recorded');
    var status = pending.status === 'pending' ? 'delivery not yet confirmed' : 'delivery unconfirmed';
    return '<div class="chat-pending-item" data-request="' + escapeMarkup(pending.request_id) + '">' +
        '<p><strong>' + (current ? 'Unresolved input' : 'Unresolved input on an earlier execution') + '</strong> · ' + escapeMarkup(status) +
        (pending.checks ? ' · checked ' + pending.checks + '×, last ' + escapeMarkup(timeLabel(pending.lastCheckedAt)) : '') + '</p>' +
        '<p class="text-dim">Bound to ' + escapeMarkup(target) + ' · request ' + escapeMarkup(pending.request_id) + '</p>' +
        (pending.lastReason ? '<p class="text-dim">' + escapeMarkup(pending.lastReason) + '</p>' : '') +
        '<details><summary>Input text</summary><pre>' + escapeMarkup(pending.input) + '</pre></details>' +
        '<button type="button" data-check-request="' + escapeMarkup(pending.request_id) + '"' + (snapshot.checking ? ' disabled' : '') + '>' + (snapshot.checking ? 'Checking…' : 'Check receipt') + '</button>' +
        '<p class="text-dim">Check receipt asks this exact execution about the same request; it never sends a second prompt. Inspect the terminal if delivery stays unknown.</p>' +
        '</div>';
}

function continuationCard(pending) {
    return '<div class="chat-pending-item" data-request="' + escapeMarkup(pending.request_id) + '"><p><strong>Unconfirmed follow-up</strong></p>' +
        '<p class="text-dim">' + escapeMarkup(pending.lastReason || 'Waiting for the wing to confirm this follow-up.') + '</p>' +
        '<details><summary>Message</summary><pre>' + escapeMarkup(pending.input) + '</pre></details>' +
        '<button type="button" data-check-request="' + escapeMarkup(pending.request_id) + '"' + (snapshot.checking ? ' disabled' : '') + '>Check follow-up</button></div>';
}

function renderPending() {
    if (!pendingEl) return;
    var items = (snapshot.pending ? [[snapshot.pending, true]] : []).concat(snapshot.otherPending.map(function(p) { return [p, false]; }));
    var signature = JSON.stringify([snapshot.checking, snapshot.continuation, items.map(function(item) { return [item[0].request_id, item[0].status, item[0].checks, item[0].lastReason, item[1]]; })]);
    if (signature === pendingSignature) return;
    pendingSignature = signature;
    // Keep expanded input inspectors and keyboard focus stable across refresh.
    var open = new Set();
    pendingEl.querySelectorAll('.chat-pending-item').forEach(function(item) { if (item.querySelector('details[open]')) open.add(item.dataset.request); });
    var focused = pendingEl.contains(document.activeElement) && document.activeElement.dataset ? document.activeElement.dataset.checkRequest : '';
    pendingEl.hidden = !items.length && !snapshot.continuation;
    pendingEl.innerHTML = (snapshot.continuation ? continuationCard(snapshot.continuation) : '') + items.map(function(item) { return pendingCard(item[0], item[1]); }).join('');
    pendingEl.querySelectorAll('.chat-pending-item').forEach(function(item) {
        if (open.has(item.dataset.request)) item.querySelector('details').open = true;
        var button = item.querySelector('[data-check-request]');
        if (focused && button && button.dataset.checkRequest === focused) button.focus();
    });
}

var markerText = {
    attention: 'Needs a human decision',
    turn: 'Foreground turn finished (native Stop) · not proof the task succeeded',
    turn_failed: 'Native provider reported a failed turn',
    warning: 'Provider warning',
    exit: 'Provider session ended',
};

function renderMessages(thinking) {
    if (!container) return;
    var messages = snapshot.messages;
    var wasAtBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 48;
    var scrollTop = container.scrollTop;
    var expanded = new Set();
    container.querySelectorAll('[data-sequence]').forEach(function(message) {
        message.querySelectorAll('details').forEach(function(detail, i) { if (detail.open) expanded.add(message.dataset.sequence + ':' + i); });
    });
    container.innerHTML = '';
    if (snapshot.cached || snapshot.trimmed) {
        var banner = document.createElement('p');
        banner.className = 'cv-cache-banner';
        banner.textContent = (snapshot.cached ? 'Cached transcript saved in this browser; waiting for a live native read. ' : '') + (snapshot.trimmed ? 'Older cached records were trimmed to stay within the browser bound.' : '');
        container.appendChild(banner);
    }
    for (var i = 0; i < messages.length; i++) {
        var msg = messages[i];
        var el = document.createElement('div');
        el.className = 'cv-msg cv-' + msg.type;
        el.dataset.sequence = String(msg.sequence) + ':' + (msg.itemIndex || 0);

        if (msg.type === 'user') {
            el.innerHTML = '<div class="cv-bubble cv-user-bubble">' + escapeMarkup(msg.content) + '</div>';
        } else if (msg.type === 'assistant') {
            var html = '';
            if (msg.thinking) {
                html += '<details class="cv-thinking"><summary>thinking</summary><pre>' + escapeMarkup(msg.thinking) + '</pre></details>';
            }
            if (msg.content) {
                html += '<div class="cv-text">' + renderSafeSimpleMarkdown(msg.content) + '</div>';
            }
            if (msg.toolCalls) {
                for (var j = 0; j < msg.toolCalls.length; j++) {
                    var tc = msg.toolCalls[j];
                    if (tc.result !== undefined) {
                        html += '<details class="cv-tool"><summary>' + escapeMarkup(tc.name || 'tool result') + (tc.isError ? ' · error' : '') + '</summary><pre>' + escapeMarkup(tc.result) + '</pre></details>';
                    } else {
                        var inputPreview = '';
                        if (tc.input) {
                            try { inputPreview = typeof tc.input === 'string' ? tc.input : JSON.stringify(tc.input, null, 2); }
                            catch (e) { inputPreview = '...'; }
                        }
                        html += '<details class="cv-tool"><summary>' + escapeMarkup(tc.name) + (tc.id ? ' · ' + escapeMarkup(tc.id) : '') + '</summary><pre>' + escapeMarkup(inputPreview) + '</pre></details>';
                    }
                }
            }
            el.innerHTML = html;
        } else if (msg.type === 'tool_result') {
            el.innerHTML = '<details class="cv-tool"><summary>tool result' + (msg.name ? ' · ' + escapeMarkup(msg.name) : '') + '</summary><pre>' + escapeMarkup(msg.content) + '</pre></details>';
        } else if (msg.type === 'provider_record') {
            el.innerHTML = '<details class="cv-tool"><summary>provider record · ' + escapeMarkup(msg.name) + '</summary><pre>' + escapeMarkup(msg.content) + '</pre></details>';
        } else if (msg.type === 'lifecycle') {
            el.className += ' cv-lifecycle-' + msg.kind;
            el.innerHTML = '<p>' + escapeMarkup(markerText[msg.kind] || msg.kind) + (msg.reason ? ' · ' + escapeMarkup(msg.reason) : '') + '<span class="text-dim"> · ' + escapeMarkup(msg.source || 'native') + ' #' + escapeMarkup(String(msg.sequence)) + '</span></p>';
        }
        if (msg.truncated || msg.cacheTruncated) {
            var truncation = document.createElement('p');
            truncation.className = 'text-dim';
            truncation.textContent = msg.truncated ? 'Native provider record exceeded the reader bound; this record is truncated.' : 'This cached record was shortened in browser storage; reopen the live reader for full detail.';
            el.appendChild(truncation);
        }
        el.querySelectorAll('details').forEach(function(detail, k) { detail.open = expanded.has(el.dataset.sequence + ':' + k); });
        container.appendChild(el);
    }

    if (thinking) {
        var dot = document.createElement('div');
        dot.className = 'cv-msg cv-assistant cv-thinking-indicator';
        dot.textContent = 'working…';
        container.appendChild(dot);
    }
    container.scrollTop = wasAtBottom ? container.scrollHeight : scrollTop;
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

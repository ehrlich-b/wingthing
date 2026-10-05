import { S, DOM } from './state.js';
import { sendTunnelRequest, randomUUID } from './tunnel.js';
import { escapeHtml, wingDisplayName } from './helpers.js';
import { showTerminal, switchToSession } from './nav.js';
import { detachPTY, connectPTY } from './pty.js';
import { startChatPolling, setViewPreference, chatSelectionVersion, onChatSnapshot, chatSnapshot, stopChatExecution, refreshChat } from './chat-view.js';
import { orderConversationTree } from './conversation-state.js';
import { loadHome } from './data.js';
import { conversationResponseCurrent, canStartFreshLaunch, conversationTaskAvailability, readPendingLaunch, planLaunchStart, launchFieldsFromPending } from './conversation-response.js';
import { createNavigationGuard, resolveConversationExecution, readConversationTree, resumeInTerminalState, findArchivedExecution, taskRootId, mergeRootRead, mergeWingListing } from './conversation-recovery.js';
import { readTree, saveTree, readSelection, saveSelection } from './conversation-recovery-store.js';
import './conversation-recovery.css';

var TREE_REFRESH_MS = 5000;
var STOP_CONFIRM_MS = 6000;

var treeCache = new Map();
var refreshing = false;
var timer = null;
var pendingLaunch = null;
var navigation = createNavigationGuard();
var panel = null;
var snapshotListener = false;

window.addEventListener('wingthing:continuation-started', function(event) {
    var source = event.detail.source;
    if (!panel || !panelCurrent(panel) || source.userId !== userId() || source.wingId !== panel.wingId || source.conversationId !== panel.conversation.conversation_id) return;
    openFromCard({ wingId: source.wingId, conversationId: source.conversationId }, document.querySelector('.conversation-recovery-status'));
});

function userId() { return S.currentUser ? S.currentUser.id : ''; }
function storage() { try { return window.localStorage; } catch (e) { return null; } }
function request(wingId, payload) { return sendTunnelRequest(wingId, payload); }
function treeCacheKey(wingId) { return JSON.stringify([userId(), wingId]); }

function readerRequest() { return { userId: userId(), hash: location.hash, viewGeneration: chatSelectionVersion() }; }
function readerResponseCurrent(requested) { return conversationResponseCurrent(requested, readerRequest()); }

function errorText(error, fallback) { return (error && error.message) || fallback; }
// Live regions re-announce rewritten text; only write when it changed.
function setText(el, text) { if (el && el.textContent !== text) el.textContent = text; }
function shortId(id) { return id ? String(id).slice(0, 8) : '—'; }
function timeLabel(ms) { try { return ms ? new Date(ms).toLocaleTimeString() : 'never'; } catch (e) { return String(ms); } }

// In-memory trees carry fresh observation time. Trees restored from browser
// storage are cached evidence (observedAt 0) and never present live state.
function cachedWingTree(wingId) {
    var key = treeCacheKey(wingId);
    if (treeCache.has(key)) return treeCache.get(key);
    var stored = readTree(storage(), userId(), wingId);
    var restored = { tasks: stored.tasks, deliveries: stored.deliveries, observedAt: 0, cached: stored.tasks.length > 0, savedAt: stored.savedAt, error: '' };
    treeCache.set(key, restored);
    return restored;
}

function cacheWingTree(wingId, value) {
    treeCache.set(treeCacheKey(wingId), value);
    saveTree(storage(), userId(), wingId, { tasks: value.tasks, deliveries: value.deliveries, savedAt: value.observedAt || Date.now() });
}

// Freshness is per task: only tasks from a typed read in this page carry an
// observation time (the read's issue time). Storage drops it, so restored
// tasks are never fresh. Other roots keep their own observation times.
function mergeRootTree(wingId, rootId, tasks, delivery, issuedAt) {
    var merged = mergeRootRead(cachedWingTree(wingId), { rootId: rootId, tasks: tasks, delivery: delivery, issuedAt: issuedAt });
    if (!merged) return false;
    cacheWingTree(wingId, { tasks: merged.tasks, deliveries: merged.deliveries, error: '', observedAt: Date.now(), cached: false });
    return true;
}

function taskEvidence(wingId, task) {
    var data = cachedWingTree(wingId);
    return conversationTaskAvailability(task, data.error || '', { observedAt: task.observedAt || 0 });
}

export function cachedConversationTask(reference) {
    var data = cachedWingTree(reference.wingId);
    var task = data.tasks.find(function(task) { return task.conversation.conversation_id === reference.conversationId; });
    if (!task) return null;
    var readError = data.error || task.lifecycle_error || '';
    return { task: task, error: readError, readError: readError, observedAt: data.error ? 0 : (task.observedAt || 0) };
}

function control(wingId, operation, args) {
    return sendTunnelRequest(wingId, { type: 'session.control', operation: operation, arguments: args });
}

function wingFor(wingId) { return S.wingsData.find(function(wing) { return wing.wing_id === wingId; }); }

function persistSelection(conversation, wingId) {
    saveSelection(storage(), userId(), { wingId: wingId, conversationId: conversation.conversation_id, rootConversationId: conversation.root_conversation_id || conversation.conversation_id, sessionId: conversation.session_id, title: conversation.title || '', savedAt: Date.now() });
}

// Re-render without losing keyboard focus or expanded inspectors.
function stableRender(container, render) {
    var active = document.activeElement;
    var focusKey = active && container.contains(active) && active.dataset ? active.dataset.focusKey : '';
    var open = new Set();
    container.querySelectorAll('details[data-detail-key]').forEach(function(detail) { if (detail.open) open.add(detail.dataset.detailKey); });
    render();
    container.querySelectorAll('details[data-detail-key]').forEach(function(detail) { if (open.has(detail.dataset.detailKey)) detail.open = true; });
    if (focusKey) {
        var target = container.querySelector('[data-focus-key="' + CSS.escape(focusKey) + '"]');
        if (target) target.focus();
    }
}

function inspectRows(rows) {
    return '<dl class="conversation-facts">' + rows.filter(function(row) { return row[1] !== undefined && row[1] !== ''; }).map(function(row) {
        return '<dt>' + escapeHtml(row[0]) + '</dt><dd>' + escapeHtml(String(row[1])) + '</dd>';
    }).join('') + '</dl>';
}

// One linked task card: logical identity, exact execution, current vs
// archived state, attention, and separately inspectable history issues.
function conversationCard(task, wingId, depth, options) {
    var conversation = task.conversation;
    var availability = taskEvidence(wingId, task);
    var lifecycle = task.lifecycle || {};
    var key = wingId + ':' + conversation.conversation_id;
    var item = document.createElement('div');
    item.className = 'conversation-task' + (options.selected ? ' conversation-selected' : '');
    item.setAttribute('role', 'listitem');
    item.dataset.state = availability.state;
    item.style.paddingLeft = (Math.min(depth, 5) * 18) + 'px';
    var role = conversation.parent_conversation_id ? 'child' : 'parent';
    var execution = conversation.session_id ? 'execution ' + shortId(conversation.session_id) : 'no execution started';
    item.innerHTML = '<button class="conversation-open" type="button" data-focus-key="open:' + escapeHtml(key) + '"' + (options.selected ? ' aria-current="true"' : '') + '>' +
        '<span>' + (depth === 0 ? '● ' : '↳ ') + escapeHtml(conversation.title || conversation.agent || conversation.conversation_id) + '</span>' +
        '<small><span class="conversation-badge" data-state="' + escapeHtml(availability.state) + '">' + escapeHtml(availability.label) + '</span> · ' + role + ' · ' + escapeHtml(wingDisplayName(wingFor(wingId)) || wingId) + ' · ' + escapeHtml(execution) + '</small></button>' +
        (options.addChild ? '<button class="conversation-add-child" type="button" data-focus-key="child:' + escapeHtml(key) + '">Add child</button>' : '');
    var details = document.createElement('details');
    details.className = 'conversation-inspect';
    details.dataset.detailKey = 'inspect:' + key;
    details.innerHTML = '<summary>Inspect</summary>' + inspectRows([
        ['State', availability.label + (availability.detail ? ' — ' + availability.detail : '')],
        ['Logical conversation', conversation.conversation_id],
        ['Root', conversation.root_conversation_id],
        ['Parent', conversation.parent_conversation_id],
        ['Wing', wingId],
        ['Current execution', conversation.session_id],
        ['Provider conversation', lifecycle.provider_session_id || 'not reported'],
        ['State source', lifecycle.state_source],
        ['Native reason', lifecycle.reason],
        ['Process', lifecycle.session_id || lifecycle.state_source ? (lifecycle.process_alive ? 'running' : 'exited (archived)') : 'no execution observed'],
        ['Launch', conversation.launch_state + (conversation.launch_error ? ' · ' + conversation.launch_error : '')],
        ['Workspace', conversation.cwd],
    ]);
    item.appendChild(details);
    [['Current state issue', availability.error], ['History issue', availability.historyIssue]].forEach(function(issue) {
        if (!issue[1]) return;
        var detail = document.createElement('details');
        detail.className = 'conversation-issue';
        detail.dataset.detailKey = issue[0] + ':' + key;
        detail.innerHTML = '<summary>' + escapeHtml(issue[0]) + '</summary><pre>' + escapeHtml(issue[1]) + '</pre>';
        item.appendChild(detail);
    });
    item.querySelector('.conversation-open').addEventListener('click', function() { options.onOpen(conversation); });
    var add = item.querySelector('.conversation-add-child');
    if (add) {
        add.disabled = !!options.addChildDisabled;
        add.addEventListener('click', function() { options.addChild(conversation); });
    }
    return item;
}

function openFromCard(reference, statusEl) {
    if (statusEl) statusEl.textContent = 'Reading the current execution…';
    openConversationReference(reference).catch(function(error) {
        if (statusEl && statusEl.isConnected) statusEl.textContent = 'Could not read the current execution: ' + errorText(error, 'Reconnect to its execution wing.') + (panel && panel.cachedOnly ? ' Showing cached evidence from this browser.' : '');
    });
}

// ---- Conversation reader panel ----

function panelCurrent(state) { return panel === state && state.version === chatSelectionVersion(); }

function buildPanel() {
    var chatView = document.getElementById('chat-view');
    var toolbar = document.getElementById('conversation-toolbar');
    if (!toolbar) {
        toolbar = document.createElement('div');
        toolbar.id = 'conversation-toolbar';
        chatView.prepend(toolbar);
    }
    toolbar.className = 'conversation-toolbar conversation-panel';
    toolbar.innerHTML = '<div class="conversation-identity"><strong class="cp-title"></strong><span class="conversation-badge cp-state" role="status"></span><span class="cp-identity"></span></div>' +
        '<div class="conversation-actions">' +
        '<button class="conversation-terminal" type="button" title="Attach this exact execution’s terminal as an interactive writer">Open terminal</button>' +
        '<button class="conversation-stop" type="button" hidden>Stop execution</button>' +
        '<button class="conversation-resume" type="button" hidden>Resume in terminal</button>' +
        '<button class="conversation-retry" type="button" hidden>Retry read</button>' +
        '<button class="conversation-current" type="button">Check current execution</button>' +
        '<button class="conversation-bootstrap" type="button">Copy MCP setup</button></div>' +
        '<p class="conversation-setup-status" role="status" aria-live="polite"></p>' +
        '<p class="conversation-recovery-status" role="status" aria-live="polite"></p>' +
        '<details class="conversation-tree" open><summary>Linked conversations</summary><div class="conversation-cards" role="list"></div></details>' +
        '<details class="conversation-deliveries" hidden data-detail-key="deliveries"><summary></summary><ol></ol></details>';
    var state = panel;
    toolbar.querySelector('.conversation-terminal').addEventListener('click', function() {
        DOM.sessionCloseBtn.style.display = '';
        switchToSession(state.conversation.session_id, undefined, state.wingId);
    });
    toolbar.querySelector('.conversation-bootstrap').addEventListener('click', async function() {
        var status = toolbar.querySelector('.conversation-setup-status');
        var requested = readerRequest();
        try {
            var result = await control(state.wingId, 'conversation_bootstrap', { conversation_id: state.conversation.conversation_id });
            if (!readerResponseCurrent(requested)) return;
            await navigator.clipboard.writeText(JSON.stringify(result.configuration, null, 2));
            if (!readerResponseCurrent(requested)) return;
            status.textContent = 'Copied. Configure this provider using its existing authorized access.';
        } catch (error) { if (readerResponseCurrent(requested)) status.textContent = errorText(error, 'Setup could not be copied'); }
    });
    toolbar.querySelector('.conversation-stop').addEventListener('click', function() {
        if (!panelCurrent(state)) return;
        if (Date.now() > state.stopConfirmUntil) {
            state.stopConfirmUntil = Date.now() + STOP_CONFIRM_MS;
            setTimeout(function() { if (panelCurrent(state)) renderPanelState(); }, STOP_CONFIRM_MS + 50);
            renderPanelState();
            return;
        }
        state.stopConfirmUntil = 0;
        stopChatExecution().then(function() { if (panelCurrent(state)) renderPanelState(); });
        renderPanelState();
    });
    toolbar.querySelector('.conversation-resume').addEventListener('click', function() { resumeInTerminal(state); });
    toolbar.querySelector('.conversation-retry').addEventListener('click', function() { refreshChat(); });
    toolbar.querySelector('.conversation-current').addEventListener('click', function() {
        openFromCard({ wingId: state.wingId, conversationId: state.conversation.conversation_id }, toolbar.querySelector('.conversation-recovery-status'));
    });
    return toolbar;
}

function renderPanelState() {
    var toolbar = document.getElementById('conversation-toolbar');
    if (!panel || !toolbar) return;
    var snap = chatSnapshot();
    var conversation = panel.conversation;
    var wingName = wingDisplayName(wingFor(panel.wingId)) || panel.wingId;
    setText(toolbar.querySelector('.cp-title'), (conversation.parent_conversation_id ? 'Child · ' : 'Parent · ') + (conversation.title || conversation.conversation_id));
    var target = snap && snap.target;
    var provider = target && target.providerSessionId ? target.providerSessionId : 'provider conversation not reported yet';
    setText(toolbar.querySelector('.cp-identity'), wingName + ' · logical ' + conversation.conversation_id + ' · execution ' + (conversation.session_id || 'none') + ' · ' + provider);
    var badge = toolbar.querySelector('.cp-state');
    var shown = snap ? snap.presentation : { state: 'connecting', label: 'connecting' };
    setText(badge, shown.label);
    badge.dataset.state = shown.state;

    var stop = toolbar.querySelector('.conversation-stop');
    var stopState = snap ? snap.stop : { pending: false, error: '', acknowledgedAt: 0 };
    stop.hidden = !target || shown.archived;
    stop.disabled = stopState.pending;
    stop.textContent = stopState.pending ? 'Stopping…' : (Date.now() <= panel.stopConfirmUntil ? 'Confirm stop' : 'Stop execution');

    var retry = toolbar.querySelector('.conversation-retry');
    retry.hidden = !snap || !snap.readError || snap.halted;

    var resume = toolbar.querySelector('.conversation-resume');
    resume.hidden = !panel.resume.available;
    resume.disabled = panel.resume.busy;
    resume.title = 'Starts a new execution from this archived provider conversation in an interactive terminal. Unresolved input stays bound to the archived execution.';

    var lines = [];
    if (panel.cachedOnly) lines.push('Showing the last execution this browser knew about; the current execution could not be read' + (panel.openError ? ': ' + panel.openError : '.') + ' Use Check current execution after reconnecting.');
    if (panel.newerSession) lines.push('This logical conversation now has a newer execution (' + panel.newerSession + '). This reader stays on ' + conversation.session_id + '; use Check current execution to switch.');
    if (Date.now() <= panel.stopConfirmUntil) lines.push('Stop ends the process for execution ' + conversation.session_id + ' on ' + wingName + '. It does not prove the task completed or cancel external effects. Click Confirm stop to proceed.');
    if (stopState.error) lines.push(stopState.error + ' This execution remains listed and readable.');
    if (stopState.acknowledgedAt) lines.push('Stop acknowledged by the wing at ' + timeLabel(stopState.acknowledgedAt) + '. Waiting for native exit evidence; this does not prove the task completed or cancel external effects.');
    if (shown.archived && !panel.resume.available && panel.resume.reason) lines.push('Resume unavailable: ' + panel.resume.reason);
    if (panel.resume.status) lines.push(panel.resume.status);
    if (panel.treeError) lines.push('Linked tree not refreshed: ' + panel.treeError);
    setText(toolbar.querySelector('.conversation-recovery-status'), lines.join(' '));
    if (snap && shown.archived && !snap.cached) checkResume(panel, false);
}

function renderPanelTree() {
    var toolbar = document.getElementById('conversation-toolbar');
    if (!panel || !toolbar) return;
    var state = panel;
    var data = cachedWingTree(state.wingId);
    var tasks = data.tasks.filter(function(task) { return taskRootId(task) === state.rootId; });
    var byId = new Map(tasks.map(function(task) { return [task.conversation.conversation_id, task]; }));
    var cards = toolbar.querySelector('.conversation-cards');
    stableRender(toolbar, function() {
        cards.innerHTML = '';
        orderConversationTree(tasks.map(function(task) { return task.conversation; })).forEach(function(row) {
            cards.appendChild(conversationCard(byId.get(row.conversation.conversation_id), state.wingId, row.depth, {
                selected: row.conversation.conversation_id === state.conversation.conversation_id,
                onOpen: function(conversation) { openFromCard({ wingId: state.wingId, conversationId: conversation.conversation_id }, toolbar.querySelector('.conversation-recovery-status')); },
            }));
        });
        if (!tasks.length) cards.innerHTML = '<p class="text-dim">Linked tasks appear after the first tree read.</p>';
        toolbar.querySelector('.conversation-tree summary').textContent = 'Linked conversations (' + tasks.length + ')' + (data.error || data.cached ? ' · cached' : '');
        renderDeliveries(toolbar, data, byId, state.rootId);
    });
}

// Root state deliveries after the parent's checkpoint cursor. Reading them here
// never acknowledges them; whether the parent provider saw them is unknown
// until it checkpoints past them.
function renderDeliveries(toolbar, data, byId, rootId) {
    var section = toolbar.querySelector('.conversation-deliveries');
    var delivery = data.deliveries && data.deliveries[rootId];
    var root = byId.get(rootId);
    var events = delivery ? delivery.events || [] : [];
    section.hidden = !events.length;
    if (!events.length) return;
    var cursor = root ? root.conversation.delivered_cursor : 0;
    section.querySelector('summary').textContent = events.length + (delivery.has_more ? '+' : '') + ' child state deliveries after the parent checkpoint (cursor ' + (cursor || 0) + ') · parent receipt unknown';
    section.querySelector('ol').innerHTML = events.map(function(event) {
        var task = byId.get(event.conversation_id);
        var name = task ? (task.conversation.title || event.conversation_id) : event.conversation_id;
        return '<li data-state="' + escapeHtml(event.state) + '">#' + escapeHtml(String(event.sequence)) + ' ' + escapeHtml(name) + ' · ' + escapeHtml(event.state) + ' (' + escapeHtml(event.type || 'state') + ', ' + escapeHtml(event.state_source || 'native') + ') · execution ' + escapeHtml(shortId(event.session_id)) + '</li>';
    }).join('') + '<li class="text-dim">Replayable deliveries: this reader does not checkpoint, wake or prompt the parent.</li>';
}

function scheduleTreeRefresh(state, delay) {
    if (state.treeTimer) clearTimeout(state.treeTimer);
    state.treeTimer = setTimeout(function() { refreshPanelTree(state); }, delay);
}

async function refreshPanelTree(state) {
    state.treeTimer = null;
    if (!panelCurrent(state)) return;
    var rootTask = cachedWingTree(state.wingId).tasks.find(function(task) { return task.conversation.conversation_id === state.rootId; });
    try {
        var issuedAt = Date.now();
        var tree = await readConversationTree(request, state.wingId, rootTask ? rootTask.conversation : { conversation_id: state.rootId });
        if (!panelCurrent(state)) return;
        state.treeError = '';
        if (mergeRootTree(state.wingId, state.rootId, tree.tasks, tree.delivery, issuedAt)) {
            var selected = tree.tasks.find(function(task) { return task.conversation.conversation_id === state.conversation.conversation_id; });
            state.newerSession = selected && selected.conversation.session_id && selected.conversation.session_id !== state.conversation.session_id ? selected.conversation.session_id : '';
        }
    } catch (error) {
        if (!panelCurrent(state)) return;
        state.treeError = errorText(error, 'Connection interrupted');
    }
    renderPanelTree();
    renderPanelState();
    scheduleTreeRefresh(state, TREE_REFRESH_MS);
}

async function checkResume(state, force) {
    var snap = chatSnapshot();
    if (!snap || !snap.target || state.resume.busy) return;
    if (!force && state.resume.checkedSession === snap.target.sessionId) return;
    state.resume = { available: false, reason: '', status: 'Checking the wing’s archived history for this exact execution…', busy: true, checkedSession: snap.target.sessionId, entry: null };
    var wing = wingFor(state.wingId);
    var capability = !!wing && Array.isArray(wing.capabilities) && wing.capabilities.includes('session.provider_resume.v1');
    var execution = { wingId: state.wingId, sessionId: snap.target.sessionId, conversationId: state.conversation.conversation_id, agent: (snap.lifecycle && snap.lifecycle.agent) || state.conversation.agent || '' };
    var verdict;
    var entry = null;
    try {
        entry = await findArchivedExecution(function(wingId, payload) { return sendTunnelRequest(wingId, payload, { skipPasskey: true }); }, state.wingId, snap.target.sessionId);
        var latest = chatSnapshot();
        verdict = resumeInTerminalState(execution, latest && latest.presentation, !latest || latest.cached, entry, S.currentUser, capability);
        if (verdict.available && state.newerSession) verdict = { available: false, reason: 'A newer execution exists; open it instead of resuming this one.' };
    } catch (error) {
        verdict = { available: false, reason: 'Archived history could not be read: ' + errorText(error, 'request failed') };
    }
    if (panel !== state) return;
    state.resume = { available: verdict.available, reason: verdict.reason, status: '', busy: false, checkedSession: execution.sessionId, entry: entry };
    renderPanelState();
}

// Explicit terminal writer action through the existing PTYStart resume
// contract. It is re-verified immediately before connecting.
async function resumeInTerminal(state) {
    if (!panelCurrent(state)) return;
    await checkResume(state, true);
    if (!panelCurrent(state) || !state.resume.available || !state.resume.entry) return;
    var entry = state.resume.entry;
    // resumeInTerminalState already required a reported, matching provider.
    var agent = entry.agent || state.conversation.agent;
    showTerminal();
    DOM.sessionCloseBtn.style.display = '';
    connectPTY(agent, entry.cwd || state.conversation.cwd || '', state.wingId, entry.session_id);
}

function ensureSnapshotListener() {
    if (snapshotListener) return;
    snapshotListener = true;
    onChatSnapshot(function() { renderPanelState(); });
}

// context: { providerSessionId, cachedOnly, openError }
export function openConversationTranscript(conversation, wingId, pushHistory, context) {
    context = context || {};
    detachPTY();
    if (!showTerminal()) return;
    ensureSnapshotListener();
    if (panel && panel.treeTimer) clearTimeout(panel.treeTimer);
    document.getElementById('terminal-section').classList.add('chat-active');
    DOM.headerTitle.textContent = conversation.title || 'Conversation';
    DOM.sessionCloseBtn.style.display = 'none';
    panel = {
        conversation: conversation, wingId: wingId, rootId: conversation.root_conversation_id || conversation.conversation_id,
        cachedOnly: !!context.cachedOnly, openError: context.openError || '', newerSession: '', treeError: '', treeTimer: null,
        stopConfirmUntil: 0, resume: { available: false, reason: '', status: '', busy: false, checkedSession: '', entry: null }, version: -1,
    };
    persistSelection(conversation, wingId);
    buildPanel();
    setViewPreference('chat');
    startChatPolling({ sessionId: conversation.session_id, wingId: wingId, conversationId: conversation.conversation_id, providerSessionId: context.providerSessionId || '' });
    panel.version = chatSelectionVersion();
    renderPanelTree();
    renderPanelState();
    var route = '#conversation/' + encodeURIComponent(conversation.conversation_id) + '?wing=' + encodeURIComponent(wingId);
    if (pushHistory !== false && location.hash !== route) history.pushState({ view: 'conversation', conversationId: conversation.conversation_id, wingId: wingId }, '', route);
    window.dispatchEvent(new CustomEvent('wingthing:conversation-selected', { detail: { wingId: wingId, conversationId: conversation.root_conversation_id || conversation.conversation_id, title: conversation.parent_conversation_id ? undefined : conversation.title } }));
    scheduleTreeRefresh(panel, 0);
}

// Every activation resolves the logical task to its exact current execution
// with a fresh typed read. Only the newest activation may switch the reader;
// on failure the last cached execution is shown as cached evidence.
export async function openConversationReference(reference, pushHistory) {
    var token = navigation.begin(reference);
    var requested = readerRequest();
    var issuedAt = Date.now();
    try {
        var resolved = await resolveConversationExecution(request, reference);
        if (!navigation.current(token) || !readerResponseCurrent(requested)) return false;
        mergeRootTree(reference.wingId, resolved.rootConversationId, resolved.tasks, null, issuedAt);
        openConversationTranscript(resolved.conversation, reference.wingId, pushHistory, { providerSessionId: resolved.execution.providerSessionId });
        return true;
    } catch (error) {
        if (!navigation.current(token) || !readerResponseCurrent(requested)) return false;
        var cached = cachedConversationTask(reference);
        var shown = panel && panelCurrent(panel) && panel.wingId === reference.wingId && panel.conversation.conversation_id === reference.conversationId;
        if (shown && (!cached || cached.task.conversation.session_id === panel.conversation.session_id)) {
            // Keep the open reader (and any in-progress Stop/Resume state) on
            // the same execution; only the logical re-resolution failed.
            panel.cachedOnly = true;
            panel.openError = errorText(error, 'Connection interrupted');
            renderPanelState();
        } else if (cached && cached.task.conversation.session_id) {
            openConversationTranscript(cached.task.conversation, reference.wingId, pushHistory, { cachedOnly: true, openError: errorText(error, 'Connection interrupted'), providerSessionId: (cached.task.lifecycle || {}).provider_session_id || '' });
        }
        throw error;
    }
}

export async function restoreConversationRoute() {
    var match = location.hash.match(/^#conversation\/([^?]+)\?wing=([^&]+)$/);
    if (!match) return false;
    var id = decodeURIComponent(match[1]);
    var wingId = decodeURIComponent(match[2]);
    var requested = readerRequest();
    try {
        await openConversationReference({ wingId: wingId, conversationId: id }, false);
    } catch (error) {
        if (panel && panel.cachedOnly && panel.wingId === wingId && panel.conversation.conversation_id === id) return true;
        if (!readerResponseCurrent(requested)) return true;
        if (showTerminal()) {
            // Nothing from a previously open conversation may remain on screen
            // under this route.
            if (panel && panel.treeTimer) clearTimeout(panel.treeTimer);
            panel = null;
            var toolbar = document.getElementById('conversation-toolbar');
            if (toolbar) toolbar.remove();
            var messages = document.getElementById('chat-messages');
            if (messages) messages.innerHTML = '';
            document.getElementById('terminal-section').classList.add('chat-active');
            document.getElementById('chat-view-status').textContent = errorText(error, 'Conversation unavailable.') + ' No cached execution is saved in this browser; reconnect to its execution wing.';
            document.getElementById('chat-input').disabled = true;
            document.getElementById('chat-send').disabled = true;
        }
    }
    return true;
}

// ---- Home inventory ----

function eligibleWings(includeOffline) {
    if (S.currentUser && S.currentUser.relay_allowed === false) return [];
    return S.wingsData.filter(function(wing) {
        return (includeOffline || (wing.online !== false && !wing.tunnel_error)) && wing.tunnel_error !== 'not_allowed' && Array.isArray(wing.capabilities) && wing.capabilities.includes('conversation.personal.v1');
    });
}

export async function refreshConversationInventory() {
    var mount = document.getElementById('conversation-inventory');
    if (!mount || refreshing) return;
    if (timer) clearTimeout(timer);
    var wings = eligibleWings(true);
    if (!wings.length) { mount.innerHTML = ''; return; }
    refreshing = true;
    await Promise.all(wings.map(async function(wing) {
        try {
            if (wing.online === false || wing.tunnel_error) {
                var offline = cachedWingTree(wing.wing_id);
                treeCache.set(treeCacheKey(wing.wing_id), { ...offline, error: 'Offline · cached tasks', observedAt: 0 });
                return;
            }
            var listedAt = Date.now();
            var listing = await control(wing.wing_id, 'conversation_list', {});
            var reads = [];
            for (var root of (listing.conversations || []).filter(function(c) { return !c.parent_conversation_id; })) {
                var issuedAt = Date.now();
                var tree = await readConversationTree(request, wing.wing_id, root);
                reads.push({ rootId: root.conversation_id, tasks: tree.tasks, delivery: tree.delivery, issuedAt: issuedAt });
            }
            // A reader panel may have refreshed a subtree while these reads
            // were in flight; the later-issued read wins per root.
            var merged = mergeWingListing(cachedWingTree(wing.wing_id), reads, listedAt);
            cacheWingTree(wing.wing_id, { tasks: merged.tasks, deliveries: merged.deliveries, error: '', observedAt: Date.now(), cached: false });
        } catch (error) {
            var cached = cachedWingTree(wing.wing_id);
            treeCache.set(treeCacheKey(wing.wing_id), { ...cached, error: errorText(error, 'Connection interrupted'), observedAt: 0 });
        }
    }));
    refreshing = false;
    // Do not replace an active launch form while its user is editing it.
    if (!mount.querySelector('form')) stableRender(mount, function() { renderInventory(mount, wings); });
    window.dispatchEvent(new CustomEvent('wingthing:conversation-inventory-updated'));
    if (S.activeView === 'home') timer = setTimeout(refreshConversationInventory, TREE_REFRESH_MS);
}

function renderInventory(mount, wings) {
    var selection = readSelection(storage(), userId()).selection;
    mount.innerHTML = '<div class="conversation-heading"><h3>Conversations</h3><button id="new-parent-conversation" type="button" data-focus-key="new-parent">New parent</button></div>' +
        '<p class="conversation-inventory-status text-dim" role="status" aria-live="polite"></p><div id="conversation-tasks" role="list"></div>';
    var status = mount.querySelector('.conversation-inventory-status');
    var list = mount.querySelector('#conversation-tasks');
    if (selection) {
        var resume = document.createElement('div');
        resume.className = 'conversation-last-selected';
        resume.innerHTML = '<span>Last opened: ' + escapeHtml(selection.title || selection.conversationId) + ' · ' + escapeHtml(wingDisplayName(wingFor(selection.wingId)) || selection.wingId) + '</span><button type="button" data-focus-key="reopen-selection">Reopen</button>';
        resume.querySelector('button').addEventListener('click', function() { openFromCard({ wingId: selection.wingId, conversationId: selection.conversationId }, status); });
        mount.insertBefore(resume, list);
    }
    wings.forEach(function(wing) {
        var data = cachedWingTree(wing.wing_id);
        if (data.error || data.cached) {
            var error = document.createElement('p');
            error.className = 'text-dim';
            error.textContent = wingDisplayName(wing) + ': ' + (data.error || 'Cached tasks from this browser · checking') + (data.savedAt ? ' · saved ' + timeLabel(data.savedAt) : '');
            list.appendChild(error);
        }
        var taskById = new Map(data.tasks.map(function(task) { return [task.conversation.conversation_id, task]; }));
        orderConversationTree(data.tasks.map(function(task) { return task.conversation; })).forEach(function(row) {
            var conversation = row.conversation;
            list.appendChild(conversationCard(taskById.get(conversation.conversation_id), wing.wing_id, row.depth, {
                selected: !!selection && selection.wingId === wing.wing_id && selection.conversationId === conversation.conversation_id,
                onOpen: function() { openFromCard({ wingId: wing.wing_id, conversationId: conversation.conversation_id }, status); },
                addChild: row.depth === 0 ? function() { showLaunchForm(mount, [wing], conversation); } : null,
                addChildDisabled: wing.online === false || !!wing.tunnel_error || !!data.error,
            }));
        });
    });
    if (!list.children.length) list.innerHTML = '<p class="text-dim">Create a persistent parent conversation, then inspect its linked children here.</p>';
    var newParent = mount.querySelector('#new-parent-conversation');
    newParent.disabled = eligibleWings().length === 0;
    newParent.addEventListener('click', function() { showLaunchForm(mount, eligibleWings(), null); });
}

function showLaunchForm(mount, wings, parent) {
    try { pendingLaunch = JSON.parse(sessionStorage.getItem('wt_conversation_launch:' + S.currentUser.id) || 'null'); } catch (e) { pendingLaunch = null; }
    var form = document.createElement('form');
    form.className = 'conversation-launch';
    form.innerHTML = '<h3>' + (parent ? 'New child conversation' : 'New parent conversation') + '</h3>' +
        '<label>Execution wing<select name="wing">' + wings.map(function(w) { return '<option value="' + escapeHtml(w.wing_id) + '">' + escapeHtml(wingDisplayName(w)) + '</option>'; }).join('') + '</select></label>' +
        '<label>Name<input name="label" placeholder="' + (parent ? 'research' : 'personal-parent') + '" required maxlength="64" pattern="[A-Za-z0-9][A-Za-z0-9_.-]*"></label>' +
        '<label>Workspace on this wing<input name="cwd" required></label>' +
        '<label>Model (optional)<input name="model" maxlength="128" autocomplete="off" spellcheck="false" placeholder="Provider model name"></label>' +
        '<p class="text-dim">Claude conversation support. This launch uses the selected wing’s existing provider configuration and, if named, the model above for its first turn onward.</p>' +
        '<button type="submit">Create conversation</button><button class="conversation-fresh-attempt" type="button" hidden>New attempt after confirmed failure</button><button class="conversation-cancel" type="button">Cancel</button><p class="conversation-launch-status" role="status"></p>';
    mount.replaceChildren(form);
    form.elements.cwd.value = parent ? parent.cwd : ((wings[0].projects || [])[0] || {}).path || '';
    var fresh = form.querySelector('.conversation-fresh-attempt');
    var status = form.querySelector('.conversation-launch-status');
    var requested = readerRequest();
    var launchKey = 'wt_conversation_launch:' + S.currentUser.id;
    try { pendingLaunch = readPendingLaunch(sessionStorage.getItem(launchKey)); } catch (e) { pendingLaunch = { malformed: { requestId: '' } }; }
    if (pendingLaunch && pendingLaunch.malformed) {
        fresh.textContent = 'Discard unreadable launch record';
        fresh.hidden = false;
        status.textContent = 'A saved launch record on this tab is unreadable' + (pendingLaunch.malformed.requestId ? ' (request ' + pendingLaunch.malformed.requestId + ')' : '') + '. Its launch may already exist: inspect existing conversations, then discard the record to start a new launch.';
    } else if (pendingLaunch) {
        var saved = launchFieldsFromPending(pendingLaunch.pending);
        if (saved.parentConversationId === (parent ? parent.conversation_id : '') && wings.some(function(w) { return w.wing_id === saved.wingId; })) {
            form.elements.wing.value = saved.wingId;
            form.elements.label.value = saved.label;
            form.elements.cwd.value = saved.cwd;
            form.elements.model.value = saved.model;
        }
        fresh.hidden = !pendingLaunch.pending.confirmedFailed;
        status.textContent = pendingLaunch.pending.confirmedFailed ? 'The earlier launch failed. Choose a new attempt to use a new request identity.' : 'An earlier launch has an uncertain result. Inspect existing conversations, or submit that identical launch to retry it with the same request identity.';
    }
    fresh.addEventListener('click', function() {
        if (!pendingLaunch || !(pendingLaunch.malformed || pendingLaunch.pending.confirmedFailed)) return;
        pendingLaunch = null;
        sessionStorage.removeItem(launchKey);
        fresh.hidden = true;
        status.textContent = 'Fresh attempt selected. Create conversation uses a new request identity.';
    });
    form.querySelector('.conversation-cancel').addEventListener('click', function() { renderInventory(mount, eligibleWings(true)); });
    form.addEventListener('submit', async function(event) {
        event.preventDefault();
        var wingId = form.elements.wing.value;
        var plan = planLaunchStart({ wingId: wingId, label: form.elements.label.value, cwd: form.elements.cwd.value, model: form.elements.model.value, parentConversationId: parent ? parent.conversation_id : '' }, pendingLaunch, randomUUID);
        if (plan.action === 'malformed') {
            status.textContent = 'A saved launch record on this tab is unreadable. Inspect existing conversations, then discard the record before starting a new launch.';
            return;
        }
        if (plan.action === 'refused') {
            status.textContent = 'An earlier launch has an uncertain result and may already exist. Inspect existing conversations, or submit that identical launch again to retry it with the same request identity.';
            return;
        }
        var args = plan.args;
        pendingLaunch = { pending: plan.pending };
        sessionStorage.setItem(launchKey, JSON.stringify(plan.pending));
        var button = form.querySelector('[type="submit"]');
        button.disabled = true;
        status.textContent = 'Starting the conversation…';
        try {
            var result = await control(wingId, 'agent_start', args);
            if (!readerResponseCurrent(requested) || !form.isConnected) return;
            if (canStartFreshLaunch(result)) {
                plan.pending.confirmedFailed = true;
                sessionStorage.setItem(launchKey, JSON.stringify(plan.pending));
                fresh.textContent = 'New attempt after confirmed failure';
                fresh.hidden = false;
            }
            if (result.launch_state !== 'started') throw new Error(result.launch_error || 'Launch reserved but not confirmed. Inspect the existing session before retrying.');
            pendingLaunch = null;
            sessionStorage.removeItem(launchKey);
            mount.innerHTML = '';
            await loadHome();
            if (!readerResponseCurrent(requested)) return;
            await refreshConversationInventory();
            if (!readerResponseCurrent(requested)) return;
            openConversationTranscript({ title: result.label, session_id: result.session, conversation_id: result.conversation_id, root_conversation_id: result.root_conversation_id, parent_conversation_id: result.parent_conversation_id, wing_id: result.wing_id, agent: result.agent, cwd: result.cwd, launch_state: result.launch_state }, wingId);
        } catch (error) {
            if (!readerResponseCurrent(requested) || !form.isConnected) return;
            status.textContent = errorText(error, 'Launch interrupted. Retry uses the same request identity.');
            button.disabled = false;
        }
    });
}

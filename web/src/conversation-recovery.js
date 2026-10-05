// Reader and recovery controller for one exact conversation execution.
//
// Contract:
// - open/reopen only reads (`session_read`). It never prompts, checks a
//   receipt, checkpoints, kills, or attaches a terminal writer.
// - Send reserves an immutable pending input (request ID, text, exact
//   user/wing/session/provider) in browser storage before any transport.
// - Check receipt is explicit and re-submits the same request to the same exact
//   execution; the wing replays it without resending input.
// - Stop sends `pty.kill` to the exact wing/session and reports only its
//   acknowledgement. Reads continue so the native exit is observed.
//
// All I/O is injected, so the whole flow is testable without a browser.

import { emptyConversationState, restoredConversationState, applyConversationRead, conversationExecutionPresentation } from './conversation-state.js';
import { applyPromptReceipt } from './conversation-response.js';
import { readExecution, writeExecution, createPendingInput, updatePendingEvidence, pendingForConversation, migrateLegacyPending, validId, boundDelivery } from './conversation-recovery-store.js';
import { historyResumeState } from './session-resume.js';

export const READ_INTERVAL_MS = 1500;
export const READ_RETRY_MS = 3000;
export const PROMPT_TIMEOUT_SECONDS = 3;

function message(error, fallback) { return (error && error.message) || fallback; }

function sameExecution(a, b) {
    return !!a && !!b && a.userId === b.userId && a.wingId === b.wingId && a.sessionId === b.sessionId;
}

export function createConversationReader(options) {
    var storage = options.storage;
    var request = options.request;
    var now = options.now || Date.now;
    var schedule = options.schedule || setTimeout;
    var cancel = options.cancel || clearTimeout;
    var randomId = options.randomId;
    var onChange = options.onChange || function() {};
    var legacyStorage = options.legacyStorage || null;

    var target = null;
    var generation = 0;
    var timer = null;
    var inflight = 0;
    var readSeq = 0;
    var state = emptyConversationState();
    var trimmed = false;
    var cached = false;
    var observedAt = 0;
    var readError = '';
    var storageError = '';
    var halted = false;
    var persisted = '';
    var pending = null;
    var otherPending = [];
    var checking = '';
    var notice = '';
    var draft = null;
    var stop = { pending: false, error: '', acknowledgedAt: 0 };

    function ref() { return { userId: target.userId, wingId: target.wingId, sessionId: target.sessionId }; }

    function present() {
        return conversationExecutionPresentation(state.lifecycle, { observedAt: observedAt, now: now(), cached: cached, readError: readError });
    }

    function snapshot() {
        var shown = present();
        return {
            target: target ? { ...target } : null,
            messages: state.messages, lifecycle: state.lifecycle, cursor: state.cursor, trimmed: trimmed,
            presentation: shown, cached: cached, observedAt: observedAt, readError: readError, storageError: storageError,
            identityError: state.identityError || '', halted: halted,
            pending: pending, otherPending: otherPending.slice(), checking: !!checking, notice: notice,
            stop: { ...stop },
            inputReady: !!target && !pending && !checking && !halted && shown.inputReady,
        };
    }

    function emit() { onChange(snapshot()); }

    function persist() {
        if (!target) return;
        var lifecycle = state.lifecycle;
        var result = writeExecution(storage, ref(), {
            conversationId: target.conversationId, providerSessionId: target.providerSessionId, agent: lifecycle && lifecycle.agent,
            cursor: state.cursor, lifecycle: lifecycle, observedAt: observedAt, savedAt: now(),
            messages: state.messages, trimmed: trimmed, pending: pending,
        });
        storageError = result.error;
    }

    function scheduleRead(gen, delay) {
        if (gen !== generation || halted) return;
        if (timer) cancel(timer);
        timer = schedule(function() { timer = null; read(gen); }, delay);
    }

    function read(gen) {
        if (gen !== generation || !target || halted || inflight === gen) return;
        inflight = gen;
        var seq = ++readSeq;
        var t = target;
        request(t.wingId, { type: 'session.control', operation: 'session_read', arguments: { session: t.sessionId, after_cursor: state.cursor, limit: 100 } }).then(function(result) {
            if (inflight === gen) inflight = 0;
            if (gen !== generation) return;
            var view = result && result.lifecycle;
            if (!view) throw new Error('The wing does not support the conversation reader yet. Open its terminal.');
            if (result.session && result.session !== t.sessionId) view = { ...view, session_id: result.session };
            var next = applyConversationRead(state, view, { readSeq: seq, expected: { sessionId: t.sessionId, providerSessionId: t.providerSessionId } });
            if (next.identityError) {
                state = next;
                halted = true;
                readError = next.identityError;
                emit();
                return;
            }
            state = next;
            if (!target.providerSessionId && state.lifecycle && state.lifecycle.provider_session_id) target.providerSessionId = state.lifecycle.provider_session_id;
            cached = false;
            observedAt = now();
            readError = '';
            // Rewrite browser storage only when the evidence changed, not on
            // every idle poll.
            var lifecycle = state.lifecycle || {};
            var signature = [state.cursor, state.messages.length, lifecycle.state, lifecycle.process_alive, lifecycle.head_cursor, target.providerSessionId].join(':');
            if (signature !== persisted) { persisted = signature; persist(); }
            emit();
            scheduleRead(gen, view.has_more ? 0 : READ_INTERVAL_MS);
        }).catch(function(error) {
            if (inflight === gen) inflight = 0;
            if (gen !== generation) return;
            readError = message(error, 'Connection interrupted · reconnecting');
            emit();
            scheduleRead(gen, READ_RETRY_MS);
        });
    }

    function close() {
        generation++;
        if (timer) cancel(timer);
        timer = null;
        target = null;
        state = emptyConversationState();
        pending = null;
        otherPending = [];
        checking = '';
        notice = '';
        draft = null;
        halted = false;
        persisted = '';
        cached = false;
        observedAt = 0;
        readError = '';
        stop = { pending: false, error: '', acknowledgedAt: 0 };
    }

    // target: { userId, wingId, sessionId, conversationId?, providerSessionId? }
    function open(next) {
        close();
        if (!next || !validId(next.userId) || !validId(next.wingId) || !validId(next.sessionId)) {
            readError = 'Conversation execution identity is incomplete; nothing was opened.';
            emit();
            return false;
        }
        target = { userId: next.userId, wingId: next.wingId, sessionId: next.sessionId, conversationId: next.conversationId || '', providerSessionId: next.providerSessionId || '' };
        var restored = readExecution(storage, ref());
        storageError = restored.error;
        var record = restored.record;
        if (record && record.providerSessionId && target.providerSessionId && record.providerSessionId !== target.providerSessionId) {
            storageError = 'Cached transcript belonged to another provider conversation and was not shown.';
            record = { ...record, messages: [], lifecycle: null, cursor: 0 };
        }
        state = restoredConversationState(record);
        trimmed = !!(record && record.trimmed);
        cached = !!(record && (record.lifecycle || record.messages.length));
        if (record && record.providerSessionId && !target.providerSessionId) target.providerSessionId = record.providerSessionId;
        if (!target.conversationId && record && record.conversationId) target.conversationId = record.conversationId;
        pending = record && record.pending ? record.pending : null;
        if (!pending) {
            pending = migrateLegacyPending(legacyStorage, { ...ref(), conversationId: target.conversationId });
            if (pending) persist();
        }
        otherPending = target.conversationId ? pendingForConversation(storage, target.userId, target.wingId, target.conversationId, target.sessionId) : [];
        emit();
        scheduleRead(generation, 0);
        return true;
    }

    function storePending(p, next) {
        var execution = { userId: p.userId, wingId: p.wingId, sessionId: p.sessionId };
        if (target && sameExecution(target, execution)) {
            if (!pending || pending.request_id === p.request_id) pending = next;
            persist();
            return;
        }
        var existing = readExecution(storage, execution).record;
        if (existing && existing.pending && existing.pending.request_id !== p.request_id) return;
        writeExecution(storage, execution, { ...(existing || {}), conversationId: (existing && existing.conversationId) || p.conversationId, pending: next, savedAt: now() });
    }

    function settle(p, action, gen) {
        if (checking === p.request_id) checking = '';
        var next = action.pending ? updatePendingEvidence(p, { status: action.pending.status, lastReason: action.pending.lastReason || '', checks: (p.checks || 0) + 1, lastCheckedAt: now() }) : null;
        // Evidence is stored under the request's own execution even if the
        // reader has since moved to another execution or conversation.
        var current = !!pending && pending.request_id === p.request_id && sameExecution(target, p);
        storePending(p, next);
        otherPending = otherPending.map(function(item) { return item.request_id === p.request_id ? next : item; }).filter(Boolean);
        // A closed reader stays silent; a reader reopened elsewhere refreshes
        // its own view of other executions' unresolved input.
        if (gen !== generation) { if (target) emit(); return; }
        if (!current) {
            if (action.pending === null) {
                notice = action.draft ? 'Input to earlier execution ' + p.sessionId + ' was not sent (' + (action.notice || 'not sent') + '). It was not moved to the current execution; its text was: ' + action.draft : 'Native receipt observed on earlier execution ' + p.sessionId + '.';
            }
        } else {
            if (action.draft !== null && action.draft !== '') draft = action.draft;
            notice = action.notice;
        }
        emit();
    }

    function submit(p) {
        var gen = generation;
        checking = p.request_id;
        emit();
        return request(p.wingId, { type: 'session.control', operation: 'session_prompt', arguments: { session: p.sessionId, input: p.input, request_id: p.request_id, timeout_seconds: PROMPT_TIMEOUT_SECONDS } }).then(function(result) {
            var receipt = (result && (result.receipt || result.prompt)) || result || {};
            settle(p, applyPromptReceipt(p, receipt), gen);
            return true;
        }).catch(function(error) {
            settle(p, { pending: { ...p, status: 'unconfirmed', lastReason: message(error, 'Prompt delivery unconfirmed') }, draft: null, notice: '' }, gen);
            return false;
        });
    }

    function send(text) {
        text = typeof text === 'string' ? text.trim() : '';
        if (!target || !text || !snapshot().inputReady) return Promise.resolve(false);
        var lifecycle = state.lifecycle || {};
        pending = createPendingInput({ request_id: randomId(), input: text, userId: target.userId, wingId: target.wingId, sessionId: target.sessionId, providerSessionId: target.providerSessionId || lifecycle.provider_session_id || '', conversationId: target.conversationId, createdAt: now() });
        if (!pending) return Promise.resolve(false);
        notice = '';
        // The reservation is durable before any transport so a reload can only
        // offer an explicit receipt check for this exact request.
        persist();
        return submit(pending);
    }

    function checkReceipt(requestId) {
        if (checking) return Promise.resolve(false);
        var p = pending && (!requestId || pending.request_id === requestId) ? pending : otherPending.find(function(item) { return item.request_id === requestId; });
        if (!p) return Promise.resolve(false);
        return submit(p);
    }

    function stopExecution() {
        if (!target || stop.pending) return Promise.resolve(false);
        var t = target;
        var gen = generation;
        stop = { pending: true, error: '', acknowledgedAt: 0 };
        emit();
        return request(t.wingId, { type: 'pty.kill', session_id: t.sessionId }).then(function(result) {
            if (!result || (result.ok !== true && result.ok !== 'true')) throw new Error((result && result.error) || 'The wing did not confirm the stop request');
            if (gen !== generation) return true;
            stop = { pending: false, error: '', acknowledgedAt: now() };
            emit();
            refresh();
            return true;
        }).catch(function(error) {
            if (gen !== generation) return false;
            stop = { pending: false, error: 'Stop not confirmed: ' + message(error, 'request failed') + '. The execution may still be running.', acknowledgedAt: 0 };
            emit();
            return false;
        });
    }

    function refresh() {
        if (!target) return;
        if (halted) return;
        scheduleRead(generation, 0);
    }

    function takeDraft() { var out = draft; draft = null; return out; }

    return { open: open, close: close, send: send, checkReceipt: checkReceipt, stopExecution: stopExecution, refresh: refresh, snapshot: snapshot, takeDraft: takeDraft };
}

// Selections race: only the newest activation may switch the reader.
export function createNavigationGuard() {
    var seq = 0;
    var active = null;
    return {
        begin: function(reference) { active = { seq: ++seq, reference: reference }; return active; },
        current: function(token) { return !!token && !!active && token.seq === active.seq; },
    };
}

// Qualify a typed conversation_read for one exact logical reference. Tasks on
// another wing, or a lifecycle for a different execution, are rejected rather
// than adopted. Missing earlier history stays a separate task issue.
export function qualifyConversationRead(result, reference) {
    var tasks = Array.isArray(result && result.tasks) ? result.tasks : [];
    var onWing = tasks.filter(function(task) { return task && task.conversation && validId(task.conversation.conversation_id) && (!task.conversation.wing_id || task.conversation.wing_id === reference.wingId); });
    var task = onWing.find(function(item) { return item.conversation.conversation_id === reference.conversationId; });
    var conversation = task ? task.conversation : result && result.conversation;
    if (!conversation || conversation.conversation_id !== reference.conversationId) throw new Error('The wing did not return this logical conversation.');
    if (conversation.wing_id && conversation.wing_id !== reference.wingId) throw new Error('This conversation is recorded on another wing; it was not opened here.');
    var lifecycle = task && task.lifecycle;
    if (lifecycle && lifecycle.session_id && conversation.session_id && lifecycle.session_id !== conversation.session_id) throw new Error('The current execution identity is inconsistent; it was not opened.');
    return {
        conversation: conversation,
        task: task || { conversation: conversation },
        tasks: onWing,
        rootConversationId: conversation.root_conversation_id || conversation.conversation_id,
        delivery: boundDelivery(result),
        execution: { wingId: reference.wingId, sessionId: conversation.session_id || '', providerSessionId: (lifecycle && lifecycle.provider_session_id) || '', conversationId: conversation.conversation_id },
    };
}

export async function resolveConversationExecution(request, reference) {
    var result = await request(reference.wingId, { type: 'session.control', operation: 'conversation_read', arguments: { conversation_id: reference.conversationId } });
    return qualifyConversationRead(result, reference);
}

// Root tree read for cards; reading never acknowledges deliveries.
export async function readConversationTree(request, wingId, root) {
    var args = { conversation_id: root.conversation_id, limit: 50 };
    if (Number.isSafeInteger(root.delivered_cursor) && root.delivered_cursor > 0) args.after_cursor = root.delivered_cursor;
    var result = await request(wingId, { type: 'session.control', operation: 'conversation_read', arguments: args });
    var tree = qualifyConversationRead(result, { wingId: wingId, conversationId: root.conversation_id });
    // The caller may not have known the checkpoint cursor yet; never list an
    // already acknowledged delivery as outstanding.
    var acknowledged = tree.conversation.delivered_cursor;
    if (Number.isSafeInteger(acknowledged) && acknowledged > (args.after_cursor || 0)) {
        tree.delivery = boundDelivery({ ...result, events: (result.events || []).filter(function(event) { return event.sequence > acknowledged; }) });
    }
    return tree;
}

// Linked-tree merges are ordered by when each typed read was issued, never by
// when its response happened to arrive. Each task carries the issue time of
// the read that produced it; tasks restored from storage carry none.
export function taskRootId(task) { return task.conversation.root_conversation_id || task.conversation.conversation_id; }

export function rootObservedAt(tasks, rootId) {
    return tasks.reduce(function(latest, task) { return taskRootId(task) === rootId ? Math.max(latest, task.observedAt || 0) : latest; }, 0);
}

function stampTasks(tasks, at) { return tasks.map(function(task) { return { ...task, observedAt: at }; }); }

// read: { rootId, tasks, delivery, issuedAt }. Returns null when a read issued
// later already supplied this root.
export function mergeRootRead(tree, read) {
    if (rootObservedAt(tree.tasks, read.rootId) > read.issuedAt) return null;
    var deliveries = { ...(tree.deliveries || {}) };
    if (read.delivery) deliveries[read.rootId] = read.delivery;
    return { tasks: tree.tasks.filter(function(task) { return taskRootId(task) !== read.rootId; }).concat(stampTasks(read.tasks, read.issuedAt)), deliveries: deliveries };
}

// A full listing replaces the wing's roots, except subtrees observed by a read
// issued later (per root), and roots first observed after the listing began.
export function mergeWingListing(tree, reads, listedAt) {
    var listed = new Set(reads.map(function(read) { return read.rootId; }));
    var previous = tree.deliveries || {};
    var tasks = tree.tasks.filter(function(task) { return !listed.has(taskRootId(task)) && (task.observedAt || 0) > listedAt; });
    var deliveries = {};
    tasks.forEach(function(task) { var root = taskRootId(task); if (previous[root]) deliveries[root] = previous[root]; });
    reads.forEach(function(read) {
        if (rootObservedAt(tree.tasks, read.rootId) > read.issuedAt) {
            tasks = tasks.concat(tree.tasks.filter(function(task) { return taskRootId(task) === read.rootId; }));
            if (previous[read.rootId]) deliveries[read.rootId] = previous[read.rootId];
        } else {
            tasks = tasks.concat(stampTasks(read.tasks, read.issuedAt));
            if (read.delivery) deliveries[read.rootId] = read.delivery;
        }
    });
    return { tasks: tasks, deliveries: deliveries };
}

// The existing explicit terminal resume (PTYStart.resume_session_id) is the
// only typed resume. Offer it only for the exact current execution, after a
// fresh native read shows it archived and a fresh sessions.history entry shows
// the same owner, logical conversation and resumable provider archive.
export function resumeInTerminalState(execution, presentation, cached, historySession, currentUser, capabilityAvailable) {
    if (!execution || !execution.sessionId) return { available: false, reason: 'No execution is selected.' };
    if (!presentation || !presentation.archived || cached) return { available: false, reason: 'Resume is offered only after a fresh read shows this execution archived.' };
    if (!historySession) return { available: false, reason: 'The wing did not list this execution in its archived history.' };
    if (historySession.session_id !== execution.sessionId) return { available: false, reason: 'Archived history did not match this exact execution.' };
    if (historySession.conversation_id && execution.conversationId && historySession.conversation_id !== execution.conversationId) return { available: false, reason: 'Archived execution belongs to another logical conversation.' };
    var agent = historySession.agent || execution.agent || '';
    if (!agent) return { available: false, reason: 'The archived execution does not report its provider.' };
    if (historySession.agent && execution.agent && historySession.agent !== execution.agent) return { available: false, reason: 'Archived execution belongs to another provider.' };
    return historyResumeState(historySession, currentUser, capabilityAvailable);
}

export async function findArchivedExecution(request, wingId, sessionId, pageLimit) {
    var limit = 50;
    for (var page = 0; page < (pageLimit || 6); page++) {
        var data = await request(wingId, { type: 'sessions.history', offset: page * limit, limit: limit });
        var sessions = (data && data.sessions) || [];
        var found = sessions.find(function(session) { return session.session_id === sessionId; });
        if (found) return found;
        if (sessions.length < limit) return null;
    }
    return null;
}

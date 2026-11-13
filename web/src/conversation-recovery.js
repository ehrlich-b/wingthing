// Reader and recovery controller for one exact conversation execution.
//
// Contract:
// - open/reopen reads (`session_read`). Only the session shell may opt in
//   to dispatching explicitly queued input after a fresh native-ready read.
//   Waiting-answer receipts are reconciled by reads, without a sender.
// - Send reserves an immutable pending input (request ID, text, exact
//   user/wing/session/provider) in browser storage before any transport.
// - Ordinary receipt checks are explicit same-request replays. Waiting answers
//   use a receipt-only selector on session_read, including after reopen.
// - Stop sends `pty.kill` to the exact wing/session and reports only its
//   acknowledgement. Reads continue so the native exit is observed.
//
// All I/O is injected, so the whole flow is testable without a browser.

import { emptyConversationState, restoredConversationState, applyConversationRead, conversationExecutionPresentation, CONVERSATION_FRESH_MS } from './conversation-state.js';
import { applyPromptReceipt } from './conversation-response.js';
import { readExecution, writeExecution, createPendingInput, updatePendingEvidence, pendingForConversation, migrateLegacyPending, validId, boundDelivery } from './conversation-recovery-store.js';
import { historyResumeState } from './session-resume.js';
import { continuationAvailability, createPendingContinuation, readPendingContinuation, savePendingContinuation, clearPendingContinuation, continuationArguments, continuationReceipt } from './conversation-continuation.js';

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
    var monotonic = options.monotonic || options.now || (() => performance.now());
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
    var answers = {};
    const answerDeadlines = new Map();
    var answerTimer = null;
    var revision = 0;
    var lastInputRequestId = '';
    var draftRequestId = '';
    var receiptChecks = new Set();
    var receiptRetryAt = new Map();
    var queued = null;
    var otherPending = [];
    var checking = '';
    var notice = '';
    var draft = null;
    var savedDraft = null;
    var deliveryReceipt = null;
    var stop = { pending: false, error: '', acknowledgedAt: 0 };
    var continuation = null;
    var availableContinuation = null;

    function ref() { return { userId: target.userId, wingId: target.wingId, sessionId: target.sessionId }; }

    function present() {
        return conversationExecutionPresentation(state.lifecycle, { observedAt: observedAt, now: now(), cached: cached, readError: readError });
    }

    function snapshot() {
        var shown = present();
        const waiting = shown.state === 'needs_input';
        const answerReady = waiting && shown.live && state.lifecycle?.state_source === state.lifecycle?.agent + '_hook' &&
            !!target?.providerSessionId && Number.isSafeInteger(state.lifecycle?.state_cursor);
        const answerReserved = waiting && Object.values(answers).some(a => !a.outcome &&
            a.providerSessionId === target?.providerSessionId && a.answerToStateCursor === state.lifecycle?.state_cursor);
        var canContinue = !!availableContinuation && !cached && !readError && !halted && observedAt > 0 && now() - observedAt < CONVERSATION_FRESH_MS;
        return {
            target: target ? { ...target } : null,
            messages: state.messages, lifecycle: state.lifecycle, cursor: state.cursor, trimmed: trimmed,
            presentation: shown, cached: cached, observedAt: observedAt, readError: readError, storageError: storageError,
            identityError: state.identityError || '', halted: halted,
            pending: pending, waitingAnswers: Object.values(answers), deliveryUnknown: Object.values(answers).some(a => !a.outcome && a.providerSessionId === target?.providerSessionId), queued: queued, otherPending: otherPending.slice(), checking: !!checking, notice: notice,
            deliveryReceipt: deliveryReceipt, sendingUntil: state.sending?.expiresAt || 0,
            stop: { ...stop },
            continuation: continuation, continuationAvailable: canContinue,
            continuationReady: canContinue && !pending && !continuation && !checking,
            waitingRemainingMS: Math.max(0, ...[...answerDeadlines.values()].map(at => at-monotonic())),
            inputReady: !!target && !answerReserved && !pending && !continuation && !checking && !halted &&
                (shown.inputReady || answerReady || canContinue || shown.state === 'working' && !!target.providerSessionId && !!options.canSendQueued),
        };
    }

    function emit() { onChange(snapshot()); }

    function persist() {
        if (!target) return false;
        var lifecycle = state.lifecycle;
        var result = writeExecution(storage, ref(), {
            conversationId: target.conversationId, providerSessionId: target.providerSessionId, agent: lifecycle && lifecycle.agent,
            cursor: state.cursor, lifecycle: lifecycle, observedAt: observedAt, savedAt: now(),
            messages: state.messages, trimmed: trimmed, pending: pending, queued: queued, draft: savedDraft,
            answers, lastInputRequestId, draftRequestId, expectedRevision: revision,
        });
        storageError = result.error;
        if (result.record) adoptDelivery(result.record);
        return result.ok;
    }

    // Each controller observes the current storage revision before a mutation.
    // Receipt updates use an immutable request entry, never a stale whole view.
    function adoptDelivery(record) {
        revision = record.revision || 0;
        pending = record.pending || null;
        queued = record.queued || null;
        answers = record.answers || {};
        lastInputRequestId = record.lastInputRequestId || '';
        if (savedDraft !== record.draft) draft = record.draft || null;
        savedDraft = record.draft || null;
        draftRequestId = record.draftRequestId || '';
        const candidate = answers[lastInputRequestId];
        const latest = candidate?.providerSessionId === target?.providerSessionId ? candidate : null;
        if (latest?.outcome === 'expired' || latest?.outcome === 'not_sent') notice = 'Not sent - try again';
        if (latest?.outcome === 'delivered') { notice = ''; deliveryReceipt = latest.receipt; }
    }

    function syncDelivery() {
        if (!target) return;
        const record = readExecution(storage, ref()).record;
        if (record && (record.revision || 0) > revision) adoptDelivery(record);
    }

    function isAnswer(p) { return Number.isSafeInteger(p?.answerToStateCursor); }
    function selector(p) { return { request_id: p.request_id, provider_session_id: p.providerSessionId, answer_to_state_cursor: p.answerToStateCursor }; }
    function validReceipt(p, receipt, result) {
        return receipt?.request_id === p.request_id && receipt.session_id === p.sessionId &&
            (!result?.session || result.session === p.sessionId) &&
            (!receipt.provider_session_id || receipt.provider_session_id === p.providerSessionId) &&
            (!isAnswer(p) || !['native_receipt_observed','waiting_answer_delivered','waiting_answer_expired','not_sent'].includes(receipt.status) || receipt.provider_session_id === p.providerSessionId && receipt.answer_to_state_cursor === p.answerToStateCursor) &&
            (receipt.answer_to_state_cursor === undefined || receipt.answer_to_state_cursor === p.answerToStateCursor);
    }

    // Receipt evidence is monotonic: delivered wins over expiry; a late unknown
    // observation cannot erase terminal evidence or a newer composer reservation.
    function settleAnswer(p, receipt) {
        const existing = readExecution(storage, p).record;
        const previous = existing?.answers?.[p.request_id];
        if (!previous || !validReceipt(p, receipt)) return;
        const core = ['request_id','input','userId','wingId','sessionId','providerSessionId','conversationId','createdAt','answerToStateCursor'];
        if (core.some(k => previous[k] !== p[k])) return;
        const action = applyPromptReceipt(previous, receipt);
        let outcome = receipt.status === 'native_receipt_observed' && receipt.native_receipt_observed || receipt.status === 'waiting_answer_delivered' && receipt.transport_enqueued ? 'delivered' : receipt.status === 'not_sent' && receipt.definitely_not_sent ? 'not_sent' : receipt.status === 'waiting_answer_expired' && receipt.transport_enqueued ? 'expired' : '';
        const rank = { '': 0, expired: 1, not_sent: 2, delivered: 3 };
        if (rank[previous.outcome || ''] > rank[outcome]) return;
        const serverVersion = Number(receipt.observed_at_ms) || 0;
        if (!outcome && serverVersion && serverVersion < (previous.serverObservedAt || 0)) return;
        const version = (previous.evidenceVersion || 0)+1;
        const updated = updatePendingEvidence(previous, {
            ...(action.pending || {}), outcome, receipt, evidenceVersion: Math.max(version, previous.evidenceVersion || 0),
            transportEnqueued: receipt.transport_enqueued || previous.transportEnqueued || false,
            transportEnqueuedAt: receipt.enqueued_at_ms || previous.transportEnqueuedAt,
            serverObservedAt: receipt.observed_at_ms || previous.serverObservedAt,
        });
        const next = { ...existing, answers: { ...existing.answers, [p.request_id]: updated }, expectedRevision: existing.revision || 0, savedAt: now() };
        if (next.pending?.request_id === p.request_id && outcome) next.pending = null;
        // A saved follow-up has its own lifecycle and must not hide this answer's
        // returned draft. Request identity still protects newer composer input.
        if (outcome && existing.providerSessionId === p.providerSessionId && existing.lastInputRequestId === p.request_id && (!existing.pending || existing.pending.request_id === p.request_id)) {
            if (outcome === 'not_sent' || outcome === 'expired') { next.draft = p.input; next.draftRequestId = p.request_id; }
            else if (existing.draftRequestId === p.request_id) { next.draft = null; next.draftRequestId = ''; }
        }
        const saved = writeExecution(storage, p, next);
        if (!saved.ok) return;
        if (target && sameExecution(target,p)) {
            if (pending?.request_id === p.request_id && checking === p.request_id) checking = '';
            adoptDelivery(saved.record);
            if (outcome) answerDeadlines.delete(p.request_id);
            else if (receipt.waiting_deadline_ms && receipt.remaining_ms > 0) {
                // Server and browser wall clocks are unrelated. Anchor this
                // receipt's remaining duration to local monotonic arrival time.
                const deadline = monotonic() + receipt.remaining_ms;
                answerDeadlines.set(p.request_id, Math.min(answerDeadlines.get(p.request_id) ?? Infinity, deadline));
            }
            scheduleAnswerDeadline();
        }
    }

    function scheduleAnswerDeadline() {
        if (answerTimer) cancel(answerTimer);
        answerTimer = null;
        const deadlines = [...answerDeadlines.values()];
        if (!target || !deadlines.length) return;
        answerTimer = schedule(() => {
            answerTimer = null;
            for (const [id, at] of answerDeadlines) if (at <= monotonic()) {
                answerDeadlines.delete(id);
                receiptRetryAt.delete(id);
                if (answers[id]) reconcileAnswer(answers[id]);
            }
            refresh();
        }, Math.max(0, Math.min(...deadlines)-monotonic()));
    }

    function reconcileAnswer(p) {
        if (!target || !sameExecution(target,p) || receiptChecks.has(p.request_id) || monotonic() < (receiptRetryAt.get(p.request_id) || 0)) return Promise.resolve(false);
        receiptChecks.add(p.request_id);
        const gen = generation;
        return request(p.wingId, { type: 'session.control', operation: 'session_read', arguments: { session: p.sessionId, after_cursor: state.cursor, limit: 100, original_request: selector(p) } }).then(result => {
            if (result?.receipt) {
                if (!validReceipt(p,result.receipt,result)) throw Error('Delivery receipt did not match this request.');
                settleAnswer(p,result.receipt);
            }
            receiptRetryAt.set(p.request_id, monotonic()+READ_INTERVAL_MS);
            if (gen === generation) { syncDelivery(); resolveWaitingAnswer(); drainQueue(); emit(); }
            return true;
        }).catch(() => { receiptRetryAt.set(p.request_id, monotonic()+READ_RETRY_MS); return false; })
            .finally(() => receiptChecks.delete(p.request_id));
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
            var next = applyConversationRead(state, view, { readSeq: seq, now: now(), expected: { sessionId: t.sessionId, providerSessionId: t.providerSessionId } });
            if (next.identityError) {
                state = next;
                halted = true;
                readError = next.identityError;
                emit();
                return;
            }
            syncDelivery();
            state = next;
            if (!target.providerSessionId && state.lifecycle && state.lifecycle.provider_session_id) target.providerSessionId = state.lifecycle.provider_session_id;
            availableContinuation = continuationAvailability(result, target, state.lifecycle);
            cached = false;
            observedAt = now();
            readError = '';
            // Rewrite browser storage only when the evidence changed, not on
            // every idle poll.
            var lifecycle = state.lifecycle || {};
            var signature = [state.cursor, state.messages.length, lifecycle.state, lifecycle.process_alive, lifecycle.head_cursor, target.providerSessionId].join(':');
            if (signature !== persisted) { persisted = signature; persist(); }
            resolveWaitingAnswer();
            Object.values(answers).filter(a => a.providerSessionId === target.providerSessionId && (!a.outcome || a.outcome === 'expired')).forEach(reconcileAnswer);
            drainQueue();
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
        if (answerTimer) cancel(answerTimer);
        answerTimer = null; answerDeadlines.clear();
        if (timer) cancel(timer);
        timer = null;
        target = null;
        state = emptyConversationState();
        pending = null;
        answers = {}; revision = 0; lastInputRequestId = ''; draftRequestId = '';
        queued = null;
        otherPending = [];
        checking = '';
        notice = '';
        draft = null;
        savedDraft = null;
        deliveryReceipt = null;
        halted = false;
        persisted = '';
        cached = false;
        observedAt = 0;
        readError = '';
        stop = { pending: false, error: '', acknowledgedAt: 0 };
        continuation = null;
        availableContinuation = null;
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
            record = { ...record, messages: [], lifecycle: null, cursor: 0, pending:null, queued:null, draft:null };
        }
        state = restoredConversationState(record);
        trimmed = !!(record && record.trimmed);
        cached = !!(record && (record.lifecycle || record.messages.length));
        if (record && record.providerSessionId && !target.providerSessionId) target.providerSessionId = record.providerSessionId;
        if (!target.conversationId && record && record.conversationId) target.conversationId = record.conversationId;
        if (record) adoptDelivery(record);
        pending = record && record.pending ? record.pending : null;
        if (isAnswer(pending)) {
            answers[pending.request_id] ||= pending;
            // Older reservations predate the latest-input pointer. Preserve
            // their draft recovery without claiming ownership of newer input.
            if (!lastInputRequestId && !draftRequestId) lastInputRequestId = pending.request_id;
            persist();
        }
        queued = record && record.queued ? record.queued : null;
        savedDraft = record?.draft || null;
        draft = savedDraft;
        if (!pending) {
            pending = migrateLegacyPending(legacyStorage, { ...ref(), conversationId: target.conversationId });
            if (pending) persist();
        }
        otherPending = target.conversationId ? pendingForConversation(storage, target.userId, target.wingId, target.conversationId, target.sessionId) : [];
        continuation = target.conversationId ? readPendingContinuation(storage, target) : null;
        resolveWaitingAnswer();
        emit();
        scheduleRead(generation, 0);
        return true;
    }

    function storePending(p, next, returnedDraft, requeue) {
        var execution = { userId: p.userId, wingId: p.wingId, sessionId: p.sessionId };
        if (target && sameExecution(target, execution)) {
            syncDelivery();
            if (!pending || pending.request_id !== p.request_id) return;
            pending = next;
            if (requeue && !queued) queued = requeue;
            if (returnedDraft) savedDraft = returnedDraft;
            persist();
            return;
        }
        var existing = readExecution(storage, execution).record;
        if (!existing?.pending || existing.pending.request_id !== p.request_id) return;
        writeExecution(storage, execution, { ...(existing || {}), conversationId: (existing && existing.conversationId) || p.conversationId, pending: next, queued: existing?.queued || requeue || null, draft: returnedDraft || existing?.draft, savedAt: now() });
    }

    function settle(p, action, gen) {
        if (checking === p.request_id) checking = '';
        var next = action.pending ? updatePendingEvidence(p, { status: action.pending.status, lastReason: action.pending.lastReason || '', checks: (p.checks || 0) + 1, lastCheckedAt: now(), transportEnqueued: action.pending.transportEnqueued,
        }) : null;
        // Evidence is stored under the request's own execution even if the
        // reader has since moved to another execution or conversation.
        var current = !!pending && pending.request_id === p.request_id && sameExecution(target, p);
        // A proven preflight rejection did not transport input. Reserve a new
        // queued identity because the rejected request's receipt is immutable.
        const requeue = action.requeue ? createPendingInput({ ...p, request_id: randomId(), createdAt: now() }) : null;
        storePending(p, next, action.draft, requeue);
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

    function resolveWaitingAnswer() {
        syncDelivery();
        const view = state.lifecycle;
        if (!cached && !readError && view?.state_source === view?.agent + '_hook') {
            for (const p of Object.values(answers)) {
                if ((!p.outcome || p.outcome === 'expired') && p.transportEnqueued && view.provider_session_id === p.providerSessionId && view.state && view.state !== 'needs_input' && view.state_cursor > p.answerToStateCursor) {
                    settleAnswer(p, { session_id:p.sessionId, request_id:p.request_id, provider_session_id:p.providerSessionId, answer_to_state_cursor:p.answerToStateCursor, status:'waiting_answer_delivered', transport_enqueued:true, native_receipt_observed:false, provider_request_acknowledged:false, receipt_kind:'waiting_state_resolved', observed_at_ms:now() });
                }
            }
        }
        // An unresolved delivery record has its own lifecycle. It must not
        // gate editing/queueing while fresh native state says the agent works.
        if (isAnswer(pending)) {
            pending = null;
            if (checking && answers[checking]) checking = '';
            persist();
        }
    }

    function submit(p) {
        if (isAnswer(p)) {
            emit();
            const gen = generation;
            return request(p.wingId, { type: 'session.control', operation: 'session_prompt', arguments: {
                session: p.sessionId, input: p.input, request_id: p.request_id,
                answer_to_state_cursor: p.answerToStateCursor, timeout_seconds: PROMPT_TIMEOUT_SECONDS,
            } }).then(result => {
                const receipt = result?.receipt || result?.prompt || result || {};
                if (!validReceipt(p, receipt, result)) throw Error('Delivery receipt did not match this request.');
                settleAnswer(p, receipt);
                return true;
            }).catch(() => false).finally(() => {
                if (gen === generation) { resolveWaitingAnswer(); reconcileAnswer(p); refresh(); emit(); }
            });
        }
        var gen = generation;
        checking = p.request_id;
        emit();
        const args = { session: p.sessionId, input: p.input, request_id: p.request_id, timeout_seconds: PROMPT_TIMEOUT_SECONDS };
        return request(p.wingId, { type: 'session.control', operation: 'session_prompt', arguments: args }).then(function(result) {
            var receipt = (result && (result.receipt || result.prompt)) || result || {};
            if (!validReceipt(p, receipt, result)) {
                throw new Error('Delivery receipt did not match this request.');
            }
            if (gen === generation && sameExecution(target, p) && (receipt.status === 'native_receipt_observed' && receipt.native_receipt_observed === true)) {
                deliveryReceipt = { ...receipt };
            }
            settle(p, applyPromptReceipt(p, receipt), gen);
            if (gen === generation) resolveWaitingAnswer();
            if (gen === generation && !pending) refresh();
            return true;
        }).catch(function(error) {
            settle(p, { pending: { ...p, status: 'unconfirmed', lastReason: message(error, 'Prompt delivery unconfirmed') }, draft: null, notice: '' }, gen);
            return false;
        });
    }

    function validText(text) {
        if (typeof text !== 'string' || !text.trim()) return false;
        if (new TextEncoder().encode(text).length > 65536 || /[\x00-\x08\x0b-\x1f\x7f]/.test(text)) {
            notice = 'Enter a message of at most 64 KiB without terminal control characters.';
            emit();
            return false;
        }
        return true;
    }

    // Only the session shell opts into queueing. Save before clearing its
    // composer, and atomically replace queued input with a pending reservation
    // before transport. A reload can never interpret sent input as unsent.
    function queue(text) {
        syncDelivery();
        if (!target || state.lifecycle?.state === 'needs_input' || pending || queued || halted || !validText(text) || !target.providerSessionId) return false;
        var previousDraft = savedDraft;
        savedDraft = null;
        queued = createPendingInput({ request_id: randomId(), input: text, ...ref(), providerSessionId: target.providerSessionId, conversationId: target.conversationId, createdAt: now() });
        lastInputRequestId = queued?.request_id || lastInputRequestId;
        draftRequestId = '';
        if (!queued || !persist()) {
            queued = null;
            savedDraft = previousDraft;
            notice = 'Message was not queued. Keep your draft and try again.';
            emit();
            return false;
        }
        draft = null;
        notice = '';
        emit();
        return true;
    }

    function drainQueue() {
        syncDelivery();
        // A follow-up queued while the agent worked is not an answer to a later
        // permission question. Waiting prompts require a separately reserved explicit answer.
        if (!queued || pending || state.lifecycle?.state === 'needs_input' || !snapshot().inputReady || snapshot().continuationReady ||
            !present().inputReady || queued.providerSessionId !== target.providerSessionId || !options.canSendQueued?.(ref())) return;
        var saved = queued;
        queued = null;
        pending = saved;
        lastInputRequestId = saved.request_id;
        if (!persist()) {
            pending = null;
            queued = saved;
            notice = 'Message is saved. Waiting to send.';
            return;
        }
        deliveryReceipt = null;
        submit(pending);
    }

    function send(text) {
        syncDelivery();
        const answering = present().state === 'needs_input' && present().live;
        if (snapshot().continuationReady) return sendContinuation(text);
        if (present().state === 'working' && options.canSendQueued) return Promise.resolve(queue(text));
        text = typeof text === 'string' ? text : '';
        // A saved follow-up never becomes an answer to a later prompt.
        if (!target || (queued && !answering) || !text.trim() || !snapshot().inputReady) return Promise.resolve(false);
        if (!validText(text)) return Promise.resolve(false);
        var lifecycle = state.lifecycle || {};
        pending = createPendingInput({ request_id: randomId(), input: text, userId: target.userId, wingId: target.wingId, sessionId: target.sessionId, providerSessionId: target.providerSessionId || lifecycle.provider_session_id || '', conversationId: target.conversationId, createdAt: now(), ...(answering ? {answerToStateCursor: lifecycle.state_cursor} : {}) });
        if (!pending) return Promise.resolve(false);
        lastInputRequestId = pending.request_id;
        draftRequestId = '';
        notice = '';
        var previousDraft = savedDraft;
        savedDraft = null;
        deliveryReceipt = null;
        // The reservation is durable before any transport so a reload can only
        // offer an explicit receipt check for this exact request.
        if (answering) answers[pending.request_id] = pending;
        if (!persist()) {
            if (answering) delete answers[pending.request_id];
            draft = pending.input;
            savedDraft = previousDraft;
            pending = null;
            notice = 'Input was not sent because this browser could not save its reservation. Your draft is still available; retry when browser storage is available.';
            emit();
            return Promise.resolve(false);
        }
        draft = null;
        return submit(pending);
    }

    function checkReceipt(requestId) {
        syncDelivery();
        const answer = requestId ? answers[requestId] : Object.values(answers).find(a => !a.outcome);
        if (answer) { receiptRetryAt.delete(answer.request_id); return reconcileAnswer(answer); }
        if (checking) return Promise.resolve(false);
        if (continuation && (!requestId || continuation.request_id === requestId)) return submitContinuation(continuation);
        var p = pending && (!requestId || pending.request_id === requestId) ? pending : otherPending.find(function(item) { return item.request_id === requestId; });
        if (!p) return Promise.resolve(false);
        return submit(p);
    }

    function sendContinuation(text) {
        var saved = createPendingContinuation(target, text, randomId(), now());
        if (!saved) { notice = "Enter a message of at most 64 KiB that does not begin with '-'."; emit(); return Promise.resolve(false); }
        if (!savePendingContinuation(storage, saved)) {
            continuation = readPendingContinuation(storage, target);
            storageError = continuation ? 'A follow-up is already waiting for confirmation. Your new message has not been sent.' : 'The follow-up could not be saved in this browser. Your message has not been sent.';
            emit();
            return Promise.resolve(false);
        }
        continuation = saved;
        notice = '';
        return submitContinuation(saved);
    }

    async function submitContinuation(saved) {
        var gen = generation;
        checking = saved.request_id;
        emit();
        var result, status = 'unconfirmed', reason = 'Follow-up unconfirmed. Check reuses this saved message.';
        try {
            result = await request(saved.wingId, { type: 'session.control', operation: 'agent_start', arguments: continuationArguments(saved) });
            status = await continuationReceipt(saved, result);
            if (status === 'failed') reason = result.launch_error || 'The follow-up did not start.';
        } catch (error) { reason = message(error, reason); }
        if (status === 'unconfirmed') savePendingContinuation(storage, { ...saved, status: status, lastReason: reason });
        else clearPendingContinuation(storage, saved);
        if (gen !== generation) return status === 'started';
        checking = '';
        continuation = status === 'unconfirmed' ? { ...saved, status: status, lastReason: reason } : null;
        notice = status === 'started' ? 'Follow-up started.' : reason;
        if (status === 'failed' && sameExecution(target, saved)) draft = saved.input;
        if (status === 'started') {
            availableContinuation = null;
            if (options.onContinuationStarted) options.onContinuationStarted(saved, result);
        }
        emit();
        return status === 'started';
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

    return { open: open, close: close, send: send, queue: queue, checkReceipt: checkReceipt, stopExecution: stopExecution, refresh: refresh, snapshot: snapshot, takeDraft: takeDraft };
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

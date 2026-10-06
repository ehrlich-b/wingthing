import { userStorageKey } from './storage-scope.js';
import { sessionResourceKey } from './session-reference.js';

export const COMPLETION_STORAGE_PREFIX = 'wt_session_seen_v1:';
export const COMPLETION_LIMIT = 1000;
export const COMPLETION_MAX_AGE_MS = 30 * 24 * 60 * 60 * 1000;
var statuses = ['working', 'blocked', 'idle', 'done', 'exited'];

function cursor(value) { return Number.isSafeInteger(value) && value >= 0 ? value : null; }

export function sessionCompletionObservation(session, status, seen) {
    return { id: session.id, wing_id: session.wing_id, status: status, cursor: (session.lifecycle || {}).state_cursor, seen: !!seen };
}

function readRecords(storage, userId, now) {
    var records = new Map();
    try {
        var saved = JSON.parse(storage.getItem(userStorageKey(COMPLETION_STORAGE_PREFIX, userId)) || '[]');
        if (!Array.isArray(saved)) return records;
        saved.forEach(function(record) {
            if (!record || typeof record.id !== 'string' || !record.id || typeof record.wing_id !== 'string' || !record.wing_id ||
                !statuses.includes(record.status) || !Number.isFinite(record.updatedAt) || record.updatedAt > now || now - record.updatedAt > COMPLETION_MAX_AGE_MS) return;
            records.set(sessionResourceKey(record), { id: record.id, wing_id: record.wing_id, status: record.status,
                cursor: cursor(record.cursor), worked: record.worked === true, unseen: record.unseen === true, updatedAt: record.updatedAt });
        });
    } catch (e) {}
    return records;
}

function boundedRecords(records) {
    // Evict the least recently changed/acknowledged execution, across all wings.
    return Array.from(records.values()).sort(function(a, b) { return b.updatedAt - a.updatedAt; }).slice(0, COMPLETION_LIMIT);
}

function saveRecords(storage, userId, records) {
    var key = userStorageKey(COMPLETION_STORAGE_PREFIX, userId);
    if (!key) return;
    try {
        var encoded = JSON.stringify(boundedRecords(records));
        if (storage.getItem(key) !== encoded) storage.setItem(key, encoded);
    } catch (e) {}
}

function unseenKeys(records) {
    return new Set(boundedRecords(records).filter(function(record) { return record.unseen; }).map(sessionResourceKey));
}

export function unseenSessionCompletions(storage, userId, now = Date.now()) {
    return userId ? unseenKeys(readRecords(storage, userId, now)) : new Set();
}

// Callers supply only fresh, trusted provider observations. Unknown/cache reads
// must not erase working evidence or create a completion. No notifications here:
// completion is local presentation state; blocked alerts keep their existing path.
export function trackSessionCompletions(storage, userId, observations, now = Date.now()) {
    if (!userId) return new Set();
    var records = readRecords(storage, userId, now);
    observations.forEach(function(observation) {
        if (!observation.id || !observation.wing_id || !statuses.includes(observation.status)) return;
        var key = sessionResourceKey(observation);
        var previous = records.get(key);
        var nextCursor = cursor(observation.cursor);
        if (previous && previous.cursor !== null && nextCursor !== null && nextCursor < previous.cursor) return;
        var changed = !previous || previous.status !== observation.status || (nextCursor !== null && nextCursor !== previous.cursor);
        var completed = changed && (observation.status === 'done' || (observation.status === 'idle' && previous && previous.worked));
        var unseen = !!(previous && previous.unseen) || !!completed;
        if (observation.seen) unseen = false;
        records.set(key, { id: observation.id, wing_id: observation.wing_id, status: observation.status,
            cursor: nextCursor === null && previous ? previous.cursor : nextCursor,
            worked: observation.status === 'working' || (!!(previous && previous.worked) && !['idle', 'done', 'exited'].includes(observation.status)),
            unseen: unseen, updatedAt: changed || unseen !== previous.unseen ? now : previous.updatedAt });
    });
    saveRecords(storage, userId, records);
    return unseenKeys(records);
}

// A stale row in another tab cannot acknowledge a newer completion cursor.
export function acknowledgeSessionCompletions(storage, userId, sessions, now = Date.now()) {
    if (!userId) return;
    var records = readRecords(storage, userId, now);
    sessions.forEach(function(session) {
        var record = records.get(sessionResourceKey(session));
        var shownCursor = cursor((session.lifecycle || {}).state_cursor);
        if (!record || (shownCursor !== null && record.cursor !== null && shownCursor < record.cursor)) return;
        if (record.unseen) records.set(sessionResourceKey(session), { ...record, unseen: false, updatedAt: now });
    });
    saveRecords(storage, userId, records);
}

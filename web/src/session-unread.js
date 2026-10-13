import { userStorageKey } from './storage-scope.js';
import { sessionResourceKey } from './session-reference.js';

export const UNREAD_STORAGE_PREFIX = 'wt_output_seen_v1:';
export const UNREAD_LIMIT = 1000;
const MAX_AGE = 30 * 24 * 60 * 60 * 1000;
function valid(value) { return Number.isSafeInteger(value) && value >= 0; }
function observation(session) {
    const output = session.output;
    return session.id && session.wing_id && output && typeof output.epoch === 'string' && output.epoch && valid(output.cursor) ? output : null;
}
function read(storage, userId, now) {
    const records = new Map();
    try {
        const saved = JSON.parse(storage.getItem(userStorageKey(UNREAD_STORAGE_PREFIX, userId)) || '[]');
        if (Array.isArray(saved)) saved.forEach(record => {
            if (record && typeof record.id === 'string' && typeof record.wing_id === 'string' && observation(record) &&
                valid(record.seen) && record.seen <= record.output.cursor && Number.isFinite(record.updatedAt) &&
                record.updatedAt <= now && now - record.updatedAt <= MAX_AGE) records.set(sessionResourceKey(record), record);
        });
    } catch {}
    return records;
}
function save(storage, userId, records) {
    try {
        const value = JSON.stringify([...records.values()].sort((a, b) => b.updatedAt - a.updatedAt).slice(0, UNREAD_LIMIT));
        const key = userStorageKey(UNREAD_STORAGE_PREFIX, userId);
        if (storage.getItem(key) !== value) storage.setItem(key, value);
    } catch {}
}

// Only fresh encrypted inventory enters this function. A cached/failed read
// cannot acknowledge output or infer anything about provider state.
export function trackSessionOutput(storage, userId, sessions, now = Date.now()) {
    if (!userId) return;
    const records = read(storage, userId, now);
    sessions.forEach(session => {
        const output = observation(session);
        if (!output) return;
        const key = sessionResourceKey(session), previous = records.get(key);
        if (previous && previous.output.epoch === output.epoch && output.cursor <= previous.output.cursor) return;
        records.set(key, { id: session.id, wing_id: session.wing_id, output: { ...output },
            seen: previous && previous.output.epoch === output.epoch ? previous.seen : 0, updatedAt: now });
    });
    save(storage, userId, records);
}
export function unreadSessionOutput(storage, userId, now = Date.now()) {
    if (!userId) return new Set();
    return new Set([...read(storage, userId, now).values()].filter(record => record.output.cursor > record.seen).map(sessionResourceKey));
}
// Stale tabs acknowledge only displayed bytes, never newer output or another
// execution. This is local presentation state, not input or approval.
export function acknowledgeSessionOutput(storage, userId, sessions, now = Date.now()) {
    if (!userId) return;
    const records = read(storage, userId, now);
    sessions.forEach(session => {
        const output = observation(session), key = sessionResourceKey(session), record = records.get(key);
        if (!output || !record || output.epoch !== record.output.epoch || output.cursor > record.output.cursor) return;
        if (output.cursor > record.seen) records.set(key, { ...record, seen: output.cursor, updatedAt: now });
    });
    save(storage, userId, records);
}

import { sessionResourceKey } from './session-reference.js';

// Canvas geometry/focus is local UI state. Wire payloads always use entry.id;
// every registry, focus, timer and persistence reference uses entry.key.
export function createCanvasSessionState() {
    var state = { sessions: Object.create(null), focusedKey: null };
    state.put = function(entry) {
        entry.key = sessionResourceKey(entry);
        state.sessions[entry.key] = entry;
        return entry.key;
    };
    state.find = function(id, wingId) {
        if (wingId) return state.sessions[sessionResourceKey({ id: id, wingId: wingId })] || null;
        var matches = Object.values(state.sessions).filter(function(entry) { return entry.id === id; });
        return matches.length === 1 ? matches[0] : null;
    };
    state.focus = function(key) {
        state.focusedKey = key && state.sessions[key] ? key : null;
    };
    state.rekey = function(oldKey, id) {
        var entry = state.sessions[oldKey];
        if (!entry) return null;
        delete state.sessions[oldKey];
        entry.id = id;
        var key = state.put(entry);
        if (state.focusedKey === oldKey) state.focusedKey = key;
        return key;
    };
    state.remove = function(key) {
        var entry = state.sessions[key];
        delete state.sessions[key];
        if (state.focusedKey === key) state.focusedKey = null;
        return entry;
    };
    state.layout = function() {
        return Object.fromEntries(Object.values(state.sessions).map(function(entry) {
            return [entry.key, { col: entry.col, row: entry.row, cellW: entry.cellW, cellH: entry.cellH }];
        }));
    };
    return state;
}

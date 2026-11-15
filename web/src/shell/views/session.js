import { S } from '../../state.js';
import { escapeMarkup as esc } from '../../security.js';
import { sessionDisplayName } from '../../helpers.js';
import { machineState, agentName, machineGuidance } from '../../paper-state.js';
import { head } from '../frame.js';
import { wingOf, machineName, sessionState, findSession } from '../select.js';
import { read, lines, request, terminal, project, chip, clock, prose, ui, drafts, accessPanel, staleState, inputAllowed, lastSeen, machineActions } from '../common.js';
import '../../session.css';

const phones = new Map(), windows = new Map();
const sessionKey = s => S.currentUser?.id + '/' + s.wing_id + '/' + s.id;
export function showEarlierMessages(s) { windows.set(sessionKey(s), { all: true }); }
export function setSessionPanel(session, panel) { phones.set(session.wing_id + '/' + session.id, panel); }
export function missing() { return head('Session', '') + '<section class="pp-empty"><h2>This session is not listed</h2><p>It may have ended, or its machine has not reported yet.</p><a class="pp-btn" href="#sessions">All sessions</a></section>'; }

function observedSession(s) {
    const snap = ui.inputSnapshot, target = snap?.target;
    return snap?.presentation?.live && target?.wingId === s.wing_id && target.sessionId === s.id
        ? { ...s, needs_attention: false, lifecycle: snap.lifecycle } : s;
}

function presentation(s, w, entry, term) {
    const machine = machineState(w), snap = ui.inputSnapshot;
    const current = observedSession(s);
    const st = sessionState(current), where = machineName(w);
    const control = S.activeView === 'terminal' && S.ptySessionId === s.id && S.ptyWingId === s.wing_id;
    if (machine === 'locked') return { key: 'locked', kicker: 'Locked', sentence: 'Unlock ' + where + ' to read this session.' };
    if (machine === 'offline') return { key: 'offline', kicker: 'Last screen' + (clock(term.at || w.last_seen) ? ', ' + clock(term.at || w.last_seen) : ''), sentence: where + ' is offline.' };
    if (st.key === 'ended' || snap?.presentation?.archived) return { key: 'ended', kicker: 'Ended', sentence: 'This session ended' + (clock(s.ended_at || snap?.lifecycle?.state_observed_at) ? ' at ' + clock(s.ended_at || snap?.lifecycle?.state_observed_at) : '') + '.' };
    if (control) {
        const ready = !!S.e2eKey && !S.ptyInputBlocked && S.ptyWs?.readyState === WebSocket.OPEN;
        return { key: 'control', kicker: ready ? "You're typing" : S.ptyInputBlocked ? 'Control unavailable · hand back and retry' : 'Connecting terminal…', sentence: 'Messages wait while you have control.' };
    }
    if (!entry.at && !entry.error) return { key: 'reading', kicker: 'Connecting…', sentence: 'Connecting to ' + where + '…' };
    if (snap?.presentation?.state === 'unsupported') return { key: 'unsupported', kicker: 'Read-only', sentence: "wingthing can't read this agent's conversation, so there's no chat here. The terminal on the right is the whole session." };
    if (!term.supported) return { key: 'older', kicker: 'Read-only', sentence: 'Update wt on ' + where + ' to see its terminal here:' };
    if (st.key === 'need' || snap?.presentation?.attention) return { key: 'waiting', kicker: 'Read-only · waiting for you', sentence: '' };
    if (st.key === 'work' || snap?.lifecycle?.state === 'working') return { key: 'working', kicker: 'Read-only', sentence: agentName(s.agent) + ' is working…' };
    return { key: lines(s).length ? 'live' : 'empty', kicker: 'Read-only', sentence: 'No messages yet. What you send goes to the agent\'s prompt.' };
}
function compose(s, state) {
    const snap = ui.inputSnapshot, queued = !!snap?.queued;
    const disabled = ['reading', 'control', 'unsupported', 'offline', 'locked', 'ended'].includes(state.key) || !inputAllowed(s);
    ui.sessionCanQueue = state.key === 'working';
    const ready = !disabled && (!queued || state.key === 'waiting') && snap?.inputReady && !snap?.pending;
    const editable = ready || !disabled && state.key === 'working' && !snap?.pending;
    const placeholder = state.key === 'unsupported' ? 'Use the terminal' : 'Message ' + sessionDisplayName(s);
    const status = ['control', 'offline', 'locked', 'ended'].includes(state.key) ? state.sentence : snap?.notice || snap?.storageError ? (snap.notice || snap.storageError) : snap?.waitingRemainingMS > 0 ? 'Waiting for an answer · ' + Math.ceil(snap.waitingRemainingMS / 1000) + 's' : snap?.deliveryUnknown ? 'Delivery unknown' : state.key === 'working' ? state.sentence : snap?.pending ? (snap.checking ? 'Sending…' : 'Not confirmed. Check delivery.') : snap?.notice || snap?.storageError || (snap?.waitingRemainingMS > 0 ? 'Waiting for an answer · ' + Math.ceil(snap.waitingRemainingMS / 1000) + 's' : snap?.deliveryUnknown ? 'Delivery unknown' : snap?.deliveryReceipt ? 'Delivered' : '');

    return '<form class="pp-composer" data-composer="' + esc(s.wing_id + '/' + s.id) + '" data-stable>' + '<label class="pp-sr" for="pp-message">' + esc(placeholder) + '</label><div class="pp-compose-entry"><textarea id="pp-message" rows="1" placeholder="' + esc(placeholder) + '"' + (editable ? '' : ' disabled') + '></textarea><button type="submit" class="pp-btn pp-btn-ink"' + (ready && drafts.get(ui.inputKey)?.trim() ? '' : ' disabled') + '>Send</button></div>' + '<div class="pp-delivery" role="status">' + (snap?.sendingUntil > Date.now() ? '<span class="pp-sending">Sending…</span>' : esc(status)) + '</div><div class="pp-queue-note">' + (state.key === 'working' || queued ? "Sends when it's ready" : '') + '</div>' + '<div class="pp-check-delivery"><button type="button" class="pp-btn" data-check-receipt' + (snap?.pending || snap?.deliveryUnknown ? '' : ' disabled style="visibility:hidden"') + '>Check delivery</button></div>' + '</form>';
}
export function viewSession(route) {
    const s = findSession(route); if (!s) return missing();
    const w = wingOf(s.wing_id), entry = read(s), term = terminal(s), state = presentation(s, w, entry, term);
    const where = machineName(w), control = state.key === 'control', locked = state.key === 'locked', r = request(observedSession(s));
    const panel = phones.get(s.wing_id + '/' + s.id) || (control || state.key === 'waiting' ? 'terminal' : 'messages');
    const hideMessages = ['locked', 'unsupported', 'reading'].includes(state.key);
    const all = lines(s), key = sessionKey(s);
    if (!windows.has(key) && entry.at && all.length) windows.set(key, { first: all[Math.max(0, all.length - 24)].id });
    const window = windows.get(key), start = window?.all ? 0 : Math.max(0, all.findIndex(l => l.id === window?.first));
    const earlier = start ? '<button class="pp-earlier" data-session-earlier>+' + start + ' more</button>' : '';
    const msgs = hideMessages ? '' : all.slice(start).map((l, index) => '<div' + (index < 10 ? ' data-stable' : '') + ' class="pp-row pp-row-' + (l.who === 'you' ? 'you' : 'session') + '" data-key="' + esc(s.wing_id + '/' + s.id + '/' + l.id) + '"><div class="pp-who"><b>' + (l.who === 'you' ? 'You' : esc(agentName(s.agent))) + '</b><small>' + esc(clock(l.at)) + '</small></div><details class="pp-say"><summary><span>' + prose(l.text) + '</span><small>Read</small></summary><div class="pp-message-full">' + prose(l.text) + '</div></details></div>').join('');
    const skeleton = '<div class="pp-reading" aria-label="Reading messages">' + '<div></div>'.repeat(3) + '</div>';
    const note = ['unsupported', 'empty', 'locked'].includes(state.key) ? '<p class="pp-session-note">' + esc(state.sentence) + '</p>' : '';
    const need = state.key === 'waiting' ? '<article class="pp-need" tabindex="-1"><b>Needs you</b><p>' + prose(r.reason || 'Waiting for your answer.') + '</p>' + (r.command ? '<pre class="pp-cmd">' + esc(r.command) + '</pre>' : '') + '<button class="pp-btn pp-btn-ink" data-takeover="' + esc(s.wing_id + '/' + s.id) + '">Answer in the terminal</button></article>' : '';
    const unlocked = machineState(w) === 'connected' && state.key !== 'ended' && state.key !== 'reading';
    const action = control ? '<button class="pp-btn" data-handback>Hand back</button>' : state.key === 'offline' ? '<button class="pp-btn" data-retry>Retry</button>' : state.key === 'ended' ? '<a class="pp-btn" href="#new/' + esc(encodeURIComponent(s.wing_id)) + '">New session here</a>' : unlocked ? '<button class="pp-btn" data-takeover="' + esc(s.wing_id + '/' + s.id) + '">Take over</button>' : '';
    const overlay = locked ? '<div class="pp-pane-note pp-unlock"><h3>Unlock ' + esc(where) + '</h3><p>' + esc(machineGuidance(w)) + '</p>' + (w.tunnel_error === 'no_passkeys_configured' ? machineActions(w) : '<button class="pp-btn pp-btn-ink" data-unlock-machine="' + esc(s.wing_id) + '">Unlock with passkey</button>') + '<p>Or attach from ' + esc(where) + ' itself: <code>wt attach ' + esc(sessionDisplayName(s)) + '</code></p></div>' : state.key === 'reading' ? '<p class="pp-pane-note">' + esc(state.sentence) + '</p>' : state.key === 'older' ? '<p class="pp-pane-note">' + esc(state.sentence) + ' <code>wt update</code></p>' : term.error && !term.text && !control ? '<p class="pp-pane-note">' + esc(term.error) + '</p>' : '';
    return head(esc(sessionDisplayName(s)), esc([agentName(s.agent), where, project(s)].filter(Boolean).join(' · ')) + chip(sessionState(observedSession(s))), staleState(s) ? esc(staleState(s)) : undefined) + (!['connected','offline','locked','checking'].includes(machineState(w)) ? accessPanel(w) : '') + '<div class="pp-session-switch" role="group" aria-label="Session view"><button data-session-panel="messages" aria-pressed="' + (panel === 'messages') + '">Messages</button><button data-session-panel="terminal" aria-pressed="' + (panel === 'terminal') + '">Terminal</button></div><div class="pp-session" data-state="' + state.key + '" data-panel="' + panel + '"><section class="pp-session-messages" data-stable aria-label="Messages"><div class="pp-session-bar">Messages</div><div class="pp-conversation" tabindex="0" aria-label="Session messages">' + (state.key === 'reading' ? skeleton : earlier + msgs + note + need) + '</div>' + compose(s, state) + '</section><section class="pp-session-terminal" data-stable aria-label="Terminal"><div class="pp-session-bar">Terminal <span data-terminal-kicker>' + esc(state.kicker) + '</span></div><div class="pp-session-screen" data-stable><div data-engine="session-screen" class="pp-screen-snapshot"' + (control || locked ? ' hidden' : '') + '></div><div data-engine="terminal" class="pp-screen-control"' + (control ? '' : ' hidden') + '></div>' + overlay + '</div><div class="pp-terminal-actions">' + action + '</div><p class="pp-terminal-help">' + (state.key === 'offline' ? 'Last seen ' + esc(lastSeen(w) || 'time unavailable') + '. Sessions are kept here.' : 'Your keys go to the session until you hand it back.') + '</p></section></div>';
}
export const viewApprove = viewSession;

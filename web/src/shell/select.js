// One source for the shell inventory, attention and counts.
import { S } from '../state.js';
import { wingDisplayName } from '../helpers.js';
import { machineState, sessionState as stateOf } from '../paper-state.js';
const wingOf = id => S.wingsData.find(w => w.wing_id === id);
const machineName = w => wingDisplayName(w, S.wingsData) || 'Machine';
const isCoordinator = s => s.pinned === true && s.role === 'coordinator';
function sessions() { return S.sessionsData.filter(s => (s.kind || 'terminal') !== 'chat' && wingOf(s.wing_id)); }
function coordinator() { return sessions().find(isCoordinator); }
function workSessions() { return sessions().filter(s => !isCoordinator(s)); }
function sessionState(s) { return stateOf(s, wingOf(s.wing_id)); }
function needing() { return workSessions().filter(s => stateOf(s, { online: true }).key === 'need'); }
function findSession(route) { return sessions().find(s => s.id === route.sessionId && s.wing_id === route.wingId); }

function machines() { return S.wingsData; }
function machineSessions(id) { return workSessions().filter(s => s.wing_id === id); }
function machineCounts() { return { connected: machines().filter(w => machineState(w) === 'connected').length, offline: machines().filter(w => machineState(w) === 'offline').length }; }

export { wingOf, machineName, isCoordinator, sessions, coordinator, workSessions, sessionState, needing, findSession, machines, machineSessions, machineCounts };

export function sessionGroups() {
    const work = workSessions();
    const by = key => work.filter(s => sessionState(s).key === key);
    return {
        work, by,
        offlineWings: machines().filter(w => machineState(w) === 'offline'),
        awake: machines().filter(w => machineState(w) === 'connected').map(machineName),
        unavailable: machines().filter(w => !['connected', 'checking'].includes(machineState(w))),
        away: by('offline'),
    };
}

export function machineSummary() {
    const counts = machineCounts();
    const otherStates = ['checking', 'locked', 'no-access', 'key-mismatch', 'unavailable', 'unknown'].map(state => {
        const count = machines().filter(w => machineState(w) === state).length;
        return count ? count + ' ' + ({'no-access':'no access', 'key-mismatch':'key changed'}[state] || state) : '';
    }).filter(Boolean);
    return [counts.connected + ' connected', counts.offline + ' offline', ...otherStates].join(' · ');
}

import { escapeMarkup as esc } from '../../security.js';
import { sessionDisplayName } from '../../helpers.js';
import { machineState, machineGuidance } from '../../paper-state.js';
import { installedAgents, agentSetup } from '../../onboarding.js';
import { machineSummary } from '../select.js';
import { head } from '../frame.js';
import { viewEmpty } from './home.js';
import { machineName, sessions, sessionState, machines, machineSessions } from '../select.js';
import { machineActions, kicker, machineRow, sessionHref, chip, isAway, staleState, ui, machineListLimit } from '../common.js';
function viewMachines() {
    if (!machines().length) return viewEmpty();
    const visible = ui.allMachines ? machines() : machines().slice(0, machineListLimit);
    const rows = visible.map(w => {
        const list = machineSessions(w.wing_id);
        const item = s => '<li data-session-row data-key="' + esc(s.wing_id + '/' + s.id) + '"><a href="' + sessionHref(s) + '">' + esc(sessionDisplayName(s)) + '<small class="pp-stale">' + esc(staleState(s)) + '</small></a>' + chip(sessionState(s)) + '</li>';
        return '<section class="pp-machine" data-key="' + esc(w.wing_id) + '" data-machine="' + esc(w.wing_id) + '"><div class="pp-machine-heading">' + machineRow(w) + (!['connected', 'checking'].includes(machineState(w)) ? machineActions(w) : '') + '</div>' +
            (machineGuidance(w) ? '<p class="pp-quiet">' + esc((isAway(w) ? 'Sessions kept. ' : '') + machineGuidance(w)) + '</p>' : '') +
            (list.length ? '<div data-session-list="3"><ul>' + list.slice(0, 3).map(item).join('') + '</ul>' + (list.length > 3 ? '<details class="pp-more-sessions"><summary>+' + (list.length - 3) + ' more sessions</summary><ul>' + list.slice(3).map(item).join('') + '</ul></details>' : '') + '</div>' : '<p class="pp-quiet">' + (machineState(w) === 'connected' ? 'No sessions on this machine.' : 'Sessions unavailable.') + '</p>') + (machineState(w) === 'connected' && !installedAgents(w).length ? agentSetup(w, machineName(w)) : '') + '</section>';
    }).join('');
    return head('Machines', machineSummary()) + '<div class="pp-cols"><div class="pp-col-main">' + kicker('Your machines', String(machines().length)) + rows +
        (!ui.allMachines && machines().length > machineListLimit ? '<button type="button" class="pp-more" data-more-machines>+' + (machines().length - machineListLimit) + ' more machines</button>' : '') + '</div>' +
        '<aside class="pp-col-rail">' + kicker('Add a device') + '<p>Run <code>wt start</code> on another computer to add it here.</p><p class="pp-quiet">To list a machine\'s sessions in another browser without signing in, run <code>wt pair --show-link</code> on it, then open Pair.</p><a class="pp-btn" href="#pair">Pair a machine</a></aside></div>';
}

export { viewMachines };

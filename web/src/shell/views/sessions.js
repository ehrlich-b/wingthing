import { escapeMarkup as esc } from '../../security.js';
import { machineLabel } from '../../paper-state.js';
import { viewEmpty } from './home.js';
import { sessionRow } from '../common.js';
import { sessionGroups } from '../select.js';
import { head } from '../frame.js';
import { machineName, sessions, coordinator, machines } from '../select.js';
import { lines, kicker, machineRow, machineOverflow, clock, prose, machineLimit } from '../common.js';
function viewSessions() {
    const { work, by, offlineWings, awake, unavailable, away } = sessionGroups();
    if (!work.length) return viewEmpty();
    const banner = unavailable.slice(0, machineLimit).map(w => '<p class="pp-machine-status">' + esc(machineName(w) + ' · ' + machineLabel(w)) + ' · <a href="#machines">Machine controls</a></p>').join('') + (unavailable.length > machineLimit ? '<a class="pp-more" href="#machines">+' + (unavailable.length - machineLimit) + ' more machines</a>' : '');
    const meta = '<span data-session-summary><b>' + work.length + '</b> sessions</span> · ' + machines().length + (machines().length === 1 ? ' machine' : ' machines') + ' · ' + awake.length + ' connected';
    const coord = coordinator(), coordLast = coord && lines(coord).filter(l => l.who === 'agent').pop();
    return head('Sessions', meta) + '<div class="pp-cols"><div class="pp-col-main">' +
        '<div class="pp-searchbar"><label class="pp-sr" for="pp-filter">Search sessions</label><input id="pp-filter" type="search" placeholder="Search work or machines" autocomplete="off"><button type="button" class="pp-btn" data-new-session>New session</button></div>' +
        '<div class="pp-search-result" data-search-empty role="status" hidden><span data-search-message></span><button type="button" class="pp-btn" data-clear-search>Clear search</button></div>' +
        group('Needs you', by('need')) + banner + group('Working', by('work')) + group('Idle and done', [...by('idle'), ...by('done'), ...by('ended'), ...by('unknown')]) +
        group('Access needed', [...by('locked'), ...by('no-access'), ...by('key-mismatch'), ...by('unavailable')]) +
        offlineWings.map(w => group('Last seen on ' + esc(machineName(w)), away.filter(s => s.wing_id === w.wing_id))).join('') + '</div>' +
        '<aside class="pp-col-rail">' + (coord ? kicker('Pinned') + '<div class="pp-pin"><p><b class="pp-wing">wing</b> <small>' + esc(clock(coordLast && coordLast.at)) + '</small></p><p>' + prose(coordLast ? coordLast.text : 'No update yet.') + '</p><a class="pp-btn" href="#">Open wing</a></div>' : '') +
        kicker('Machines', String(machines().length)) + machines().slice(0, machineLimit).map(w => machineRow(w)).join('') + machineOverflow() + '</aside></div>';
}

export { viewSessions };
function group(title, list) {
    const count = String(list.length);
    return list.length ? '<section class="pp-group"><div class="pp-kick"><span>' + title + '</span><span data-group-count>' + esc(count) + '</span></div>' + list.slice(0, 8).map(sessionRow).join('') + (list.length > 8 ? '<details class="pp-more-sessions"><summary>+' + (list.length - 8) + ' more</summary>' + list.slice(8).map(sessionRow).join('') + '</details>' : '') + '</section>' : '';
}

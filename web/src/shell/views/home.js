import { escapeMarkup as esc } from '../../security.js';
import { sessionDisplayName } from '../../helpers.js';
import { agentName } from '../../paper-state.js';
import { head } from '../frame.js';
import { wingOf, machineName, sessions, coordinator, workSessions, sessionState, needing, machines } from '../select.js';
import { accessPanel, squares, lines, terminal, needsSummary, kicker, composer, machineRow, machineOverflow, sessionHref, chip, clock, prose, machineLimit } from '../common.js';
function viewEmpty() {
    if (!machines().length) return head('wing', '') + '<section class="pp-empty"><h2>No machines yet</h2><p>Install wt on a computer where your agents run, then sign in and start it. It shows up here when it connects.</p>' +
        '<pre class="pp-cmd"><span class="pp-prompt">$</span> curl -fsSL https://wingthing.ai/install.sh | sh\n<span class="pp-prompt">$</span> wt login\n<span class="pp-prompt">$</span> wt start</pre>' +
        '<div class="pp-actions"><button type="button" class="pp-btn" data-copy="curl -fsSL https://wingthing.ai/install.sh | sh">Copy install command</button><a class="pp-btn" href="#pair">Pair a machine</a></div></section>';
    return head('wing', '') + '<div class="pp-cols"><section class="pp-empty"><h2>No sessions yet</h2><p>Choose a machine and set up an agent to begin.</p>' +
        '<div class="pp-actions"><button type="button" class="pp-btn pp-btn-ink" data-new-session>New session</button></div></section>' +
        '<aside class="pp-col-rail">' + kicker('Machines', String(machines().length)) + machines().slice(0, machineLimit).map(w => machineRow(w)).join('') + machineOverflow() +
        kicker('From a terminal') + '<pre class="pp-cmd">wt setup</pre><p class="pp-quiet pp-gap">Install an agent and sign in to its provider on that machine.</p></aside></div>';
}
function viewWing() {
    const work = workSessions(), coord = coordinator();
    if (!work.length && !coord) return viewEmpty();
    const rows = [];
    if (coord) for (const l of lines(coord)) rows.push({ id: coord.wing_id + '/' + coord.id + '/' + l.id, who: l.who === 'you' ? 'you' : 'wing', text: l.text, at: l.at, where: machineName(wingOf(coord.wing_id)) });
    for (const s of work) {
        const last = lines(s).filter(l => l.who === 'agent').pop();
        if (last) rows.push({ id: s.wing_id + '/' + s.id, who: 'session', s, text: last.text, at: last.at, where: agentName(s.agent) + ' · ' + machineName(wingOf(s.wing_id)) });
    }
    rows.sort((a, b) => (Date.parse(a.at) || 0) - (Date.parse(b.at) || 0));
    const reporting = new Set(rows.filter(r => r.s).map(r => JSON.stringify([r.s.wing_id, r.s.id]))).size;
    const need = needing();
    const thread = rows.slice(-14).map(r => '<div class="pp-row pp-row-' + r.who + '" data-key="' + esc(r.id) + '" data-source="' + esc((r.s ? sessionDisplayName(r.s) : r.who) + ' · ' + (r.s ? machineName(wingOf(r.s.wing_id)) : r.where)) + '">' +
        '<div class="pp-who">' + (r.s ? '<a href="' + sessionHref(r.s) + '">' + esc(sessionDisplayName(r.s)) + '</a>' + chip(sessionState(r.s)) : '<b>' + r.who + '</b>') +
        '<small>' + esc([clock(r.at), r.who === 'you' ? '' : r.where].filter(Boolean).join(' · ')) + '</small></div>' +
        '<div class="pp-say">' + prose(r.text) + '</div></div>').join('');
    const meta = '<b>wing</b>' + (coord ? ' · ' + esc(machineName(wingOf(coord.wing_id))) : '') + (coord ? ' · ' + esc(agentName(coord.agent)) + chip(sessionState(coord)) : '') + (need.length ? ' · <em>' + need.length + ' needs you</em>' : '');
    return head('wing', meta) + '<div class="pp-cols"><div class="pp-col-main">' +
        needsSummary() +
        '<div class="pp-thread-heading">' + kicker('<span data-thread-source>Today</span>', reporting + (reporting === 1 ? ' session' : ' sessions') + ' reporting') + '</div><div class="pp-conversation" tabindex="0" aria-label="Wing messages">' +
        (coord ? accessPanel(wingOf(coord.wing_id)) : '') + (thread || '<p class="pp-quiet">Wing has not reported yet. Its updates and each session\'s latest line appear here.</p>') +
        '</div>' + (coord ? composer('Message wing', coord) : '') + '</div>' +
        '<aside class="pp-col-rail">' + kicker('Sessions', String(work.length)) + work.slice(0, 8).map(s => '<a class="pp-mini" data-key="' + esc(s.wing_id + '/' + s.id) + '" href="' + sessionHref(s) + '">' + chip(sessionState(s)) + '<span>' + esc(sessionDisplayName(s)) + '</span><small>' + esc(machineName(wingOf(s.wing_id))) + '</small></a>').join('') + (work.length > 8 ? '<a class="pp-quiet" href="#sessions">+' + (work.length - 8) + ' more</a>' : '') +
        kicker('Machines', String(machines().length)) + '<div class="pp-squares">' + squares() + '</div></aside></div>';
}

export { viewEmpty, viewWing };

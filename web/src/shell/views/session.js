import { escapeMarkup as esc } from '../../security.js';
import { sessionDisplayName } from '../../helpers.js';
import { machineState, agentName, machineLabel, machineGuidance } from '../../paper-state.js';
import { head } from '../frame.js';
import { wingOf, machineName, sessions, sessionState, findSession } from '../select.js';
import { accessPanel, read, lines, request, terminal, needCard, kicker, composer, sessionHref, project, chip, clock, prose, awayText, isAway, staleState } from '../common.js';
function missing() { return head('Session', '') + '<section class="pp-empty"><h2>This session is not listed</h2><p>It may have ended, or its machine has not reported yet.</p><div class="pp-actions"><a class="pp-btn" href="#sessions">All sessions</a></div></section>'; }
function viewSession(route) {
    const s = findSession(route); if (!s) return missing();
    const w = wingOf(s.wing_id), st = sessionState(s), entry = read(s), term = terminal(s);
    const msgs = lines(s).slice(-24).map(l => '<div class="pp-row pp-row-' + (l.who === 'you' ? 'you' : 'session') + '" data-key="' + esc(s.wing_id + '/' + s.id + '/' + l.id) + '"><div class="pp-who"><b>' + (l.who === 'you' ? 'you' : 'session') + '</b><small>' + esc(clock(l.at)) + '</small></div><div class="pp-say">' + prose(l.text) + '</div></div>').join('');
    const live = machineState(w) === 'connected', phone = matchMedia('(max-width: 760px)').matches;
    const meta = esc([agentName(s.agent), machineName(w), project(s)].filter(Boolean).join(' · ')) + chip(st);
    return head(esc(sessionDisplayName(s)), meta) + accessPanel(w) + (staleState(s) ? '<p class="pp-quiet pp-stale">' + esc(staleState(s)) + '</p>' : '') + '<div class="pp-cols pp-split"><div class="pp-col-main"><div class="pp-conversation" tabindex="0" aria-label="Session messages">' + kicker('Messages', entry.error ? esc(entry.error) : '') +
        (msgs || '<p class="pp-quiet">' + (entry.at ? 'No messages in this session yet.' : 'Reading the session…') + '</p>') +
        (st.key === 'need' ? needCard(s) : '') + '</div>' + composer('Message ' + sessionDisplayName(s), s) + '</div>' +
        '<aside class="pp-col-rail pp-term-rail">' + (phone ? '<details class="pp-terminal"><summary>Show terminal</summary>' : '<div class="pp-terminal">') + kicker('Terminal', live ? 'Agent has control' : 'Last seen') +
        '<pre class="pp-term" aria-label="Terminal preview">' + esc((term.text || '').split('\n').slice(-18).join('\n').trimEnd() || term.error || (live ? 'Waiting for terminal output…' : machineName(w) + ' · ' + (isAway(w) ? awayText(w) : machineLabel(w)) + '. ' + machineGuidance(w))) + '</pre>' +
        (live ? '<button type="button" class="pp-btn" data-takeover="' + esc(s.wing_id + '/' + s.id) + '">Take over terminal</button><p class="pp-quiet">Your keys go to the session until you hand control back. Messages wait while you have control.</p>' : '') + (phone ? '</details>' : '</div>') + '</aside></div>';
}
function viewApprove(route) {
    const s = findSession(route); if (!s) return missing();
    const w = wingOf(s.wing_id), r = request(s), st = sessionState(s), where = machineName(w);
    const why = lines(s).filter(l => l.who === 'agent').slice(-3);
    const live = machineState(w) === 'connected';
    const facts = [['Where', '<b>' + esc(where) + '</b>' + (project(s) ? ' · ' + esc(project(s)) : '')], ['Agent', esc(agentName(s.agent))], ['Asked', esc(clock(r.at) || 'Time not recorded')]];
    const id = esc(s.wing_id + '/' + s.id);
    const actions = (live && st.key === 'need' ? '<button type="button" class="pp-btn pp-btn-ink" data-takeover="' + id + '">Answer in terminal</button>' : '') + '<a class="pp-btn" href="' + sessionHref(s) + '">Open session</a>';
    const note = !live ? where + ' · ' + (isAway(w) ? awayText(w) : machineLabel(w)) + '. ' + machineGuidance(w) : 'Opens the terminal on ' + where + '. Check the current prompt there before you answer.';
    return head('Answer in terminal', esc(sessionDisplayName(s)) + ' · ' + esc(where)) + '<div class="pp-cols"><div class="pp-col-main">' +
        kicker('Request from ' + esc(sessionDisplayName(s)), chip(st)) +
        '<p class="pp-lead">' + (st.key === 'need' ? esc(sessionDisplayName(s)) + ' is waiting for an answer on ' + esc(where) + '. The current prompt and its choices are in its terminal.' : 'This recorded request may have changed. Check its current status in the terminal.') + '</p>' +
        kicker('Recorded request') + '<p>' + prose(r.reason || 'The session did not record a request.') + '</p>' +
        '<dl class="pp-facts">' + facts.map(f => '<dt>' + f[0] + '</dt><dd>' + f[1] + '</dd>').join('') + '</dl>' +
        '<div class="pp-actions">' + actions + '</div><p class="pp-quiet pp-note" role="status">' + esc(note) + '</p></div>' +
        '<aside class="pp-col-rail">' + kicker('Recent messages') + (why.length ? why.map(l => '<div class="pp-why"><small>' + esc(clock(l.at)) + '</small><p>' + prose(l.text) + '</p></div>').join('') : '<p class="pp-quiet">No earlier messages were recorded.</p>') + '</aside></div>';
}

export { missing, viewSession, viewApprove };

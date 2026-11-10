import { escapeMarkup as esc } from '../../security.js';
import { machineState, agentName } from '../../paper-state.js';
import { installedAgents, needsPasskey, agentSetup } from '../../onboarding.js';
import { head } from '../frame.js';
import { viewEmpty } from './home.js';
import { machineName, machines } from '../select.js';
import { project, ui } from '../common.js';
function viewNew(route) {
    if (!machines().length) return viewEmpty();
    const w = machines().find(w => w.wing_id === (route.wingId || ui.launchWingId)) || machines().find(w => machineState(w) === 'connected') || machines()[0];
    ui.launchWingId = w.wing_id;
    return head('New session', 'Choose a machine') + '<section class="pp-setup"><label for="pp-launch-machine">Machine</label><select id="pp-launch-machine">' +
        machines().map(item => '<option data-key="' + esc(item.wing_id) + '" value="' + esc(item.wing_id) + '"' + (item === w ? ' selected' : '') + '>' + esc(machineName(item)) + '</option>').join('') + '</select>' +
        (needsPasskey(w) ? '<h2>Machine locked</h2><p>Unlock ' + esc(machineName(w)) + ' with your passkey to check its agents.</p>' +
            (w.tunnel_error === 'no_passkeys_configured' ? '<p>Add a passkey to your account first.</p><button type="button" class="pp-btn" data-add-passkey>Add passkey</button>' : '<button type="button" class="pp-btn pp-btn-ink" data-unlock-machine="' + esc(w.wing_id) + '">Unlock machine</button>') :
        machineState(w) !== 'connected' ? '<h2>Machine unavailable</h2><p>Reconnect ' + esc(machineName(w)) + ', then try again.</p><button type="button" class="pp-btn" data-retry>Retry</button>' :
            installedAgents(w).length ? '<h2>Ready on ' + esc(machineName(w)) + '</h2><p>' + esc(installedAgents(w).map(agentName).join(', ')) + '</p><button type="button" class="pp-btn pp-btn-ink" data-choose-project="' + esc(w.wing_id) + '">Choose project</button>' : agentSetup(w, machineName(w))) + '</section>';
}

export { viewNew };

import { escapeMarkup as esc } from './security.js';

export const browserUnavailable = user => user?.relay_allowed === false;
export const installedAgents = wing => Array.isArray(wing?.agents) ? wing.agents : [];

export function directSetup(user) {
    const commands = [
        'codex mcp add wingthing -- wt mcp connect --client codex',
        'claude mcp add --scope user wingthing -- wt mcp connect --client claude',
    ];
    return '<section class="pp-setup"><h2>Start free with your agent</h2><p>On each machine, run <code>wt setup</code>. Install an agent and sign in to its provider, then run <code>wt login</code> and <code>wt start</code>.</p>' +
        '<h3>Connect your agent</h3><p>Run either command where your parent agent runs. Keep the machines on the same LAN or tailnet.</p>' +
        commands.map(command => '<pre class="pp-cmd">' + esc(command) + '</pre><button type="button" class="pp-btn" data-copy="' + esc(command) + '">Copy command</button>').join('') +
        '<h3>Want browser control?</h3><p>This account does not have hosted browser control. It requires relay access on the account and permission on the selected machine. ' + (user?.self_service_plans ? 'See Account for the available plan options.' : 'Hosted access is provisioned separately; there is no self-service purchase here.') + '</p>' +
        '<p>For a free self-hosted browser, run <code>wt roost start --https</code> on your computer. Pairing alone lists sessions.</p></section>';
}

export function agentSetup(wing, name) {
    const known = Array.isArray(wing?.agents);
    return '<section class="pp-setup" role="status"><h2>' + (known ? 'No agents detected' : 'Check for agents') + '</h2>' +
        '<p>Run on <b>' + esc(name) + '</b>:</p><pre class="pp-cmd">wt setup</pre>' +
        '<p>Install an agent, then sign in to its provider on that machine. If it is already installed, make sure it is on the wing’s PATH. Restart the wing if its PATH changed.</p>' +
        '<button type="button" class="pp-btn" data-refresh-agents="' + esc(wing.wing_id) + '">Refresh agents</button></section>';
}

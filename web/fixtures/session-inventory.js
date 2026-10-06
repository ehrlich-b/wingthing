// Synthetic local UI fixture. No relay, provider, credentials or live sessions.
import { S, initDOM } from '../src/state.js';
import { renderDashboard, renderSidebar } from '../src/render.js';
import { initParentDot } from '../src/parent-dot.js';

initDOM();
S.currentUser = { id: 'fixture-owner', display_name: 'Personal preview', release_channel: 'preview', channel_label: 'Wingthing Preview', version: 'fixture' };
S.wingsData = [
    { wing_id: 'fixture-mac', wing_label: 'Personal Mac', hostname: 'personal-mac', online: true, platform: 'darwin', agents: ['claude', 'codex'], capabilities: ['session.rename.v1'], projects: [], user_id: 'fixture-owner' },
    { wing_id: 'fixture-wsl', wing_label: 'WSL workstation', hostname: 'wsl-workstation', online: true, platform: 'linux', agents: ['claude', 'codex'], capabilities: ['session.rename.v1'], projects: [], user_id: 'fixture-owner' },
    { wing_id: 'fixture-offline', wing_label: 'Travel laptop', hostname: 'travel-laptop', online: false, platform: 'darwin', agents: ['claude'], projects: [], user_id: 'fixture-owner' }
];
S.sessionsData = [
    { id: 'fixture-parent', wing_id: 'fixture-mac', name: 'weekend-coordinator', agent: 'claude', cwd: '/Users/example/Projects/wingthing', user_id: 'fixture-owner', swept: true, status: 'detached', conversation_id: 'fixture-logical-parent', conversation_role: 'parent', lifecycle: { state: 'idle', state_source: 'claude_hook', process_alive: true }, lifecycle_seen_at: Date.now() },
    { id: 'fixture-child', wing_id: 'fixture-wsl', name: 'remote-reconnect-review', agent: 'claude', cwd: '/home/example/Projects/wingthing', user_id: 'fixture-owner', swept: true, status: 'detached', conversation_role: 'child', lifecycle: { state: 'working', state_source: 'claude_hook' } },
    { id: 'fixture-child', wing_id: 'fixture-mac', name: 'local-review-same-provider-id', agent: 'claude', cwd: '/Users/example/Projects/wingthing', user_id: 'fixture-owner', swept: true, status: 'detached', conversation_role: 'child', needs_attention: true },
    { id: 'fixture-input', wing_id: 'fixture-mac', name: 'ui-accessibility-check', agent: 'claude', cwd: '/Users/example/Projects/wingthing/web', user_id: 'fixture-owner', swept: true, status: 'detached', conversation_role: 'child', lifecycle: { state: 'needs_input', state_source: 'claude_hook' } },
    { id: 'fixture-unknown', wing_id: 'fixture-wsl', name: 'provider-compatibility', agent: 'codex', cwd: '/home/example/Projects/provider-fixtures', user_id: 'fixture-owner', swept: true, status: 'detached' },
    { id: 'fixture-disconnected', wing_id: 'fixture-offline', name: 'docs-review', agent: 'claude', cwd: '/Users/example/Projects/wingthing/docs', user_id: 'fixture-owner', swept: false, status: 'detached', lifecycle: { state: 'working', state_source: 'claude_hook' } }
];
function render() { renderSidebar(); renderDashboard(); }
initParentDot();
window.dispatchEvent(new CustomEvent('wingthing:conversation-selected', { detail: { wingId: 'fixture-mac', conversationId: 'fixture-logical-parent', title: 'weekend-coordinator' } }));
render();
document.getElementById('fixture-parent-attention').addEventListener('click', function() {
    var parent = S.sessionsData.find(function(session) { return session.conversation_id === 'fixture-logical-parent'; });
    parent.lifecycle.state = parent.lifecycle.state === 'needs_input' ? 'idle' : 'needs_input';
    parent.lifecycle_seen_at = Date.now();
    this.textContent = parent.lifecycle.state === 'needs_input' ? 'simulate parent ready' : 'simulate parent needs input';
    render();
});
document.getElementById('fixture-disconnect').addEventListener('click', function() {
    const wing = S.wingsData.find(wing => wing.wing_id === 'fixture-wsl');
    wing.online = !wing.online;
    this.textContent = wing.online ? 'simulate WSL disconnect' : 'simulate WSL reconnect';
    render();
});
// Block every runtime action in this fixture, including keyboard attachment.
// Search, focus navigation, detail inspection and disconnect simulation work.
document.addEventListener('click', function(event) {
    var rowAction = event.target.closest('.egg-box, .session-tab') && !event.target.closest('.inventory-details');
    if (rowAction || event.target.closest('#detail-egg-connect, #detail-egg-rename, #detail-egg-delete, .session-fork-btn, .wing-box, #parent-dot')) {
        event.preventDefault(); event.stopImmediatePropagation();
        document.getElementById('fixture-notice').textContent = 'Synthetic UI fixture: runtime actions are disabled.';
    }
}, true);
document.addEventListener('keydown', function(event) {
    if ((event.key === 'Enter' || event.key === ' ') && event.target.matches('.egg-box, .session-tab, .wing-box')) {
        event.preventDefault(); event.stopImmediatePropagation();
    }
}, true);

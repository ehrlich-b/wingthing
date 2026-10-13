import { sessionPromptStore } from './session-viewer-prompts.js';
import { Terminal } from '@xterm/xterm';
import { S } from './state.js';
import { sendTunnelRequest, randomUUID } from './tunnel.js';
import { createSessionViewer } from './session-viewer-state.js';

let viewer, dialog, terminal, poll, state, tab = 'transcript', lastTerminal = '', previousFocus;

function render(value) {
    state = value;
    if (!value) {
        if (dialog?.open) dialog.close();
        clearTimeout(poll);
        terminal?.reset(); lastTerminal = '';
        previousFocus?.focus?.({ preventScroll: true });
        return;
    }
    if (!dialog.open) { previousFocus = document.activeElement; dialog.showModal(); }
    dialog.querySelector('[data-title]').textContent = value.row.name || value.row.session_id;
    dialog.querySelector('[data-identity]').textContent = `${value.row.machine_label || 'Wing'} · ${value.row.origin_wing_id} / ${value.row.session_id}`;
    dialog.querySelector('[data-route]').textContent = value.route ? value.route.kind === 'via' ? 'via ' + value.route.machine_label : 'direct' : 'no route';
    dialog.querySelector('[data-status]').textContent = value.status;
    dialog.querySelector('[data-handoff-warning]').hidden = !value.releaseWarning;
    dialog.querySelector('[data-transcript]').textContent = (value.trimmedEvents ? 'Showing the last 400 events. Earlier events remain on the wing.\n\n' : '') + value.events.filter(event => event.role || event.text).map(event => `${event.role || event.type}: ${event.text || event.reason || ''}`).join('\n\n') || 'No recorded conversation text.';
    const attention = value.lifecycle?.state === 'needs_input' || value.row.needs_attention;
    dialog.querySelector('[data-attention]').hidden = !attention;
    dialog.querySelector('[data-more]').hidden = !value.hasMore;
    dialog.querySelector('[data-more]').disabled = value.busy;
    dialog.querySelector('[data-take]').hidden = Boolean(value.attachment);
    dialog.querySelector('[data-take]').disabled = value.busy || !value.route || !value.row.live || S.currentUser?.release_channel !== 'preview';
    dialog.querySelector('[data-take]').title = S.currentUser?.release_channel === 'preview' ? 'Deliberately replace the current terminal writer' : 'Takeover requires Wingthing Preview';
    dialog.querySelector('[data-back]').hidden = !value.attachment;
    dialog.querySelector('[data-back]').disabled = value.busy;
    dialog.querySelector('[data-send]').disabled = value.busy || !value.route || !value.row.live || Boolean(value.attachment || value.pendingPrompt || value.promptStorageError);
    dialog.querySelector('[data-recheck]').hidden = !value.pendingPrompt;
    dialog.querySelector('[data-recheck]').disabled = value.busy;
    dialog.querySelector('[data-composer]').hidden = Boolean(value.attachment);
    dialog.querySelector('[data-input]').disabled = value.busy || value.mode !== 'control';
    dialog.querySelector('[data-terminal-send]').disabled = value.busy || value.mode !== 'control';
    dialog.querySelector('[data-interrupt]').disabled = value.busy || value.mode !== 'control';
    dialog.querySelector('[data-refresh]').disabled = value.busy || Boolean(value.attachment);
    renderTerminal();
    clearTimeout(poll);
    poll = setTimeout(() => { if (document.visibilityState === 'visible' && S.activeView === 'home') viewer.read(); }, 2000);
}

function renderTerminal() {
    if (!terminal || !state || tab !== 'terminal') return;
    const output = state.attachment ? state.terminal.replace(/[ \t]+$/gm, '').replace(/\n+$/, '').replace(/\n/g, '\r\n') : state.terminal;
    if (output !== lastTerminal) {
        lastTerminal = output;
        terminal.reset();
        terminal.write(output, () => terminal.refresh(0, terminal.rows - 1));
    }
}

function showTab(next) {
    tab = next;
    dialog.querySelector('[data-transcript]').hidden = tab !== 'transcript';
    dialog.querySelector('[data-terminal-panel]').hidden = tab !== 'terminal';
    dialog.querySelectorAll('[data-tab]').forEach(button => button.setAttribute('aria-selected', String(button.dataset.tab === tab)));
    if (tab === 'terminal' && dialog.open) {
        if (!terminal) {
            terminal = new Terminal({ cols: 80, rows: 18, disableStdin: true, screenReaderMode: true, fontSize: 13, scrollback: 1000, theme: { background: '#111111', foreground: '#dddddd' } });
            terminal.open(dialog.querySelector('[data-terminal]'));
        }
        lastTerminal = ''; renderTerminal();
        terminal.refresh(0, terminal.rows - 1);
    }
}

export function initSessionViewer(inventory) {
    if (viewer) return viewer;
    dialog = document.createElement('dialog');
    dialog.id = 'session-viewer';
    dialog.setAttribute('aria-labelledby', 'session-viewer-title');
    dialog.innerHTML = `<header class="viewer-header"><div><span data-route class="search-route"></span><h2 id="session-viewer-title" data-title></h2></div><button type="button" data-close aria-label="Close session viewer">Close</button></header>
        <p data-identity class="search-identity"></p><p data-handoff-warning class="viewer-attention" hidden>A previous hand-back was unconfirmed. Its idle lease will release input after 15 seconds without activity.</p><p data-status role="status" aria-live="polite"></p>
        <div class="viewer-tabs" role="tablist" aria-label="Session view"><button data-tab="transcript" role="tab" aria-selected="true">Transcript</button><button data-tab="terminal" role="tab" aria-selected="false">Terminal</button><button data-refresh>Refresh route</button></div>
        <pre data-transcript aria-label="Live conversation transcript"></pre><button data-more hidden>Load more events</button>
        <section data-terminal-panel hidden><div data-terminal aria-label="Live terminal output"></div><label for="viewer-terminal-input">Type into the controlled terminal</label><input id="viewer-terminal-input" data-input autocomplete="off"><button data-terminal-send>Send + Enter</button><button data-interrupt>Ctrl+C</button></section>
        <div class="viewer-control"><button data-take>Take control</button><button data-back hidden>Hand back</button><span>Hand back keeps the agent running.</span></div>
        <p data-attention class="viewer-attention" hidden>Permission or input attention. This provider has no connected approval responder. Inspect the terminal, take control, and enter your decision there.</p>
        <form data-composer><label for="viewer-message">Send a message to this session</label><textarea id="viewer-message" rows="3" maxlength="65536" placeholder="Steer the next step…"></textarea><div><button data-send type="submit">Send message</button><button data-recheck type="button" hidden>Recheck original message</button></div></form>`;
    document.body.appendChild(dialog);
    viewer = createSessionViewer({ inventory, request: sendTunnelRequest, user: () => S.currentUser, wings: () => S.wingsData, update: render, uuid: randomUUID, prompts: sessionPromptStore(sessionStorage) });
    const click = (attribute, action) => dialog.querySelector(`[${attribute}]`).addEventListener('click', action);
    async function closeWithNotice() {
        const identity = dialog.querySelector('[data-identity]').textContent;
        if (!await viewer.close()) {
            const status = document.getElementById('session-search-status');
            if (status) status.textContent = `Hand-back unconfirmed for ${identity}. Input releases after 15 seconds without activity.`;
        }
    }
    click('data-close', closeWithNotice);
    dialog.addEventListener('cancel', event => { event.preventDefault(); closeWithNotice(); });
    dialog.querySelectorAll('[data-tab]').forEach(button => button.addEventListener('click', () => showTab(button.dataset.tab)));
    click('data-refresh', () => { if (state) viewer.open(state.row); });
    click('data-more', () => viewer.read());
    click('data-take', () => viewer.takeControl());
    click('data-back', () => viewer.handBack());
    click('data-recheck', () => viewer.sendMessage('', true));
    click('data-terminal-send', async () => {
        const input = dialog.querySelector('[data-input]');
        const text = input.value;
        await viewer.terminalInput(text + '\r');
        if (state?.mode === 'control') input.value = '';
    });
    click('data-interrupt', () => viewer.terminalInput('\x03'));
    dialog.querySelector('[data-composer]').addEventListener('submit', async event => {
        event.preventDefault();
        const input = dialog.querySelector('#viewer-message');
        await viewer.sendMessage(input.value);
        if (state?.receipt?.native_receipt_observed) input.value = '';
    });
    document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') viewer.read(); });
    window.addEventListener('pagehide', () => viewer.close());
    showTab(tab);
    return viewer;
}

export function openSessionViewer(row) { if (viewer) { showTab('transcript'); return viewer.open(row); } }
export function closeSessionViewer() { return viewer?.close(); }

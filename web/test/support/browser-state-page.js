import { S, DOM, initDOM, TERM_BUF_PREFIX, TERM_THUMB_PREFIX } from '../../src/state.js';
import * as state from '../../src/state.js';
import * as helpers from '../../src/helpers.js';
import * as references from '../../src/session-reference.js';
import * as inventory from '../../src/session-inventory.js';
import * as merge from '../../src/session-merge.js';
import * as completion from '../../src/session-completion.js';
import { applyWingEventMetadata } from '../../src/wing-event.js';
import { sessionRoute } from '../../src/session-route.js';

// The serverless journey runs real module bodies against real DOM/xterm. Only
// transport, unrelated views and notifications are stubbed. make web verifies
// the production import graph separately.
export async function bootBrowserState(sources) {
    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    const { SerializeAddon } = await import('@xterm/addon-serialize');
    function instantiate(name, bindings, names) {
        return new Function(...Object.keys(bindings), `${sources[name]}\nreturn {${names.join(',')}};`)(...Object.values(bindings));
    }
    initDOM();
    S.currentUser = { id: 'fixture-owner', has_passkeys: true };
    const noop = () => {};
    const actions = [], connections = [], inputs = [];
    let render, data, dashboard, nav, terminal, loading;
    class LocalSocket {
        static OPEN = 1;
        constructor() { this.readyState = 1; connections.push(this); }
        close() { this.readyState = 3; this.onclose?.(); }
        send(message) { inputs.push(JSON.parse(message)); }
    }
    const common = { ...state, S, DOM, ...helpers, ...references, ...inventory, ...merge, ...completion,
        Terminal, FitAddon, SerializeAddon, TERM_BUF_PREFIX, TERM_THUMB_PREFIX,
        WebSocket: LocalSocket, applyWingEventMetadata, sessionRoute,
        refreshParentDot: noop, refreshConversationInventory: noop,
        renderDashboard: noop, renderWingDetailPage: noop, renderCanvasToolbar: noop,
        isCanvasActive: () => false, hideCanvasView: noop, showCanvasView: noop,
        stopChatPolling: noop, chatSnapshot: () => null, browserLocalStorage: () => localStorage,
        saveTunnelAuthTokens: noop, setNotification: noop, clearNotification: noop,
        refreshSessionFilesButton: noop, updatePaletteState: noop, updateCanvasSessionName: noop,
        sendTunnelRequest: async (wingId, message) => {
            const response = await fetch(`/fixture/tunnel?wing=${encodeURIComponent(wingId)}&type=${encodeURIComponent(message.type)}`);
            const result = await response.json();
            if (result.error) throw Object.assign(new Error(result.error), { metadata: result });
            return result;
        },
        renderSidebar: () => render.renderSidebar(),
        switchToSession: (...args) => nav.switchToSession(...args),
        showHome: (...args) => nav.showHome(...args),
        rebuildAgentLists: () => dashboard.rebuildAgentLists(),
        updateHeaderStatus: () => dashboard.updateHeaderStatus(),
        loadHome: () => { loading = data.loadHome(); return loading; },
        cancelWingProbe: id => data.cancelWingProbe(id), probeWing: wing => data.probeWing(wing),
        getCachedWings: () => data.getCachedWings(), saveWingCache: () => data.saveWingCache(),
        fetchWingSessions: id => data.fetchWingSessions(id), mergeWingSessions: (...args) => data.mergeWingSessions(...args),
        saveSessionCache: () => data.saveSessionCache(), setEggOrder: order => data.setEggOrder(order),
        clearTermBuffer: (...args) => terminal.clearTermBuffer(...args), tunnelCloseWing: noop,
        detachPTY() { S.ptyWs = null; S.ptySessionId = null; S.ptyWingId = null; },
        attachPTY(id, _, wingId) {
            actions.push([wingId, id]); S.ptySessionId = id; S.ptyWingId = wingId;
            S.ptyWs = { readyState: 1, send: message => inputs.push(JSON.parse(message)) };
            render.renderSidebar();
        },
        e2eEncrypt: async text => text, sendViaDC: () => false,
    };
    render = instantiate('render', common, ['renderSidebar']);
    data = instantiate('data', common, ['loadHome', 'probeWing', 'cancelWingProbe', 'getCachedWings', 'saveWingCache', 'fetchWingSessions', 'mergeWingSessions', 'saveSessionCache', 'setEggOrder']);
    dashboard = instantiate('dashboard', common, ['connectAppWS', 'rebuildAgentLists', 'updateHeaderStatus']);
    nav = instantiate('nav', common, ['switchToSession', 'showHome', 'showTerminal']);
    terminal = instantiate('terminal', common, ['initTerminal', 'saveTermBuffer', 'restoreTermBuffer', 'clearTermBuffer', 'writeTerminalCache', 'terminalStorageBytes', 'TERMINAL_CACHE_BUDGET']);
    window.fixture = { S, DOM, render, data, dashboard, nav, terminal, actions, connections, inputs,
        get loading() { return loading; },
        event(event) { S.appWs.onmessage({ data: JSON.stringify(event) }); },
        async open() { S.appWs.onopen(); await loading; },
        cacheBytes() {
            let bytes = 0;
            for (let i = 0; i < localStorage.length; i++) {
                const key = localStorage.key(i);
                if (key.startsWith(TERM_BUF_PREFIX) || key.startsWith(TERM_THUMB_PREFIX) || key === 'wt_terminal_cache_v1') {
                    bytes += terminal.terminalStorageBytes(key) + terminal.terminalStorageBytes(localStorage.getItem(key));
                }
            }
            return bytes;
        },
        buffer(id, wing = 'mac') { return references.readSessionContent(localStorage, TERM_BUF_PREFIX, wing, id); },
        async write(text) { await new Promise(resolve => S.term.write(text, resolve)); },
        screen() {
            const buffer = S.term.buffer.active;
            return Array.from({ length: buffer.length }, (_, i) => buffer.getLine(i)?.translateToString(true) || '').join('\n');
        },
    };
    dashboard.connectAppWS();
    await window.fixture.open();
    nav.showTerminal(); terminal.initTerminal(); nav.showHome(false);
    window.fixture.ready = true;
}

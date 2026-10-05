import { S, DOM, CACHE_KEY, EGG_ORDER_KEY } from './state.js';
import { renderSidebar, renderDashboard, renderAccountPage, renderWingDetailPage } from './render.js';
import { detachPTY, connectPTY, attachPTY } from './pty.js';
import { clearTermBuffer } from './terminal.js';
import { clearNotification } from './notify.js';
import { sendTunnelRequest } from './tunnel.js';
import { loadHome, saveSessionCache, setEggOrder } from './data.js';
import { stopChatPolling } from './chat-view.js';
import { showCanvasView, hideCanvasView } from './canvas.js';
import { findSessionResource, sessionResourceKey } from './session-inventory.js';
import { sessionRoute } from './session-route.js';

function hideCanvasChrome() {
    if (DOM.canvasToolbar) DOM.canvasToolbar.style.display = 'none';
    DOM.headerTitle.style.display = '';
    DOM.ptyStatus.style.display = '';
    var canvasBtn = document.getElementById('canvas-toggle-btn');
    if (canvasBtn) canvasBtn.classList.remove('active');
}

function hostedRelayUnavailable() {
    return S.currentUser && S.currentUser.relay_allowed === false;
}

export function showCanvas(pushHistory) {
    if (hostedRelayUnavailable()) { showHome(pushHistory); return false; }
    if (window.innerWidth < 768) { showHome(pushHistory); return; }
    S.activeView = 'canvas';
    stopChatPolling();
    document.getElementById('app').classList.remove('in-terminal');
    DOM.homeSection.style.display = 'none';
    DOM.terminalSection.style.display = 'none';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = 'none';
    DOM.accountSection.style.display = 'none';
    DOM.canvasSection.style.display = '';
    detachPTY();
    showCanvasView();
    DOM.headerTitle.style.display = 'none';
    DOM.ptyStatus.style.display = 'none';
    DOM.sessionCloseBtn.style.display = 'none';
    var canvasBtn = document.getElementById('canvas-toggle-btn');
    if (canvasBtn) canvasBtn.classList.add('active');
    if (pushHistory !== false) {
        history.pushState({ view: 'canvas' }, '', '#canvas');
    }
    return true;
}

export function showHome(pushHistory) {
    S.activeView = 'home';
    stopChatPolling();
    hideCanvasChrome();
    document.getElementById('app').classList.remove('in-terminal');
    DOM.homeSection.style.display = '';
    DOM.terminalSection.style.display = 'none';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = 'none';
    DOM.accountSection.style.display = 'none';
    DOM.canvasSection.style.display = 'none';
    hideCanvasView();
    S.currentWingId = null;
    DOM.headerTitle.textContent = '';
    DOM.ptyStatus.textContent = '';
    var detachingId = S.ptySessionId;
    var detachingWingId = S.ptyWingId;
    detachPTY();
    if (detachingId) {
        var s = findSessionResource(S.sessionsData, detachingId, detachingWingId);
        if (s) s.status = 'detached';
    }
    renderSidebar();
    renderDashboard();
    if (pushHistory !== false) {
        history.pushState({ view: 'home' }, '', location.pathname);
    }
}

export function showTerminal() {
    if (hostedRelayUnavailable()) { showHome(); return false; }
    S.activeView = 'terminal';
    hideCanvasChrome();
    var canvasBtn = document.getElementById('canvas-toggle-btn');
    if (canvasBtn) canvasBtn.style.display = 'none';
    document.getElementById('app').classList.add('in-terminal');
    document.getElementById('terminal-section').classList.remove('chat-active');
    DOM.homeSection.style.display = 'none';
    DOM.terminalSection.style.display = '';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = 'none';
    DOM.accountSection.style.display = 'none';
    DOM.canvasSection.style.display = 'none';
    hideCanvasView();
    stopChatPolling();
    if (S.term && S.fitAddon) {
        S.fitAddon.fit();
        S.term.focus();
    }
    return true;
}

export function switchToSession(sessionId, pushHistory, wingId) {
    if (hostedRelayUnavailable()) { showHome(pushHistory); return false; }
    var sess = findSessionResource(S.sessionsData, sessionId, wingId);
    if (!sess && S.sessionsData.filter(function(s) { return s.id === sessionId; }).length > 1) return false;
    if (sess && !sess.swept) return;
    detachPTY();
    if (!showTerminal()) return false;
    attachPTY(sessionId, undefined, wingId || (sess && sess.wing_id));
    if (pushHistory !== false) {
        history.pushState({ view: 'terminal', sessionId: sessionId, wingId: wingId || (sess && sess.wing_id) }, '', sessionRoute(sessionId, wingId || (sess && sess.wing_id)));
    }
    return true;
}

export function navigateToWingDetail(wingId, pushHistory) {
    if (hostedRelayUnavailable()) { showHome(pushHistory); return false; }
    S.activeView = 'wing-detail';
    S.currentWingId = wingId;
    stopChatPolling();
    hideCanvasChrome();
    DOM.homeSection.style.display = 'none';
    DOM.terminalSection.style.display = 'none';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = '';
    DOM.accountSection.style.display = 'none';
    DOM.canvasSection.style.display = 'none';
    hideCanvasView();
    detachPTY();
    DOM.headerTitle.textContent = '';
    DOM.ptyStatus.textContent = '';
    renderWingDetailPage(wingId);
    if (pushHistory !== false) {
        history.pushState({ view: 'wing-detail', wingId: wingId }, '', '#w/' + wingId);
    }
    return true;
}

export function navigateToAccount(pushHistory, orgSlug) {
    if (!S.currentUser) return;
    S.activeView = 'account';
    S.accountExpandSlug = orgSlug || null;
    stopChatPolling();
    hideCanvasChrome();
    DOM.homeSection.style.display = 'none';
    DOM.terminalSection.style.display = 'none';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = 'none';
    DOM.accountSection.style.display = '';
    DOM.canvasSection.style.display = 'none';
    hideCanvasView();
    detachPTY();
    DOM.headerTitle.textContent = '';
    DOM.ptyStatus.textContent = '';
    renderAccountPage();
    if (pushHistory !== false) {
        var hash = orgSlug ? '#account/' + orgSlug : '#account';
        history.pushState({ view: 'account', orgSlug: orgSlug || null }, '', hash);
    }
}

export function deleteSession(sessionId, skipKill, targetWingId) {
    var sess = findSessionResource(S.sessionsData, sessionId, targetWingId);
    if (!sess && S.sessionsData.filter(function(s) { return s.id === sessionId; }).length > 1) return false;
    var wingId = '';
    if (sess) {
        var wing = S.wingsData.find(function(w) { return w.wing_id === sess.wing_id; });
        if (wing) wingId = wing.wing_id;
    }
    S.sessionsData = S.sessionsData.filter(function(s) { return s.id !== sessionId || (targetWingId && s.wing_id !== targetWingId); });
    setEggOrder(S.sessionsData.map(sessionResourceKey));
    saveSessionCache();
    clearTermBuffer(sessionId, targetWingId || (sess && sess.wing_id));
    if (targetWingId || sess) delete S.sessionNotifications[sessionResourceKey({ id: sessionId, wingId: targetWingId || sess.wing_id })];
    if (S.activeView === 'home') renderDashboard();
    renderSidebar();
    if (wingId && !skipKill) {
        sendTunnelRequest(wingId, { type: 'pty.kill', session_id: sessionId })
            .then(function() { loadHome(); })
            .catch(function() { loadHome(); });
    }
}

// Expose globally for inline onclick handlers
window._deleteSession = deleteSession;

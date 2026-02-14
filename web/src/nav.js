import { S, DOM, CACHE_KEY, EGG_ORDER_KEY } from './state.js';
import { renderSidebar, renderDashboard, renderAccountPage, renderWingDetailPage } from './render.js';
import { detachPTY, connectPTY, attachPTY } from './pty.js';
import { clearTermBuffer } from './terminal.js';
import { clearNotification } from './notify.js';
import { sendTunnelRequest } from './tunnel.js';
import { loadHome, getCachedSessions, setCachedSessions, setEggOrder } from './data.js';

export function showHome(pushHistory) {
    S.activeView = 'home';
    DOM.homeSection.style.display = '';
    DOM.terminalSection.style.display = 'none';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = 'none';
    DOM.accountSection.style.display = 'none';
    S.currentWingId = null;
    DOM.headerTitle.textContent = '';
    DOM.ptyStatus.textContent = '';
    var detachingId = S.ptySessionId;
    detachPTY();
    if (detachingId) {
        var s = S.sessionsData.find(function(s) { return s.id === detachingId; });
        if (s) s.status = 'detached';
    }
    renderSidebar();
    renderDashboard();
    if (pushHistory !== false) {
        history.pushState({ view: 'home' }, '', location.pathname);
    }
}

export function showTerminal() {
    S.activeView = 'terminal';
    DOM.homeSection.style.display = 'none';
    DOM.terminalSection.style.display = '';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = 'none';
    DOM.accountSection.style.display = 'none';
    if (S.term && S.fitAddon) {
        S.fitAddon.fit();
        S.term.focus();
    }
}

export function switchToSession(sessionId, pushHistory) {
    detachPTY();
    showTerminal();
    attachPTY(sessionId);
    if (pushHistory !== false) {
        history.pushState({ view: 'terminal', sessionId: sessionId }, '', '#s/' + sessionId);
    }
}

export function navigateToWingDetail(wingId, pushHistory) {
    S.activeView = 'wing-detail';
    S.currentWingId = wingId;
    DOM.homeSection.style.display = 'none';
    DOM.terminalSection.style.display = 'none';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = '';
    DOM.accountSection.style.display = 'none';
    detachPTY();
    DOM.headerTitle.textContent = '';
    DOM.ptyStatus.textContent = '';
    renderWingDetailPage(wingId);
    if (pushHistory !== false) {
        history.pushState({ view: 'wing-detail', wingId: wingId }, '', '#w/' + wingId);
    }
}

export function navigateToAccount(pushHistory, orgSlug) {
    if (!S.currentUser) return;
    S.activeView = 'account';
    S.accountExpandSlug = orgSlug || null;
    DOM.homeSection.style.display = 'none';
    DOM.terminalSection.style.display = 'none';
    DOM.chatSection.style.display = 'none';
    DOM.wingDetailSection.style.display = 'none';
    DOM.accountSection.style.display = '';
    detachPTY();
    DOM.headerTitle.textContent = '';
    DOM.ptyStatus.textContent = '';
    renderAccountPage();
    if (pushHistory !== false) {
        var hash = orgSlug ? '#account/' + orgSlug : '#account';
        history.pushState({ view: 'account', orgSlug: orgSlug || null }, '', hash);
    }
}

export function deleteSession(sessionId) {
    var sess = S.sessionsData.find(function(s) { return s.id === sessionId; });
    var wingId = '';
    if (sess) {
        var wing = S.wingsData.find(function(w) { return w.id === sess.wing_id; });
        if (wing) wingId = wing.wing_id;
    }
    var cached = getCachedSessions().filter(function (s) { return s.id !== sessionId; });
    setCachedSessions(cached);
    S.sessionsData = S.sessionsData.filter(function(s) { return s.id !== sessionId; });
    setEggOrder(S.sessionsData.map(function(s) { return s.id; }));
    clearTermBuffer(sessionId);
    delete S.sessionNotifications[sessionId];
    if (S.activeView === 'home') renderDashboard();
    renderSidebar();
    if (wingId) {
        sendTunnelRequest(wingId, { type: 'pty.kill', session_id: sessionId })
            .then(function() { loadHome(); })
            .catch(function() { loadHome(); });
    }
}

// Expose globally for inline onclick handlers
window._deleteSession = deleteSession;

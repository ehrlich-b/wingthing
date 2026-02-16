import '@xterm/xterm/css/xterm.css';
import { S, DOM, initDOM } from './state.js';
import { loginRedirect } from './helpers.js';
import { initTerminal, sendPTYInput } from './terminal.js';
import { showHome, showTerminal, switchToSession, navigateToWingDetail, navigateToAccount } from './nav.js';
import { disconnectPTY, retryReconnect, attachPTY, handlePTYPasskey } from './pty.js';
import { showPalette, hidePalette, cyclePaletteAgent, cyclePaletteWing, navigatePalette, tabCompletePalette, launchFromPalette, debouncedDirList, isDirListPending } from './palette.js';
import { connectAppWS } from './dashboard.js';
import { loadHome } from './data.js';
import { closeAuditOverlay } from './audit.js';
import { hideDetailModal, showSessionInfo, renderSidebar, renderDashboard } from './render.js';
import { initNotifyListeners } from './notify.js';
import { loadTunnelAuthTokens } from './tunnel.js';

async function init() {
    initDOM();
    loadTunnelAuthTokens();

    try {
        var resp = await fetch('/api/app/me');
        if (resp.status === 401) { loginRedirect(); return; }
        S.currentUser = await resp.json();
        DOM.userInfo.textContent = S.currentUser.display_name || 'user';
    } catch (e) { loginRedirect(); return; }

    if (!location.hash.startsWith('#s/') && !location.hash.startsWith('#w/') && !location.hash.startsWith('#account')) {
        history.replaceState({ view: 'home' }, '', location.pathname);
    }

    // Event handlers
    DOM.homeBtn.addEventListener('click', showHome);
    DOM.newSessionBtn.addEventListener('click', showPalette);
    DOM.userInfo.addEventListener('click', function() { navigateToAccount(); });
    DOM.userInfo.style.cursor = 'pointer';
    DOM.headerTitle.addEventListener('click', function() {
        if (S.ptySessionId) showSessionInfo();
    });
    DOM.sessionCloseBtn.addEventListener('click', function() {
        if (DOM.sessionCloseBtn.dataset.confirm) {
            delete DOM.sessionCloseBtn.dataset.confirm;
            DOM.sessionCloseBtn.textContent = 'x';
            disconnectPTY();
            showHome();
        } else {
            DOM.sessionCloseBtn.dataset.confirm = '1';
            DOM.sessionCloseBtn.textContent = 'end session?';
            setTimeout(function() {
                if (DOM.sessionCloseBtn.dataset.confirm) {
                    delete DOM.sessionCloseBtn.dataset.confirm;
                    DOM.sessionCloseBtn.textContent = 'x';
                }
            }, 3000);
        }
    });

    // Reconnect button
    var reconnectBtn = document.getElementById('reconnect-btn');
    if (reconnectBtn) {
        reconnectBtn.addEventListener('click', function() {
            retryReconnect();
        });
    }

    // Passkey button
    var passkeyBtn = document.getElementById('passkey-btn');
    if (passkeyBtn) {
        passkeyBtn.addEventListener('click', function() {
            var overlay = document.getElementById('passkey-overlay');
            if (overlay) overlay.style.display = 'none';
            handlePTYPasskey();
        });
    }

    // Type overlay (mobile text input)
    var typeOverlay = document.getElementById('type-overlay');
    var typeInput = document.getElementById('type-input');
    var typeSend = document.getElementById('type-send');

    function showTypeOverlay() {
        typeOverlay.style.display = 'flex';
        typeInput.value = '';
        typeInput.focus();
    }

    function hideTypeOverlay() {
        typeOverlay.style.display = 'none';
        typeInput.value = '';
        if (S.term) S.term.focus();
    }

    function submitTypeInput() {
        var text = typeInput.value;
        if (text) sendPTYInput(text + '\r');
        hideTypeOverlay();
    }

    typeSend.addEventListener('click', function(e) {
        e.preventDefault();
        submitTypeInput();
    });

    typeInput.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            e.preventDefault();
            submitTypeInput();
        }
        if (e.key === 'Escape') {
            e.preventDefault();
            hideTypeOverlay();
        }
    });

    // Modifier keys (mobile)
    document.querySelectorAll('.mod-key').forEach(function (btn) {
        btn.addEventListener('click', function (e) {
            e.preventDefault();
            var key = btn.dataset.key;
            if (key === 'ctrl') {
                S.ctrlActive = !S.ctrlActive;
                btn.classList.toggle('active', S.ctrlActive);
            } else if (key === 'type') {
                showTypeOverlay();
                return;
            } else if (key === 'esc') {
                sendPTYInput('\x1b');
            } else if (key === 'top') {
                if (S.term) S.term.scrollToTop();
            } else if (key === 'btm') {
                if (S.term) S.term.scrollToBottom();
            }
            var seq = btn.dataset.seq;
            if (seq === '\u2191') sendPTYInput('\x1b[A');
            if (seq === '\u2193') sendPTYInput('\x1b[B');
            if (S.term) S.term.focus();
        });
    });

    // Keyboard shortcuts
    document.addEventListener('keydown', function(e) {
        if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
            e.preventDefault();
            if (DOM.commandPalette.style.display === 'none') showPalette();
            else hidePalette();
        }
        if ((e.key === '.' || e.key === '+') && DOM.commandPalette.style.display === 'none') {
            var tag = document.activeElement && document.activeElement.tagName;
            if (tag !== 'INPUT' && tag !== 'TEXTAREA' && tag !== 'SELECT' && !document.activeElement.closest('#terminal-container, #chat-container')) {
                e.preventDefault();
                showPalette();
            }
        }
        if (e.key === 'Escape' && DOM.commandPalette.style.display !== 'none') {
            hidePalette();
        }
        if ((e.ctrlKey || e.metaKey) && e.key === '.' && S.activeView !== 'home') {
            e.preventDefault();
            showHome();
        }
    });

    // Palette events
    DOM.paletteBackdrop.addEventListener('click', hidePalette);
    DOM.paletteSearch.addEventListener('input', function() {
        debouncedDirList(DOM.paletteSearch.value);
    });
    DOM.paletteSearch.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            e.preventDefault();
            if (isDirListPending()) return;
            var selected = DOM.paletteResults.querySelector('.palette-item.selected');
            if (selected) launchFromPalette(selected.dataset.path);
        }
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            navigatePalette(e.key === 'ArrowDown' ? 1 : -1);
        }
        if (e.key === 'Tab') {
            e.preventDefault();
            if (e.shiftKey) {
                cyclePaletteAgent();
            } else {
                tabCompletePalette();
            }
        }
        if (e.key === '`') {
            e.preventDefault();
            cyclePaletteWing();
        }
    });

    window.addEventListener('resize', function () {
        if (S.term && S.fitAddon) {
            var wasAtBottom = S.term.buffer.active.viewportY >= S.term.buffer.active.baseY;
            S.fitAddon.fit();
            if (wasAtBottom) S.term.scrollToBottom();
        }
    });

    // Resize app to visual viewport when mobile keyboard appears/disappears.
    // Setting height directly on #app overrides the CSS 100dvh and forces
    // the flex layout to fit within the visible area above the keyboard.
    // IMPORTANT: Don't call fitAddon.fit() when the keyboard appears — that
    // changes the terminal row count and causes xterm to reflow all content,
    // which is the source of the scroll-jump jank. Only refit when the
    // viewport returns to full height (keyboard dismissed) or on a real
    // orientation/window resize (handled by the separate resize listener).
    if (window.visualViewport) {
        var appEl = document.getElementById('app');
        var fullHeight = window.visualViewport.height;
        function syncViewport() {
            var vh = window.visualViewport.height;
            appEl.style.height = vh + 'px';
            // iOS scrolls the page when focusing an input — force it back
            window.scrollTo(0, 0);
            // Only refit terminal when keyboard is dismissed (back to full height)
            if (vh >= fullHeight) {
                fullHeight = vh;
                if (S.term && S.fitAddon) {
                    var wasAtBottom = S.term.buffer.active.viewportY >= S.term.buffer.active.baseY;
                    S.fitAddon.fit();
                    if (wasAtBottom) S.term.scrollToBottom();
                }
            }
            if (S.touchProxyScrollToBottom && S.term &&
                S.term.buffer.active.viewportY === S.term.buffer.active.baseY) {
                S.touchProxyScrollToBottom();
            }
        }
        window.visualViewport.addEventListener('resize', syncViewport);
        window.visualViewport.addEventListener('scroll', syncViewport);
    }

    // Detail modal close
    DOM.detailBackdrop.addEventListener('click', hideDetailModal);
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape' && DOM.detailOverlay.classList.contains('open')) {
            e.stopImmediatePropagation();
            hideDetailModal();
        }
    });

    initTerminal();
    initNotifyListeners();
    loadHome();
    setInterval(loadHome, 30000);
    connectAppWS();

    // Deep links
    var hashMatch = location.hash.match(/^#s\/(.+)$/);
    if (hashMatch) {
        var deepSessionId = hashMatch[1];
        history.replaceState({ view: 'terminal', sessionId: deepSessionId }, '', '#s/' + deepSessionId);
        showTerminal();
        attachPTY(deepSessionId);
    }
    var wingMatch = location.hash.match(/^#w\/(.+)$/);
    if (wingMatch) {
        navigateToWingDetail(wingMatch[1]);
    }
    var accountMatch = location.hash.match(/^#account(?:\/(.+))?$/);
    if (accountMatch) {
        navigateToAccount(true, accountMatch[1] || null);
    }
}

// Browser history (back/forward)
window.addEventListener('popstate', function(e) {
    var auditOverlay = document.getElementById('audit-overlay');
    if (auditOverlay && auditOverlay.style.display !== 'none') {
        closeAuditOverlay();
        return;
    }
    var state = e.state;
    if (!state || state.view === 'home') {
        showHome(false);
    } else if (state.view === 'terminal' && state.sessionId) {
        switchToSession(state.sessionId, false);
    } else if (state.view === 'wing-detail' && state.wingId) {
        navigateToWingDetail(state.wingId, false);
    } else if (state.view === 'account') {
        navigateToAccount(false, state.orgSlug || null);
    }
});

if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('sw.js').catch(function () {});
}

init();

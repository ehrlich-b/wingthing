import { S, DOM, TERM_THUMB_PREFIX } from './state.js';
import { escapeHtml, wingDisplayName, shortenPath, projectName, sessionDisplayName, validSessionName, formatRelativeTime, semverCompare, nestedRepoCount, agentIcon, agentWithIcon, dirParent, setupCopyable } from './helpers.js';
import { identityPubKey } from './crypto.js';
import { sendTunnelRequest, tunnelCloseWing } from './tunnel.js';
import { switchToSession, deleteSession } from './nav.js';
import { showHome, navigateToWingDetail, navigateToAccount } from './nav.js';
import { connectPTY } from './pty.js';
import { setLastTermAgent, getLastTermAgent, setWingOrder, setEggOrder, getCachedWingSessions, setCachedWingSessions, probeWing, fetchWingSessions, mergeWingSessions, saveSessionCache } from './data.js';
import { rebuildAgentLists } from './dashboard.js';
import { openAuditReplay, openAuditKeylog, downloadChatHistory } from './audit.js';
import { showTerminal } from './nav.js';
import { safeTerminalThumbnail } from './security.js';
import { shouldFetchWingSessions } from './session-merge.js';
import { updateCanvasSessionName } from './canvas.js';
import { historyResumeState } from './session-resume.js';
import { sessionForkAvailable, sessionForkControl } from './session-fork.js';
import { readSessionContent, notificationForSession } from './session-reference.js';
import { sessionInventoryState, sessionStatusDot, sessionInventoryActions, filterSessionInventory, captureSessionFocus, restoreSessionFocus, navigateSessionRows, findSessionResource, sessionIsSelected, sessionResourceKey, groupSessionInventory, sessionGroupHeader, unseenCompletionBadge, sessionProjectRoot } from './session-inventory.js';
import { refreshConversationInventory } from './conversation-view.js';
import { refreshParentDot } from './parent-dot.js';
import { unseenSessionCompletions, acknowledgeSessionCompletions } from './session-completion.js';
import { browserLocalStorage } from './storage-scope.js';

var inventoryFilters = { query: '', wing: '', agent: '', status: '' };
var sessionStopPending = new Set();
var sessionStopConfirm = new Map();
var sessionActionErrors = new Map();

function sessionWing(session) {
    return S.wingsData.find(function(wing) { return wing.wing_id === session.wing_id; });
}

function renderChannelBanner() {
    var banner = document.getElementById('channel-banner');
    if (!banner) return;
    var preview = S.currentUser && S.currentUser.release_channel === 'preview';
    banner.style.display = preview ? '' : 'none';
    if (preview) banner.textContent = (S.currentUser.channel_label || 'Wingthing Preview') +
        ' · personal preview' + (S.currentUser.version ? ' · ' + S.currentUser.version : '');
}

function wingNameById(wingId) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    return wing ? wingDisplayName(wing) : '';
}

function isWingAccessible(wingId) {
    if (!wingId) return false;
    var w = S.wingsData.find(function(ww) { return ww.wing_id === wingId; });
    return w && w.online !== false && !w.tunnel_error;
}

function isWingVisible(wingId) {
    if (!wingId) return false;
    var w = S.wingsData.find(function(ww) { return ww.wing_id === wingId; });
    return w && w.tunnel_error !== 'not_allowed';
}

function wingHasCapability(wingId, capability) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    return !!wing && Array.isArray(wing.capabilities) && wing.capabilities.indexOf(capability) !== -1;
}

export function renderSidebar() {
    renderChannelBanner();
    refreshParentDot();
    // Live inventory refreshes must not discard an unfinished rename.
    if (DOM.sessionTabs.querySelector('.renaming, .forking')) return;
    var focus = captureSessionFocus(DOM.sessionTabs, document.activeElement);
    var unseen = unseenSessionCompletions(browserLocalStorage(), S.currentUser && S.currentUser.id);
    var sessions = S.sessionsData.filter(function(s) {
        if ((s.kind || 'terminal') === 'chat') return false;
        if (sessionIsSelected(s, S.ptySessionId, S.ptyWingId)) return true;
        return isWingVisible(s.wing_id);
    });
    function renderTab(s) {
        var name = sessionDisplayName(s);
        var letter = name.charAt(0).toUpperCase();
        var isActive = S.activeView === 'terminal' && sessionIsSelected(s, S.ptySessionId, S.ptyWingId);
        var state = sessionInventoryState(s, sessionWing(s), notificationForSession(S.sessionNotifications, s));
        var ownsSession = !!S.currentUser && !!s.user_id && s.user_id === S.currentUser.id;
        var canRename = sessionInventoryActions(s, sessionWing(s), S.currentUser).rename;
        var title = name + ' \u00b7 ' + (s.agent || '?') + ' \u00b7 ' + wingNameById(s.wing_id) + ' \u00b7 ' + state.connectionLabel + ' \u00b7 ' + state.agentLabel;
        if (!ownsSession) title += ' \u00b7 only the session owner can rename';
        else if (!canRename) title += ' \u00b7 update this wing to rename';
        if (unseen.has(sessionResourceKey(s))) title += ' · unseen completion';
        return '<div class="session-tab' + (isActive ? ' active' : '') + '" data-blocked="' + (state.status === 'blocked') + '" role="button" tabindex="0" ' +
            'aria-label="' + escapeHtml(title) + '" ' + (isActive ? 'aria-current="page" ' : '') +
            'title="' + escapeHtml(title) + '" ' +
            'data-sid="' + escapeHtml(s.id) + '" data-wing-id="' + escapeHtml(s.wing_id || '') + '">' +
            sessionStatusDot(state.status, true) +
            '<span class="tab-letter">' + escapeHtml(letter) + '</span>' +
            '<span class="tab-copy"><span class="tab-label">' + escapeHtml(name) + '</span>' +
            '<span class="tab-meta">' + escapeHtml((s.agent || '?') + ' · ' + state.agentLabel) + '</span>' + unseenCompletionBadge(unseen.has(sessionResourceKey(s))) + '</span>' +
            (isActive ? sessionTabActions(s, name, canRename) : '') +
        '</div>';
    }
    // Only the open session carries actions, so rows never change shape or
    // swap controls under the pointer.
    function sessionTabActions(s, name, canRename) {
        var actions = (canRename ? '<button class="session-rename-btn" type="button" data-session-action="rename" aria-label="Rename ' + escapeHtml(name) + '" title="Rename session">rename</button>' : '') +
            sessionForkControl(s, sessionWing(s), S.currentUser);
        return actions ? '<span class="session-tab-actions">' + actions + '</span>' : '';
    }
    var groups = groupSessionInventory(sessions, S.wingsData, S.sessionNotifications, unseen);
    DOM.sessionTabs.innerHTML = groups.map(function(group) {
        return '<section class="inventory-project-group" data-group-key="' + escapeHtml(group.key) + '" data-blocked="' + (group.rollup.blocked > 0) + '">' + sessionGroupHeader(group) + group.sessions.map(renderTab).join('') + '</section>';
    }).join('');
    bindGroupAcknowledgements(DOM.sessionTabs, groups);

    DOM.sessionTabs.querySelectorAll('.session-tab').forEach(function(tab) {
        function openSession() {
            var sid = tab.dataset.sid;
            var s = findSessionResource(S.sessionsData, sid, tab.dataset.wingId);
            if (s && sessionIsSelected(s, S.ptySessionId, S.ptyWingId) && S.activeView === 'terminal') return;
            if (s && !s.swept) return;
            switchToSession(sid, undefined, tab.dataset.wingId);
        }
        tab.addEventListener('click', function(e) {
            if (e.target.closest('button, input, .forking')) return;
            openSession();
        });
        tab.addEventListener('keydown', function(e) {
            if (e.target === tab && navigateSessionRows(e, Array.from(DOM.sessionTabs.querySelectorAll('.session-tab')), tab)) return;
            if ((e.key === 'Enter' || e.key === ' ') && e.target === tab) {
                e.preventDefault();
                openSession();
            }
        });
        var rename = tab.querySelector('.session-rename-btn');
        var fork = tab.querySelector('.session-fork-btn');
        if (fork) fork.addEventListener('click', function(e) {
            e.stopPropagation();
            var session = findSessionResource(S.sessionsData, tab.dataset.sid, tab.dataset.wingId);
            if (session) beginSessionFork(tab, session, session.wing_id, fork);
        });
        if (rename) {
            rename.addEventListener('click', function(e) {
                e.stopPropagation();
                var session = findSessionResource(S.sessionsData, tab.dataset.sid, tab.dataset.wingId);
                if (session) beginSessionRename(tab, session);
            });
        }
    });
    restoreSessionFocus(DOM.sessionTabs, focus);
}

function beginSessionFork(container, session, wingId, button) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!sessionForkAvailable(session, wing, S.currentUser) || container.classList.contains('forking')) return;
    container.classList.add('forking');
    var form = document.createElement('form');
    form.className = 'session-fork-form';
    var hint = document.createElement('span');
    hint.className = 'session-fork-hint';
    hint.textContent = 'new session from a copy of this conversation; this one keeps running';
    var input = document.createElement('input');
    input.className = 'session-name-input';
    input.setAttribute('aria-label', 'New session name');
    input.maxLength = 64;
    input.value = ((session.name || 'session').slice(0, 59) + '-fork');
    var submit = document.createElement('button');
    submit.type = 'submit';
    submit.className = 'btn-sm';
    submit.textContent = 'fork';
    var cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'btn-sm';
    cancel.textContent = 'cancel';
    var status = document.createElement('span');
    status.setAttribute('role', 'status');
    form.append(hint, input, submit, cancel, status);
    button.hidden = true;
    button.after(form);
    cancel.addEventListener('click', function() {
        form.remove();
        container.classList.remove('forking');
        button.hidden = false;
        button.focus();
    });
    form.addEventListener('submit', async function(event) {
        event.preventDefault();
        if (submit.disabled) return;
        if (!input.value || !validSessionName(input.value)) {
            status.textContent = 'Use a name of up to 64 letters, numbers, dots, underscores or dashes.';
            return;
        }
        submit.disabled = true;
        cancel.disabled = true;
        input.disabled = true;
        status.textContent = 'forking…';
        var sourceId = session.id || session.session_id;
        try {
            var result = await sendTunnelRequest(wingId, { type: 'session.control', operation: 'session_fork', arguments: { session: sourceId, name: input.value } });
            if (!result.session || result.session === sourceId || result.source_session !== sourceId) throw new Error('Fork was not confirmed');
            S.sessionsData.push({ id: result.session, name: result.label, agent: result.agent, cwd: result.cwd, wing_id: wingId, user_id: session.user_id, status: 'detached', swept: true,
                conversation_id: result.conversation_id, root_conversation_id: result.root_conversation_id, parent_conversation_id: result.parent_conversation_id });
            container.classList.remove('forking');
            saveSessionCache();
            renderSidebar();
            refreshConversationInventory();
            showTerminal();
            switchToSession(result.session, undefined, wingId);
        } catch (err) {
            status.textContent = (err && err.message) || 'Fork failed';
            submit.disabled = false;
            cancel.disabled = false;
            input.disabled = false;
        }
    });
    input.focus();
    input.select();
}

function beginSessionRename(tab, session) {
    if (tab.classList.contains('renaming')) return;
    tab.classList.add('renaming');
    var label = tab.querySelector('.tab-label');
    var input = document.createElement('input');
    input.className = 'session-name-input';
    input.type = 'text';
    input.maxLength = 64;
    input.value = session.name || '';
    input.placeholder = projectName(session.cwd);
    input.setAttribute('aria-label', 'Session name');
    label.replaceWith(input);
    var status = document.createElement('span');
    status.className = 'session-rename-status';
    status.setAttribute('role', 'status');
    input.insertAdjacentElement('afterend', status);
    input.focus();
    input.select();

    var settled = false;
    function cancel() {
        if (settled) return;
        settled = true;
        tab.classList.remove('renaming');
        renderSidebar();
        if (S.activeView === 'home') renderSessionInventory();
    }
    function save() {
        if (settled) return;
        var name = input.value.trim();
        if (!name) { cancel(); return; }
        if (!validSessionName(name)) {
            input.classList.add('invalid');
            input.title = "Use up to 64 letters, numbers, '.', '_', or '-'";
            status.textContent = 'letters, numbers, . _ or - only, no spaces';
            input.focus();
            return;
        }
        settled = true;
        input.disabled = true;
        sendTunnelRequest(session.wing_id, { type: 'sessions.rename', session_id: session.id, name: name })
            .then(function(result) {
                session.name = (result && result.name) || name;
                saveSessionCache();
                updateCanvasSessionName(session.id, session.name, session.wing_id);
                if (sessionIsSelected(session, S.ptySessionId, S.ptyWingId) && S.activeView === 'terminal') {
                    DOM.headerTitle.textContent = session.name + ' \u00b7 ' + (session.agent || '?');
                }
                tab.classList.remove('renaming');
                renderSidebar();
                if (S.activeView === 'home') renderDashboard();
            })
            .catch(function(err) {
                settled = false;
                input.disabled = false;
                input.classList.add('invalid');
                var message = (err && err.message) || 'rename failed';
                input.title = message;
                input.setAttribute('aria-invalid', 'true');
                status.textContent = message;
                input.focus();
                input.select();
            });
    }
    input.addEventListener('click', function(e) { e.stopPropagation(); });
    input.addEventListener('keydown', function(e) {
        e.stopPropagation();
        if (e.key === 'Enter') { e.preventDefault(); save(); }
        if (e.key === 'Escape') { e.preventDefault(); cancel(); }
    });
    input.addEventListener('blur', save);
}

export function setupWingDrag() {
    var grid = DOM.wingStatusEl.querySelector('.wing-grid');
    if (!grid) return;
    var cards = grid.querySelectorAll('.wing-box');
    var dragSrc = null;

    cards.forEach(function(card) {
        card.addEventListener('dragstart', function(e) {
            dragSrc = card;
            card.classList.add('dragging');
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/plain', card.dataset.wingId);
        });
        card.addEventListener('dragend', function() {
            card.classList.remove('dragging');
            cards.forEach(function(c) { c.classList.remove('drag-over'); });
            dragSrc = null;
        });
        card.addEventListener('dragover', function(e) {
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
            if (card !== dragSrc) {
                cards.forEach(function(c) { c.classList.remove('drag-over'); });
                card.classList.add('drag-over');
            }
        });
        card.addEventListener('dragleave', function() {
            card.classList.remove('drag-over');
        });
        card.addEventListener('drop', function(e) {
            e.preventDefault();
            card.classList.remove('drag-over');
            if (!dragSrc || dragSrc === card) return;
            if (dragSrc.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING) {
                grid.insertBefore(dragSrc, card.nextSibling);
            } else {
                grid.insertBefore(dragSrc, card);
            }
            saveWingOrder();
        });
    });

    // Touch drag (mobile)
    var touchSrc = null;

    cards.forEach(function(card) {
        card.addEventListener('touchstart', function(e) {
            if (e.target.closest('.wing-update-btn')) return;
            touchSrc = card;
            card.classList.add('dragging');
        }, { passive: true });
    });

    grid.addEventListener('touchmove', function(e) {
        if (!touchSrc) return;
        e.preventDefault();
        var touch = e.touches[0];
        var target = document.elementFromPoint(touch.clientX, touch.clientY);
        var targetCard = target ? target.closest('.wing-box') : null;
        cards.forEach(function(c) { c.classList.remove('drag-over'); });
        if (targetCard && targetCard !== touchSrc) {
            targetCard.classList.add('drag-over');
        }
    }, { passive: false });

    grid.addEventListener('touchend', function(e) {
        if (!touchSrc) return;
        var touch = e.changedTouches[0];
        var target = document.elementFromPoint(touch.clientX, touch.clientY);
        var targetCard = target ? target.closest('.wing-box') : null;
        cards.forEach(function(c) { c.classList.remove('drag-over'); });
        touchSrc.classList.remove('dragging');
        if (targetCard && targetCard !== touchSrc) {
            if (touchSrc.compareDocumentPosition(targetCard) & Node.DOCUMENT_POSITION_FOLLOWING) {
                grid.insertBefore(touchSrc, targetCard.nextSibling);
            } else {
                grid.insertBefore(touchSrc, targetCard);
            }
            saveWingOrder();
        }
        touchSrc = null;
    }, { passive: true });
}

function saveWingOrder() {
    var order = [];
    DOM.wingStatusEl.querySelectorAll('.wing-box').forEach(function(card) {
        if (card.dataset.wingId) order.push(card.dataset.wingId);
    });
    setWingOrder(order);
    var byWing = {};
    S.wingsData.forEach(function(w) { byWing[w.wing_id] = w; });
    var reordered = [];
    order.forEach(function(mid) { if (byWing[mid]) reordered.push(byWing[mid]); });
    S.wingsData.forEach(function(w) { if (order.indexOf(w.wing_id) === -1) reordered.push(w); });
    S.wingsData = reordered;
}

export function setupEggDrag() {
    var grids = DOM.sessionsList.querySelectorAll('.egg-grid');
    if (!grids.length) return;

    var isTouch = 'ontouchstart' in window || navigator.maxTouchPoints > 0;

    grids.forEach(function(grid) {
        var cards = grid.querySelectorAll('.egg-box');
        var dragSrc = null;
        var touchSrc = null;
        var touchTimer = null;

        cards.forEach(function(card) {
            if (!isTouch) card.setAttribute('draggable', 'true');
            card.addEventListener('dragstart', function(e) {
                dragSrc = card;
                card.classList.add('dragging');
                e.dataTransfer.effectAllowed = 'move';
                e.dataTransfer.setData('text/plain', card.dataset.sid);
            });
            card.addEventListener('dragend', function() {
                card.classList.remove('dragging');
                cards.forEach(function(c) { c.classList.remove('drag-over'); });
                dragSrc = null;
            });
            card.addEventListener('dragover', function(e) {
                e.preventDefault();
                e.dataTransfer.dropEffect = 'move';
                if (card !== dragSrc) {
                    cards.forEach(function(c) { c.classList.remove('drag-over'); });
                    card.classList.add('drag-over');
                }
            });
            card.addEventListener('dragleave', function() {
                card.classList.remove('drag-over');
            });
            card.addEventListener('drop', function(e) {
                e.preventDefault();
                card.classList.remove('drag-over');
                if (!dragSrc || dragSrc === card) return;
                if (dragSrc.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING) {
                    grid.insertBefore(dragSrc, card.nextSibling);
                } else {
                    grid.insertBefore(dragSrc, card);
                }
                saveEggOrder();
            });
        });

        // Touch drag (mobile) — requires long press (400ms) to start dragging.
        // Short taps pass through to the click handler for opening sessions.
        cards.forEach(function(card) {
            card.addEventListener('touchstart', function(e) {
                if (e.target.closest('button, input')) return;
                touchTimer = setTimeout(function() {
                    touchSrc = card;
                    card.classList.add('dragging');
                }, 400);
            }, { passive: true });
            card.addEventListener('touchmove', function() {
                if (touchTimer) { clearTimeout(touchTimer); touchTimer = null; }
            }, { passive: true });
            card.addEventListener('touchend', function() {
                if (touchTimer) { clearTimeout(touchTimer); touchTimer = null; }
            }, { passive: true });
        });

        grid.addEventListener('touchmove', function(e) {
            if (!touchSrc) return;
            e.preventDefault();
            var touch = e.touches[0];
            var target = document.elementFromPoint(touch.clientX, touch.clientY);
            var targetCard = target ? target.closest('.egg-box') : null;
            cards.forEach(function(c) { c.classList.remove('drag-over'); });
            if (targetCard && targetCard.parentNode === grid && targetCard !== touchSrc) {
                targetCard.classList.add('drag-over');
            }
        }, { passive: false });

        function clearTouchDrag() {
            if (touchTimer) { clearTimeout(touchTimer); touchTimer = null; }
            cards.forEach(function(c) { c.classList.remove('drag-over'); });
            if (touchSrc) touchSrc.classList.remove('dragging');
            touchSrc = null;
        }

        grid.addEventListener('touchend', function(e) {
            if (!touchSrc) return;
            try {
                var touch = e.changedTouches[0];
                var target = document.elementFromPoint(touch.clientX, touch.clientY);
                var targetCard = target ? target.closest('.egg-box') : null;
                if (touchSrc.parentNode === grid && targetCard && targetCard.parentNode === grid && targetCard !== touchSrc) {
                    if (touchSrc.compareDocumentPosition(targetCard) & Node.DOCUMENT_POSITION_FOLLOWING) {
                        grid.insertBefore(touchSrc, targetCard.nextSibling);
                    } else {
                        grid.insertBefore(touchSrc, targetCard);
                    }
                    saveEggOrder();
                }
            } finally {
                clearTouchDrag();
            }
        }, { passive: true });
        grid.addEventListener('touchcancel', clearTouchDrag, { passive: true });
    });
}

function saveEggOrder() {
    var order = [];
    DOM.sessionsList.querySelectorAll('.egg-box').forEach(function(card) {
        if (card.dataset.sid) order.push(sessionResourceKey({ id: card.dataset.sid, wing_id: card.dataset.wingId }));
    });
    setEggOrder(order);
    var byId = new Map();
    S.sessionsData.forEach(function(s) { byId.set(sessionResourceKey(s), s); });
    var reordered = [];
    order.forEach(function(key) { if (byId.has(key)) reordered.push(byId.get(key)); });
    S.sessionsData.forEach(function(s) { if (order.indexOf(sessionResourceKey(s)) === -1) reordered.push(s); });
    S.sessionsData = reordered;
}

export function renderAccountPage() {
    var tier = S.currentUser.tier || 'free';
    var email = S.currentUser.email || '';
    var provider = S.currentUser.provider || '';
    var pubKeyShort = identityPubKey ? identityPubKey.substring(0, 16) + '...' : 'none';

    var html = '<div class="ac-page">' +
        '<div class="wd-header"><a class="wd-back" id="ac-back">back</a></div>' +
        '<div class="ac-hero">' + escapeHtml(S.currentUser.display_name || 'user') + '</div>' +
        '<div class="wd-info">' +
            (email ? '<div class="detail-row"><span class="detail-key">email</span><span class="detail-val">' + escapeHtml(email) + '</span></div>' : '') +
            '<div class="detail-row"><span class="detail-key">login</span><span class="detail-val">' + escapeHtml(provider) + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">tier</span><span class="detail-val">' + escapeHtml(tier) + '</span></div>' +
            (S.currentUser.id ? '<div class="detail-row"><span class="detail-key">user id</span><span class="detail-val copyable" data-copy="' + escapeHtml(S.currentUser.id) + '">' + escapeHtml(S.currentUser.id) + '</span></div>' : '') +
            '<div class="detail-row"><span class="detail-key">browser key</span><span class="detail-val copyable" data-copy="' + escapeHtml(identityPubKey) + '">' + escapeHtml(pubKeyShort) + '</span></div>' +
        '</div>' +
        '<div class="ac-actions">';

    if (!S.currentUser.roost_mode) {
        if (tier === 'free') {
            if (S.currentUser.self_service_plans) {
                html += '<button class="btn-sm btn-accent" id="account-upgrade">give me pro</button>';
            } else {
                html += '<span class="text-dim" id="account-plan-note" style="font-size:12px">free uses direct control. hosted relay is provisioned separately.</span>';
            }
        } else if (S.currentUser.personal_pro) {
            html += '<button class="btn-sm" id="account-downgrade" style="color:var(--text-dim)">cancel pro</button>';
        } else {
            html += '<span class="text-dim" style="font-size:12px">pro via org</span>';
        }
    }
    html += '<button class="btn-sm btn-danger" id="account-logout">log out</button>';
    html += '</div>';

    // Passkeys section
    html += '<div class="ac-section">' +
        '<div class="ac-section-header">' +
            '<h3>passkeys</h3>' +
            '<button class="ac-create-btn" id="ac-passkey-add" title="register passkey">+</button>' +
        '</div>' +
        '<div id="ac-passkey-list"><span class="text-dim">loading...</span></div>' +
    '</div>';

    // ntfy push notifications section
    html += '<div class="ac-section">' +
        '<div class="ac-section-header">' +
            '<h3>push notifications</h3>' +
        '</div>' +
        '<div id="ac-ntfy-content"><span class="text-dim">loading...</span></div>' +
    '</div>';

    // Org section (hidden in roost mode)
    if (!S.currentUser.roost_mode) {
        html += '<div class="ac-section">' +
            '<div class="ac-section-header">' +
                '<h3>organizations</h3>' +
                '<button class="ac-create-btn" id="ac-create-toggle" title="create org">+</button>' +
            '</div>' +
            '<div id="ac-create-form" class="ac-create-form" style="display:none;">' +
                '<input type="text" class="ac-input" id="ac-create-name" placeholder="team name">' +
                '<button class="btn-sm btn-accent" id="ac-create-btn">create</button>' +
            '</div>' +
            '<div id="ac-create-error" class="ac-error" style="display:none;"></div>' +
            '<div id="ac-org-list" class="ac-org-list"><span class="text-dim">loading...</span></div>' +
        '</div>';
    }

    html += '</div>';
    DOM.accountContent.innerHTML = html;

    document.getElementById('ac-back').addEventListener('click', function() { showHome(); });

    var upgradeBtn = document.getElementById('account-upgrade');
    if (upgradeBtn) {
        upgradeBtn.addEventListener('click', function() {
            upgradeBtn.textContent = 'upgrading...';
            upgradeBtn.disabled = true;
            fetch('/api/app/upgrade', { method: 'POST' })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.tier) S.currentUser.tier = data.tier;
                    upgradeBtn.textContent = 'done — you are pro';
                })
                .catch(function() { upgradeBtn.textContent = 'failed'; upgradeBtn.disabled = false; });
        });
    }
    var downgradeBtn = document.getElementById('account-downgrade');
    if (downgradeBtn) {
        downgradeBtn.addEventListener('click', function() {
            downgradeBtn.textContent = 'canceling...';
            downgradeBtn.disabled = true;
            fetch('/api/app/downgrade', { method: 'POST' })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.tier) S.currentUser.tier = data.tier;
                    downgradeBtn.textContent = 'done — ' + (data.tier || 'free');
                })
                .catch(function() { downgradeBtn.textContent = 'failed'; downgradeBtn.disabled = false; });
        });
    }

    document.getElementById('account-logout').addEventListener('click', function() {
        fetch('/auth/logout', { method: 'POST' }).then(function() {
            window.location.href = '/';
        });
    });

    var createToggle = document.getElementById('ac-create-toggle');
    if (createToggle) {
        var createForm = document.getElementById('ac-create-form');
        createToggle.addEventListener('click', function() {
            createForm.style.display = createForm.style.display === 'none' ? '' : 'none';
            if (createForm.style.display !== 'none') {
                document.getElementById('ac-create-name').focus();
            }
        });

        document.getElementById('ac-create-btn').addEventListener('click', function() {
            var btn = this;
            var nameInput = document.getElementById('ac-create-name');
            var errEl = document.getElementById('ac-create-error');
            var name = nameInput.value.trim();
            if (!name) return;
            btn.textContent = 'creating...';
            btn.disabled = true;
            errEl.style.display = 'none';
            fetch('/api/orgs', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ name: name })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) {
                    errEl.textContent = data.error;
                    errEl.style.display = '';
                    btn.textContent = 'create';
                    btn.disabled = false;
                    return;
                }
                nameInput.value = '';
                createForm.style.display = 'none';
                errEl.style.display = 'none';
                btn.textContent = 'create';
                btn.disabled = false;
                loadAccountOrgs();
            })
            .catch(function() {
                btn.textContent = 'create';
                btn.disabled = false;
                errEl.textContent = 'request failed';
                errEl.style.display = '';
            });
        });

        loadAccountOrgs();
    }
    loadAccountPasskeys();
    loadNtfyConfig();

    document.getElementById('ac-passkey-add').addEventListener('click', function() {
        var btn = this;
        btn.disabled = true;
        btn.textContent = '...';
        fetch('/api/app/passkey/register/begin', { method: 'POST' })
            .then(function(r) { return r.json(); })
            .then(function(options) {
                options.publicKey.challenge = Uint8Array.from(atob(options.publicKey.challenge.replace(/-/g,'+').replace(/_/g,'/')), function(c) { return c.charCodeAt(0); });
                options.publicKey.user.id = Uint8Array.from(atob(options.publicKey.user.id.replace(/-/g,'+').replace(/_/g,'/')), function(c) { return c.charCodeAt(0); });
                if (options.publicKey.excludeCredentials) {
                    options.publicKey.excludeCredentials = options.publicKey.excludeCredentials.map(function(c) {
                        c.id = Uint8Array.from(atob(c.id.replace(/-/g,'+').replace(/_/g,'/')), function(ch) { return ch.charCodeAt(0); });
                        return c;
                    });
                }
                return navigator.credentials.create(options);
            })
            .then(function(cred) {
                function toB64url(buf) {
                    return btoa(String.fromCharCode.apply(null, new Uint8Array(buf)))
                        .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
                }
                var body = {
                    id: cred.id,
                    rawId: toB64url(cred.rawId),
                    type: cred.type,
                    response: {
                        attestationObject: toB64url(cred.response.attestationObject),
                        clientDataJSON: toB64url(cred.response.clientDataJSON)
                    }
                };
                return fetch('/api/app/passkey/register/finish', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body)
                });
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) throw new Error(data.error);
                btn.textContent = '+';
                btn.disabled = false;
                if (S.currentUser) S.currentUser.has_passkeys = true;
                loadAccountPasskeys();
            })
            .catch(function(e) {
                console.error('passkey registration failed:', e);
                btn.textContent = '+';
                btn.disabled = false;
            });
    });
}

function loadAccountPasskeys() {
    var listEl = document.getElementById('ac-passkey-list');
    if (!listEl) return;

    fetch('/api/app/passkey')
        .then(function(r) { return r.json(); })
        .then(function(creds) {
            if (!creds || creds.length === 0) {
                listEl.innerHTML = '<span class="text-dim">no passkeys registered</span>';
                return;
            }
            listEl.innerHTML = creds.map(function(c) {
                var keyShort = c.public_key ? c.public_key.substring(0, 16) + '...' : '';
                var created = c.created_at ? formatRelativeTime(new Date(c.created_at)) : '';
                return '<div class="ac-passkey-row">' +
                    '<span class="ac-passkey-label">' + escapeHtml(c.label || 'passkey') + '</span>' +
                    '<span class="ac-passkey-meta text-dim">' + escapeHtml(keyShort) + (created ? ' &middot; ' + created : '') + '</span>' +
                    '<button class="btn-sm btn-danger ac-passkey-del" data-id="' + escapeHtml(c.id) + '">remove</button>' +
                '</div>';
            }).join('');

            listEl.querySelectorAll('.ac-passkey-del').forEach(function(btn) {
                btn.addEventListener('click', function() {
                    var id = this.getAttribute('data-id');
                    if (this.classList.contains('btn-armed')) {
                        this.textContent = '...';
                        fetch('/api/app/passkey/' + id, { method: 'DELETE' })
                            .then(function() { loadAccountPasskeys(); })
                            .catch(function() { loadAccountPasskeys(); });
                    } else {
                        this.classList.add('btn-armed');
                        this.textContent = 'confirm';
                        var el = this;
                        setTimeout(function() {
                            el.classList.remove('btn-armed');
                            el.textContent = 'remove';
                        }, 3000);
                    }
                });
            });
        })
        .catch(function() {
            listEl.innerHTML = '<span class="text-dim">failed to load</span>';
        });
}

function loadNtfyConfig() {
    var el = document.getElementById('ac-ntfy-content');
    if (!el) return;

    fetch('/api/app/ntfy')
        .then(function(r) { return r.json(); })
        .then(function(cfg) {
            if (cfg.enabled) {
                // Configured: just show status + disable. Topic is never shown again.
                el.innerHTML =
                    '<div style="display:flex;align-items:center;gap:12px;">' +
                        '<span style="color:var(--text);">enabled</span>' +
                        '<button class="btn-sm btn-danger" id="ac-ntfy-disable">disable</button>' +
                        '<button class="btn-sm" id="ac-ntfy-test">send test</button>' +
                    '</div>';
                document.getElementById('ac-ntfy-disable').addEventListener('click', function() {
                    var btn = this;
                    if (btn.classList.contains('btn-armed')) {
                        btn.textContent = '...';
                        fetch('/api/app/ntfy', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ topic: '', token: '', events: '' })
                        }).then(function() { loadNtfyConfig(); });
                    } else {
                        btn.classList.add('btn-armed');
                        btn.textContent = 'confirm';
                        setTimeout(function() {
                            btn.classList.remove('btn-armed');
                            btn.textContent = 'disable';
                        }, 3000);
                    }
                });
                document.getElementById('ac-ntfy-test').addEventListener('click', function() {
                    var btn = this;
                    btn.textContent = '...';
                    btn.disabled = true;
                    fetch('/api/app/ntfy/test', { method: 'POST' })
                        .then(function(r) { return r.json(); })
                        .then(function(data) {
                            btn.textContent = data.ok ? 'sent!' : 'failed';
                            setTimeout(function() { btn.textContent = 'send test'; btn.disabled = false; }, 2000);
                        })
                        .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
                });
            } else {
                // Not configured: one-line explainer + enable button
                el.innerHTML =
                    '<div style="display:flex;align-items:center;gap:12px;">' +
                        '<span class="text-dim">get notified on your phone when agents need you</span>' +
                        '<button class="btn-sm btn-accent" id="ac-ntfy-enable">enable</button>' +
                    '</div>';
                document.getElementById('ac-ntfy-enable').addEventListener('click', function() {
                    ntfyWizardStep1(el);
                });
            }
        })
        .catch(function() {
            el.innerHTML = '<span class="text-dim">failed to load</span>';
        });
}

// --- ntfy setup wizard ---
// Step 1: Install the ntfy app on your phone
function ntfyWizardStep1(container) {
    container.innerHTML =
        '<div class="ac-ntfy-wizard">' +
            '<div class="ac-ntfy-step">step 1 of 3</div>' +
            '<div style="margin:8px 0 12px;">install the <strong>ntfy</strong> app on your phone</div>' +
            '<div style="display:flex;gap:8px;margin-bottom:12px;">' +
                '<a href="https://play.google.com/store/apps/details?id=io.heckel.ntfy" target="_blank" rel="noopener noreferrer" class="btn-sm btn-accent">android</a>' +
                '<a href="https://apps.apple.com/us/app/ntfy/id1625396347" target="_blank" rel="noopener noreferrer" class="btn-sm btn-accent">iOS</a>' +
            '</div>' +
            '<div style="display:flex;gap:8px;">' +
                '<button class="btn-sm btn-accent" id="ac-ntfy-next1">done, next</button>' +
                '<button class="btn-sm" id="ac-ntfy-cancel1">cancel</button>' +
            '</div>' +
        '</div>';
    document.getElementById('ac-ntfy-next1').addEventListener('click', function() {
        ntfyWizardStep2(container);
    });
    document.getElementById('ac-ntfy-cancel1').addEventListener('click', function() {
        loadNtfyConfig();
    });
}

// Step 2: Generate your topic (or bring your own reserved topic)
function ntfyWizardStep2(container) {
    container.innerHTML =
        '<div class="ac-ntfy-wizard">' +
            '<div class="ac-ntfy-step">step 2 of 3</div>' +
            '<div style="margin:8px 0 4px;">generate a private notification channel</div>' +
            '<div class="text-dim" style="font-size:11px;margin-bottom:12px;">' +
                'this creates a random topic name on ntfy.sh — the name is the secret, so don\'t share it. ' +
                'if you have a <a href="https://ntfy.sh/#pricing" target="_blank" rel="noopener noreferrer" style="color:var(--accent)">paid ntfy account</a> with a reserved topic, you can enter it instead.' +
            '</div>' +
            '<div id="ac-ntfy-topic-area">' +
                '<button class="btn-sm btn-accent" id="ac-ntfy-gen">generate topic</button>' +
            '</div>' +
            '<div id="ac-ntfy-reserved" style="margin-top:8px;">' +
                '<span id="ac-ntfy-show-reserved" style="font-size:11px;color:var(--text-dim);cursor:pointer;text-decoration:underline;">or enter a reserved topic</span>' +
            '</div>' +
            '<div style="display:flex;gap:8px;margin-top:12px;">' +
                '<button class="btn-sm" id="ac-ntfy-cancel2">cancel</button>' +
            '</div>' +
        '</div>';

    document.getElementById('ac-ntfy-gen').addEventListener('click', function() {
        var btn = this;
        btn.textContent = 'generating...';
        btn.disabled = true;
        fetch('/api/app/ntfy/generate', { method: 'POST' })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                // Save immediately and go to step 3
                return fetch('/api/app/ntfy', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ topic: data.topic, token: '', events: 'attention,exit' })
                }).then(function() { return data.topic; });
            })
            .then(function(topic) {
                ntfyWizardStep3(container, topic);
            })
            .catch(function() { btn.textContent = 'failed — try again'; btn.disabled = false; });
    });

    function expandReservedTopic() {
        var area = document.getElementById('ac-ntfy-reserved');
        area.innerHTML =
            '<div style="margin-top:4px;">' +
                '<input type="text" class="ac-input" id="ac-ntfy-custom-topic" placeholder="your reserved topic" style="width:220px;">' +
                '<input type="password" class="ac-input" id="ac-ntfy-custom-token" placeholder="access token" style="width:220px;margin-top:4px;">' +
                '<div style="display:flex;gap:8px;margin-top:4px;">' +
                    '<button class="btn-sm btn-accent" id="ac-ntfy-custom-save">save</button>' +
                    '<button class="btn-sm" id="ac-ntfy-custom-cancel">cancel</button>' +
                '</div>' +
            '</div>';
        document.getElementById('ac-ntfy-custom-save').addEventListener('click', function() {
            var topic = document.getElementById('ac-ntfy-custom-topic').value.trim();
            var token = document.getElementById('ac-ntfy-custom-token').value.trim();
            if (!topic) return;
            var btn = this;
            btn.textContent = 'saving...';
            btn.disabled = true;
            fetch('/api/app/ntfy', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ topic: topic, token: token, events: 'attention,exit' })
            })
            .then(function() { ntfyWizardStep3(container, topic); })
            .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
        });
        document.getElementById('ac-ntfy-custom-cancel').addEventListener('click', function() {
            area.innerHTML =
                '<span id="ac-ntfy-show-reserved" style="font-size:11px;color:var(--text-dim);cursor:pointer;text-decoration:underline;">or enter a reserved topic</span>';
            document.getElementById('ac-ntfy-show-reserved').addEventListener('click', expandReservedTopic);
        });
    }
    document.getElementById('ac-ntfy-show-reserved').addEventListener('click', expandReservedTopic);

    document.getElementById('ac-ntfy-cancel2').addEventListener('click', function() {
        loadNtfyConfig();
    });
}

// Step 3: Subscribe to the topic in the ntfy app — this is the only time we show the topic
function ntfyWizardStep3(container, topic) {
    container.innerHTML =
        '<div class="ac-ntfy-wizard">' +
            '<div class="ac-ntfy-step">step 3 of 3</div>' +
            '<div style="margin:8px 0 4px;">subscribe to this topic in the ntfy app</div>' +
            '<div style="margin:8px 0;padding:8px 12px;background:var(--bg-dim);border-radius:4px;font-family:monospace;font-size:13px;user-select:all;cursor:text;">' +
                escapeHtml(topic) +
            '</div>' +
            '<div class="text-dim" style="font-size:11px;margin-bottom:12px;">' +
                'open ntfy on your phone, tap <strong>+</strong>, paste this topic, and subscribe. ' +
                'this is the only time we\'ll show it — if you lose it, disable and set up again.' +
            '</div>' +
            '<div style="display:flex;gap:8px;">' +
                '<button class="btn-sm btn-accent" id="ac-ntfy-done">done</button>' +
                '<button class="btn-sm" id="ac-ntfy-test3">send test notification</button>' +
            '</div>' +
        '</div>';

    document.getElementById('ac-ntfy-done').addEventListener('click', function() {
        loadNtfyConfig();
    });
    document.getElementById('ac-ntfy-test3').addEventListener('click', function() {
        var btn = this;
        btn.textContent = '...';
        btn.disabled = true;
        fetch('/api/app/ntfy/test', { method: 'POST' })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                btn.textContent = data.ok ? 'sent!' : 'failed';
                setTimeout(function() { btn.textContent = 'send test notification'; btn.disabled = false; }, 2000);
            })
            .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
    });
}

function loadAccountOrgs() {
    var listEl = document.getElementById('ac-org-list');
    if (!listEl) return;

    fetch('/api/orgs')
        .then(function(r) { return r.json(); })
        .then(function(orgs) {
            if (!orgs || orgs.length === 0) {
                listEl.innerHTML = '<span class="text-dim">no organizations yet</span>';
                return;
            }
            var html = '';
            for (var i = 0; i < orgs.length; i++) {
                html += renderOrgCard(orgs[i]);
            }
            listEl.innerHTML = html;
            wireOrgCards(orgs);
            if (S.accountExpandSlug) {
                expandOrgCard(S.accountExpandSlug, orgs, false);
                S.accountExpandSlug = null;
            }
        })
        .catch(function() {
            listEl.innerHTML = '<span class="text-dim">failed to load orgs</span>';
        });
}

function renderOrgCard(org) {
    var roleLabel = org.is_owner ? 'owner' : 'member';
    var memberCount = org.member_count || 0;
    return '<div class="ac-org-card" data-oid="' + escapeHtml(org.id) + '">' +
        '<div class="ac-org-header" data-oid="' + escapeHtml(org.id) + '">' +
            '<span class="ac-org-name">' + escapeHtml(org.name) + '</span>' +
            '<span class="ac-org-role">' + roleLabel + '</span>' +
            '<span class="ac-org-count">' + memberCount + (memberCount === 1 ? ' member' : ' members') + '</span>' +
        '</div>' +
        '<div class="ac-org-detail" id="ac-org-detail-' + escapeHtml(org.id) + '">' +
            renderOrgDetail(org) +
        '</div>' +
    '</div>';
}

function renderOrgDetail(org) {
    var oid = escapeHtml(org.id);
    var html = '';

    if (!org.is_owner) {
        html += '<div class="detail-row"><span class="detail-val text-dim">you are a member of this org</span></div>';
        html += '<div class="ac-cancel-row"><button class="btn-sm btn-danger org-leave-btn" data-oid="' + oid + '">leave org</button></div>';
        return html;
    }

    if (!org.has_subscription) {
        html += '<div class="detail-row"><span class="detail-val text-dim">no active hosted relay plan</span></div>';
        if (S.currentUser.self_service_plans) {
            html += '<div class="ac-form-row">' +
                    '<input type="number" class="ac-input ac-input-sm" id="org-seats-input-' + oid + '" min="1" value="5">' +
                    '<span class="ac-hint">seats</span>' +
                    '<div class="ac-plan-toggle" id="org-plan-toggle-' + oid + '">' +
                        '<button class="ac-plan-opt active" data-plan="team_yearly">yearly</button>' +
                        '<button class="ac-plan-opt" data-plan="team_monthly">monthly</button>' +
                    '</div>' +
                    '<button class="btn-sm btn-accent org-give-seats-btn" data-oid="' + oid + '">give me seats</button>' +
                '</div>' +
                '<div class="ac-hint" style="margin-top:4px">1 seat includes you. each additional seat adds one team member.</div>';
        } else {
            html += '<div class="ac-hint">organizations can use direct control; hosted relay seats are provisioned separately.</div>';
        }
        html += '<div class="ac-cancel-row"><button class="btn-sm org-delete-btn" data-oid="' + oid + '">delete org</button></div>';
        return html;
    }

    html += '<div class="detail-row"><span class="detail-key">plan</span><span class="detail-val">' + escapeHtml(org.plan || 'team') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">seats</span><span class="detail-val">' + (org.seats_used || 0) + '/' + (org.seats_total || 0) + ' used</span></div>';

    if (S.currentUser.self_service_plans) {
        html += '<div class="ac-form-row">' +
            '<input type="number" class="ac-input ac-input-sm" id="org-add-seats-input-' + oid + '" min="' + ((org.seats_total || 0) + 1) + '" value="' + ((org.seats_total || 0) + 1) + '">' +
            '<span class="ac-hint">new total</span>' +
            '<button class="btn-sm btn-accent org-add-seats-btn" data-oid="' + oid + '">add seats</button>' +
        '</div>';
    }

    html += '<div class="ac-form-row">' +
        '<input type="email" class="ac-input" id="org-invite-email-' + oid + '" placeholder="email">' +
        '<select class="ac-input ac-input-select" id="org-invite-role-' + oid + '">' +
            '<option value="member">member</option>' +
            '<option value="admin">admin</option>' +
        '</select>' +
        '<button class="btn-sm btn-accent org-invite-btn" data-oid="' + oid + '">invite</button>' +
    '</div>';

    html += '<div id="org-members-list-' + oid + '" class="ac-members-container"><span class="text-dim">loading members...</span></div>';

    html += '<div class="ac-cancel-row"><button class="btn-sm org-cancel-btn" data-oid="' + oid + '">cancel subscription</button></div>';

    return html;
}

function expandOrgCard(oid, orgs, updateHash) {
    var detail = document.getElementById('ac-org-detail-' + oid);
    if (!detail) return;
    var wasOpen = detail.classList.contains('open');
    document.querySelectorAll('.ac-org-detail').forEach(function(d) { d.classList.remove('open'); });
    if (!wasOpen) {
        detail.classList.add('open');
        var org = orgs.find(function(o) { return o.id === oid; });
        if (org && org.has_subscription && org.is_owner) {
            loadOrgMembers(org, 'org-members-list-' + oid);
        }
        if (updateHash) {
            history.replaceState({ view: 'account', orgSlug: oid }, '', '#account/' + oid);
        }
    } else if (updateHash) {
        history.replaceState({ view: 'account', orgSlug: null }, '', '#account');
    }
}

function wireOrgCards(orgs) {
    var headers = document.querySelectorAll('.ac-org-header');
    headers.forEach(function(header) {
        header.addEventListener('click', function() {
            var oid = this.getAttribute('data-oid');
            expandOrgCard(oid, orgs, true);
        });
    });

    document.querySelectorAll('.ac-plan-toggle').forEach(function(toggle) {
        toggle.querySelectorAll('.ac-plan-opt').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.stopPropagation();
                toggle.querySelectorAll('.ac-plan-opt').forEach(function(b) { b.classList.remove('active'); });
                this.classList.add('active');
            });
        });
    });

    orgs.forEach(function(org) {
        var leaveBtn = document.querySelector('.org-leave-btn[data-oid="' + org.id + '"]');
        if (leaveBtn) {
            var leaveConfirmed = false;
            leaveBtn.addEventListener('click', function(e) {
                e.stopPropagation();
                var btn = this;
                if (!leaveConfirmed) {
                    btn.textContent = 'you may lose pro — confirm?';
                    btn.classList.add('btn-armed');
                    leaveConfirmed = true;
                    setTimeout(function() { btn.textContent = 'leave org'; btn.classList.remove('btn-armed'); leaveConfirmed = false; }, 4000);
                    return;
                }
                btn.textContent = 'leaving...';
                btn.disabled = true;
                fetch('/api/orgs/' + org.id + '/members/' + S.currentUser.id, { method: 'DELETE' })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.error) { btn.textContent = 'failed'; btn.disabled = false; leaveConfirmed = false; return; }
                    loadAccountOrgs();
                    // Refresh user tier in case entitlement was revoked
                    fetch('/api/app/me').then(function(r) { return r.json(); }).then(function(u) {
                        S.currentUser = u;
                    });
                })
                .catch(function() { btn.textContent = 'failed'; btn.disabled = false; leaveConfirmed = false; });
            });
        }

        if (!org.is_owner) return;

        var giveBtn = document.querySelector('.org-give-seats-btn[data-oid="' + org.id + '"]');
        if (giveBtn) {
            giveBtn.addEventListener('click', function(e) {
                e.stopPropagation();
                var btn = this;
                var seats = parseInt(document.getElementById('org-seats-input-' + org.id).value) || 1;
                var planToggle = document.getElementById('org-plan-toggle-' + org.id);
                var activeOpt = planToggle ? planToggle.querySelector('.ac-plan-opt.active') : null;
                var plan = activeOpt ? activeOpt.getAttribute('data-plan') : 'team_yearly';
                btn.textContent = 'working...';
                btn.disabled = true;
                fetch('/api/orgs/' + org.id + '/upgrade', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ seats: seats, plan: plan })
                })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.error) { btn.textContent = 'failed'; btn.disabled = false; return; }
                    S.accountExpandSlug = org.id;
                    loadAccountOrgs();
                })
                .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
            });
        }

        var addBtn = document.querySelector('.org-add-seats-btn[data-oid="' + org.id + '"]');
        if (addBtn) {
            addBtn.addEventListener('click', function(e) {
                e.stopPropagation();
                var btn = this;
                var seats = parseInt(document.getElementById('org-add-seats-input-' + org.id).value);
                if (!seats || seats <= (org.seats_total || 0)) return;
                btn.textContent = 'working...';
                btn.disabled = true;
                fetch('/api/orgs/' + org.id + '/upgrade', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ seats: seats })
                })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.error) { btn.textContent = 'failed'; btn.disabled = false; return; }
                    S.accountExpandSlug = org.id;
                    loadAccountOrgs();
                })
                .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
            });
        }

        var inviteBtn = document.querySelector('.org-invite-btn[data-oid="' + org.id + '"]');
        if (inviteBtn) {
            inviteBtn.addEventListener('click', function(e) {
                e.stopPropagation();
                var btn = this;
                var emailInput = document.getElementById('org-invite-email-' + org.id);
                var roleSelect = document.getElementById('org-invite-role-' + org.id);
                var invEmail = emailInput.value.trim();
                if (!invEmail) return;
                var invRole = roleSelect ? roleSelect.value : 'member';
                btn.textContent = 'working...';
                btn.disabled = true;
                fetch('/api/orgs/' + org.id + '/invite', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ emails: [invEmail], role: invRole })
                })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.error) { btn.textContent = 'failed'; btn.disabled = false; return; }
                    btn.textContent = 'invite';
                    btn.disabled = false;
                    emailInput.value = '';
                    if (data.invited && data.invited.length > 0 && data.invited[0].link) {
                        var link = data.invited[0].link;
                        navigator.clipboard.writeText(link).then(function() {
                            btn.textContent = 'copied link';
                            setTimeout(function() { btn.textContent = 'invite'; }, 2000);
                        });
                    }
                    loadOrgMembers(org, 'org-members-list-' + org.id);
                })
                .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
            });
        }

        var cancelBtn = document.querySelector('.org-cancel-btn[data-oid="' + org.id + '"]');
        if (cancelBtn) {
            var cancelClicks = 0;
            cancelBtn.addEventListener('click', function(e) {
                e.stopPropagation();
                var btn = this;
                cancelClicks++;
                if (cancelClicks === 1) {
                    btn.textContent = 'are you sure?';
                    btn.classList.add('btn-warn');
                    return;
                }
                if (cancelClicks === 2) {
                    btn.textContent = 'click again to confirm';
                    btn.classList.remove('btn-warn');
                    btn.classList.add('btn-armed');
                    return;
                }
                btn.textContent = 'canceling...';
                btn.disabled = true;
                fetch('/api/orgs/' + org.id + '/cancel', { method: 'POST' })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.error) { btn.textContent = 'failed'; btn.disabled = false; cancelClicks = 0; return; }
                    loadAccountOrgs();
                })
                .catch(function() { btn.textContent = 'failed'; btn.disabled = false; cancelClicks = 0; });
            });
        }

        var deleteBtn = document.querySelector('.org-delete-btn[data-oid="' + org.id + '"]');
        if (deleteBtn) {
            var deleteConfirmed = false;
            deleteBtn.addEventListener('click', function(e) {
                e.stopPropagation();
                var btn = this;
                if (!deleteConfirmed) {
                    btn.textContent = 'click again to delete';
                    btn.classList.add('btn-armed');
                    deleteConfirmed = true;
                    setTimeout(function() { btn.textContent = 'delete org'; btn.classList.remove('btn-armed'); deleteConfirmed = false; }, 4000);
                    return;
                }
                btn.textContent = 'deleting...';
                btn.disabled = true;
                fetch('/api/orgs/' + org.id, { method: 'DELETE' })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.error) { btn.textContent = 'failed'; btn.disabled = false; deleteConfirmed = false; return; }
                    loadAccountOrgs();
                })
                .catch(function() { btn.textContent = 'failed'; btn.disabled = false; deleteConfirmed = false; });
            });
        }
    });
}

function loadOrgMembers(org, containerId) {
    var list = document.getElementById(containerId);
    if (!list) return;

    fetch('/api/orgs/' + org.id + '/members')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var html = '<div class="ac-member-label">members</div>';
            var members = data.members || [];
            for (var i = 0; i < members.length; i++) {
                var m = members[i];
                var display = m.email || m.display_name || m.user_id;
                html += '<div class="ac-member-row">' +
                    '<span>' + escapeHtml(display) + ' <span class="ac-role-badge">' + escapeHtml(m.role) + '</span></span>' +
                    '<span class="ac-member-actions">';
                if (m.role !== 'owner' && org.is_owner) {
                    html += '<button class="btn-sm btn-danger org-remove-member" data-uid="' + escapeHtml(m.user_id) + '" data-oid="' + escapeHtml(org.id) + '">remove</button>';
                }
                html += '</span></div>';
            }
            var invites = data.invites || [];
            if (invites.length > 0) {
                html += '<div class="ac-member-label">pending invites</div>';
            }
            for (var j = 0; j < invites.length; j++) {
                var inv = invites[j];
                html += '<div class="ac-member-row">' +
                    '<span class="text-dim">' + escapeHtml(inv.email) + (inv.role && inv.role !== 'member' ? ' <span class="ac-role-badge">' + escapeHtml(inv.role) + '</span>' : '') + '</span>' +
                    '<span class="ac-member-actions">';
                if (inv.link) {
                    html += '<button class="btn-sm org-copy-link" data-link="' + escapeHtml(inv.link) + '">copy</button>';
                    var token = inv.link.split('/invite/')[1] || '';
                    html += '<button class="btn-sm btn-danger org-revoke-invite" data-oid="' + escapeHtml(org.id) + '" data-token="' + escapeHtml(token) + '">revoke</button>';
                }
                html += '</span></div>';
            }
            list.innerHTML = html;

            list.querySelectorAll('.org-copy-link').forEach(function(btn) {
                btn.addEventListener('click', function(e) {
                    e.stopPropagation();
                    var link = this.getAttribute('data-link');
                    var self = this;
                    navigator.clipboard.writeText(link).then(function() {
                        self.textContent = 'copied';
                        setTimeout(function() { self.textContent = 'copy'; }, 2000);
                    });
                });
            });

            list.querySelectorAll('.org-revoke-invite').forEach(function(btn) {
                btn.addEventListener('click', function(e) {
                    e.stopPropagation();
                    var self = this;
                    if (!self.dataset.confirmed) {
                        self.textContent = 'sure?';
                        self.dataset.confirmed = '1';
                        setTimeout(function() { self.textContent = 'revoke'; delete self.dataset.confirmed; }, 3000);
                        return;
                    }
                    var oid = self.getAttribute('data-oid');
                    var token = self.getAttribute('data-token');
                    self.textContent = '...';
                    self.disabled = true;
                    fetch('/api/orgs/' + oid + '/invites/' + token + '/revoke', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' }
                    })
                    .then(function(r) { return r.json(); })
                    .then(function() { loadOrgMembers(org, containerId); })
                    .catch(function() { self.textContent = 'failed'; self.disabled = false; });
                });
            });

            var removeBtns = list.querySelectorAll('.org-remove-member');
            removeBtns.forEach(function(btn) {
                btn.addEventListener('click', function(e) {
                    e.stopPropagation();
                    var uid = this.getAttribute('data-uid');
                    this.textContent = '...';
                    this.disabled = true;
                    var self = this;
                    fetch('/api/orgs/' + org.id + '/members/' + uid, { method: 'DELETE' })
                    .then(function(r) { return r.json(); })
                    .then(function() { loadOrgMembers(org, containerId); })
                    .catch(function() { self.textContent = 'failed'; self.disabled = false; });
                });
            });
        })
        .catch(function() {
            list.innerHTML = '<span class="text-dim">failed to load members</span>';
        });
}

export function hideDetailModal() {
    DOM.detailOverlay.classList.remove('open');
    DOM.detailDialog.innerHTML = '';
}

export function renderWingDetailPage(wingId) {
    var searchEl = document.getElementById('wd-search');
    if (searchEl && document.activeElement === searchEl) {
        updateWingDetailSessions(wingId);
        return;
    }

    var w = S.wingsData.find(function(w) { return w.wing_id === wingId; });

    var isUpdating = w && w.updating_at && (Date.now() - w.updating_at < 60000);
    if (!isUpdating && w && w.updating_at) {
        delete w.updating_at;
    }

    if (!w || isUpdating) {
        var msg = isUpdating
            ? '<span class="text-dim">updating... wing will reconnect shortly</span>'
            : '<span class="text-dim">wing not found</span>';
        DOM.wingDetailContent.innerHTML = '<div class="wd-page"><div class="wd-header"><a class="wd-back" id="wd-back">back</a>' + msg + '</div></div>';
        document.getElementById('wd-back').addEventListener('click', function() { showHome(); });
        if (isUpdating) {
            setTimeout(function() {
                if (S.activeView === 'wing-detail' && S.currentWingId === wingId) {
                    renderWingDetailPage(wingId);
                }
            }, 3000);
        }
        return;
    }

    var name = wingDisplayName(w);
    var isOnline = w.online !== false;
    var ver = w.version || '';
    var updateAvailable = !w.updating_at && S.latestVersion && ver && semverCompare(S.latestVersion, ver) > 0;

    var pubKeyHtml = '';
    if (w.public_key) {
        var pubKeyShort = w.public_key.substring(0, 16) + '...';
        pubKeyHtml = '<span class="detail-val text-dim copyable" data-copy="' + escapeHtml(w.public_key) + '">' + escapeHtml(pubKeyShort) + '</span>';
    } else {
        pubKeyHtml = '<span class="detail-val text-dim">none</span>';
    }

    var projects = (w.projects || []).slice();
    projects.sort(function(a, b) { return (b.mod_time || 0) - (a.mod_time || 0); });
    var maxProjects = 8;
    var visibleProjects = projects.slice(0, maxProjects);
    var projList = visibleProjects.map(function(p) {
        return '<div class="detail-subitem">' + escapeHtml(p.name) + ' <span class="text-dim">' + escapeHtml(shortenPath(p.path)) + '</span></div>';
    }).join('');
    if (projects.length > maxProjects) {
        projList += '<div class="detail-projects-more">+' + (projects.length - maxProjects) + ' more</div>';
    }
    if (!projList) projList = '<span class="text-dim">none</span>';

    var scopeHtml = w.org_id ? escapeHtml(w.org_id) : 'personal';

    var activeSessions = S.sessionsData.filter(function(s) { return s.wing_id === w.wing_id; });
    var activeHtml = '';
    if (activeSessions.length > 0) {
        activeHtml = '<div class="wd-section"><h3 class="section-label">active sessions</h3><div class="wd-sessions" id="wd-active-sessions">';
        activeHtml += renderActiveSessionRows(activeSessions);
        activeHtml += '</div></div>';
    }

    var isNotAllowed = w.tunnel_error === 'not_allowed';
    var isPasskeyNeeded = w.tunnel_error === 'passkey_required' || w.tunnel_error === 'passkey_failed';
    var isNoPasskeys = w.tunnel_error === 'no_passkeys_configured';
    var isLocked = isNotAllowed || isPasskeyNeeded || isNoPasskeys;
    var userEmail = (S.currentUser && S.currentUser.email) || 'your@email.com';

    var lockBanner = '';
    if (isNotAllowed) {
        lockBanner = '<div class="wd-lock-banner"><span class="wd-lock-icon">&#x1f512;</span> This wing is locked. Ask the owner to add you:<br><code>wt wing allow --email ' + escapeHtml(userEmail) + '</code></div>';
    } else if (isNoPasskeys) {
        lockBanner = '<div class="wd-lock-banner"><span class="wd-lock-icon">&#x1f512;</span> Configure a passkey to access this wing<br><button class="btn-sm btn-accent" id="wd-passkey-setup-btn">set up passkey</button></div>';
    } else if (isPasskeyNeeded) {
        lockBanner = '<div class="wd-lock-banner"><span class="wd-lock-icon">&#x1f512;</span> Authenticate to access this wing<br><button class="btn-sm btn-accent" id="wd-auth-btn">authenticate</button></div>';
    }

    var html =
        '<div class="wd-page">' +
        '<div class="wd-header">' +
            '<a class="wd-back" id="wd-back">back</a>' +
        '</div>' +
        lockBanner +
        (updateAvailable ? '<div class="wd-update-banner" id="wd-update">' +
            escapeHtml(S.latestVersion) + ' available (you have ' + escapeHtml(ver) + ') <span class="wd-update-action">update now</span>' +
        '</div>' : '') +
        '<div class="wd-hero">' +
            '<div class="wd-hero-top">' +
                '<span class="session-dot ' + (isOnline ? 'live' : 'offline') + '"></span>' +
                '<span class="wd-name" id="wd-name" title="click to rename">' + escapeHtml(name) + '</span>' +
                (w.owner && !(S.currentUser && w.user_id === S.currentUser.id) ? '<span class="wd-owner">' + escapeHtml(w.owner) + '</span>' : '') +
                (w.locked && !S.tunnelAuthTokens[wingId] ? '<span class="wd-pinned-badge" title="passkey required">&#x1f512; locked</span>' : '') +
                (!w.locked && w.passkey_enrolled ? '<span class="wd-pinned-badge" title="passkey enrolled">&#x1f511; passkey</span>' : '') +
                (w.wing_label ? '<a class="wd-clear-label" id="wd-delete-label" title="clear name">x</a>' : '') +
                (!isOnline || w.tunnel_error === 'unreachable' ? '<a class="wd-dismiss-link" id="wd-dismiss">remove</a>' : '') +
            '</div>' +
        '</div>' +
        (isOnline && !isLocked && (w.agents || []).length > 0 ? '<div class="wd-palette">' +
            '<input id="wd-search" type="text" class="wd-search" placeholder="' + (w.locked && !S.tunnelAuthTokens[wingId] ? 'start a session (passkey auth on first browse)...' : 'start a session...') + '" autocomplete="off" spellcheck="false">' +
            '<div id="wd-search-results" class="wd-search-results"></div>' +
            '<div id="wd-search-status" class="wd-search-status"></div>' +
        '</div>' : '') +
        (isOnline && !isLocked && (w.agents || []).length === 0 ? '<div class="wd-no-agents"><span class="text-dim">no agents installed — run <code>wt doctor</code> on this machine to diagnose</span></div>' : '') +
        (isLocked ? '' : activeHtml) +
        (isLocked ? '' : '<div class="wd-section"><h3 class="section-label">session history</h3><div id="wd-past-sessions"><span class="text-dim">' + (isOnline ? 'loading...' : 'wing offline') + '</span></div></div>') +
        '<div class="wd-info">' +
            '<div class="detail-row"><span class="detail-key">scope</span><span class="detail-val">' + scopeHtml + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">platform</span><span class="detail-val">' + escapeHtml(w.platform || 'unknown') + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">version</span><span class="detail-val">' + escapeHtml(ver || 'unknown') + '</span></div>' +
            (isLocked ? '' : '<div class="detail-row"><span class="detail-key">agents</span><span class="detail-val">' + ((w.agents || []).map(function(a) {
                return agentWithIcon(a);
            }).join(' ') || 'none') + '</span></div>') +
            '<div class="detail-row"><span class="detail-key">public key</span>' + pubKeyHtml + '</div>' +
            (isLocked ? '' : '<div class="detail-row"><span class="detail-key">projects</span><div class="detail-val">' + projList + '</div></div>') +
        '</div>' +
        (isOnline ? '<div class="wd-section"><h3 class="section-label">access control</h3>' +
            '<div id="wd-allowlist"><span class="text-dim">loading...</span></div>' +
            '<div class="wd-allow-actions">' +
                '<button class="btn-sm btn-accent" id="wd-allow-me">allow me</button>' +
            '</div>' +
        '</div>' : '') +
        (isOnline && !isLocked ? '<div class="wd-section"><h3 class="section-label">folder access</h3>' +
            '<div id="wd-paths"><span class="text-dim">loading...</span></div>' +
        '</div>' : '') +
        '</div>';

    DOM.wingDetailContent.innerHTML = html;
    setupCopyable(DOM.wingDetailContent);

    document.getElementById('wd-back').addEventListener('click', function() { showHome(); });

    var authBtn = document.getElementById('wd-auth-btn');
    if (authBtn) {
        authBtn.addEventListener('click', function() {
            authBtn.textContent = 'authenticating...';
            authBtn.disabled = true;
            delete w.tunnel_error;
            sendTunnelRequest(w.wing_id, { type: 'wing.info' })
                .then(function() {
                    tunnelCloseWing(w.wing_id);
                    return probeWing(w);
                })
                .then(function() {
                    renderWingDetailPage(wingId);
                    if (shouldFetchWingSessions(S.currentUser, w)) {
                        fetchWingSessions(w.wing_id).then(function(sessions) {
                            if (sessions !== null) {
                                mergeWingSessions(w.wing_id, sessions);
                                renderSidebar();
                            }
                        });
                    }
                })
                .catch(function(e) {
                    if (e.message && e.message.indexOf('not_allowed') !== -1) {
                        w.tunnel_error = 'not_allowed';
                    } else if (e.noPasskeys) {
                        w.tunnel_error = 'no_passkeys_configured';
                    } else {
                        w.tunnel_error = 'passkey_failed';
                    }
                    renderWingDetailPage(wingId);
                });
        });
    }

    var passkeySetupBtn = document.getElementById('wd-passkey-setup-btn');
    if (passkeySetupBtn) {
        passkeySetupBtn.addEventListener('click', function() { navigateToAccount(true); });
    }

    var nameEl = document.getElementById('wd-name');
    nameEl.addEventListener('click', function() {
        var current = w.wing_label || w.hostname || '';
        var input = document.createElement('input');
        input.type = 'text';
        input.className = 'wd-name-input';
        input.value = current;
        nameEl.replaceWith(input);
        input.focus();
        input.select();
        function save() {
            var val = input.value.trim();
            if (val && val !== current) {
                fetch('/api/app/wings/' + encodeURIComponent(wingId) + '/label', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ label: val })
                }).then(function() {
                    w.wing_label = val;
                    renderWingDetailPage(wingId);
                });
            } else {
                renderWingDetailPage(wingId);
            }
        }
        input.addEventListener('blur', save);
        input.addEventListener('keydown', function(e) {
            if (e.key === 'Enter') { e.preventDefault(); input.blur(); }
            if (e.key === 'Escape') { e.preventDefault(); renderWingDetailPage(wingId); }
        });
    });

    var delLabelBtn = document.getElementById('wd-delete-label');
    if (delLabelBtn) {
        delLabelBtn.addEventListener('click', function(e) {
            e.stopPropagation();
            fetch('/api/app/wings/' + encodeURIComponent(wingId) + '/label', { method: 'DELETE' })
                .then(function() {
                    delete w.wing_label;
                    renderWingDetailPage(wingId);
                });
        });
    }

    var updateBtn = document.getElementById('wd-update');
    if (updateBtn) {
        updateBtn.addEventListener('click', function() {
            updateBtn.innerHTML = 'updating...';
            sendTunnelRequest(w.wing_id, { type: 'wing.update' })
                .then(function() {
                    w.updating_at = Date.now();
                    renderWingDetailPage(wingId);
                })
                .catch(function() { updateBtn.innerHTML = 'update failed'; });
        });
    }

    var dismissBtn = document.getElementById('wd-dismiss');
    if (dismissBtn) {
        dismissBtn.addEventListener('click', function() {
            S.wingsData = S.wingsData.filter(function(ww) { return ww.wing_id !== wingId; });
            saveWingCache();
            showHome();
        });
    }

    wireActiveSessionRows();

    if (isOnline && !isLocked) {
        loadWingPastSessions(wingId, 0);
    } else if (isLocked) {
        var pastEl = document.getElementById('wd-past-sessions');
        if (pastEl) pastEl.innerHTML = '<span class="text-dim">authenticate to view session history</span>';
    }

    if (isOnline && !isLocked) {
        setupWingPalette(w);
    }
    if (isOnline) {
        loadWingAllowlist(w);
    }
    if (isOnline && !isLocked) {
        loadWingPaths(w);
    }
}

function renderActiveSessionRows(sessions) {
    return sessions.map(function(s) {
        var sName = sessionDisplayName(s);
        var presentation = sessionInventoryState(s, sessionWing(s), notificationForSession(S.sessionNotifications, s));
        var actions = sessionInventoryActions(s, sessionWing(s), S.currentUser);
        var kind = s.kind || 'terminal';
        var sid = escapeHtml(s.id);
        var auditBadge = s.audit ? '<span class="wd-audit-badge">audit</span>' : '';
        var auditBtns = s.audit
            ? '<button class="btn-sm wd-replay-btn" data-sid="' + sid + '">replay</button>' +
              '<button class="btn-sm wd-keylog-btn" data-sid="' + sid + '">keylog</button>'
            : '';
        return '<div class="wd-session-row" role="group" tabindex="0" aria-label="' + escapeHtml(sName + ' · ' + presentation.agentLabel + ' · ' + presentation.connectionLabel) + '" data-sid="' + sid + '" data-wing-id="' + escapeHtml(s.wing_id || '') + '" data-kind="' + escapeHtml(kind) + '" data-agent="' + escapeHtml(s.agent || 'claude') + '">' +
            sessionStatusDot(presentation.status) +
            '<span class="wd-session-name">' + escapeHtml(sName) + ' \u00b7 ' + agentWithIcon(s.agent || '?') + '</span>' +
            '<span class="wd-session-state">' + escapeHtml(presentation.agentLabel) + '</span>' +
            auditBadge +
            auditBtns +
            (actions.stop ? '<button class="wd-kill-btn" data-sid="' + sid + '" title="Stop ' + escapeHtml(sName) + '">stop</button>' : '') +
        '</div>';
    }).join('');
}

function wireActiveSessionRows() {
    var rows = Array.from(DOM.wingDetailContent.querySelectorAll('.wd-session-row'));
    rows.forEach(function(row) {
        row.addEventListener('click', function(e) {
            if (e.target.classList.contains('wd-kill-btn') || e.target.classList.contains('wd-replay-btn') || e.target.classList.contains('wd-keylog-btn')) return;
            var sid = row.dataset.sid;
            switchToSession(sid, undefined, row.dataset.wingId);
        });
        row.addEventListener('keydown', function(event) {
            if (event.target !== row) return;
            if (navigateSessionRows(event, rows, row)) return;
            if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); switchToSession(row.dataset.sid, undefined, row.dataset.wingId); }
        });
    });
    DOM.wingDetailContent.querySelectorAll('.wd-kill-btn').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            if (btn.dataset.confirming) {
                var sid = btn.dataset.sid;
                var wingId = S.currentWingId;
                btn.disabled = true;
                btn.textContent = '...';
                sendTunnelRequest(wingId, { type: 'pty.kill', session_id: sid })
                    .then(function() {
                        deleteSession(sid, true, wingId);
                        updateWingDetailSessions(wingId);
                        loadWingPastSessions(wingId, 0);
                    }).catch(function(error) {
                        btn.disabled = false;
                        btn.textContent = 'retry stop';
                        btn.title = (error && error.message) || 'Stop failed';
                    });
            } else {
                btn.dataset.confirming = '1';
                btn.textContent = 'sure?';
                btn.classList.add('confirming');
                setTimeout(function() {
                    delete btn.dataset.confirming;
                    btn.textContent = 'stop';
                    btn.classList.remove('confirming');
                }, 3000);
            }
        });
    });
    DOM.wingDetailContent.querySelectorAll('.wd-session-row .wd-replay-btn').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            openAuditReplay(S.currentWingId, btn.dataset.sid);
        });
    });
    DOM.wingDetailContent.querySelectorAll('.wd-session-row .wd-keylog-btn').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            openAuditKeylog(S.currentWingId, btn.dataset.sid);
        });
    });
}

function updateWingDetailSessions(wingId) {
    var w = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!w) return;
    var container = document.getElementById('wd-active-sessions');
    var activeSessions = S.sessionsData.filter(function(s) { return s.wing_id === w.wing_id; });
    if (container) {
        container.innerHTML = renderActiveSessionRows(activeSessions);
        wireActiveSessionRows();
    }
}

function setupWingPalette(wing) {
    var searchEl = document.getElementById('wd-search');
    var resultsEl = document.getElementById('wd-search-results');
    var statusEl = document.getElementById('wd-search-status');
    if (!searchEl || !resultsEl || !statusEl) return;

    var wpAgentIndex = 0;
    var wpSelectedIndex = 0;
    var wpDirCache = [];
    var wpDirCacheDir = '';
    var wpDirTimer = null;
    var wpDirAbort = null;
    var wpHomeDirCache = [];

    var agents = wing.agents || ['claude'];
    var lastAgent = getLastTermAgent();
    var idx = agents.indexOf(lastAgent);
    wpAgentIndex = idx >= 0 ? idx : 0;

    function currentAgent() { return agents[wpAgentIndex % agents.length]; }

    function renderStatus() {
        var agent = currentAgent();
        statusEl.innerHTML = '<span class="accent">' + agentWithIcon(agent) + '</span>' +
            (agents.length > 1 ? ' <span class="text-dim">(shift+tab to switch)</span>' : '');
    }
    renderStatus();

    if (wing.wing_id) {
        sendTunnelRequest(wing.wing_id, { type: 'dir.list', path: '~/' }, { skipPasskey: true }).then(function(data) {
            var entries = data.entries || [];
            if (Array.isArray(entries)) {
                wpHomeDirCache = entries.map(function(e) {
                    return { name: e.name, path: e.path, isDir: e.is_dir };
                });
            }
        }).catch(function() {});
    }

    function renderResults(filter) {
        var wingId = wing.wing_id || '';
        var wingProjects = wingId
            ? S.allProjects.filter(function(p) { return p.wingId === wingId; })
            : S.allProjects;

        var items = [];
        var lower = filter ? filter.toLowerCase() : '';
        var filtered = lower
            ? wingProjects.filter(function(p) {
                return p.name.toLowerCase().indexOf(lower) !== -1 ||
                       p.path.toLowerCase().indexOf(lower) !== -1;
            })
            : wingProjects.slice();

        filtered.sort(function(a, b) {
            var ca = nestedRepoCount(a.path, wingProjects);
            var cb = nestedRepoCount(b.path, wingProjects);
            if (ca !== cb) return cb - ca;
            return a.name.localeCompare(b.name);
        });

        filtered.forEach(function(p) {
            items.push({ name: p.name, path: p.path, isDir: true });
        });

        var seenPaths = {};
        items.forEach(function(it) { seenPaths[it.path] = true; });
        var homeExtras = wpHomeDirCache.filter(function(e) {
            if (seenPaths[e.path]) return false;
            return !lower || e.name.toLowerCase().indexOf(lower) !== -1 ||
                   e.path.toLowerCase().indexOf(lower) !== -1;
        });
        homeExtras.sort(function(a, b) {
            var ca = nestedRepoCount(a.path, wingProjects);
            var cb = nestedRepoCount(b.path, wingProjects);
            if (ca !== cb) return cb - ca;
            return a.name.localeCompare(b.name);
        });
        homeExtras.forEach(function(e) {
            items.push({ name: e.name, path: e.path, isDir: e.isDir });
        });

        renderItems(items);
    }

    function renderItems(items) {
        wpSelectedIndex = 0;
        if (items.length === 0) { resultsEl.innerHTML = ''; return; }

        resultsEl.innerHTML = items.map(function(item, i) {
            var icon = item.isDir ? '/' : '';
            return '<div class="palette-item' + (i === 0 ? ' selected' : '') + '" data-path="' + escapeHtml(item.path) + '" data-index="' + i + '">' +
                '<span class="palette-name">' + escapeHtml(item.name) + icon + '</span>' +
                (item.path ? '<span class="palette-path">' + escapeHtml(shortenPath(item.path)) + '</span>' : '') +
            '</div>';
        }).join('');

        resultsEl.querySelectorAll('.palette-item').forEach(function(item) {
            item.addEventListener('click', function() { launch(item.dataset.path); });
            item.addEventListener('mouseenter', function() {
                resultsEl.querySelectorAll('.palette-item').forEach(function(el) { el.classList.remove('selected'); });
                item.classList.add('selected');
                wpSelectedIndex = parseInt(item.dataset.index);
            });
        });
    }

    function wpFilterCached(prefix) {
        var items = wpDirCache;
        if (prefix) {
            items = items.filter(function(e) { return e.name.toLowerCase().indexOf(prefix) === 0; });
        }
        return items;
    }

    function wpDebouncedDirList(value) {
        if (wpDirTimer) clearTimeout(wpDirTimer);
        if (wpDirAbort) { wpDirAbort.abort(); wpDirAbort = null; }

        if (!value || (value.charAt(0) !== '/' && value.charAt(0) !== '~')) {
            wpDirCache = [];
            wpDirCacheDir = '';
            renderResults(value);
            return;
        }

        var parsed = dirParent(value);
        if (wpDirCacheDir && wpDirCacheDir === parsed.dir) {
            renderItems(wpFilterCached(parsed.prefix));
            return;
        }
        if (wpDirCache.length > 0) {
            renderItems(wpFilterCached(parsed.prefix));
        }
        wpDirTimer = setTimeout(function() { wpFetchDirList(parsed.dir); }, 150);
    }

    function wpFetchDirList(dirPath) {
        sendTunnelRequest(wing.wing_id, { type: 'dir.list', path: dirPath }).then(function(data) {
            var entries = data.entries || [];
            var currentParsed = dirParent(searchEl.value);
            if (currentParsed.dir !== dirPath) return;

            if (!entries || !Array.isArray(entries)) {
                wpDirCache = [];
                wpDirCacheDir = '';
                renderItems([]);
                return;
            }
            var items = entries.map(function(e) {
                return { name: e.name, path: e.path, isDir: e.is_dir };
            });
            items.sort(function(a, b) {
                if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
                var ca = nestedRepoCount(a.path, S.allProjects);
                var cb = nestedRepoCount(b.path, S.allProjects);
                if (ca !== cb) return cb - ca;
                return a.name.localeCompare(b.name);
            });
            var absDirPath = dirPath;
            if (items.length > 0 && items[0].path) {
                absDirPath = items[0].path.replace(/\/[^\/]+$/, '');
            }
            var dirLabel = shortenPath(absDirPath).replace(/\/$/, '') || absDirPath;
            items.unshift({ name: dirLabel, path: absDirPath, isDir: true });
            wpDirCache = items;
            wpDirCacheDir = dirPath;
            renderItems(wpFilterCached(currentParsed.prefix));
        }).catch(function() {});
    }

    function navigate(dir) {
        var items = resultsEl.querySelectorAll('.palette-item');
        if (items.length === 0) return;
        items[wpSelectedIndex].classList.remove('selected');
        wpSelectedIndex = (wpSelectedIndex + dir + items.length) % items.length;
        items[wpSelectedIndex].classList.add('selected');
        items[wpSelectedIndex].scrollIntoView({ block: 'nearest' });
    }

    function tabComplete() {
        var selected = resultsEl.querySelector('.palette-item.selected');
        if (!selected) return;
        var path = selected.dataset.path;
        if (!path) return;
        var short = shortenPath(path);
        var nameEl = selected.querySelector('.palette-name');
        var isDir = nameEl && nameEl.textContent.slice(-1) === '/';
        if (isDir) {
            searchEl.value = short + '/';
            wpDebouncedDirList(searchEl.value);
        } else {
            searchEl.value = short;
        }
    }

    function launch(cwd) {
        var agent = currentAgent();
        var validCwd = (cwd && cwd.charAt(0) === '/') ? cwd : '';
        setLastTermAgent(agent);
        if (!showTerminal()) return;
        connectPTY(agent, validCwd, wing.wing_id);
    }

    renderResults('');

    searchEl.addEventListener('input', function() {
        wpDebouncedDirList(searchEl.value);
    });

    searchEl.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            e.preventDefault();
            var selected = resultsEl.querySelector('.palette-item.selected');
            if (selected) launch(selected.dataset.path);
        }
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            navigate(e.key === 'ArrowDown' ? 1 : -1);
        }
        if (e.key === 'Tab') {
            e.preventDefault();
            if (e.shiftKey) {
                if (agents.length > 1) {
                    wpAgentIndex = (wpAgentIndex + 1) % agents.length;
                    renderStatus();
                }
            } else {
                tabComplete();
            }
        }
        if (e.key === '`') {
            e.preventDefault();
            if (agents.length > 1) {
                wpAgentIndex = (wpAgentIndex + 1) % agents.length;
                renderStatus();
            }
        }
    });
}

function loadWingAllowlist(wing) {
    var container = document.getElementById('wd-allowlist');
    var allowBtn = document.getElementById('wd-allow-me');
    if (!container) return;

    sendTunnelRequest(wing.wing_id, { type: 'allow.list' }, { skipPasskey: true }).then(function(data) {
        var allowed = data.allowed || [];
        if (allowed.length === 0) {
            container.innerHTML = '<span class="text-dim">no allowed users — anyone with wing access can connect</span>';
        } else {
            var html = allowed.map(function(p) {
                var display = p.email || p.user_id || '(key-only)';
                var keyShort = p.key ? p.key.substring(0, 12) + '...' : 'none';
                return '<div class="wd-allow-row">' +
                    '<span class="wd-allow-email">' + escapeHtml(display) + '</span>' +
                    '<span class="wd-allow-key text-dim">pk: ' + escapeHtml(keyShort) + '</span>' +
                    '<button class="btn-sm btn-danger wd-allow-remove" data-allow-uid="' + escapeHtml(p.user_id || '') + '" data-allow-key="' + escapeHtml(p.key || '') + '">remove</button>' +
                '</div>';
            }).join('');
            container.innerHTML = html;

            // Check if current user is already allowed
            var myId = S.currentUser.id || '';
            var alreadyAllowed = allowed.some(function(p) { return p.user_id === myId; });
            if (allowBtn && alreadyAllowed) {
                allowBtn.textContent = 'allowed';
                allowBtn.disabled = true;
            }

            // Wire remove buttons
            container.querySelectorAll('.wd-allow-remove').forEach(function(btn) {
                btn.addEventListener('click', function() {
                    var uid = btn.getAttribute('data-allow-uid');
                    var key = btn.getAttribute('data-allow-key');
                    btn.textContent = '...';
                    btn.disabled = true;
                    sendTunnelRequest(wing.wing_id, { type: 'allow.remove', allow_user_id: uid, key: key })
                        .then(function() { loadWingAllowlist(wing); })
                        .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
                });
            });
        }
    }).catch(function() {
        container.innerHTML = '<span class="text-dim">could not load allowlist</span>';
    });

    // Wire "Allow me" button
    if (allowBtn) {
        allowBtn.addEventListener('click', function() {
            allowBtn.textContent = 'adding...';
            allowBtn.disabled = true;

            // Try to create a passkey
            var rpId = location.hostname;
            var userId = S.currentUser.id || 'anonymous';
            var userName = S.currentUser.email || S.currentUser.display_name || userId;

            var challenge = new Uint8Array(32);
            crypto.getRandomValues(challenge);

            navigator.credentials.create({
                publicKey: {
                    challenge: challenge,
                    rp: { name: 'wingthing', id: rpId },
                    user: {
                        id: new TextEncoder().encode(userId),
                        name: userName,
                        displayName: userName
                    },
                    pubKeyCredParams: [{ alg: -7, type: 'public-key' }],
                    authenticatorSelection: { userVerification: 'preferred' },
                    timeout: 60000
                }
            }).then(function(cred) {
                // Extract raw P-256 public key from COSE in attestation
                var pubKeyBytes = new Uint8Array(cred.response.getPublicKey());
                var keyB64 = btoa(String.fromCharCode.apply(null, pubKeyBytes));
                return sendTunnelRequest(wing.wing_id, { type: 'allow.add', key: keyB64 });
            }).then(function(resp) {
                if (resp.error) {
                    allowBtn.textContent = resp.error;
                    allowBtn.disabled = false;
                    return;
                }
                allowBtn.textContent = 'allowed';
                loadWingAllowlist(wing);
            }).catch(function() {
                // Passkey creation failed — allow by user ID only
                sendTunnelRequest(wing.wing_id, { type: 'allow.add' })
                    .then(function(resp) {
                        if (resp.error) {
                            allowBtn.textContent = resp.error;
                            allowBtn.disabled = false;
                            return;
                        }
                        allowBtn.textContent = 'allowed (no passkey)';
                        loadWingAllowlist(wing);
                    })
                    .catch(function() {
                        allowBtn.textContent = 'failed';
                        allowBtn.disabled = false;
                    });
            });
        });
    }
}

function loadWingPaths(wing) {
    var container = document.getElementById('wd-paths');
    if (!container) return;

    sendTunnelRequest(wing.wing_id, { type: 'paths.list' }).then(function(data) {
        var paths = data.paths || [];
        // Admin: paths is array of {path, members}; Member: array of strings
        var isAdmin = paths.length > 0 && typeof paths[0] === 'object';

        if (paths.length === 0) {
            container.innerHTML = '<span class="text-dim">' +
                (isAdmin ? 'no paths configured — wing exposes ~/' : 'no folders assigned to you on this wing') +
                '</span>';
            return;
        }

        if (!isAdmin) {
            // Member view: read-only list of accessible paths
            var html = paths.map(function(p) {
                return '<div class="wd-path-row"><span class="wd-path-name">' + escapeHtml(p) + '</span></div>';
            }).join('');
            container.innerHTML = html;
            return;
        }

        // Admin view: paths with member management
        var html = paths.map(function(entry, idx) {
            var p = entry.path || '';
            var members = entry.members || [];
            var memberHtml = '';
            if (members.length > 0) {
                memberHtml = members.map(function(m) {
                    return '<span class="wd-path-member">' + escapeHtml(m) +
                        ' <button class="wd-path-rm-member" data-path="' + escapeHtml(p) + '" data-email="' + escapeHtml(m) + '">x</button></span>';
                }).join('');
            } else {
                memberHtml = '<span class="text-dim">all users</span>';
            }
            return '<div class="wd-path-row">' +
                '<div class="wd-path-name">' + escapeHtml(p) + '</div>' +
                '<div class="wd-path-members">' + memberHtml +
                    ' <button class="btn-sm wd-path-add-member" data-path="' + escapeHtml(p) + '">+ member</button>' +
                '</div>' +
            '</div>';
        }).join('');
        container.innerHTML = html;

        // Wire remove member buttons
        container.querySelectorAll('.wd-path-rm-member').forEach(function(btn) {
            btn.addEventListener('click', function() {
                var path = btn.getAttribute('data-path');
                var email = btn.getAttribute('data-email');
                btn.textContent = '...';
                btn.disabled = true;
                sendTunnelRequest(wing.wing_id, { type: 'paths.remove_member', path: path, email: email })
                    .then(function() { loadWingPaths(wing); })
                    .catch(function() { btn.textContent = 'err'; btn.disabled = false; });
            });
        });

        // Wire add member buttons
        container.querySelectorAll('.wd-path-add-member').forEach(function(btn) {
            btn.addEventListener('click', function() {
                var path = btn.getAttribute('data-path');
                var input = document.createElement('input');
                input.type = 'email';
                input.className = 'wd-path-email-input';
                input.placeholder = 'email@example.com';
                btn.replaceWith(input);
                input.focus();
                var submitted = false;
                function submit() {
                    if (submitted) return;
                    submitted = true;
                    var email = input.value.trim();
                    if (!email) { loadWingPaths(wing); return; }
                    input.disabled = true;
                    sendTunnelRequest(wing.wing_id, { type: 'paths.add_member', path: path, email: email })
                        .then(function() { loadWingPaths(wing); })
                        .catch(function() { submitted = false; input.disabled = false; input.value = 'failed'; });
                }
                input.addEventListener('blur', submit);
                input.addEventListener('keydown', function(e) {
                    if (e.key === 'Enter') { e.preventDefault(); input.blur(); }
                    if (e.key === 'Escape') { e.preventDefault(); submitted = true; loadWingPaths(wing); }
                });
            });
        });
    }).catch(function() {
        container.innerHTML = '<span class="text-dim">could not load paths</span>';
    });
}

function loadWingPastSessions(wingId, offset) {
    var limit = 20;
    var container = document.getElementById('wd-past-sessions');
    if (!container) return;

    if (offset === 0) {
        var cached = getCachedWingSessions(wingId);
        if (cached && cached.length > 0) {
            renderPastSessions(container, wingId, cached, true);
        }
    }

    sendTunnelRequest(wingId, { type: 'sessions.history', offset: offset, limit: limit }, { skipPasskey: true })
        .then(function(data) {
            var sessions = data.sessions || [];
            if (offset === 0) {
                S.wingPastSessions[wingId] = { sessions: sessions, offset: offset, hasMore: sessions.length >= limit };
                setCachedWingSessions(wingId, sessions);
            } else {
                var existing = S.wingPastSessions[wingId] || { sessions: [], offset: 0, hasMore: true };
                existing.sessions = existing.sessions.concat(sessions);
                existing.offset = offset;
                existing.hasMore = sessions.length >= limit;
                S.wingPastSessions[wingId] = existing;
            }
            if (container && S.currentWingId === wingId) {
                renderPastSessions(container, wingId, S.wingPastSessions[wingId].sessions, S.wingPastSessions[wingId].hasMore);
            }
        })
        .catch(function() {
            if (container && S.currentWingId === wingId && offset === 0) {
                var cached = getCachedWingSessions(wingId);
                if (!cached || cached.length === 0) {
                    container.innerHTML = '<span class="text-dim">could not reach wing - it may be reconnecting</span>';
                }
            }
        });
}

function renderPastSessions(container, wingId, sessions, hasMore) {
    if (!sessions || sessions.length === 0) {
        container.innerHTML = '<span class="text-dim">no audited sessions</span>';
        return;
    }
    var canResume = wingHasCapability(wingId, 'session.provider_resume.v1');
    var html = sessions.map(function(s) {
        var name = s.name || (s.cwd ? projectName(s.cwd) : s.session_id.substring(0, 8));
        var startStr = s.started_at ? formatRelativeTime(s.started_at * 1000) : '';
        var auditBadge = s.audit ? '<span class="wd-audit-badge">audit</span>' : '';
        var chatBadge = s.chat ? '<span class="wd-audit-badge">chat</span>' : '';
        var auditBtns = s.audit
            ? '<button class="btn-sm wd-replay-btn" data-sid="' + escapeHtml(s.session_id) + '">replay</button>' +
              '<button class="btn-sm wd-keylog-btn" data-sid="' + escapeHtml(s.session_id) + '">keylog</button>'
            : '';
        var chatBtn = s.chat
            ? '<button class="btn-sm wd-chat-btn" data-sid="' + escapeHtml(s.session_id) + '">chat</button>'
            : '';
        var resumeState = historyResumeState(s, S.currentUser, canResume);
        var resumeBtn = resumeState.available
            ? '<button class="btn-sm wd-resume-btn" data-sid="' + escapeHtml(s.session_id) + '">resume</button>'
            : '<button class="btn-sm wd-resume-unavailable" disabled title="' + escapeHtml(resumeState.reason) + '">resume unavailable</button>';
        return '<div class="wd-past-row">' +
            '<span class="wd-past-name">' + escapeHtml(name) + ' \u00b7 ' + escapeHtml(s.agent || '?') + '</span>' +
            '<span class="wd-past-time text-dim">' + startStr + '</span>' +
            auditBadge +
            chatBadge +
            auditBtns +
            chatBtn +
            resumeBtn +
            sessionForkControl(s, S.wingsData.find(function(w) { return w.wing_id === wingId; }), S.currentUser) +
        '</div>';
    }).join('');

    if (hasMore) {
        html += '<button class="btn-sm wd-load-more" id="wd-load-more">load more</button>';
    }
    container.innerHTML = html;

    var loadMoreBtn = document.getElementById('wd-load-more');
    if (loadMoreBtn) {
        loadMoreBtn.addEventListener('click', function() {
            var state = S.wingPastSessions[wingId] || { sessions: [], offset: 0 };
            loadWingPastSessions(wingId, state.sessions.length);
        });
    }

    container.querySelectorAll('.wd-replay-btn').forEach(function(btn) {
        btn.addEventListener('click', function() {
            openAuditReplay(wingId, btn.dataset.sid);
        });
    });
    container.querySelectorAll('.wd-keylog-btn').forEach(function(btn) {
        btn.addEventListener('click', function() {
            openAuditKeylog(wingId, btn.dataset.sid);
        });
    });
    container.querySelectorAll('.wd-chat-btn').forEach(function(btn) {
        btn.addEventListener('click', function() {
            downloadChatHistory(wingId, btn.dataset.sid);
        });
    });
    container.querySelectorAll('.wd-resume-btn').forEach(function(btn) {
        btn.addEventListener('click', function() {
            var session = sessions.find(function(item) { return item.session_id === btn.dataset.sid; });
            if (!session || !historyResumeState(session, S.currentUser, wingHasCapability(wingId, 'session.provider_resume.v1')).available) return;
            showTerminal();
            connectPTY(session.agent || 'claude', session.cwd || '', wingId, session.session_id);
        });
    });
    container.querySelectorAll('.wd-past-row').forEach(function(row, index) {
        var fork = row.querySelector('.session-fork-btn');
        if (fork) fork.addEventListener('click', function() { beginSessionFork(row, sessions[index], wingId, fork); });
    });
}

export function showEggDetail(sessionId, wingId) {
    var s = findSessionResource(S.sessionsData, sessionId, wingId);
    if (!s) return;
    var name = sessionDisplayName(s);
    var kind = s.kind || 'terminal';
    var state = sessionInventoryState(s, sessionWing(s), notificationForSession(S.sessionNotifications, s));
    var actions = sessionInventoryActions(s, sessionWing(s), S.currentUser);
    var wingName = '';
    if (s.wing_id) {
        var wing = S.wingsData.find(function(w) { return w.wing_id === s.wing_id; });
        if (wing) wingName = wingDisplayName(wing);
    }
    var cwdDisplay = s.cwd ? shortenPath(s.cwd) : '~';

    var configSummary = '';
    var configYaml = s.egg_config || '';
    if (configYaml) {
        var isoMatch = configYaml.match(/isolation:\s*(\S+)/);
        var mountCount = (configYaml.match(/^\s*-\s+~/gm) || []).length;
        var denyCount = (configYaml.match(/deny:/g) || []).length > 0 ? (configYaml.match(/^\s+-\s+~/gm) || []).length : 0;
        var isoLevel = isoMatch ? isoMatch[1] : '?';
        var parts = [isoLevel];
        if (mountCount > 0) parts.push(mountCount + ' mount' + (mountCount > 1 ? 's' : ''));
        if (denyCount > 0) parts.push(denyCount + ' denied');
        configSummary =
            '<div class="detail-row"><span class="detail-key">config</span>' +
            '<span class="detail-val copyable" data-copy="' + escapeHtml(configYaml) + '" title="click to copy full YAML">' +
            escapeHtml(parts.join(' | ')) + '</span></div>';
    }

    DOM.detailDialog.innerHTML =
        '<h3>' + escapeHtml(name) + ' &middot; ' + escapeHtml(s.agent || '?') + '</h3>' +
        '<div class="detail-row"><span class="detail-key">session</span><span class="detail-val text-dim">' + escapeHtml(s.id) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">wing</span><span class="detail-val">' + escapeHtml(wingName || 'unknown') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">type</span><span class="detail-val">' + escapeHtml(kind) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">agent</span><span class="detail-val">' + escapeHtml(s.agent || '?') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">cwd</span><span class="detail-val text-dim">' + escapeHtml(cwdDisplay) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">agent state</span><span class="detail-val">' + escapeHtml(state.agentLabel) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">connection</span><span class="detail-val">' + escapeHtml(state.connectionLabel + ' · ' + state.attachment) + '</span></div>' +
        configSummary +
        '<div class="detail-actions">' +
            '<button class="btn-sm btn-accent" id="detail-egg-connect"' + (!actions.attach ? ' disabled' : '') + '>attach</button>' +
            (actions.stop ? '<button class="btn-sm btn-danger" id="detail-egg-delete">stop session</button>' : '') +
        '</div>';

    setupCopyable(DOM.detailDialog);
    DOM.detailOverlay.classList.add('open');

    document.getElementById('detail-egg-connect').addEventListener('click', function() {
        hideDetailModal();
        switchToSession(sessionId, undefined, s.wing_id);
    });

    var delBtn = document.getElementById('detail-egg-delete');
    if (delBtn) delBtn.addEventListener('click', function() {
        stopInventorySession(s, delBtn, hideDetailModal);
    });
}

export function showSessionInfo() {
    var s = findSessionResource(S.sessionsData, S.ptySessionId, S.ptyWingId);
    var w = S.ptyWingId ? S.wingsData.find(function(w) { return w.wing_id === S.ptyWingId; }) : null;
    if (!s && !w) return;

    var wingName = w ? wingDisplayName(w) : 'unknown';
    var agent = s ? (s.agent || '?') : '?';
    var cwdDisplay = s && s.cwd ? shortenPath(s.cwd) : '~';

    var wingVersion = w ? (w.version || 'unknown') : 'unknown';
    var wingPlatform = w ? (w.platform || 'unknown') : 'unknown';
    var wingAgents = w ? (w.agents || []).join(', ') || 'none' : 'unknown';
    var isOnline = w ? w.online !== false : false;
    var dotClass = isOnline ? 'live' : 'offline';

    var configSummary = '';
    if (s && s.egg_config) {
        var isoMatch = s.egg_config.match(/isolation:\s*(\S+)/);
        var isoLevel = isoMatch ? isoMatch[1] : '?';
        configSummary = '<div class="detail-row"><span class="detail-key">isolation</span>' +
            '<span class="detail-val copyable" data-copy="' + escapeHtml(s.egg_config) + '" title="click to copy full YAML">' +
            escapeHtml(isoLevel) + '</span></div>';
    }

    var e2eStatus = S.e2eKey ? 'active' : 'none';

    DOM.detailDialog.innerHTML =
        '<h3><span class="detail-connection-dot ' + dotClass + '"></span>' + escapeHtml(wingName) + ' &middot; ' + escapeHtml(agent) + '</h3>' +
        '<div class="detail-row"><span class="detail-key">session</span><span class="detail-val text-dim">' + escapeHtml(S.ptySessionId || '') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">cwd</span><span class="detail-val text-dim">' + escapeHtml(cwdDisplay) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">e2e</span><span class="detail-val">' + e2eStatus + '</span></div>' +
        configSummary +
        '<div class="detail-row" style="margin-top:12px"><span class="detail-key" style="font-weight:600">wing</span></div>' +
        '<div class="detail-row"><span class="detail-key">wing</span><span class="detail-val">' + escapeHtml(wingName) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">version</span><span class="detail-val">' + escapeHtml(wingVersion) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">platform</span><span class="detail-val">' + escapeHtml(wingPlatform) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">agents</span><span class="detail-val">' + escapeHtml(wingAgents) + '</span></div>';

    setupCopyable(DOM.detailDialog);
    DOM.detailOverlay.classList.add('open');
}

export function renderDashboard() {
    refreshConversationInventory();
    var visibleWings = S.wingsData.filter(function(w) {
        return wingDisplayName(w);
    });
    if (visibleWings.length > 0) {
        var wingHtml = '<h3 class="section-label">wings</h3><div class="wing-grid">';
        wingHtml += visibleWings.map(function(w) {
            var name = wingDisplayName(w);
            var isUnreachable = w.tunnel_error === 'unreachable' || w.tunnel_error === 'key_mismatch';
            var dotClass = isUnreachable ? 'dot-offline' :
                (w.online === undefined) ? '' : (w.online === true ? 'dot-live' : 'dot-offline');
            var projectCount = (w.projects || []).length;
            var plat = w.platform === 'darwin' ? 'mac' : (w.platform || '');
            var isCardPasskey = w.tunnel_error === 'passkey_required' || w.tunnel_error === 'passkey_failed';
            var isCardNoPasskeys = w.tunnel_error === 'no_passkeys_configured';
            var hasAuth = !!S.tunnelAuthTokens[w.wing_id];
            var userHasPasskeys = S.currentUser && S.currentUser.has_passkeys;
            var needsPasskeySetup = w.locked && !hasAuth && !userHasPasskeys && !isCardPasskey;
            var needsAuth = w.locked && !hasAuth && userHasPasskeys && !needsPasskeySetup;
            var lockedBadge = needsPasskeySetup ? '<span class="wing-pinned-badge wing-badge-passkey">add passkey</span>' :
                (needsAuth || isCardPasskey) ? '<span class="wing-pinned-badge">authenticate</span>' : '';
            var lockIcon = needsPasskeySetup ? '<span class="wing-lock wing-lock-setup" title="add a passkey to unlock">&#x1f511;</span>' :
                ((needsAuth || isCardPasskey || isCardNoPasskeys) ? '<span class="wing-lock" title="passkey required">&#x1f512;</span>' :
                (w.passkey_enrolled && !w.locked ? '<span class="wing-lock" title="passkey active">&#x1f511;</span>' : ''));
            var draggable = ('ontouchstart' in window || navigator.maxTouchPoints > 0) ? '' : ' draggable="true"';
            var isMine = S.currentUser && w.user_id === S.currentUser.id;
            var ownerTag = (w.owner && !isMine) ? '<span class="wing-owner">' + escapeHtml(w.owner) + '</span>' : '';
            return '<div class="wing-box" role="button" tabindex="0" aria-label="Open ' + escapeHtml(name) + '"' + draggable + ' data-wing-id="' + escapeHtml(w.wing_id || '') + '">' +
                '<div class="wing-box-top">' +
                    '<span class="wing-dot ' + dotClass + '"></span>' +
                    '<span class="wing-name">' + escapeHtml(name) + lockIcon + '</span>' +
                    ownerTag +
                '</div>' +
                '<span class="wing-agents">' + ((needsPasskeySetup || needsAuth || isCardPasskey) ? '' : ((w.agents || []).length === 0 ? '<span class="text-dim">no agents</span>' : (w.agents || []).map(function(a) {
                    return agentIcon(a) || escapeHtml(a);
                }).join(' '))) + '</span>' +
                '<div class="wing-statusbar">' +
                    '<span>' + escapeHtml(plat) + '</span><span class="wing-connection-label">' + (w.online === true ? 'online' : w.online === false ? 'offline' : 'checking') + '</span>' +
                    (isUnreachable ? '<span class="text-dim">unreachable</span>' :
                    ((needsPasskeySetup || needsAuth || isCardPasskey) ? lockedBadge : (projectCount ? '<span>' + projectCount + ' proj</span>' : '<span></span>'))) +
                '</div>' +
            '</div>';
        }).join('');
        wingHtml += '</div>';
        DOM.wingStatusEl.innerHTML = wingHtml;

        setupWingDrag();

        DOM.wingStatusEl.querySelectorAll('.wing-box').forEach(function(box) {
            box.addEventListener('keydown', function(event) {
                if (event.target === box && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); box.click(); }
            });
            box.addEventListener('click', function(e) {
                if (e.target.closest('.box-menu-btn')) return;
                var mid = box.dataset.wingId;
                var w = S.wingsData.find(function(w) { return w.wing_id === mid; });
                if (w && w.tunnel_error === 'no_passkeys_configured') {
                    navigateToAccount(true);
                    return;
                }
                // Locked wing without passkeys → go add one
                if (w && w.locked && !S.tunnelAuthTokens[w.wing_id] && !(S.currentUser && S.currentUser.has_passkeys)) {
                    navigateToAccount(true);
                    return;
                }
                // Locked wing with passkeys (or passkey_required from probe) → authenticate
                if (w && ((w.locked && !S.tunnelAuthTokens[w.wing_id]) || w.tunnel_error === 'passkey_required' || w.tunnel_error === 'passkey_failed')) {
                    // Passkey auth on dashboard — unlock in-place, don't navigate
                    var badge = box.querySelector('.wing-pinned-badge');
                    if (badge) badge.textContent = 'authenticating...';
                    sendTunnelRequest(mid, { type: 'wing.info' })
                        .then(function(data) {
                            w.hostname = data.hostname || w.hostname;
                            w.platform = data.platform || w.platform;
                            w.version = data.version || w.version;
                            w.agents = data.agents || [];
                            w.projects = data.projects || [];
                            w.locked = data.locked || false;
                            w.allowed_count = data.allowed_count || 0;
                            w.passkey_enrolled = !!data.passkey_enrolled;
                            delete w.tunnel_error;
                            rebuildAgentLists();
                            renderDashboard();
                            if (shouldFetchWingSessions(S.currentUser, w)) {
                                fetchWingSessions(mid).then(function(sessions) {
                                    if (sessions) {
                                        mergeWingSessions(mid, sessions);
                                        renderSidebar();
                                        renderDashboard();
                                    }
                                });
                            }
                        })
                        .catch(function() {
                            w.tunnel_error = 'passkey_failed';
                            if (badge) badge.textContent = 'authenticate';
                        });
                    return;
                }
                navigateToWingDetail(mid);
            });
            box.style.cursor = 'pointer';
        });
    } else {
        DOM.wingStatusEl.innerHTML = '';
    }

    renderChannelBanner();
    initSessionInventoryControls();
    renderSessionInventory();
}

function visibleInventorySessions() {
    return S.sessionsData.filter(function(session) {
        return sessionIsSelected(session, S.ptySessionId, S.ptyWingId) || isWingVisible(session.wing_id);
    });
}

function initSessionInventoryControls() {
    var controls = document.getElementById('session-inventory-controls');
    if (!controls || controls.dataset.bound) return;
    controls.dataset.bound = '1';
    ['query', 'wing', 'agent', 'status'].forEach(function(key) {
        var input = document.getElementById('session-inventory-' + (key === 'query' ? 'search' : key));
        input.addEventListener(key === 'query' ? 'input' : 'change', function() {
            inventoryFilters[key] = input.value;
            renderSessionInventory();
        });
    });
    document.getElementById('session-inventory-clear').addEventListener('click', function() {
        inventoryFilters = { query: '', wing: '', agent: '', status: '' };
        ['search', 'wing', 'agent', 'status'].forEach(function(key) {
            document.getElementById('session-inventory-' + key).value = '';
        });
        renderSessionInventory();
        document.getElementById('session-inventory-search').focus();
    });
    document.getElementById('session-inventory-search').addEventListener('keydown', function(event) {
        if (event.key === 'ArrowDown') {
            var first = DOM.sessionsList.querySelector('.egg-box');
            if (first) { event.preventDefault(); first.focus(); }
        } else if (event.key === 'Escape') {
            event.preventDefault();
            this.value = '';
            inventoryFilters.query = '';
            renderSessionInventory();
        }
    });
}

function updateInventoryOptions(id, entries, allLabel, value) {
    var select = document.getElementById(id);
    if (!select) return;
    // Replacing options only when they changed preserves a focused native menu.
    if (value && !entries.some(function(entry) { return entry.value === value; })) entries.push({ value: value, label: value + ' (unavailable)' });
    var html = '<option value="">' + allLabel + '</option>' + entries.map(function(entry) {
        return '<option value="' + escapeHtml(entry.value) + '">' + escapeHtml(entry.label) + '</option>';
    }).join('');
    if (select.dataset.options !== html) {
        select.innerHTML = html;
        select.dataset.options = html;
    }
    select.value = value;
    // A filter with one choice cannot narrow anything; keep an active one clearable.
    if (select.closest('label')) select.closest('label').hidden = entries.length < 2 && !value;
}

export function renderSessionInventory() {
    // Preserve the active editor while background status probes finish.
    if (DOM.sessionsList.querySelector('.renaming, .forking')) return;
    var focus = captureSessionFocus(DOM.sessionsList, document.activeElement);
    var allSessions = visibleInventorySessions();
    var hasWings = S.wingsData.some(function(wing) { return wingDisplayName(wing); });
    DOM.emptyState.style.display = allSessions.length ? 'none' : '';
    var noWings = document.getElementById('empty-no-wings');
    var noSessions = document.getElementById('empty-no-sessions');
    if (noWings) noWings.style.display = !allSessions.length && !hasWings ? '' : 'none';
    if (noSessions) noSessions.style.display = !allSessions.length && hasWings ? '' : 'none';
    var signedIn = document.getElementById('empty-signed-in');
    if (signedIn && S.currentUser) {
        var account = S.currentUser.email || S.currentUser.display_name || '';
        signedIn.textContent = account ? 'signed in as ' + account : '';
        signedIn.style.display = account ? '' : 'none';
    }
    var controls = document.getElementById('session-inventory-controls');
    if (controls) controls.style.display = allSessions.length ? '' : 'none';
    if (!allSessions.length) { DOM.sessionsList.innerHTML = ''; return; }

    updateInventoryOptions('session-inventory-wing', S.wingsData.filter(function(wing) {
        return allSessions.some(function(session) { return session.wing_id === wing.wing_id; });
    }).map(function(wing) { return { value: wing.wing_id, label: wingDisplayName(wing) || wing.wing_id }; }), 'all wings', inventoryFilters.wing);
    updateInventoryOptions('session-inventory-agent', Array.from(new Set(allSessions.map(function(session) {
        return session.agent;
    }).filter(Boolean))).sort().map(function(agent) { return { value: agent, label: agent }; }), 'all agents', inventoryFilters.agent);
    var sessions = filterSessionInventory(allSessions, S.wingsData, S.sessionNotifications, inventoryFilters);
    var unseen = unseenSessionCompletions(browserLocalStorage(), S.currentUser && S.currentUser.id);
    var count = document.getElementById('session-inventory-count');
    var filtered = Object.values(inventoryFilters).some(Boolean);
    var attention = sessions.filter(function(session) {
        return sessionInventoryState(session, sessionWing(session), notificationForSession(S.sessionNotifications, session)).attention;
    }).length;
    if (count) count.textContent = (filtered ? sessions.length + ' of ' : '') + allSessions.length + (attention ? ' · ' + attention + (attention === 1 ? ' needs' : ' need') + ' attention' : '');
    var clear = document.getElementById('session-inventory-clear');
    if (clear) { clear.disabled = !filtered; clear.hidden = !filtered; }
    if (!sessions.length) {
        DOM.sessionsList.innerHTML = '<div class="inventory-no-results" role="status">No sessions match these filters. Clear filters to see every session.</div>';
        return;
    }

    function renderEggCard(session) {
        var name = sessionDisplayName(session);
        var wing = sessionWing(session);
        var state = sessionInventoryState(session, wing, notificationForSession(S.sessionNotifications, session));
        var actions = sessionInventoryActions(session, wing, S.currentUser);
        var selected = S.activeView === 'terminal' && sessionIsSelected(session, S.ptySessionId, S.ptyWingId);
        var thumbnail = '';
        try { thumbnail = readSessionContent(localStorage, TERM_THUMB_PREFIX, session.wing_id, session.id) || ''; } catch (error) {}
        thumbnail = safeTerminalThumbnail(thumbnail);
        var sid = escapeHtml(session.id);
        var role = session.conversation_role;
        var label = name + ' · ' + (session.agent || 'unknown agent') + ' · ' + (wing && wingDisplayName(wing) || 'unknown wing');
        var resourceKey = sessionResourceKey(session);
        var error = sessionActionErrors.get(resourceKey);
        var unread = unseen.has(resourceKey);
        return '<article class="egg-box inventory-session' + (selected ? ' selected' : '') + '" data-blocked="' + (state.status === 'blocked') + '" role="group" tabindex="0" data-sid="' + sid + '" data-wing-id="' + escapeHtml(session.wing_id || '') + '" data-kind="' + escapeHtml(session.kind || 'terminal') + '" aria-label="' + escapeHtml(label + ' · ' + state.connectionLabel + ' · ' + state.agentLabel + (unread ? ' · unseen completion' : '')) + '"' + (selected ? ' aria-current="page"' : '') + '>' +
            (thumbnail ? '<div class="egg-preview"><img src="' + thumbnail + '" alt="" loading="lazy"></div>' : '') +
            '<div class="egg-footer">' + sessionStatusDot(state.status) +
            '<span class="egg-label tab-label">' + escapeHtml(name) + '</span>' +
            (role ? '<span class="session-role">' + escapeHtml(role) + '</span>' : '') +
            (actions.stop ? '<button class="btn-sm btn-danger inventory-stop" type="button" data-session-action="stop" title="Stop session"' + (sessionStopPending.has(resourceKey) ? ' disabled' : '') + '>' + (sessionStopPending.has(resourceKey) ? 'stopping…' : (sessionStopConfirm.get(resourceKey) || 0) > Date.now() ? 'stop now?' : 'stop') + '</button>' : '') + '</div>' +
            '<div class="inventory-session-meta">' + agentWithIcon(session.agent || '?') + '<span>·</span><span class="inventory-status status-' + state.tone + '">' + escapeHtml(state.agentLabel) + '</span></div>' +
            // The group header names the wing and project; repeat only what differs.
            (S.currentUser && S.currentUser.roost_mode && session.user_id !== S.currentUser.id ? '<div class="inventory-session-owner">' + escapeHtml(session.email || 'unknown owner') + '</div>' : '') +
            ((session.cwd || '').replace(/\/+$/, '') !== sessionProjectRoot(session, wing) ? '<div class="inventory-session-path" title="' + escapeHtml(session.cwd || '') + '">' + escapeHtml(shortenPath(session.cwd || '~')) + '</div>' : '') +
            (unread || state.connection !== 'available' ? '<div class="inventory-session-state">' + unseenCompletionBadge(unread) + (state.connection !== 'available' ? '<span>' + escapeHtml(state.connectionLabel) + '</span>' : '') + '</div>' : '') +
            '<div class="inventory-session-actions">' +
                '<button class="btn-sm btn-accent inventory-attach" type="button" data-session-action="attach" title="' + escapeHtml(actions.attach ? 'Open session' : state.connectionLabel) + '"' + (!actions.attach ? ' disabled' : '') + '>attach</button>' +
                '<button class="btn-sm inventory-details" type="button" data-session-action="details" title="Session details">details</button>' +
                (actions.rename ? '<button class="btn-sm inventory-rename" type="button" data-session-action="rename" title="Rename session">rename</button>' : '') +
                sessionForkControl(session, wing, S.currentUser) +
            '</div><div class="inventory-action-status" role="status">' + escapeHtml(error || '') + '</div></article>';
    }

    var groups = groupSessionInventory(sessions, S.wingsData, S.sessionNotifications, unseen);
    DOM.sessionsList.innerHTML = groups.map(function(group) {
        return '<section class="inventory-project-group" data-group-key="' + escapeHtml(group.key) + '" data-blocked="' + (group.rollup.blocked > 0) + '">' + sessionGroupHeader(group) + '<div class="egg-grid">' + group.sessions.map(renderEggCard).join('') + '</div></section>';
    }).join('');
    bindGroupAcknowledgements(DOM.sessionsList, groups);
    var cards = Array.from(DOM.sessionsList.querySelectorAll('.egg-box'));
    cards.forEach(function(card) {
        var session = findSessionResource(S.sessionsData, card.dataset.sid, card.dataset.wingId);
        function attach() {
            if (sessionInventoryActions(session, sessionWing(session), S.currentUser).attach) switchToSession(session.id, undefined, session.wing_id);
        }
        card.addEventListener('click', function(event) { if (!event.target.closest('button, input')) attach(); });
        card.addEventListener('keydown', function(event) {
            if (event.target !== card) return;
            if (navigateSessionRows(event, cards, card)) return;
            if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); attach(); }
        });
        card.querySelector('.inventory-attach').addEventListener('click', attach);
        card.querySelector('.inventory-details').addEventListener('click', function() { showEggDetail(session.id, session.wing_id); });
        var rename = card.querySelector('.inventory-rename');
        var fork = card.querySelector('.session-fork-btn');
        if (fork) fork.addEventListener('click', function() { beginSessionFork(card, session, session.wing_id, fork); });
        if (rename) rename.addEventListener('click', function() { beginSessionRename(card, session); });
        var stop = card.querySelector('.inventory-stop');
        if (stop) stop.addEventListener('click', function() { stopInventorySession(session, stop); });
    });
    setupEggDrag();
    if (focus && !restoreSessionFocus(DOM.sessionsList, focus)) document.getElementById('session-inventory-search').focus({ preventScroll: true });
}

function bindGroupAcknowledgements(container, groups) {
    container.querySelectorAll('.inventory-project-group').forEach(function(section) {
        var button = section.querySelector('.inventory-acknowledge');
        if (!button) return;
        var group = groups.find(function(group) { return group.key === section.dataset.groupKey; });
        button.addEventListener('click', function() {
            acknowledgeSessionCompletions(browserLocalStorage(), S.currentUser && S.currentUser.id, group.sessions);
            renderSidebar();
            if (S.activeView === 'home') renderSessionInventory();
            refreshConversationInventory();
            var first = group.sessions[0];
            var row = Array.from(container.querySelectorAll('[data-sid]')).find(function(row) { return row.dataset.sid === first.id && row.dataset.wingId === first.wing_id; });
            if (row) row.focus({ preventScroll: true });
        });
    });
}

function stopInventorySession(session, button, onStopped) {
    var key = sessionResourceKey(session);
    if (!sessionInventoryActions(session, sessionWing(session), S.currentUser).stop || sessionStopPending.has(key)) return;
    if ((sessionStopConfirm.get(key) || 0) <= Date.now()) {
        sessionStopConfirm.set(key, Date.now() + 4000);
        button.textContent = 'stop now?';
        button.classList.add('btn-armed');
        setTimeout(function() {
            if ((sessionStopConfirm.get(key) || 0) <= Date.now() && S.activeView === 'home') renderSessionInventory();
        }, 4100);
        return;
    }
    sessionStopConfirm.delete(key);
    sessionStopPending.add(key);
    sessionActionErrors.delete(key);
    button.disabled = true;
    button.textContent = 'stopping…';
    // Remove the row only after the wing acknowledges the stop. A failed request
    // must leave the session inspectable instead of optimistically losing it.
    sendTunnelRequest(session.wing_id, { type: 'pty.kill', session_id: session.id }).then(function() {
        sessionStopPending.delete(key);
        var selected = sessionIsSelected(session, S.ptySessionId, S.ptyWingId);
        deleteSession(session.id, true, session.wing_id);
        if (onStopped) onStopped();
        if (selected) showHome();
    }).catch(function(error) {
        sessionStopPending.delete(key);
        sessionActionErrors.set(key, (error && error.message) || 'Could not stop the session. Try again.');
        button.disabled = false;
        button.textContent = 'retry stop';
        button.title = sessionActionErrors.get(key);
        renderSessionInventory();
    });
}

import { saveWingCache } from './data.js';

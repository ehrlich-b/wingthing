import { S, DOM, TERM_THUMB_PREFIX } from './state.js';
import { escapeHtml, wingDisplayName, shortenPath, projectName, formatRelativeTime, semverCompare, nestedRepoCount, agentIcon, agentWithIcon, dirParent, setupCopyable } from './helpers.js';
import { identityPubKey } from './crypto.js';
import { sendTunnelRequest } from './tunnel.js';
import { switchToSession } from './nav.js';
import { showHome, navigateToWingDetail } from './nav.js';
import { connectPTY } from './pty.js';
import { setLastTermAgent, getLastTermAgent, setWingOrder, setEggOrder, getCachedWingSessions, setCachedWingSessions } from './data.js';
import { openAuditReplay, openAuditKeylog } from './audit.js';
import { showTerminal } from './nav.js';

function wingNameById(wingId) {
    var wing = S.wingsData.find(function(w) { return w.id === wingId; });
    return wing ? wingDisplayName(wing) : '';
}

export function renderSidebar() {
    var tabs = S.sessionsData.filter(function(s) { return (s.kind || 'terminal') !== 'chat'; }).map(function(s) {
        var name = projectName(s.cwd);
        var letter = name.charAt(0).toUpperCase();
        var isActive = (S.activeView === 'terminal' && s.id === S.ptySessionId);
        var needsAttention = S.sessionNotifications[s.id];
        var dotClass = s.status === 'active' ? 'dot-live' : 'dot-detached';
        if (needsAttention) dotClass = 'dot-attention';
        return '<button class="session-tab' + (isActive ? ' active' : '') + '" ' +
            'title="' + escapeHtml(name + ' \u00b7 ' + (s.agent || '?')) + '" ' +
            'data-sid="' + s.id + '">' +
            '<span class="tab-letter">' + escapeHtml(letter) + '</span>' +
            '<span class="tab-dot ' + dotClass + '"></span>' +
        '</button>';
    }).join('');
    DOM.sessionTabs.innerHTML = tabs;

    DOM.sessionTabs.querySelectorAll('.session-tab').forEach(function(tab) {
        tab.addEventListener('click', function() {
            var sid = tab.dataset.sid;
            if (sid === S.ptySessionId && S.activeView === 'terminal') return;
            switchToSession(sid);
        });
    });
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
    var grid = DOM.sessionsList.querySelector('.egg-grid');
    if (!grid) return;
    var cards = grid.querySelectorAll('.egg-box');
    var dragSrc = null;

    cards.forEach(function(card) {
        card.setAttribute('draggable', 'true');
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

    // Touch drag (mobile)
    var touchSrc = null;

    cards.forEach(function(card) {
        card.addEventListener('touchstart', function(e) {
            if (e.target.closest('.egg-delete')) return;
            touchSrc = card;
            card.classList.add('dragging');
        }, { passive: true });
    });

    grid.addEventListener('touchmove', function(e) {
        if (!touchSrc) return;
        e.preventDefault();
        var touch = e.touches[0];
        var target = document.elementFromPoint(touch.clientX, touch.clientY);
        var targetCard = target ? target.closest('.egg-box') : null;
        cards.forEach(function(c) { c.classList.remove('drag-over'); });
        if (targetCard && targetCard !== touchSrc) {
            targetCard.classList.add('drag-over');
        }
    }, { passive: false });

    grid.addEventListener('touchend', function(e) {
        if (!touchSrc) return;
        var touch = e.changedTouches[0];
        var target = document.elementFromPoint(touch.clientX, touch.clientY);
        var targetCard = target ? target.closest('.egg-box') : null;
        cards.forEach(function(c) { c.classList.remove('drag-over'); });
        touchSrc.classList.remove('dragging');
        if (targetCard && targetCard !== touchSrc) {
            if (touchSrc.compareDocumentPosition(targetCard) & Node.DOCUMENT_POSITION_FOLLOWING) {
                grid.insertBefore(touchSrc, targetCard.nextSibling);
            } else {
                grid.insertBefore(touchSrc, targetCard);
            }
            saveEggOrder();
        }
        touchSrc = null;
    }, { passive: true });
}

function saveEggOrder() {
    var order = [];
    DOM.sessionsList.querySelectorAll('.egg-box').forEach(function(card) {
        if (card.dataset.sid) order.push(card.dataset.sid);
    });
    setEggOrder(order);
    var byId = {};
    S.sessionsData.forEach(function(s) { byId[s.id] = s; });
    var reordered = [];
    order.forEach(function(sid) { if (byId[sid]) reordered.push(byId[sid]); });
    S.sessionsData.forEach(function(s) { if (order.indexOf(s.id) === -1) reordered.push(s); });
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

    if (tier === 'free') {
        html += '<button class="btn-sm btn-accent" id="account-upgrade">give me pro</button>';
    } else if (S.currentUser.personal_pro) {
        html += '<button class="btn-sm" id="account-downgrade" style="color:var(--text-dim)">cancel pro</button>';
    } else {
        html += '<span class="text-dim" style="font-size:12px">pro via org</span>';
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

    // Org section
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

    var createForm = document.getElementById('ac-create-form');
    document.getElementById('ac-create-toggle').addEventListener('click', function() {
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
    loadAccountPasskeys();

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
        html += '<div class="detail-row"><span class="detail-val text-dim">no active plan</span></div>' +
            '<div class="ac-form-row">' +
                '<input type="number" class="ac-input ac-input-sm" id="org-seats-input-' + oid + '" min="1" value="5">' +
                '<span class="ac-hint">seats</span>' +
                '<div class="ac-plan-toggle" id="org-plan-toggle-' + oid + '">' +
                    '<button class="ac-plan-opt active" data-plan="team_yearly">yearly</button>' +
                    '<button class="ac-plan-opt" data-plan="team_monthly">monthly</button>' +
                '</div>' +
                '<button class="btn-sm btn-accent org-give-seats-btn" data-oid="' + oid + '">give me seats</button>' +
            '</div>' +
            '<div class="ac-hint" style="margin-top:4px">1 seat includes you. each additional seat adds one team member.</div>' +
            '<div class="ac-cancel-row"><button class="btn-sm org-delete-btn" data-oid="' + oid + '">delete org</button></div>';
        return html;
    }

    html += '<div class="detail-row"><span class="detail-key">plan</span><span class="detail-val">' + escapeHtml(org.plan || 'team') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">seats</span><span class="detail-val">' + (org.seats_used || 0) + '/' + (org.seats_total || 0) + ' used</span></div>';

    html += '<div class="ac-form-row">' +
        '<input type="number" class="ac-input ac-input-sm" id="org-add-seats-input-' + oid + '" min="' + ((org.seats_total || 0) + 1) + '" value="' + ((org.seats_total || 0) + 1) + '">' +
        '<span class="ac-hint">new total</span>' +
        '<button class="btn-sm btn-accent org-add-seats-btn" data-oid="' + oid + '">add seats</button>' +
    '</div>';

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

    var activeSessions = S.sessionsData.filter(function(s) { return s.wing_id === w.id; });
    var activeHtml = '';
    if (activeSessions.length > 0) {
        activeHtml = '<div class="wd-section"><h3 class="section-label">active sessions</h3><div class="wd-sessions" id="wd-active-sessions">';
        activeHtml += renderActiveSessionRows(activeSessions);
        activeHtml += '</div></div>';
    }

    var html =
        '<div class="wd-page">' +
        '<div class="wd-header">' +
            '<a class="wd-back" id="wd-back">back</a>' +
        '</div>' +
        (updateAvailable ? '<div class="wd-update-banner" id="wd-update">' +
            escapeHtml(S.latestVersion) + ' available (you have ' + escapeHtml(ver) + ') <span class="wd-update-action">update now</span>' +
        '</div>' : '') +
        '<div class="wd-hero">' +
            '<div class="wd-hero-top">' +
                '<span class="session-dot ' + (isOnline ? 'live' : 'offline') + '"></span>' +
                '<span class="wd-name" id="wd-name" title="click to rename">' + escapeHtml(name) + '</span>' +
                (w.pinned ? '<span class="wd-pinned-badge" title="passkey required">&#x1f512; pinned</span>' : '') +
                (w.wing_label ? '<a class="wd-clear-label" id="wd-delete-label" title="clear name">x</a>' : '') +
                (!isOnline ? '<a class="wd-dismiss-link" id="wd-dismiss">remove</a>' : '') +
            '</div>' +
        '</div>' +
        (isOnline ? '<div class="wd-palette">' +
            '<input id="wd-search" type="text" class="wd-search" placeholder="' + (w.pinned && !S.tunnelAuthTokens[wingId] ? 'start a session (passkey auth on first browse)...' : 'start a session...') + '" autocomplete="off" spellcheck="false">' +
            '<div id="wd-search-results" class="wd-search-results"></div>' +
            '<div id="wd-search-status" class="wd-search-status"></div>' +
        '</div>' : '') +
        activeHtml +
        '<div class="wd-section"><h3 class="section-label">session history</h3><div id="wd-past-sessions"><span class="text-dim">' + (isOnline ? 'loading...' : 'wing offline') + '</span></div></div>' +
        '<div class="wd-info">' +
            '<div class="detail-row"><span class="detail-key">scope</span><span class="detail-val">' + scopeHtml + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">platform</span><span class="detail-val">' + escapeHtml(w.platform || 'unknown') + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">version</span><span class="detail-val">' + escapeHtml(ver || 'unknown') + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">agents</span><span class="detail-val">' + escapeHtml((w.agents || []).join(', ') || 'none') + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">public key</span>' + pubKeyHtml + '</div>' +
            '<div class="detail-row"><span class="detail-key">projects</span><div class="detail-val">' + projList + '</div></div>' +
        '</div>' +
        (isOnline ? '<div class="wd-section"><h3 class="section-label">access control</h3>' +
            '<div id="wd-pins"><span class="text-dim">loading...</span></div>' +
            '<div class="wd-pin-actions">' +
                '<button class="btn-sm btn-accent" id="wd-pin-me">pin me</button>' +
            '</div>' +
        '</div>' : '') +
        '</div>';

    DOM.wingDetailContent.innerHTML = html;
    setupCopyable(DOM.wingDetailContent);

    document.getElementById('wd-back').addEventListener('click', function() { showHome(); });

    var nameEl = document.getElementById('wd-name');
    nameEl.addEventListener('click', function() {
        var current = w.wing_label || w.hostname || w.wing_id.substring(0, 8);
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
            setCachedWings(S.wingsData.map(function(ww) {
                return { wing_id: ww.wing_id, hostname: ww.hostname, platform: ww.platform, version: ww.version, agents: ww.agents, labels: ww.labels, projects: ww.projects, online: ww.online, wing_label: ww.wing_label };
            }));
            showHome();
        });
    }

    wireActiveSessionRows();

    var pinnedLocked = w.pinned && !S.tunnelAuthTokens[wingId];
    if (isOnline && !pinnedLocked) {
        loadWingPastSessions(wingId, 0);
    } else if (pinnedLocked) {
        var pastEl = document.getElementById('wd-past-sessions');
        if (pastEl) pastEl.innerHTML = '<span class="text-dim">authenticate to view session history</span>';
    }

    if (isOnline) {
        setupWingPalette(w);
        loadWingPins(w);
    }
}

function renderActiveSessionRows(sessions) {
    return sessions.map(function(s) {
        var sName = projectName(s.cwd);
        var sDot = s.status === 'active' ? 'live' : 'detached';
        var kind = s.kind || 'terminal';
        var auditBadge = s.audit ? '<span class="wd-audit-badge">audit</span>' : '';
        return '<div class="wd-session-row" data-sid="' + s.id + '" data-kind="' + kind + '" data-agent="' + escapeHtml(s.agent || 'claude') + '">' +
            '<span class="session-dot ' + sDot + '"></span>' +
            '<span class="wd-session-name">' + escapeHtml(sName) + ' \u00b7 ' + agentWithIcon(s.agent || '?') + '</span>' +
            auditBadge +
            '<button class="wd-kill-btn" data-sid="' + s.id + '" title="kill session">x</button>' +
        '</div>';
    }).join('');
}

function wireActiveSessionRows() {
    DOM.wingDetailContent.querySelectorAll('.wd-session-row').forEach(function(row) {
        row.addEventListener('click', function(e) {
            if (e.target.classList.contains('wd-kill-btn')) return;
            var sid = row.dataset.sid;
            switchToSession(sid);
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
                        S.sessionsData = S.sessionsData.filter(function(s) { return s.id !== sid; });
                        updateWingDetailSessions(wingId);
                        loadWingPastSessions(wingId, 0);
                    }).catch(function() {});
            } else {
                btn.dataset.confirming = '1';
                btn.textContent = 'sure?';
                btn.classList.add('confirming');
                setTimeout(function() {
                    delete btn.dataset.confirming;
                    btn.textContent = 'x';
                    btn.classList.remove('confirming');
                }, 3000);
            }
        });
    });
}

function updateWingDetailSessions(wingId) {
    var w = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!w) return;
    var container = document.getElementById('wd-active-sessions');
    var activeSessions = S.sessionsData.filter(function(s) { return s.wing_id === w.id; });
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
        statusEl.innerHTML = '<span class="accent">' + agentWithIcon(currentAgent()) + '</span>' +
            (agents.length > 1 ? ' <span class="text-dim">(shift+tab to switch)</span>' : '');
    }
    renderStatus();

    if (wing.wing_id && !wing.pinned) {
        sendTunnelRequest(wing.wing_id, { type: 'dir.list', path: '~/' }).then(function(data) {
            var entries = data.entries || [];
            if (Array.isArray(entries)) {
                wpHomeDirCache = entries.map(function(e) {
                    return { name: e.name, path: e.path, isDir: e.is_dir };
                });
            }
        }).catch(function() {});
    }

    function renderResults(filter) {
        var wingId = wing.id || '';
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
        showTerminal();
        connectPTY(agent, validCwd, wing.id);
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

function loadWingPins(wing) {
    var container = document.getElementById('wd-pins');
    var pinBtn = document.getElementById('wd-pin-me');
    if (!container) return;

    sendTunnelRequest(wing.wing_id, { type: 'pins.list' }).then(function(data) {
        var pins = data.pins || [];
        if (pins.length === 0) {
            container.innerHTML = '<span class="text-dim">no pinned users — anyone with wing access can connect</span>';
        } else {
            var html = pins.map(function(p) {
                var display = p.email || p.user_id || '(key-only)';
                var keyShort = p.key ? p.key.substring(0, 12) + '...' : 'none';
                return '<div class="wd-pin-row">' +
                    '<span class="wd-pin-email">' + escapeHtml(display) + '</span>' +
                    '<span class="wd-pin-key text-dim">pk: ' + escapeHtml(keyShort) + '</span>' +
                    '<button class="btn-sm btn-danger wd-pin-remove" data-pin-uid="' + escapeHtml(p.user_id || '') + '" data-pin-key="' + escapeHtml(p.key || '') + '">remove</button>' +
                '</div>';
            }).join('');
            container.innerHTML = html;

            // Check if current user is already pinned
            var myId = S.currentUser.id || '';
            var alreadyPinned = pins.some(function(p) { return p.user_id === myId; });
            if (pinBtn && alreadyPinned) {
                pinBtn.textContent = 'pinned';
                pinBtn.disabled = true;
            }

            // Wire remove buttons
            container.querySelectorAll('.wd-pin-remove').forEach(function(btn) {
                btn.addEventListener('click', function() {
                    var uid = btn.getAttribute('data-pin-uid');
                    var key = btn.getAttribute('data-pin-key');
                    btn.textContent = '...';
                    btn.disabled = true;
                    sendTunnelRequest(wing.wing_id, { type: 'pins.remove', pin_user_id: uid, key: key })
                        .then(function() { loadWingPins(wing); })
                        .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
                });
            });
        }
    }).catch(function() {
        container.innerHTML = '<span class="text-dim">could not load pins</span>';
    });

    // Wire "Pin me" button
    if (pinBtn) {
        pinBtn.addEventListener('click', function() {
            pinBtn.textContent = 'pinning...';
            pinBtn.disabled = true;

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
                return sendTunnelRequest(wing.wing_id, { type: 'pins.add', key: keyB64 });
            }).then(function(resp) {
                if (resp.error) {
                    pinBtn.textContent = resp.error;
                    pinBtn.disabled = false;
                    return;
                }
                pinBtn.textContent = 'pinned';
                loadWingPins(wing);
            }).catch(function() {
                // Passkey creation failed — pin by user ID only
                sendTunnelRequest(wing.wing_id, { type: 'pins.add' })
                    .then(function(resp) {
                        if (resp.error) {
                            pinBtn.textContent = resp.error;
                            pinBtn.disabled = false;
                            return;
                        }
                        pinBtn.textContent = 'pinned (no passkey)';
                        loadWingPins(wing);
                    })
                    .catch(function() {
                        pinBtn.textContent = 'failed';
                        pinBtn.disabled = false;
                    });
            });
        });
    }
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

    sendTunnelRequest(wingId, { type: 'sessions.history', offset: offset, limit: limit })
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
    var html = sessions.map(function(s) {
        var name = s.cwd ? projectName(s.cwd) : s.session_id.substring(0, 8);
        var startStr = s.started_at ? formatRelativeTime(s.started_at * 1000) : '';
        var auditBadge = s.audit ? '<span class="wd-audit-badge">audit</span>' : '';
        var auditBtns = s.audit
            ? '<button class="btn-sm wd-replay-btn" data-sid="' + escapeHtml(s.session_id) + '">replay</button>' +
              '<button class="btn-sm wd-keylog-btn" data-sid="' + escapeHtml(s.session_id) + '">keylog</button>'
            : '';
        return '<div class="wd-past-row">' +
            '<span class="wd-past-name">' + escapeHtml(name) + ' \u00b7 ' + escapeHtml(s.agent || '?') + '</span>' +
            '<span class="wd-past-time text-dim">' + startStr + '</span>' +
            auditBadge +
            auditBtns +
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
}

export function showEggDetail(sessionId) {
    var s = S.sessionsData.find(function(s) { return s.id === sessionId; });
    if (!s) return;
    var name = projectName(s.cwd);
    var kind = s.kind || 'terminal';
    var wingName = '';
    if (s.wing_id) {
        var wing = S.wingsData.find(function(w) { return w.id === s.wing_id; });
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
        '<div class="detail-row"><span class="detail-key">status</span><span class="detail-val">' + escapeHtml(s.status || 'unknown') + '</span></div>' +
        configSummary +
        '<div class="detail-actions">' +
            '<button class="btn-sm btn-accent" id="detail-egg-connect">connect</button>' +
            '<button class="btn-sm btn-danger" id="detail-egg-delete">delete session</button>' +
        '</div>';

    setupCopyable(DOM.detailDialog);
    DOM.detailOverlay.classList.add('open');

    document.getElementById('detail-egg-connect').addEventListener('click', function() {
        hideDetailModal();
        switchToSession(sessionId);
    });

    var delBtn = document.getElementById('detail-egg-delete');
    delBtn.addEventListener('click', function() {
        if (delBtn.dataset.armed) {
            hideDetailModal();
            window._deleteSession(sessionId);
        } else {
            delBtn.dataset.armed = '1';
            delBtn.textContent = 'are you sure?';
            delBtn.classList.add('btn-armed');
        }
    });
}

export function showSessionInfo() {
    var s = S.sessionsData.find(function(s) { return s.id === S.ptySessionId; });
    var w = S.ptyWingId ? S.wingsData.find(function(w) { return w.id === S.ptyWingId; }) : null;
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
    if (S.wingsData.length > 0) {
        var wingHtml = '<h3 class="section-label">wings</h3><div class="wing-grid">';
        wingHtml += S.wingsData.map(function(w) {
            var name = wingDisplayName(w);
            var dotClass = w.online !== false ? 'dot-live' : 'dot-offline';
            var projectCount = (w.projects || []).length;
            var plat = w.platform === 'darwin' ? 'mac' : (w.platform || '');
            var pinnedBadge = w.pinned ? '<span class="wing-pinned-badge">pinned</span>' : '';
            var lockIcon = w.pinned ? '<span class="wing-lock" title="passkey required">&#x1f512;</span>' : '';
            return '<div class="wing-box" draggable="true" data-wing-id="' + escapeHtml(w.wing_id || '') + '">' +
                '<div class="wing-box-top">' +
                    '<span class="wing-dot ' + dotClass + '"></span>' +
                    '<span class="wing-name">' + escapeHtml(name) + lockIcon + '</span>' +
                '</div>' +
                '<span class="wing-agents">' + (w.agents || []).map(function(a) { return agentIcon(a) || escapeHtml(a); }).join(' ') + '</span>' +
                '<div class="wing-statusbar">' +
                    '<span>' + escapeHtml(plat) + '</span>' +
                    (w.pinned ? pinnedBadge : (projectCount ? '<span>' + projectCount + ' proj</span>' : '<span></span>')) +
                '</div>' +
            '</div>';
        }).join('');
        wingHtml += '</div>';
        DOM.wingStatusEl.innerHTML = wingHtml;

        setupWingDrag();

        DOM.wingStatusEl.querySelectorAll('.wing-box').forEach(function(box) {
            box.addEventListener('click', function(e) {
                if (e.target.closest('.box-menu-btn')) return;
                var mid = box.dataset.wingId;
                navigateToWingDetail(mid);
            });
            box.style.cursor = 'pointer';
        });
    } else {
        DOM.wingStatusEl.innerHTML = '';
    }

    var hasSessions = S.sessionsData.length > 0;
    var hasWings = S.wingsData.length > 0;
    DOM.emptyState.style.display = hasSessions ? 'none' : '';
    var noWingsEl = document.getElementById('empty-no-wings');
    var noSessionsEl = document.getElementById('empty-no-sessions');
    if (noWingsEl) noWingsEl.style.display = (!hasSessions && !hasWings) ? '' : 'none';
    if (noSessionsEl) noSessionsEl.style.display = (!hasSessions && hasWings) ? '' : 'none';

    if (!hasSessions) {
        DOM.sessionsList.innerHTML = '';
        return;
    }

    var eggHtml = '<h3 class="section-label">eggs</h3><div class="egg-grid">';
    eggHtml += S.sessionsData.map(function(s) {
        var name = projectName(s.cwd);
        var isActive = s.status === 'active';
        var kind = s.kind || 'terminal';
        var needsAttention = S.sessionNotifications[s.id];
        var dotClass = isActive ? 'live' : 'detached';
        if (needsAttention) dotClass = 'attention';

        var previewHtml = '';
        var thumbUrl = '';
        try { thumbUrl = localStorage.getItem(TERM_THUMB_PREFIX + s.id) || ''; } catch(e) {}
        if (thumbUrl) previewHtml = '<img src="' + thumbUrl + '" alt="">';

        var wingName = '';
        if (s.wing_id) {
            var wing = S.wingsData.find(function(w) { return w.id === s.wing_id; });
            if (wing) wingName = wingDisplayName(wing);
        }

        return '<div class="egg-box" data-sid="' + s.id + '" data-kind="' + kind + '" data-agent="' + escapeHtml(s.agent || 'claude') + '">' +
            '<div class="egg-preview">' + previewHtml + '</div>' +
            '<div class="egg-footer">' +
                '<span class="session-dot ' + dotClass + '"></span>' +
                '<span class="egg-label">' + escapeHtml(name) + ' \u00b7 ' + agentWithIcon(s.agent || '?') +
                    (needsAttention ? ' \u00b7 !' : '') + '</span>' +
                '<button class="box-menu-btn" title="details">\u22ef</button>' +
                '<button class="btn-sm btn-danger egg-delete" data-sid="' + s.id + '">x</button>' +
            '</div>' +
            (wingName ? '<div class="egg-wing">' + escapeHtml(wingName) + '</div>' : '') +
        '</div>';
    }).join('');
    eggHtml += '</div>';
    DOM.sessionsList.innerHTML = eggHtml;

    DOM.sessionsList.querySelectorAll('.egg-box').forEach(function(card) {
        card.addEventListener('click', function(e) {
            if (e.target.closest('.box-menu-btn, .egg-delete')) return;
            var sid = card.dataset.sid;
            switchToSession(sid);
        });
    });

    DOM.sessionsList.querySelectorAll('.egg-delete').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            if (btn.dataset.armed) {
                window._deleteSession(btn.dataset.sid);
            } else {
                btn.dataset.armed = '1';
                btn.textContent = 'sure?';
                btn.classList.add('btn-armed');
                setTimeout(function() {
                    btn.dataset.armed = '';
                    btn.textContent = 'x';
                    btn.classList.remove('btn-armed');
                }, 3000);
            }
        });
    });

    DOM.sessionsList.querySelectorAll('.egg-box .box-menu-btn').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            var sid = btn.closest('.egg-box').dataset.sid;
            showEggDetail(sid);
        });
    });

    setupEggDrag();
}

// Need setCachedWings for dismiss button
import { setCachedWings } from './data.js';

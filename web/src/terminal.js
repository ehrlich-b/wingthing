import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SerializeAddon } from '@xterm/addon-serialize';
import '@xterm/xterm/css/xterm.css';
import { S, DOM, TERM_BUF_PREFIX, TERM_THUMB_PREFIX } from './state.js';
import { readSessionContent, sessionContentKey, clearSessionContent, terminalReferenceMatches } from './session-reference.js';
import { findSessionResource } from './session-inventory.js';
import { e2eEncrypt } from './crypto.js';
import { setNotification, clearNotification } from './notify.js';
import { showHome } from './nav.js';
import { sendViaDC } from './webrtc.js';
import { cleanTerminalSelection, copyTextWithFallback, terminalClipboardAvailable } from './terminal-selection.js';

export var TERMINAL_CACHE_BUDGET = 2000000;
export var TERMINAL_CACHE_ENTRY_BUDGET = 200000;
export var TERMINAL_CACHE_MAX_AGE = 86400000;
var TERMINAL_CACHE_INDEX = 'wt_terminal_cache_v1';
var pendingTermSave = null;

// Charge each scalar for the greater of its UTF-8 and UTF-16 storage cost.
// This bounds both common localStorage quota conventions, including keys and
// metadata. ASCII costs two bytes; supplementary characters cost four.
export function terminalStorageBytes(text) {
    var bytes = 0;
    for (var i = 0; i < text.length; i++) {
        var point = text.codePointAt(i);
        bytes += point > 0xffff ? 4 : point > 0x7ff ? 3 : 2;
        if (point > 0xffff) i++;
    }
    return bytes;
}

function trimTerminalBuffer(text) {
    var bytes = 0, start = text.length;
    while (start > 0) {
        var next = start - 1, unit = text.charCodeAt(next);
        if (unit >= 0xdc00 && unit <= 0xdfff && next > 0) {
            var previous = text.charCodeAt(next - 1);
            if (previous >= 0xd800 && previous <= 0xdbff) next--;
        }
        var point = text.codePointAt(next);
        var cost = point > 0xffff ? 4 : point > 0x7ff ? 3 : 2;
        if (bytes + cost > TERMINAL_CACHE_ENTRY_BUDGET) break;
        bytes += cost;
        start = next;
    }
    return text.slice(start);
}

function terminalCacheEntries(storage, now) {
    var index = {};
    try { index = JSON.parse(storage.getItem(TERMINAL_CACHE_INDEX)) || {}; } catch (e) {}
    var keys = [];
    for (var i = 0; i < storage.length; i++) {
        var key = storage.key(i);
        if (key && (key.startsWith(TERM_BUF_PREFIX) || key.startsWith(TERM_THUMB_PREFIX))) keys.push(key);
    }
    return keys.map(function(key) {
        var prefix = key.startsWith(TERM_BUF_PREFIX) ? TERM_BUF_PREFIX : TERM_THUMB_PREFIX;
        var savedAt = index[key];
        return { key: key, prefix: prefix, group: key.slice(prefix.length), value: storage.getItem(key),
            savedAt: Number.isFinite(savedAt) && savedAt >= 0 ? savedAt : now };
    }).filter(function(entry) { return entry.value !== null; });
}

function terminalCacheIndex(entries) {
    var index = {};
    entries.slice().sort(function(a, b) { return a.key < b.key ? -1 : a.key > b.key ? 1 : 0; })
        .forEach(function(entry) { index[entry.key] = entry.savedAt; });
    return JSON.stringify(index);
}

function terminalCacheBytes(entries, index) {
    return entries.reduce(function(bytes, entry) {
        return bytes + terminalStorageBytes(entry.key) + terminalStorageBytes(entry.value);
    }, terminalStorageBytes(TERMINAL_CACHE_INDEX) + terminalStorageBytes(index));
}

function terminalCacheGroups(entries) {
    var groups = new Map();
    entries.forEach(function(entry) {
        groups.set(entry.group, Math.max(groups.get(entry.group) || 0, entry.savedAt));
    });
    return Array.from(groups, function(pair) { return { key: pair[0], savedAt: pair[1] }; })
        .sort(function(a, b) { return a.savedAt - b.savedAt || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0); });
}

function removeTerminalCacheGroup(storage, entries, group) {
    entries.filter(function(entry) { return entry.group === group; }).forEach(function(entry) { storage.removeItem(entry.key); });
    return entries.filter(function(entry) { return entry.group !== group; });
}

function quotaError(error) {
    return error && (error.name === 'QuotaExceededError' || error.name === 'NS_ERROR_DOM_QUOTA_REACHED' || error.code === 22 || error.code === 1014);
}

function currentTerminalCacheGroup() {
    var key = sessionContentKey(TERM_BUF_PREFIX, S.ptyWingId, S.ptySessionId);
    return key && key.slice(TERM_BUF_PREFIX.length);
}

// Re-read storage for every operation; there is no in-memory index to lose
// another tab's save. Eviction removes a snapshot and its thumbnail together.
function sweepTerminalCache(storage, now, pinned) {
    var entries = terminalCacheEntries(storage, now);
    terminalCacheGroups(entries).forEach(function(group) {
        // Expiration applies even to the selected session. Cached output is
        // only a preview; fresh inventory remains the attachment authority.
        if (now - group.savedAt >= TERMINAL_CACHE_MAX_AGE) entries = removeTerminalCacheGroup(storage, entries, group.key);
    });
    entries = entries.filter(function(entry) {
        if (terminalStorageBytes(entry.value) <= TERMINAL_CACHE_ENTRY_BUDGET) return true;
        if (entry.prefix === TERM_BUF_PREFIX) {
            entry.value = trimTerminalBuffer(entry.value);
            storage.setItem(entry.key, entry.value);
            return true;
        }
        storage.removeItem(entry.key);
        return false;
    });
    while (true) {
        var index = terminalCacheIndex(entries);
        var previousIndex = storage.getItem(TERMINAL_CACHE_INDEX) || '';
        var fits = Math.max(terminalCacheBytes(entries, index), terminalCacheBytes(entries, previousIndex)) <= TERMINAL_CACHE_BUDGET;
        if (fits) {
            try { storage.setItem(TERMINAL_CACHE_INDEX, index); return entries; }
            catch (error) { if (!quotaError(error)) throw error; }
        }
        var victim = terminalCacheGroups(entries).find(function(group) { return group.key !== pinned; });
        if (!victim) return entries;
        entries = removeTerminalCacheGroup(storage, entries, victim.key);
    }
}

export function writeTerminalCache(storage, prefix, wingId, sessionId, content, now) {
    var key = sessionContentKey(prefix, wingId, sessionId);
    if (!key || (prefix !== TERM_BUF_PREFIX && prefix !== TERM_THUMB_PREFIX)) return false;
    now = now === undefined ? Date.now() : now;
    if (prefix === TERM_BUF_PREFIX) content = trimTerminalBuffer(content);
    else if (terminalStorageBytes(content) > TERMINAL_CACHE_ENTRY_BUDGET) return false;
    try {
        var pinned = currentTerminalCacheGroup(), target = key.slice(prefix.length);
        var entries = sweepTerminalCache(storage, now, pinned);
        while (true) {
            var planned = entries.filter(function(entry) { return entry.key !== key; });
            planned.push({ key: key, prefix: prefix, group: target, value: content, savedAt: now });
            var index = terminalCacheIndex(planned);
            // Reserve enough space for the index while the old value is still
            // present, then replace the raw replay string without an envelope.
            var fits = Math.max(terminalCacheBytes(planned, index), terminalCacheBytes(entries, index)) <= TERMINAL_CACHE_BUDGET;
            if (fits) {
                try {
                    storage.setItem(TERMINAL_CACHE_INDEX, index);
                    storage.setItem(key, content);
                    return true;
                } catch (error) {
                    try { storage.setItem(TERMINAL_CACHE_INDEX, terminalCacheIndex(entries)); } catch (e) {}
                    if (!quotaError(error)) return false;
                }
            }
            // The selected session is protected under budget/quota pressure.
            // If no other entry can make room, skip this save rather than
            // destroy its previous snapshot or another wing's equal-ID data.
            var victim = terminalCacheGroups(entries).find(function(group) { return group.key !== pinned && group.key !== target; });
            if (!victim) return false;
            entries = removeTerminalCacheGroup(storage, entries, victim.key);
        }
    } catch (e) { return false; }
}

function withTerminalCacheLock(callback) {
    try {
        if (typeof navigator !== 'undefined' && navigator.locks) {
            return navigator.locks.request('wt-terminal-cache', callback).catch(function() { return false; });
        }
        // Older/insecure browsers still save synchronously and re-scan the
        // shared storage each time; Web Locks serialize tabs when available.
        return Promise.resolve(callback());
    } catch (e) { return Promise.resolve(false); }
}

export function copyTerminalSelection() {
    if (!S.term || !S.term.hasSelection()) return Promise.resolve(false);
    var text = cleanTerminalSelection(S.term.getSelection());
    if (!text) return Promise.resolve(false);
    return copyTextWithFallback(text).then(function(copied) {
        if (copied) {
            DOM.terminalCopyBtn.textContent = 'copied';
            setTimeout(function() { DOM.terminalCopyBtn.textContent = 'copy'; }, 1200);
        }
        return copied;
    });
}

export function initTerminal() {
    S.term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: "'JetBrains Mono', monospace",
        theme: {
            background: '#1a1a2e',
            foreground: '#eee',
            cursor: '#ffffff',
            selectionBackground: '#0f3460',
        },
        allowProposedApi: true,
    });
    S.fitAddon = new FitAddon();
    S.serializeAddon = new SerializeAddon();
    S.term.loadAddon(S.fitAddon);
    S.term.loadAddon(S.serializeAddon);
    S.term.open(DOM.terminalContainer);
    S.fitAddon.fit();

    S.term.attachCustomKeyEventHandler(function (e) {
        if (e.type === 'keydown' && (e.ctrlKey || e.metaKey) && e.key === '.') {
            e.preventDefault();
            showHome();
            return false;
        }
        // Let browser handle Ctrl+V/Cmd+V paste
        if (e.type === 'keydown' && (e.ctrlKey || e.metaKey) && e.key === 'v') {
            return false;
        }
        // Let browser handle Ctrl+C/Cmd+C copy when text is selected
        if (e.type === 'keydown' && (e.ctrlKey || e.metaKey) && e.key === 'c' && S.term.hasSelection()) {
            if (terminalClipboardAvailable()) {
                e.preventDefault();
                copyTerminalSelection();
            }
            return false;
        }
        // Spectator: show toast on real keyboard input only
        if (e.type === 'keydown' && S.spectating && !e.metaKey && !e.ctrlKey) {
            showSpectateToast();
        }
        return true;
    });

    // Alternate scroll mode, the way xterm/iTerm2/Ghostty do it.
    //
    // The egg removes the agent's mouse-tracking enable and leaves a private OSC in its
    // place (see stripMouseTracking) — that tells us the agent parses mouse input even
    // though xterm never entered mouse mode, which is what keeps drag-to-select alive.
    S.term.parser.registerOscHandler(7771, function () { S.agentMouse = true; return true; });

    // Claude Code (and other fullscreen TUIs) draw in the alternate screen buffer, which
    // has no scrollback for xterm.js to scroll, so the wheel has to reach the agent.
    // For mouse-capable agents we send what a real terminal sends — SGR wheel events,
    // button 64 up / 65 down — and the agent scrolls natively, ~3 lines a notch.
    // Agents that never enable mouse reporting get arrow keys instead, the way
    // xterm/iTerm2/Ghostty do alternate scroll mode. That is why Claude used to print
    // "Scroll wheel is sending arrow keys"; PgUp/PgDn is worse still, a page per notch.
    // In the normal buffer we return true and leave xterm's own scrollback untouched.
    var wheelAccum = 0;
    var WHEEL_NOTCH = 120; // wheel-delta px per SGR wheel event (tunable)
    var WHEEL_LINE = 40;   // wheel-delta px per arrow-key line step (tunable)
    S.term.attachCustomWheelEventHandler(function (e) {
        if (!S.term || S.term.buffer.active.type !== 'alternate') return true;
        var dy = e.deltaY;
        if (e.deltaMode === 1) dy *= 16;                                      // lines -> px
        else if (e.deltaMode === 2) dy *= DOM.terminalContainer.clientHeight; // pages -> px
        wheelAccum += dy;
        var step = S.agentMouse ? WHEEL_NOTCH : WHEEL_LINE;
        var rect = DOM.terminalContainer.getBoundingClientRect();
        var col = Math.min(S.term.cols, Math.max(1, Math.ceil((e.clientX - rect.left) / (rect.width / S.term.cols))));
        var row = Math.min(S.term.rows, Math.max(1, Math.ceil((e.clientY - rect.top) / (rect.height / S.term.rows))));
        while (Math.abs(wheelAccum) >= step) {
            var up = wheelAccum < 0;
            if (S.agentMouse) sendPTYInput('\x1b[<' + (up ? 64 : 65) + ';' + col + ';' + row + 'M');
            else sendPTYInput(up ? '\x1b[A' : '\x1b[B');
            wheelAccum += up ? step : -step;
        }
        e.preventDefault();
        return false;
    });

    S.term.onData(function (data) {
        if (S.ctrlActive) {
            S.ctrlActive = false;
            document.querySelector('[data-key="ctrl"]').classList.remove('active');
            if (data.length === 1) {
                var code = data.toUpperCase().charCodeAt(0) - 64;
                if (code >= 0 && code <= 31) { sendPTYInput(String.fromCharCode(code)); return; }
            }
        }
        sendPTYInput(data);
    });

    S.term.onBell(function() {
        if (S.ptySessionId) setNotification(S.ptySessionId, S.ptyWingId);
    });

    // Agents retitle their terminal as they work (Claude: "◐ Math question");
    // ask for a list refresh once per new topic so the sidebar label follows.
    var titleKey = '';
    var titleTimer = null;
    S.term.onTitleChange(function(raw) {
        var topic = String(raw || '').replace(/^[^\p{L}\p{N}]+/u, '').trim();
        var key = S.ptyWingId + '\n' + S.ptySessionId + '\n' + topic;
        if (!topic || key === titleKey) return;
        titleKey = key;
        clearTimeout(titleTimer);
        titleTimer = setTimeout(function() { window.dispatchEvent(new CustomEvent('wt-terminal-title')); }, 1500);
    });

    S.term.onSelectionChange(function() {
        DOM.terminalCopyBtn.disabled = !S.term.hasSelection();
        DOM.terminalCopyBtn.title = S.term.hasSelection()
            ? 'Copy selection without terminal line padding'
            : 'Select terminal text to copy';
    });
    DOM.terminalCopyBtn.addEventListener('click', function() { copyTerminalSelection(); });

    // Touch scroll proxy — xterm.js v6 replaced native scrolling with a custom JS
    // scrollbar that only handles wheel events. On touch devices we overlay a transparent
    // native-scrollable div so the browser handles momentum, overscroll, etc. for free.
    if ('ontouchstart' in window || navigator.maxTouchPoints > 0) {
        var proxy = document.createElement('div');
        var spacer = document.createElement('div');
        proxy.style.cssText = 'position:absolute;inset:0;overflow-y:auto;z-index:1;-webkit-overflow-scrolling:touch;scrollbar-width:none;-ms-overflow-style:none';
        // Hide native scrollbar on the proxy overlay (keep xterm's own scrollbar visible)
        var proxyStyle = document.createElement('style');
        proxyStyle.textContent = '#terminal-container > div:last-child::-webkit-scrollbar{display:none}';
        document.head.appendChild(proxyStyle);
        spacer.style.cssText = 'width:1px;pointer-events:none';
        proxy.appendChild(spacer);
        DOM.terminalContainer.style.position = 'relative';
        DOM.terminalContainer.appendChild(proxy);

        // Taps on the proxy should focus the terminal for keyboard input
        proxy.addEventListener('click', function() { if (S.term) S.term.focus(); });

        var syncing = false;
        function lineHeight() { return DOM.terminalContainer.clientHeight / S.term.rows; }
        function totalLines() { return S.term.buffer.active.length; }

        function syncProxyHeight() {
            spacer.style.height = (totalLines() * lineHeight()) + 'px';
        }

        // Proxy scroll -> xterm
        proxy.addEventListener('scroll', function() {
            if (syncing || !S.term) return;
            syncing = true;
            var line = Math.round(proxy.scrollTop / lineHeight());
            S.term.scrollToLine(line);
            syncing = false;
        }, { passive: true });

        // scrollTo with behavior:'instant' cancels iOS momentum scrolling
        // and forces the position. Plain scrollTop assignment gets eaten.
        function proxyScrollTo(top) {
            proxy.scrollTo({ top: top, behavior: 'instant' });
        }

        // xterm scroll -> proxy (keyboard scroll, new output, etc.)
        S.term.onScroll(function() {
            if (syncing) return;
            syncing = true;
            syncProxyHeight();
            proxyScrollTo(S.term.buffer.active.viewportY * lineHeight());
            syncing = false;
        });

        // Update spacer on resize/new output
        S.term.onResize(function() { syncProxyHeight(); });
        S.term.onWriteParsed(function() {
            syncProxyHeight();
            // If at bottom, keep proxy at bottom
            if (S.term.buffer.active.viewportY === S.term.buffer.active.baseY) {
                proxyScrollTo(proxy.scrollHeight);
            }
        });

        syncProxyHeight();
        proxyScrollTo(proxy.scrollHeight);

        // Expose scrollToBottom for use after session attach/restore.
        // Double-rAF ensures xterm has finished layout before we measure.
        S.touchProxyScrollToBottom = function() {
            syncProxyHeight();
            proxyScrollTo(proxy.scrollHeight);
            requestAnimationFrame(function() {
                requestAnimationFrame(function() {
                    syncProxyHeight();
                    proxyScrollTo(proxy.scrollHeight);
                });
            });
        };
    }

    // Set inputmode so mobile keyboards show the text layout (not url/email).
    // Don't set autocapitalize/autocorrect — they cause random capitalization
    // on iOS. The type overlay handles proper text input instead.
    var textarea = DOM.terminalContainer.querySelector('.xterm-helper-textarea');
    if (textarea) {
        textarea.setAttribute('inputmode', 'text');
    }
}

export function saveTermBuffer() {
    if (!S.ptySessionId || !S.serializeAddon) return;
    var sessionId = S.ptySessionId, wingId = S.ptyWingId, serializer = S.serializeAddon;
    var socket = S.ptyWs, user = S.currentUser;
    if (pendingTermSave) pendingTermSave.cancelled = true;
    var pending = { sessionId: sessionId, wingId: wingId, cancelled: false };
    pendingTermSave = pending;
    clearTimeout(S.saveBufferTimer);
    S.saveBufferTimer = setTimeout(function () {
        return withTerminalCacheLock(function() {
        try {
            if (pending.cancelled || S.ptySessionId !== sessionId || S.ptyWingId !== wingId || S.serializeAddon !== serializer ||
                    S.ptyWs !== socket || S.currentUser !== user) return;
            var data = serializer.serialize();
            if (writeTerminalCache(localStorage, TERM_BUF_PREFIX, wingId, sessionId, data)) saveTermThumb(wingId, sessionId);
        } catch (e) {}
        });
    }, 500);
}

var ANSI_PALETTE = [
    '#000','#c33','#3c3','#cc3','#33c','#c3c','#3cc','#ccc',
    '#888','#f66','#6f6','#ff6','#66f','#f6f','#6ff','#fff'
];

export function cellFgColor(cell) {
    if (cell.isFgDefault()) return '#eee';
    if (cell.isFgRGB()) {
        var c = cell.getFgColor();
        return '#' + ((c >> 16) & 0xff).toString(16).padStart(2, '0') +
               ((c >> 8) & 0xff).toString(16).padStart(2, '0') +
               (c & 0xff).toString(16).padStart(2, '0');
    }
    if (cell.isFgPalette()) {
        var idx = cell.getFgColor();
        if (idx < 16) return ANSI_PALETTE[idx];
        return '#eee';
    }
    return '#eee';
}

export function saveTermThumb(wingId, sessionId) {
    if (!wingId || S.ptySessionId !== sessionId || S.ptyWingId !== wingId || !S.term) return;
    try {
        var dpr = window.devicePixelRatio || 1;
        var W = 480, H = 260;
        var c = document.createElement('canvas');
        c.width = W * dpr; c.height = H * dpr;
        var ctx = c.getContext('2d');
        ctx.scale(dpr, dpr);
        ctx.fillStyle = '#1a1a2e';
        ctx.fillRect(0, 0, W, H);

        var buffer = S.term.buffer.active;
        var charW = 5.6;
        var lineH = 11;
        var padX = 4, padY = 10;
        var maxRows = Math.min(S.term.rows, Math.floor((H - padY) / lineH));
        var maxCols = Math.min(S.term.cols, Math.floor((W - padX) / charW));
        ctx.font = '9px monospace';
        ctx.textBaseline = 'top';

        var nullCell = buffer.getNullCell();
        for (var y = 0; y < maxRows; y++) {
            var line = buffer.getLine(buffer.viewportY + y);
            if (!line) continue;
            var lastColor = '';
            var run = '';
            var runX = 0;
            for (var x = 0; x < maxCols; x++) {
                var cell = line.getCell(x, nullCell);
                if (!cell) continue;
                var ch = cell.getChars() || ' ';
                var fg = cell.isDim() ? '#666' : cellFgColor(cell);
                if (fg !== lastColor) {
                    if (run) { ctx.fillStyle = lastColor; ctx.fillText(run, padX + runX * charW, padY + y * lineH); }
                    lastColor = fg;
                    run = ch;
                    runX = x;
                } else {
                    run += ch;
                }
            }
            if (run) { ctx.fillStyle = lastColor; ctx.fillText(run, padX + runX * charW, padY + y * lineH); }
        }

        writeTerminalCache(localStorage, TERM_THUMB_PREFIX, wingId, sessionId, c.toDataURL('image/webp', 0.6));
    } catch (e) {}
}

export function restoreTermBuffer(sessionId, wingId) {
    var term = S.term, selectedId = S.ptySessionId, selectedWing = S.ptyWingId;
    var socket = S.ptyWs, user = S.currentUser;
    return withTerminalCacheLock(function() {
    try {
        var key = sessionContentKey(TERM_BUF_PREFIX, wingId, sessionId);
        if (!key) return;
        sweepTerminalCache(localStorage, Date.now(), key.slice(TERM_BUF_PREFIX.length));
        var data = readSessionContent(localStorage, TERM_BUF_PREFIX, wingId, sessionId);
        if (data && term && S.term === term && S.ptySessionId === selectedId && S.ptyWingId === selectedWing &&
                S.ptyWs === socket && S.currentUser === user) term.write(data);
    } catch (e) {}
    });
}

export function clearTermBuffer(sessionId, wingId) {
    var session = wingId ? null : findSessionResource(S.sessionsData, sessionId);
    wingId = wingId || (session && session.wing_id);
    if (pendingTermSave && pendingTermSave.sessionId === sessionId && pendingTermSave.wingId === wingId) {
        pendingTermSave.cancelled = true;
        clearTimeout(S.saveBufferTimer);
    }
    return withTerminalCacheLock(function() {
        try {
            clearSessionContent(localStorage, [TERM_BUF_PREFIX, TERM_THUMB_PREFIX], wingId, sessionId);
            sweepTerminalCache(localStorage, Date.now(), currentTerminalCacheGroup());
        } catch (e) {}
    });
}

var _spectateToastTimer = null;

function showSpectateToast() {
    if (_spectateToastTimer) return;
    var existing = document.getElementById('spectate-toast');
    if (existing) return;
    var toast = document.createElement('div');
    toast.id = 'spectate-toast';
    toast.className = 'spectate-toast';
    toast.textContent = 'read-only session';
    document.body.appendChild(toast);
    _spectateToastTimer = setTimeout(function() {
        var el = document.getElementById('spectate-toast');
        if (el) el.remove();
        _spectateToastTimer = null;
    }, 2000);
}

export function sendPTYInput(text) {
    if (!S.ptySessionId || S.spectating || S.ptyInputBlocked) return;
    var sessionId = S.ptySessionId, wingId = S.ptyWingId, socket = S.ptyWs;
    clearNotification(sessionId, wingId);
    e2eEncrypt(text).then(function (encoded) {
        if (!terminalReferenceMatches(S, sessionId, wingId, socket)) return;
        var msg = { type: 'pty.input', session_id: sessionId, data: encoded };
        // P2P: try DataChannel first, fall back to relay WS
        if (sendViaDC(sessionId, msg, wingId)) return;
        if (S.ptyWs && S.ptyWs.readyState === WebSocket.OPEN) {
            S.ptyWs.send(JSON.stringify(msg));
        }
    });
}

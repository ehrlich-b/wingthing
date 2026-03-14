import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SerializeAddon } from '@xterm/addon-serialize';
import { gcm } from '@noble/ciphers/aes.js';
import { S, DOM } from './state.js';
import { deriveE2EKey, identityPubKey } from './crypto.js';
import { b64ToBytes, bytesToB64, wingDisplayName, agentIcon } from './helpers.js';
import { onlineWings, showPalette, setCanvasLaunchCallback } from './palette.js';
import { sendTunnelRequest } from './tunnel.js';
import { checkForNotification, setNotification, clearNotification } from './notify.js';

// Grid constants
var CELL = 40;
var DEF_W = 20;  // cells (800px)
var DEF_H = 13;  // cells (520px)
var MIN_W = 10;  // cells (400px)
var MIN_H = 6;   // cells (240px)

// Persistence keys
var CANVAS_LAYOUT_KEY = 'wt_canvas_layout';
var CANVAS_VIEW_KEY = 'wt_canvas_view';

// Module state
var sessions = {};
var focusedId = null;
var offset = { x: 0, y: 0 };
var scale = 1.0;
var nextZ = 1;
var active = false;
var occupied = {}; // "col,row" -> sessionId
var canvasMode = 'use'; // 'use' | 'create'

// Drag state
var moving = null; // { id, startX, startY, origCol, origRow }
var resizing = null; // { id, startX, startY, origCellW, origCellH }
var ghost = null; // the ghost DOM element
var panning = null; // { startX, startY, origOffX, origOffY }
var pendingClick = null; // { x, y, screenX, screenY } — disambiguate click vs pan

// Canvas toolbar state
var canvasWingIndex = 0;
var canvasAgentIndex = 0;
var canvasCwd = '';

// --- Grid helpers ---

function snapToGrid(px) {
    return Math.round(px / CELL);
}

function markCells(col, row, w, h, id) {
    for (var r = row; r < row + h; r++) {
        for (var c = col; c < col + w; c++) {
            occupied[c + ',' + r] = id;
        }
    }
}

function clearCells(col, row, w, h) {
    for (var r = row; r < row + h; r++) {
        for (var c = col; c < col + w; c++) {
            delete occupied[c + ',' + r];
        }
    }
}

function canPlace(col, row, w, h, excludeId) {
    for (var r = row; r < row + h; r++) {
        for (var c = col; c < col + w; c++) {
            var key = c + ',' + r;
            if (occupied[key] && occupied[key] !== excludeId) return false;
        }
    }
    return true;
}

function findFirstFit(w, h) {
    // Scan left-to-right, top-to-bottom
    var maxCol = 200; // reasonable upper bound
    var maxRow = 200;
    for (var r = 0; r < maxRow; r++) {
        for (var c = 0; c < maxCol; c++) {
            if (canPlace(c, r, w, h, null)) {
                return { col: c, row: r };
            }
        }
    }
    return { col: 0, row: 0 };
}

// --- Layout + view persistence ---

function saveCanvasLayout() {
    var layout = {};
    for (var id in sessions) {
        var s = sessions[id];
        layout[id] = { col: s.col, row: s.row, cellW: s.cellW, cellH: s.cellH };
    }
    try { localStorage.setItem(CANVAS_LAYOUT_KEY, JSON.stringify(layout)); } catch(e) {}
}

function loadCanvasLayout() {
    try {
        var data = localStorage.getItem(CANVAS_LAYOUT_KEY);
        return data ? JSON.parse(data) : {};
    } catch(e) { return {}; }
}

function saveCanvasView() {
    try {
        localStorage.setItem(CANVAS_VIEW_KEY, JSON.stringify({ x: offset.x, y: offset.y, scale: scale }));
    } catch(e) {}
}

function loadCanvasView() {
    try {
        var data = localStorage.getItem(CANVAS_VIEW_KEY);
        if (!data) return;
        var v = JSON.parse(data);
        if (typeof v.x === 'number') offset.x = v.x;
        if (typeof v.y === 'number') offset.y = v.y;
        if (typeof v.scale === 'number') scale = Math.max(0.15, Math.min(2.0, v.scale));
    } catch(e) {}
}

// --- Canvas toolbar ---

function currentCanvasWing() {
    var online = onlineWings();
    if (online.length === 0) return null;
    return online[canvasWingIndex % online.length];
}

function currentCanvasAgents() {
    var wing = currentCanvasWing();
    if (!wing || !wing.agents || wing.agents.length === 0) return ['claude'];
    return wing.agents;
}

function currentCanvasAgent() {
    var agents = currentCanvasAgents();
    return agents[canvasAgentIndex % agents.length];
}

function cycleCanvasWing() {
    var online = onlineWings();
    if (online.length <= 1) return;
    canvasWingIndex = (canvasWingIndex + 1) % online.length;
    canvasAgentIndex = 0;
    canvasCwd = '';
    renderCanvasToolbar();
}

function cycleCanvasAgent() {
    var agents = currentCanvasAgents();
    if (agents.length <= 1) return;
    canvasAgentIndex = (canvasAgentIndex + 1) % agents.length;
    renderCanvasToolbar();
}

function pickCanvasCwd() {
    setCanvasLaunchCallback(function(agent, cwd, wingId) {
        canvasCwd = cwd || '';
        renderCanvasToolbar();
    });
    showPalette();
}

export function renderCanvasToolbar() {
    if (!DOM.canvasToolbar) return;
    var wing = currentCanvasWing();
    DOM.ctWing.textContent = wing ? wingDisplayName(wing) : 'no wings online';
    DOM.ctAgent.innerHTML = agentIcon(currentCanvasAgent()) + ' ' + currentCanvasAgent();

    // Auto-pick cwd from wing's first project if not set
    if (!canvasCwd && wing) {
        var wingProjects = S.allProjects.filter(function(p) { return p.wingId === wing.wing_id; });
        if (wingProjects.length > 0) canvasCwd = wingProjects[0].path;
    }
    DOM.ctCwd.textContent = canvasCwd ? canvasCwd.replace(/^\/Users\/[^/]+/, '~') : '~/';
}

function setCanvasMode(mode) {
    canvasMode = mode;
    if (!DOM.canvasViewport) return;
    DOM.canvasViewport.classList.toggle('create-mode', mode === 'create');
    if (DOM.canvasFab) DOM.canvasFab.classList.toggle('active', mode === 'create');
}

export function toggleCanvasMode() {
    setCanvasMode(canvasMode === 'create' ? 'use' : 'create');
}

function stepZoom(delta) {
    var newScale = Math.max(0.15, Math.min(2.0, scale + delta));
    var rect = DOM.canvasViewport.getBoundingClientRect();
    var cx = rect.width / 2, cy = rect.height / 2;
    offset.x = cx - (cx - offset.x) * (newScale / scale);
    offset.y = cy - (cy - offset.y) * (newScale / scale);
    scale = newScale;
    applyTransform();
    updateZoomDisplay();
    saveCanvasView();
}

function resetZoom() {
    scale = 1.0;
    offset = { x: 0, y: 0 };
    applyTransform();
    updateZoomDisplay();
    saveCanvasView();
}

function updateZoomDisplay() {
    if (DOM.canvasZoomLevel) DOM.canvasZoomLevel.textContent = Math.round(scale * 100) + '%';
}

// --- Encryption ---

function sessionEncrypt(key, plaintext) {
    if (!key) return btoa(unescape(encodeURIComponent(plaintext)));
    var enc = new TextEncoder();
    var iv = crypto.getRandomValues(new Uint8Array(12));
    var cipher = gcm(key, iv);
    var ct = cipher.encrypt(enc.encode(plaintext));
    var result = new Uint8Array(12 + ct.length);
    result.set(iv);
    result.set(ct, 12);
    return bytesToB64(result);
}

function sessionDecrypt(key, encoded) {
    if (!key) {
        var binary = atob(encoded);
        var bytes = new Uint8Array(binary.length);
        for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
        return bytes;
    }
    var data = b64ToBytes(encoded);
    var iv = data.slice(0, 12);
    var ct = data.slice(12);
    var cipher = gcm(key, iv);
    return new Uint8Array(cipher.decrypt(ct));
}

// --- World transform ---

function worldFromScreen(sx, sy) {
    return {
        x: (sx - offset.x) / scale,
        y: (sy - offset.y) / scale
    };
}

function applyTransform() {
    DOM.canvasWorld.style.transform = 'translate(' + offset.x + 'px, ' + offset.y + 'px) scale(' + scale + ')';
}

function sessionTitle(agent, wingId) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    var name = wing ? wingDisplayName(wing) : '';
    if (name) return name + ' \u00b7 ' + agent;
    return agent || '';
}

// --- Focus ---

function unfocusAll() {
    if (focusedId && sessions[focusedId]) {
        sessions[focusedId].el.classList.remove('focused');
    }
    focusedId = null;
}

function focusSession(id) {
    if (focusedId && sessions[focusedId]) {
        sessions[focusedId].el.classList.remove('focused');
    }
    focusedId = id;
    if (sessions[id]) {
        sessions[id].el.classList.add('focused');
        sessions[id].zIndex = nextZ++;
        sessions[id].el.style.zIndex = sessions[id].zIndex;
        sessions[id].term.focus();
        setTimeout(function() {
            if (sessions[id]) sessions[id].term.focus();
        }, 0);
        // Clear attention if set
        var dot = sessions[id].el.querySelector('.canvas-dot');
        if (dot && dot.classList.contains('dot-attention')) {
            dot.classList.remove('dot-attention');
            dot.classList.add('dot-live');
            clearNotification(id);
        }
    }
}

// --- Ghost element ---

function createGhost() {
    if (ghost) return ghost;
    ghost = document.createElement('div');
    ghost.className = 'canvas-drag-ghost';
    DOM.canvasWorld.appendChild(ghost);
    return ghost;
}

function removeGhost() {
    if (ghost && ghost.parentNode) {
        ghost.parentNode.removeChild(ghost);
    }
    ghost = null;
}

function positionGhost(col, row, cellW, cellH, valid) {
    if (!ghost) return;
    ghost.style.left = (col * CELL) + 'px';
    ghost.style.top = (row * CELL) + 'px';
    ghost.style.width = (cellW * CELL) + 'px';
    ghost.style.height = (cellH * CELL) + 'px';
    if (valid) {
        ghost.classList.remove('invalid');
    } else {
        ghost.classList.add('invalid');
    }
}

// --- Terminal element creation ---

function createTerminalEl(id, x, y, width, height) {
    var el = document.createElement('div');
    el.className = 'canvas-terminal';
    el.style.left = x + 'px';
    el.style.top = y + 'px';
    el.style.width = width + 'px';
    el.style.height = height + 'px';
    el.style.zIndex = nextZ++;
    el.dataset.sessionId = id;

    // All event handlers read the CURRENT session id from the DOM attribute,
    // not the closure-captured id, because pty.started re-keys temp -> real id.
    function sid() { return el.dataset.sessionId; }

    var header = document.createElement('div');
    header.className = 'canvas-terminal-header';

    var dot = document.createElement('span');
    dot.className = 'canvas-dot tab-dot dot-live';

    var title = document.createElement('span');
    title.className = 'canvas-terminal-title';
    title.textContent = 'connecting...';

    var expandBtn = document.createElement('button');
    expandBtn.className = 'canvas-terminal-btn';
    expandBtn.textContent = '\u2922';
    expandBtn.title = 'expand';
    expandBtn.addEventListener('click', function(e) {
        e.stopPropagation();
        toggleExpand(sid());
    });

    var closeBtn = document.createElement('button');
    closeBtn.className = 'canvas-terminal-btn';
    closeBtn.textContent = '\u00d7';
    closeBtn.title = 'close';
    closeBtn.addEventListener('click', function(e) {
        e.stopPropagation();
        e.preventDefault();
        confirmClose(sid(), closeBtn);
    });

    header.appendChild(dot);
    header.appendChild(title);
    header.appendChild(expandBtn);
    header.appendChild(closeBtn);

    var body = document.createElement('div');
    body.className = 'canvas-terminal-body';

    var handle = document.createElement('div');
    handle.className = 'canvas-terminal-resize-handle';

    el.appendChild(header);
    el.appendChild(body);
    el.appendChild(handle);

    // Only intercept body clicks on the FOCUSED terminal (live, accepts input).
    // Unfocused terminals let events bubble to viewport for pan.
    body.addEventListener('mousedown', function(e) {
        if (e.button === 0 && sid() === focusedId) {
            e.stopPropagation();
        }
    });

    // Left-click drag on header -> ghost move; click header unfocuses terminal
    header.addEventListener('mousedown', function(e) {
        if (e.button === 0 && !e.target.closest('.canvas-terminal-btn')) {
            e.preventDefault();
            e.stopPropagation();
            unfocusAll();
            startMove(sid(), e.clientX, e.clientY);
        }
    });

    // Resize handle drag
    handle.addEventListener('mousedown', function(e) {
        if (e.button === 0) {
            e.preventDefault();
            e.stopPropagation();
            startResize(sid(), e.clientX, e.clientY);
        }
    });

    // Only block propagation for the focused terminal in use mode.
    // Unfocused terminals let events through to viewport for pan.
    el.addEventListener('mousedown', function(e) {
        if (canvasMode === 'use' && sid() === focusedId) e.stopPropagation();
    });

    // Only the focused terminal captures wheel for scrollback.
    // Unfocused terminals and create mode let wheel bubble to viewport for zoom.
    el.addEventListener('wheel', function(e) {
        if (canvasMode === 'use' && el.dataset.sessionId === focusedId) e.stopPropagation();
    });

    return { el: el, body: body, title: title, dot: dot };
}

// --- Move (ghost drag) ---

function startMove(id, sx, sy) {
    var sess = sessions[id];
    if (!sess || sess.el.classList.contains('expanded')) return;
    moving = { id: id, startX: sx, startY: sy, origCol: sess.col, origRow: sess.row };
    createGhost();
    positionGhost(sess.col, sess.row, sess.cellW, sess.cellH, true);
}

// --- Resize (ghost drag) ---

function startResize(id, sx, sy) {
    var sess = sessions[id];
    if (!sess || sess.el.classList.contains('expanded')) return;
    resizing = { id: id, startX: sx, startY: sy, origCellW: sess.cellW, origCellH: sess.cellH };
    createGhost();
    positionGhost(sess.col, sess.row, sess.cellW, sess.cellH, true);
}

// --- Mouse handlers ---

function onMouseMove(e) {
    // Pending click -> pan transition
    if (pendingClick && !panning) {
        var dx = e.clientX - pendingClick.screenX;
        var dy = e.clientY - pendingClick.screenY;
        if (Math.abs(dx) > PAN_THRESHOLD || Math.abs(dy) > PAN_THRESHOLD) {
            panning = {
                startX: pendingClick.screenX,
                startY: pendingClick.screenY,
                origOffX: offset.x,
                origOffY: offset.y,
            };
            pendingClick = null;
        }
    }
    if (panning) {
        offset.x = panning.origOffX + (e.clientX - panning.startX);
        offset.y = panning.origOffY + (e.clientY - panning.startY);
        applyTransform();
        return;
    }
    if (moving) {
        var dx = (e.clientX - moving.startX) / scale;
        var dy = (e.clientY - moving.startY) / scale;
        var sess = sessions[moving.id];
        if (!sess) return;
        var targetCol = snapToGrid(moving.origCol * CELL + dx);
        var targetRow = snapToGrid(moving.origRow * CELL + dy);
        var valid = canPlace(targetCol, targetRow, sess.cellW, sess.cellH, sess.id);
        positionGhost(targetCol, targetRow, sess.cellW, sess.cellH, valid);
        moving._targetCol = targetCol;
        moving._targetRow = targetRow;
        moving._valid = valid;
        return;
    }
    if (resizing) {
        var dx = (e.clientX - resizing.startX) / scale;
        var dy = (e.clientY - resizing.startY) / scale;
        var sess = sessions[resizing.id];
        if (!sess) return;
        var newCellW = Math.max(MIN_W, snapToGrid(resizing.origCellW * CELL + dx));
        var newCellH = Math.max(MIN_H, snapToGrid(resizing.origCellH * CELL + dy));
        var valid = canPlace(sess.col, sess.row, newCellW, newCellH, sess.id);
        positionGhost(sess.col, sess.row, newCellW, newCellH, valid);
        resizing._targetW = newCellW;
        resizing._targetH = newCellH;
        resizing._valid = valid;
        return;
    }
}

function onMouseUp(e) {
    // Panning finished
    if (panning) {
        panning = null;
        pendingClick = null;
        saveCanvasView();
        return;
    }
    // Click without drag
    if (pendingClick) {
        var pc = pendingClick;
        pendingClick = null;

        // Only spawn in create mode
        if (pc.source === 'create') {
            var wing = currentCanvasWing();
            if (!wing) return;

            var rect = DOM.canvasViewport.getBoundingClientRect();
            var world = worldFromScreen(pc.screenX - rect.left, pc.screenY - rect.top);
            var col = snapToGrid(world.x);
            var row = snapToGrid(world.y);

            if (!canPlace(col, row, DEF_W, DEF_H, null)) {
                var fit = findFirstFit(DEF_W, DEF_H);
                col = fit.col;
                row = fit.row;
            }

            var cwd = canvasCwd;
            if (!cwd) {
                var wingProjects = S.allProjects.filter(function(p) { return p.wingId === wing.wing_id; });
                if (wingProjects.length > 0) cwd = wingProjects[0].path;
            }

            canvasConnect(currentCanvasAgent(), cwd, wing.wing_id, col, row);
        }
        // In use mode, click (no drag) on unfocused terminal → focus it
        if (pc.source === 'pan' && pc.target) {
            var termEl = pc.target.closest && pc.target.closest('.canvas-terminal');
            if (termEl) focusSession(termEl.dataset.sessionId);
        }
        return;
    }
    if (moving) {
        var sess = sessions[moving.id];
        if (sess && moving._valid && (moving._targetCol !== undefined)) {
            clearCells(sess.col, sess.row, sess.cellW, sess.cellH);
            sess.col = moving._targetCol;
            sess.row = moving._targetRow;
            sess.x = sess.col * CELL;
            sess.y = sess.row * CELL;
            sess.el.style.left = sess.x + 'px';
            sess.el.style.top = sess.y + 'px';
            markCells(sess.col, sess.row, sess.cellW, sess.cellH, sess.id);
            saveCanvasLayout();
        }
        removeGhost();
        moving = null;
        return;
    }
    if (resizing) {
        var sess = sessions[resizing.id];
        if (sess && resizing._valid && (resizing._targetW !== undefined)) {
            clearCells(sess.col, sess.row, sess.cellW, sess.cellH);
            sess.cellW = resizing._targetW;
            sess.cellH = resizing._targetH;
            sess.width = sess.cellW * CELL;
            sess.height = sess.cellH * CELL;
            sess.el.style.width = sess.width + 'px';
            sess.el.style.height = sess.height + 'px';
            markCells(sess.col, sess.row, sess.cellW, sess.cellH, sess.id);
            saveCanvasLayout();
            setTimeout(function() {
                sess.fitAddon.fit();
                sendResize(sess);
            }, 50);
        }
        removeGhost();
        resizing = null;
        return;
    }
}

function sendResize(sess) {
    if (!sess.ws || sess.ws.readyState !== WebSocket.OPEN || !sess.id) return;
    sess.ws.send(JSON.stringify({
        type: 'pty.resize',
        session_id: sess.id,
        cols: sess.term.cols,
        rows: sess.term.rows
    }));
}

function toggleExpand(id) {
    var sess = sessions[id];
    if (!sess) return;
    sess.el.classList.toggle('expanded');
    setTimeout(function() {
        sess.fitAddon.fit();
        sendResize(sess);
        if (sess.el.classList.contains('expanded')) {
            sess.term.focus();
        }
    }, 50);
}

var closeTimers = {};

function confirmClose(id, btn) {
    if (btn.classList.contains('confirm')) {
        btn.classList.remove('confirm');
        btn.textContent = '\u00d7';
        clearTimeout(closeTimers[id]);
        delete closeTimers[id];
        killSession(id);
    } else {
        btn.classList.add('confirm');
        btn.textContent = 'end?';
        closeTimers[id] = setTimeout(function() {
            btn.classList.remove('confirm');
            btn.textContent = '\u00d7';
            delete closeTimers[id];
        }, 3000);
    }
}

function killSession(id) {
    var sess = sessions[id];
    if (!sess) return;
    // Clear grid cells
    if (sess.col !== undefined) {
        clearCells(sess.col, sess.row, sess.cellW, sess.cellH);
    }
    // Send kill via tunnel
    if (sess.wingId) {
        sendTunnelRequest(sess.wingId, { type: 'pty.kill', session_id: id }).catch(function() {});
    }
    // Close WebSocket
    if (sess.ws) {
        try { sess.ws.close(); } catch(e) {}
    }
    // Remove DOM
    if (sess.el && sess.el.parentNode) {
        sess.el.parentNode.removeChild(sess.el);
    }
    // Dispose terminal
    try { sess.term.dispose(); } catch(e) {}
    delete sessions[id];
    saveCanvasLayout();
    if (focusedId === id) {
        focusedId = null;
        var topId = null;
        var topZ = -1;
        for (var sid in sessions) {
            if (sessions[sid].zIndex > topZ) {
                topZ = sessions[sid].zIndex;
                topId = sid;
            }
        }
        if (topId) focusSession(topId);
    }
}

function gunzip(data) {
    var ds = new DecompressionStream('gzip');
    var writer = ds.writable.getWriter();
    writer.write(data);
    writer.close();
    var reader = ds.readable.getReader();
    var chunks = [];
    function pump() {
        return reader.read().then(function(result) {
            if (result.done) {
                var total = 0;
                for (var i = 0; i < chunks.length; i++) total += chunks[i].length;
                var out = new Uint8Array(total);
                var off = 0;
                for (var i = 0; i < chunks.length; i++) { out.set(chunks[i], off); off += chunks[i].length; }
                return out;
            }
            chunks.push(new Uint8Array(result.value));
            return pump();
        });
    }
    return pump();
}

// --- Connect ---

export function canvasConnect(agent, cwd, wingId, col, row) {
    var cellW = DEF_W;
    var cellH = DEF_H;
    var x = col * CELL;
    var y = row * CELL;
    var width = cellW * CELL;
    var height = cellH * CELL;

    var term = new Terminal({
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
    var fitAddon = new FitAddon();
    var serializeAddon = new SerializeAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(serializeAddon);

    var tempId = 'canvas-' + crypto.randomUUID();

    var parts = createTerminalEl(tempId, x, y, width, height);
    DOM.canvasWorld.appendChild(parts.el);

    term.open(parts.body);
    setTimeout(function() { fitAddon.fit(); }, 50);

    var sess = {
        id: tempId,
        term: term,
        fitAddon: fitAddon,
        serializeAddon: serializeAddon,
        ws: null,
        e2eKey: null,
        wingId: wingId,
        agent: agent,
        el: parts.el,
        titleEl: parts.title,
        dotEl: parts.dot,
        col: col,
        row: row,
        cellW: cellW,
        cellH: cellH,
        x: x,
        y: y,
        width: width,
        height: height,
        zIndex: parseInt(parts.el.style.zIndex),
        keyReady: false,
        dead: false,
    };

    markCells(col, row, cellW, cellH, tempId);
    sessions[tempId] = sess;
    focusSession(tempId);
    saveCanvasLayout();

    // Open WebSocket
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    if (wingId) url += '?wing_id=' + encodeURIComponent(wingId);

    var ws = new WebSocket(url);
    sess.ws = ws;

    ws.onopen = function() {
        var startMsg = {
            type: 'pty.start',
            agent: agent,
            cols: term.cols,
            rows: term.rows,
            public_key: identityPubKey,
        };
        if (cwd) startMsg.cwd = cwd;
        if (wingId) startMsg.wing_id = wingId;
        if (wingId && S.tunnelAuthTokens[wingId]) startMsg.auth_token = S.tunnelAuthTokens[wingId];
        ws.send(JSON.stringify(startMsg));
    };

    var pendingOutput = [];

    ws.onmessage = function(e) {
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'pty.started':
                var realId = msg.session_id;
                // Re-key session from temp to real ID
                clearCells(sess.col, sess.row, sess.cellW, sess.cellH);
                delete sessions[tempId];
                sess.id = realId;
                sess.el.dataset.sessionId = realId;
                sessions[realId] = sess;
                markCells(sess.col, sess.row, sess.cellW, sess.cellH, realId);
                if (focusedId === tempId) focusedId = realId;
                saveCanvasLayout();

                parts.title.textContent = sessionTitle(agent, wingId);

                if (msg.public_key) {
                    deriveE2EKey(msg.public_key).then(function(key) {
                        sess.e2eKey = key;
                        sess.keyReady = true;
                        var pending = pendingOutput;
                        pendingOutput = [];
                        pending.forEach(function(item) {
                            processOutput(sess, item.data, item.compressed);
                        });
                    }).catch(function() {
                        sess.keyReady = true;
                        var pending = pendingOutput;
                        pendingOutput = [];
                        pending.forEach(function(item) {
                            processOutput(sess, item.data, item.compressed);
                        });
                    });
                } else {
                    sess.keyReady = true;
                }

                term.onResize(function(size) {
                    sendResize(sess);
                });
                fitAddon.fit();
                sendResize(sess);
                break;

            case 'pty.output':
                if (!sess.keyReady) {
                    pendingOutput.push({ data: msg.data, compressed: !!msg.compressed });
                } else {
                    processOutput(sess, msg.data, !!msg.compressed);
                }
                break;

            case 'pty.exited':
                sess.dead = true;
                term.writeln('\r\n\x1b[2m--- session ended ---\x1b[0m');
                if (msg.error) {
                    term.writeln('\x1b[31;1m' + msg.error.replace(/\n/g, '\r\n') + '\x1b[0m');
                }
                parts.title.textContent = (parts.title.textContent || '') + ' (ended)';
                if (sess.dotEl) {
                    sess.dotEl.classList.remove('dot-live', 'dot-attention');
                    sess.dotEl.classList.add('dot-offline');
                }
                break;

            case 'passkey.challenge':
                term.writeln('\r\n\x1b[33mThis wing requires passkey auth. Use single-terminal view.\x1b[0m');
                break;

            case 'error':
                term.writeln('\r\n\x1b[31m' + (msg.message || 'error') + '\x1b[0m');
                break;
        }
    };

    ws.onclose = function() {
        if (!sess.dead) {
            sess.dead = true;
            term.writeln('\r\n\x1b[2m--- disconnected ---\x1b[0m');
        }
        if (sess.dotEl) {
            sess.dotEl.classList.remove('dot-live', 'dot-attention');
            sess.dotEl.classList.add('dot-offline');
        }
    };

    ws.onerror = function() {
        term.writeln('\r\n\x1b[31mconnection error\x1b[0m');
    };

    term.onData(function(data) {
        if (focusedId !== sess.id || sess.dead) return;
        var encoded = sessionEncrypt(sess.e2eKey, data);
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({
                type: 'pty.input',
                session_id: sess.id,
                data: encoded
            }));
        }
    });

    term.attachCustomKeyEventHandler(function(e) {
        if (e.type === 'keydown' && e.key === 'Escape' && sess.el.classList.contains('expanded')) {
            toggleExpand(sess.id);
            return false;
        }
        if (e.type === 'keydown' && (e.ctrlKey || e.metaKey) && e.key === 'k') {
            return false;
        }
        return true;
    });

    return sess;
}

function processOutput(sess, dataStr, compressed) {
    try {
        var bytes = sessionDecrypt(sess.e2eKey, dataStr);
        if (compressed) {
            gunzip(bytes).then(function(decompressed) {
                sess.term.write(decompressed);
                checkOutputForAttention(sess, decompressed);
            }).catch(function() {});
        } else {
            sess.term.write(bytes);
            checkOutputForAttention(sess, bytes);
        }
    } catch(err) {
        console.error('canvas decrypt error:', err);
    }
}

function checkOutputForAttention(sess, bytes) {
    var text = new TextDecoder().decode(bytes);
    if (checkForNotification(text) && focusedId !== sess.id) {
        canvasSetAttention(sess.id);
        setNotification(sess.id);
    }
}

// --- Viewport handlers ---

var PAN_THRESHOLD = 5; // pixels of movement before click becomes pan

function onViewportMouseDown(e) {
    if (e.target.closest && e.target.closest('#canvas-fab, #canvas-zoom')) return;
    if (e.button !== 0) return;

    if (canvasMode === 'create') {
        if (e.target.closest && e.target.closest('.canvas-terminal-header')) return;
        e.preventDefault();
        pendingClick = { screenX: e.clientX, screenY: e.clientY, source: 'create' };
    } else {
        // Focused terminal handles its own events (stopPropagation above)
        // Unfocused terminals fall through here — treat like empty space
        e.preventDefault();
        pendingClick = { screenX: e.clientX, screenY: e.clientY, source: 'pan', target: e.target };
    }
    panning = null;
}

function onViewportWheel(e) {
    // Terminal elements stopPropagation on wheel, so any event reaching here
    // is on the background. No target check needed.
    e.preventDefault();

    var rect = DOM.canvasViewport.getBoundingClientRect();
    var mx = e.clientX - rect.left;
    var my = e.clientY - rect.top;

    var factor = e.deltaY < 0 ? 1.1 : 1 / 1.1;
    var newScale = Math.max(0.15, Math.min(2.0, scale * factor));

    offset.x = mx - (mx - offset.x) * (newScale / scale);
    offset.y = my - (my - offset.y) * (newScale / scale);
    scale = newScale;

    applyTransform();
    updateZoomDisplay();
    saveCanvasView();
}

// --- Attention ---

export function canvasSetAttention(sessionId) {
    var sess = sessions[sessionId];
    if (!sess || !sess.dotEl) return;
    sess.dotEl.classList.remove('dot-live', 'dot-offline');
    sess.dotEl.classList.add('dot-attention');
}

// --- Attach to existing session ---

function canvasAttach(sessionId, agent, wingId, col, row, optCellW, optCellH) {
    var cellW = optCellW || DEF_W;
    var cellH = optCellH || DEF_H;
    var x = col * CELL;
    var y = row * CELL;
    var width = cellW * CELL;
    var height = cellH * CELL;

    var term = new Terminal({
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
    var fitAddon = new FitAddon();
    var serializeAddon = new SerializeAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(serializeAddon);

    var parts = createTerminalEl(sessionId, x, y, width, height);
    DOM.canvasWorld.appendChild(parts.el);

    term.open(parts.body);
    setTimeout(function() { fitAddon.fit(); }, 50);

    parts.title.textContent = sessionTitle(agent, wingId);

    var sess = {
        id: sessionId,
        term: term,
        fitAddon: fitAddon,
        serializeAddon: serializeAddon,
        ws: null,
        e2eKey: null,
        wingId: wingId,
        agent: agent,
        el: parts.el,
        titleEl: parts.title,
        dotEl: parts.dot,
        col: col,
        row: row,
        cellW: cellW,
        cellH: cellH,
        x: x,
        y: y,
        width: width,
        height: height,
        zIndex: parseInt(parts.el.style.zIndex),
        keyReady: false,
        dead: false,
    };

    markCells(col, row, cellW, cellH, sessionId);
    sessions[sessionId] = sess;

    // Open WebSocket and attach to existing session
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    if (wingId) url += '?wing_id=' + encodeURIComponent(wingId);

    var ws = new WebSocket(url);
    sess.ws = ws;

    ws.onopen = function() {
        var msg = {
            type: 'pty.attach',
            session_id: sessionId,
            public_key: identityPubKey,
            cols: term.cols,
            rows: term.rows,
        };
        if (wingId) msg.wing_id = wingId;
        if (wingId && S.tunnelAuthTokens[wingId]) msg.auth_token = S.tunnelAuthTokens[wingId];
        ws.send(JSON.stringify(msg));
    };

    var pendingOutput = [];

    ws.onmessage = function(e) {
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'pty.started':
                if (msg.public_key) {
                    deriveE2EKey(msg.public_key).then(function(key) {
                        sess.e2eKey = key;
                        sess.keyReady = true;
                        var pending = pendingOutput;
                        pendingOutput = [];
                        pending.forEach(function(item) {
                            processOutput(sess, item.data, item.compressed);
                        });
                    }).catch(function() {
                        sess.keyReady = true;
                        var pending = pendingOutput;
                        pendingOutput = [];
                        pending.forEach(function(item) {
                            processOutput(sess, item.data, item.compressed);
                        });
                    });
                } else {
                    sess.keyReady = true;
                }

                term.onResize(function(size) {
                    sendResize(sess);
                });
                fitAddon.fit();
                sendResize(sess);
                break;

            case 'pty.output':
                if (!sess.keyReady) {
                    pendingOutput.push({ data: msg.data, compressed: !!msg.compressed });
                } else {
                    processOutput(sess, msg.data, !!msg.compressed);
                }
                break;

            case 'pty.exited':
                sess.dead = true;
                term.writeln('\r\n\x1b[2m--- session ended ---\x1b[0m');
                if (msg.error) {
                    term.writeln('\x1b[31;1m' + msg.error.replace(/\n/g, '\r\n') + '\x1b[0m');
                }
                parts.title.textContent = (parts.title.textContent || '') + ' (ended)';
                if (sess.dotEl) {
                    sess.dotEl.classList.remove('dot-live', 'dot-attention');
                    sess.dotEl.classList.add('dot-offline');
                }
                break;

            case 'passkey.challenge':
                term.writeln('\r\n\x1b[33mThis wing requires passkey auth. Use single-terminal view.\x1b[0m');
                break;

            case 'error':
                term.writeln('\r\n\x1b[31m' + (msg.message || 'error') + '\x1b[0m');
                break;
        }
    };

    ws.onclose = function() {
        if (!sess.dead) {
            sess.dead = true;
            term.writeln('\r\n\x1b[2m--- disconnected ---\x1b[0m');
        }
        if (sess.dotEl) {
            sess.dotEl.classList.remove('dot-live', 'dot-attention');
            sess.dotEl.classList.add('dot-offline');
        }
    };

    ws.onerror = function() {
        term.writeln('\r\n\x1b[31mconnection error\x1b[0m');
    };

    term.onData(function(data) {
        if (focusedId !== sess.id || sess.dead) return;
        var encoded = sessionEncrypt(sess.e2eKey, data);
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({
                type: 'pty.input',
                session_id: sess.id,
                data: encoded
            }));
        }
    });

    term.attachCustomKeyEventHandler(function(e) {
        if (e.type === 'keydown' && e.key === 'Escape' && sess.el.classList.contains('expanded')) {
            toggleExpand(sess.id);
            return false;
        }
        if (e.type === 'keydown' && (e.ctrlKey || e.metaKey) && e.key === 'k') {
            return false;
        }
        return true;
    });

    return sess;
}

// --- Arrange existing sessions ---

function arrangeExistingSessions() {
    var existing = {};
    for (var sid in sessions) {
        existing[sid] = true;
    }

    var layout = loadCanvasLayout();

    S.sessionsData.forEach(function(s) {
        if (existing[s.id]) return;
        var wing = S.wingsData.find(function(w) { return w.wing_id === s.wing_id && w.online !== false; });
        if (!wing) return;

        var saved = layout[s.id];
        var col, row, cellW, cellH;
        if (saved && canPlace(saved.col, saved.row, saved.cellW || DEF_W, saved.cellH || DEF_H, null)) {
            col = saved.col;
            row = saved.row;
            cellW = saved.cellW || DEF_W;
            cellH = saved.cellH || DEF_H;
        } else {
            var fit = findFirstFit(DEF_W, DEF_H);
            col = fit.col;
            row = fit.row;
            cellW = DEF_W;
            cellH = DEF_H;
        }
        canvasAttach(s.id, s.agent || 'claude', s.wing_id, col, row, cellW, cellH);
    });

    saveCanvasLayout();
}

// --- Public API ---

export function initCanvas() {
    document.addEventListener('mousemove', onMouseMove);
    document.addEventListener('mouseup', onMouseUp);

    // Toolbar click handlers
    if (DOM.ctWing) DOM.ctWing.addEventListener('click', cycleCanvasWing);
    if (DOM.ctAgent) DOM.ctAgent.addEventListener('click', cycleCanvasAgent);
    if (DOM.ctCwd) DOM.ctCwd.addEventListener('click', pickCanvasCwd);

    // FAB toggle
    if (DOM.canvasFab) {
        DOM.canvasFab.addEventListener('mousedown', function(e) { e.stopPropagation(); });
        DOM.canvasFab.addEventListener('click', function(e) {
            e.stopPropagation();
            toggleCanvasMode();
        });
    }

    // Zoom bar
    if (DOM.canvasZoom) {
        DOM.canvasZoom.addEventListener('mousedown', function(e) { e.stopPropagation(); });
    }
    if (DOM.canvasZoomOut) {
        DOM.canvasZoomOut.addEventListener('click', function(e) { e.stopPropagation(); stepZoom(-0.1); });
    }
    if (DOM.canvasZoomIn) {
        DOM.canvasZoomIn.addEventListener('click', function(e) { e.stopPropagation(); stepZoom(0.1); });
    }
    if (DOM.canvasZoomLevel) {
        DOM.canvasZoomLevel.addEventListener('click', function(e) { e.stopPropagation(); resetZoom(); });
    }
    if (DOM.canvasZoomReset) {
        DOM.canvasZoomReset.addEventListener('click', function(e) { e.stopPropagation(); resetZoom(); });
    }

    // Keyboard: Escape exits create mode, n toggles create mode
    document.addEventListener('keydown', function(e) {
        if (!active) return;
        if (e.key === 'Escape' && canvasMode === 'create') {
            e.preventDefault();
            e.stopPropagation();
            setCanvasMode('use');
            return;
        }
        if (e.key === 'n' && canvasMode === 'use') {
            var tag = document.activeElement && document.activeElement.tagName;
            if (tag !== 'INPUT' && tag !== 'TEXTAREA' && tag !== 'SELECT' && !document.activeElement.closest('.xterm')) {
                e.preventDefault();
                setCanvasMode('create');
            }
        }
    });
}

export function showCanvasView() {
    active = true;
    setCanvasMode('use');
    if (DOM.newSessionBtn) DOM.newSessionBtn.style.display = 'none';
    DOM.canvasSection.style.display = '';
    DOM.canvasViewport.addEventListener('mousedown', onViewportMouseDown);
    DOM.canvasViewport.addEventListener('wheel', onViewportWheel, { passive: false });
    renderCanvasToolbar();
    loadCanvasView();
    updateZoomDisplay();
    if (DOM.canvasToolbar) DOM.canvasToolbar.style.display = '';
    applyTransform();

    // Refit existing canvas terminals, reconnect dead WebSockets
    var reconnectIds = [];
    for (var id in sessions) {
        var sess = sessions[id];
        if (sess.ws && sess.ws.readyState !== WebSocket.OPEN && !sess.dead) {
            reconnectIds.push(id);
        } else {
            setTimeout((function(s) { return function() { s.fitAddon.fit(); }; })(sess), 50);
        }
    }
    for (var i = 0; i < reconnectIds.length; i++) {
        var rid = reconnectIds[i];
        var rs = sessions[rid];
        var savedCol = rs.col, savedRow = rs.row;
        var savedCellW = rs.cellW, savedCellH = rs.cellH;
        var savedAgent = rs.agent, savedWingId = rs.wingId;
        clearCells(savedCol, savedRow, savedCellW, savedCellH);
        if (rs.el && rs.el.parentNode) rs.el.parentNode.removeChild(rs.el);
        try { rs.term.dispose(); } catch(e) {}
        delete sessions[rid];
        canvasAttach(rid, savedAgent, savedWingId, savedCol, savedRow, savedCellW, savedCellH);
    }

    // Auto-populate: connect to existing sessions that aren't already on the canvas
    arrangeExistingSessions();
}

export function hideCanvasView() {
    active = false;
    setCanvasMode('use');
    if (DOM.newSessionBtn) DOM.newSessionBtn.style.display = '';
    DOM.canvasSection.style.display = 'none';
    DOM.canvasViewport.removeEventListener('mousedown', onViewportMouseDown);
    DOM.canvasViewport.removeEventListener('wheel', onViewportWheel);
    if (DOM.canvasToolbar) DOM.canvasToolbar.style.display = 'none';
}

export function isCanvasActive() {
    return active;
}

export function canvasSpawnAtCenter(agent, cwd, wingId) {
    var fit = findFirstFit(DEF_W, DEF_H);
    canvasConnect(agent, cwd, wingId, fit.col, fit.row);
}

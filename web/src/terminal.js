import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SerializeAddon } from '@xterm/addon-serialize';
import '@xterm/xterm/css/xterm.css';
import { S, DOM, TERM_BUF_PREFIX, TERM_THUMB_PREFIX } from './state.js';
import { e2eEncrypt } from './crypto.js';
import { setNotification, clearNotification } from './notify.js';
import { showHome } from './nav.js';

export function initTerminal() {
    S.term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: "'SF Mono', 'Fira Code', 'Cascadia Code', monospace",
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
            return false;
        }
        return true;
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
        if (S.altActive) {
            S.altActive = false;
            document.querySelector('[data-key="alt"]').classList.remove('active');
            sendPTYInput('\x1b' + data);
            return;
        }
        sendPTYInput(data);
    });

    S.term.onBell(function() {
        if (S.ptySessionId) setNotification(S.ptySessionId);
    });

    // Touch scrolling — xterm.js v6 uses VS Code's SmoothScrollableElement which
    // only handles wheel events. Touch events need manual translation to scrollLines.
    var touchLastY = null;
    var touchAccum = 0;
    var touchVelocity = 0;
    var touchLastTime = 0;
    var momentumFrame = null;
    DOM.terminalContainer.addEventListener('touchstart', function(e) {
        if (momentumFrame) { cancelAnimationFrame(momentumFrame); momentumFrame = null; }
        if (e.touches.length === 1) {
            touchLastY = e.touches[0].clientY;
            touchAccum = 0;
            touchVelocity = 0;
            touchLastTime = e.timeStamp;
        }
    }, { passive: true });
    DOM.terminalContainer.addEventListener('touchmove', function(e) {
        if (touchLastY === null || !S.term || e.touches.length !== 1) return;
        var y = e.touches[0].clientY;
        var deltaY = touchLastY - y;
        var dt = e.timeStamp - touchLastTime;
        if (dt > 0) touchVelocity = 0.6 * touchVelocity + 0.4 * (deltaY / dt);
        touchLastY = y;
        touchLastTime = e.timeStamp;
        var lineHeight = DOM.terminalContainer.clientHeight / S.term.rows;
        touchAccum += deltaY;
        var lines = Math.trunc(touchAccum / lineHeight);
        if (lines !== 0) {
            S.term.scrollLines(lines);
            touchAccum -= lines * lineHeight;
            e.preventDefault();
        }
    }, { passive: false });
    DOM.terminalContainer.addEventListener('touchend', function() {
        if (!S.term || Math.abs(touchVelocity) < 0.15) { touchLastY = null; return; }
        var lineHeight = DOM.terminalContainer.clientHeight / S.term.rows;
        var vel = touchVelocity * 16; // px per frame at ~60fps
        touchLastY = null;
        touchAccum = 0;
        function momentum() {
            vel *= 0.95;
            if (Math.abs(vel) < 0.5) return;
            touchAccum += vel;
            var lines = Math.trunc(touchAccum / lineHeight);
            if (lines !== 0) {
                S.term.scrollLines(lines);
                touchAccum -= lines * lineHeight;
            }
            momentumFrame = requestAnimationFrame(momentum);
        }
        momentumFrame = requestAnimationFrame(momentum);
    }, { passive: true });
}

export function saveTermBuffer() {
    if (!S.ptySessionId || !S.serializeAddon) return;
    clearTimeout(S.saveBufferTimer);
    S.saveBufferTimer = setTimeout(function () {
        try {
            var data = S.serializeAddon.serialize();
            if (data.length > 200000) data = data.slice(-200000);
            localStorage.setItem(TERM_BUF_PREFIX + S.ptySessionId, data);
            saveTermThumb();
        } catch (e) {}
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

export function saveTermThumb() {
    if (!S.ptySessionId || !S.term) return;
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

        localStorage.setItem(TERM_THUMB_PREFIX + S.ptySessionId, c.toDataURL('image/webp', 0.6));
    } catch (e) {}
}

export function restoreTermBuffer(sessionId) {
    try {
        var data = localStorage.getItem(TERM_BUF_PREFIX + sessionId);
        if (data && S.term) S.term.write(data);
    } catch (e) {}
}

export function clearTermBuffer(sessionId) {
    try { localStorage.removeItem(TERM_BUF_PREFIX + sessionId); } catch (e) {}
    try { localStorage.removeItem(TERM_THUMB_PREFIX + sessionId); } catch (e) {}
}

export function sendPTYInput(text) {
    if (!S.ptyWs || S.ptyWs.readyState !== WebSocket.OPEN || !S.ptySessionId) return;
    clearNotification(S.ptySessionId);
    e2eEncrypt(text).then(function (encoded) {
        S.ptyWs.send(JSON.stringify({ type: 'pty.input', session_id: S.ptySessionId, data: encoded }));
    });
}

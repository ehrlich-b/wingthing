/**
 * App mouse-mode shim for the browser terminal.
 *
 * Claude Code turns on mouse tracking (DECSET 1000/1002/1003 plus 1006 SGR) so the
 * wheel scrolls its transcript. In a browser that costs us selection: the moment
 * xterm.js sees an app enable tracking it forwards button events to the app and
 * stops selecting locally, which is what broke copy/paste in June (b7ffeed).
 * Turning the app's mouse off instead brought selection back but killed the wheel,
 * because the alt screen has no xterm scrollback to fall back on (4abe51b).
 *
 * The two features want different events, so we can have both. Selection needs
 * buttons; scrolling needs the wheel. We strip the mouse-mode sequences on their
 * way to xterm and remember what the app asked for: xterm keeps selecting locally
 * because as far as it knows no app ever enabled the mouse, and the wheel handler
 * hands the app the one event it actually wants. No modifier keys either way.
 *
 * This belongs to the web client. Native terminal clients forward mouse events for
 * real and should keep doing so — it's the browser that needs its selection back.
 */

// DECSET/DECRST private modes we intercept. Everything else (1049 alt screen,
// 1004 focus, 2004 bracketed paste) passes through untouched.
var TRACKING = [9, 1000, 1001, 1002, 1003];
var ENCODING = { 1005: 'utf8', 1006: 'sgr', 1015: 'urxvt', 1016: 'sgr' };

var ESC = 0x1b, CSI = 0x5b, PRIV = 0x3f, SET = 0x68, RESET = 0x6c;

// Longest sequence we care about is "\x1b[?1000;1002;1003;1006h" — anything
// longer than this straddling a chunk boundary isn't ours, so stop holding it.
var MAX_CARRY = 32;

var encoder = new TextEncoder();
var states = new WeakMap();

function stateFor(term) {
    var s = states.get(term);
    if (!s) {
        s = { tracking: {}, encoding: 'x10', carry: null };
        states.set(term, s);
    }
    return s;
}

function isMouseMode(p) {
    return TRACKING.indexOf(p) !== -1 || ENCODING[p] !== undefined;
}

function applyMode(s, param, on) {
    if (TRACKING.indexOf(param) !== -1) {
        if (on) s.tracking[param] = true;
        else delete s.tracking[param];
        return;
    }
    if (ENCODING[param]) s.encoding = on ? ENCODING[param] : 'x10';
}

function ascii(str) {
    var out = new Uint8Array(str.length);
    for (var i = 0; i < str.length; i++) out[i] = str.charCodeAt(i) & 0xff;
    return out;
}

/**
 * Strip mouse-mode sequences from a chunk, recording them against the term.
 * Returns the chunk to hand xterm — the input array itself when nothing matched,
 * so the common case allocates nothing.
 */
function filterBytes(term, buf) {
    var s = stateFor(term);
    if (s.carry) {
        var joined = new Uint8Array(s.carry.length + buf.length);
        joined.set(s.carry, 0);
        joined.set(buf, s.carry.length);
        buf = joined;
        s.carry = null;
    }

    var pieces = null;  // stays null until we actually strip something
    var copied = 0;
    var i = 0;

    function take(end) {
        if (end > copied) pieces.push(buf.subarray(copied, end));
        copied = end;
    }

    while (i < buf.length) {
        if (buf[i] !== ESC) { i++; continue; }

        // Match "\x1b[?" <digits and semicolons> ("h" | "l").
        var j = i + 1, ps = -1, partial = false;
        if (j >= buf.length) partial = true;
        else if (buf[j] !== CSI) { i++; continue; }
        else if (++j >= buf.length) partial = true;
        else if (buf[j] !== PRIV) { i = j; continue; }
        else {
            ps = ++j;
            while (j < buf.length && ((buf[j] >= 0x30 && buf[j] <= 0x39) || buf[j] === 0x3b)) j++;
            if (j >= buf.length) partial = true;
        }

        // Chunk ended mid-sequence: hold the tail and finish it next write, the
        // way a real emulator does. Anything implausibly long isn't ours.
        if (partial) {
            if (buf.length - i > MAX_CARRY) { i++; continue; }
            if (!pieces) pieces = [];
            take(i);
            s.carry = buf.slice(i);
            copied = buf.length;
            break;
        }

        var fin = buf[j];
        if (fin !== SET && fin !== RESET) { i = j + 1; continue; }

        var text = '';
        for (var k = ps; k < j; k++) text += String.fromCharCode(buf[k]);
        var raw = text.length ? text.split(';') : [];
        var kept = [];
        for (var n = 0; n < raw.length; n++) {
            var p = parseInt(raw[n], 10);
            if (!isNaN(p) && isMouseMode(p)) { applyMode(s, p, fin === SET); continue; }
            // An app leaving the alt screen is done with the mouse whether or not it
            // said so. Without this, a TUI that dies mid-session leaves tracking on
            // and the next wheel tick types escape bytes into whatever replaced it.
            if (p === 1049 && fin === RESET) s.tracking = {};
            kept.push(raw[n]);
        }
        if (kept.length === raw.length) { i = j + 1; continue; }

        // Re-emit the sequence without our params so co-set modes still land.
        if (!pieces) pieces = [];
        take(i);
        if (kept.length) pieces.push(ascii('\x1b[?' + kept.join(';') + String.fromCharCode(fin)));
        copied = j + 1;
        i = j + 1;
    }

    if (!pieces) return buf;
    take(buf.length);

    var total = 0;
    for (var x = 0; x < pieces.length; x++) total += pieces[x].length;
    var out = new Uint8Array(total);
    var off = 0;
    for (var y = 0; y < pieces.length; y++) { out.set(pieces[y], off); off += pieces[y].length; }
    return out;
}

/** Write PTY output to a term with mouse modes filtered out. */
export function writeTerm(term, data, cb) {
    if (!term) return;
    var bytes = typeof data === 'string' ? encoder.encode(data)
        : (data instanceof Uint8Array ? data : new Uint8Array(data));
    term.write(filterBytes(term, bytes), cb);
}

/** True when the app has asked for mouse events, so the wheel belongs to it. */
export function appMouseActive(term) {
    var s = term && states.get(term);
    if (!s) return false;
    for (var k in s.tracking) if (s.tracking[k]) return true;
    return false;
}

/** One wheel notch, encoded the way the app asked for it. Null if it doesn't want one. */
export function wheelReport(term, up, col, row) {
    if (!appMouseActive(term)) return null;
    var btn = up ? 64 : 65;
    var enc = stateFor(term).encoding;
    if (enc === 'sgr') return '\x1b[<' + btn + ';' + col + ';' + row + 'M';
    if (enc === 'urxvt') return '\x1b[' + (btn + 32) + ';' + col + ';' + row + 'M';
    // X10: one byte per field, offset by 32, coordinates saturate at 223.
    return '\x1b[M' + String.fromCharCode(32 + btn, 32 + Math.min(col, 223), 32 + Math.min(row, 223));
}

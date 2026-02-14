import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { formatAuditTime } from './helpers.js';
import { sendTunnelStream } from './tunnel.js';

export function closeAuditOverlay() {
    var overlay = document.getElementById('audit-overlay');
    if (overlay.style.display === 'none') return;
    overlay.style.display = 'none';
    document.getElementById('audit-download').style.display = 'none';
    var auditTerm = overlay._auditTerm;
    if (auditTerm) { auditTerm.dispose(); overlay._auditTerm = null; }
    if (overlay._playTimer) { clearTimeout(overlay._playTimer); overlay._playTimer = null; }
}

export function openAuditReplay(wingId, sessionId) {
    var overlay = document.getElementById('audit-overlay');
    var termEl = document.getElementById('audit-terminal');
    var playBtn = document.getElementById('audit-play');
    var timeEl = document.getElementById('audit-time');
    var speedInput = document.getElementById('audit-speed');
    var speedLabel = document.getElementById('audit-speed-label');
    var downloadBtn = document.getElementById('audit-download');
    var closeBtn = document.getElementById('audit-close');

    history.pushState({ view: 'audit' }, '');
    overlay.style.display = '';
    termEl.innerHTML = '';

    speedInput.style.display = '';
    speedLabel.style.display = '';
    timeEl.style.display = '';
    downloadBtn.style.display = 'none';

    var auditCols = 120, auditRows = 40;
    var auditTerm = null;
    var auditFit = null;
    var ndjsonHeader = '';

    var frames = [];
    var playing = false;
    var playTimer = null;
    var frameIndex = 0;
    var speed = 1;

    function initTerm() {
        if (auditTerm) return;
        auditTerm = new Terminal({ fontSize: 14, cols: auditCols, rows: auditRows, theme: { background: '#0d0d1a' }, convertEol: false });
        auditFit = new FitAddon();
        auditTerm.loadAddon(auditFit);
        auditTerm.open(termEl);
    }

    playBtn.textContent = 'loading...';
    playBtn.disabled = true;

    var auditStreamDone = false;
    sendTunnelStream(wingId, { type: 'audit.request', session_id: sessionId, kind: 'pty' }, function(chunk) {
        if (Array.isArray(chunk)) {
            frames.push(chunk);
        } else if (chunk.width) {
            auditCols = chunk.width;
            auditRows = chunk.height;
            ndjsonHeader = JSON.stringify(chunk);
        }
    }).then(function() {
        auditStreamDone = true;
        if (frames.length > 0) {
            playBtn.textContent = 'play';
            playBtn.disabled = false;
            downloadBtn.style.display = '';
        } else {
            playBtn.textContent = 'no data';
            playBtn.disabled = true;
        }
    }).catch(function() {
        auditStreamDone = true;
        playBtn.textContent = 'error';
    });

    function decodeBase64UTF8(b64) {
        var bin = atob(b64);
        var bytes = new Uint8Array(bin.length);
        for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        return new TextDecoder().decode(bytes);
    }

    function playFrame() {
        if (frameIndex >= frames.length) {
            playing = false;
            playBtn.textContent = 'replay';
            return;
        }
        if (!auditTerm) initTerm();
        var f = frames[frameIndex];
        if (f[1] === 'r') {
            var parts = f[2].split('x');
            var newCols = parseInt(parts[0]);
            var newRows = parseInt(parts[1]);
            if (newCols > 0 && newRows > 0) {
                auditTerm.resize(newCols, newRows);
            }
        } else {
            var data = f[2];
            try { data = decodeBase64UTF8(data); } catch (e) {}
            auditTerm.write(data);
        }
        frameIndex++;
        var elapsed = f[0];
        timeEl.textContent = formatAuditTime(elapsed);

        if (frameIndex < frames.length) {
            var delay = (frames[frameIndex][0] - f[0]) * 1000 / speed;
            delay = Math.min(delay, 2000);
            playTimer = setTimeout(playFrame, delay);
        } else {
            playing = false;
            playBtn.textContent = 'replay';
        }
    }

    playBtn.onclick = function() {
        if (playing) {
            playing = false;
            clearTimeout(playTimer);
            playBtn.textContent = 'play';
        } else {
            if (frameIndex >= frames.length) {
                frameIndex = 0;
                if (auditTerm) auditTerm.reset();
            }
            playing = true;
            playBtn.textContent = 'pause';
            playFrame();
        }
    };

    speedInput.oninput = function() {
        speed = parseInt(speedInput.value) || 1;
        speedLabel.textContent = speed + 'x';
    };

    downloadBtn.onclick = function() {
        var text = (ndjsonHeader || '{"version":2,"width":' + auditCols + ',"height":' + auditRows + '}') + '\n';
        for (var i = 0; i < frames.length; i++) {
            text += JSON.stringify(frames[i]) + '\n';
        }
        var blob = new Blob([text], { type: 'text/plain' });
        var a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'audit-' + sessionId + '.cast';
        a.click();
        URL.revokeObjectURL(a.href);
    };

    closeBtn.onclick = function() {
        playing = false;
        clearTimeout(playTimer);
        overlay._playTimer = null;
        if (auditTerm) { auditTerm.dispose(); overlay._auditTerm = null; }
        downloadBtn.style.display = 'none';
        overlay.style.display = 'none';
        history.back();
    };

    document.getElementById('audit-backdrop').onclick = closeBtn.onclick;
}

export function openAuditKeylog(wingId, sessionId) {
    var overlay = document.getElementById('audit-overlay');
    var termEl = document.getElementById('audit-terminal');
    var playBtn = document.getElementById('audit-play');
    var timeEl = document.getElementById('audit-time');
    var speedInput = document.getElementById('audit-speed');
    var speedLabel = document.getElementById('audit-speed-label');
    var downloadBtn = document.getElementById('audit-download');
    var closeBtn = document.getElementById('audit-close');

    history.pushState({ view: 'audit' }, '');
    overlay.style.display = '';
    termEl.innerHTML = '<pre class="audit-keylog" style="color:#ccc;font-size:13px;padding:12px;overflow:auto;height:100%;margin:0;white-space:pre-wrap;"></pre>';
    var pre = termEl.querySelector('pre');
    playBtn.style.display = 'none';
    speedInput.style.display = 'none';
    speedLabel.style.display = 'none';
    timeEl.style.display = 'none';
    downloadBtn.style.display = 'none';

    sendTunnelStream(wingId, { type: 'audit.request', session_id: sessionId, kind: 'keylog' }, function(chunk) {
        if (chunk.data) pre.textContent += chunk.data + '\n';
        else if (typeof chunk === 'string') pre.textContent += chunk + '\n';
    }).then(function() {
        if (!pre.textContent) {
            pre.textContent = 'no keylog data';
        } else {
            downloadBtn.style.display = '';
        }
    }).catch(function() {
        if (!pre.textContent) pre.textContent = 'error loading keylog';
    });

    downloadBtn.onclick = function() {
        var blob = new Blob([pre.textContent], { type: 'text/plain' });
        var a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'keylog-' + sessionId + '.log';
        a.click();
        URL.revokeObjectURL(a.href);
    };

    closeBtn.onclick = function() {
        downloadBtn.style.display = 'none';
        overlay.style.display = 'none';
        playBtn.style.display = '';
        speedInput.style.display = '';
        speedLabel.style.display = '';
        timeEl.style.display = '';
        history.back();
    };
    document.getElementById('audit-backdrop').onclick = closeBtn.onclick;
}

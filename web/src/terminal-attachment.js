import { terminalReferenceMatches } from './session-reference.js';

export function terminalControlFailure(message) {
    if (typeof message !== 'string') return null;
    var busy = message.match(/(?:^|desc = )terminal input owned by (.+); use explicit takeover to take control$/);
    if (busy) return { kind: 'busy', text: 'Terminal input is controlled by ' + busy[1] + '.' };
    if (/(?:^|desc = )terminal attachment (?:was )?taken over; attach again (?:before requesting|to request) control$/.test(message)) {
        return { kind: 'revoked', text: 'Another attachment took control of terminal input.' };
    }
    return null;
}

export function terminalControlOptions(user, wing, mode) {
    if (!user || user.release_channel !== 'preview' || !wing) return null;
    if (mode === 'takeover') return { takeover: true };
    if (mode === 'observe' && wing.spectate) return { spectate: true };
    return null;
}

export function ptyAttachRequest(sessionId, wingId, publicKey, options) {
    options = options || {};
    var request = { type: 'pty.attach', session_id: sessionId, public_key: publicKey };
    if (wingId) request.wing_id = wingId;
    if (options.authToken) request.auth_token = options.authToken;
    if (options.cols && options.rows) { request.cols = options.cols; request.rows = options.rows; }
    if (options.spectate) request.spectate = true;
    else if (options.takeover === true) request.takeover = true;
    return request;
}

export function ptyResizeRequest(sessionId, controllerId, size) {
    var request = { type: 'pty.resize', session_id: sessionId, cols: size.cols, rows: size.rows };
    if (controllerId) request.controller_id = controllerId;
    return request;
}

export function currentTerminalResize(binding, state, size) {
    if (!terminalReferenceMatches(state, binding.sessionId, binding.wingId, binding.socket) ||
        state.ptyControllerId !== binding.controllerId || state.spectating || state.ptyInputBlocked) return null;
    return ptyResizeRequest(binding.sessionId, binding.controllerId, size);
}

// All text from the wing is inserted as text, and every click retains the
// exact wing/session supplied by the caller. No action runs during rendering.
export function renderTerminalControlNotice(element, failure, wing, user, onAction) {
    if (!element) return;
    element.replaceChildren();
    element.style.display = failure ? '' : 'none';
    if (!failure) return;
    var text = document.createElement('span');
    text.textContent = failure.text + ' Choose how to attach to this session.';
    element.appendChild(text);
    ['observe', 'takeover'].forEach(function(mode) {
        var options = terminalControlOptions(user, wing, mode);
        if (!options) return;
        var button = document.createElement('button');
        button.type = 'button';
        button.className = 'btn-sm';
        button.textContent = mode === 'observe' ? 'Observe' : 'Take control';
        button.title = mode === 'observe' ? 'Read terminal output without changing input ownership' : 'Replace the current input attachment and keep the agent running';
        button.addEventListener('click', function(event) { event.stopPropagation(); onAction(mode); });
        element.appendChild(button);
    });
}

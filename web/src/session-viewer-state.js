import { searchRoute } from './session-search-state.js';

export function qualifiedSessionEquals(a, b) {
    return Boolean(a?.origin_wing_id && a?.session_id && a.origin_wing_id === b?.origin_wing_id && a.session_id === b?.session_id);
}

export function createSessionViewer({ inventory, request, user, wings, update, uuid, prompts, now = Date.now }) {
    let generation = 0, owner, active = null, busy = false, reading = false;
    function publish() { if (owner === user()?.id) update(active ? { ...active, busy: busy || reading } : null); }
    function valid(ticket) { return ticket === generation && owner === user()?.id && active; }
    function envelope(row, operation, args) {
        return { type: 'session.control', operation: 'session_action', arguments: {
            origin_wing_id: row.origin_wing_id, session_id: row.session_id, operation, arguments: args,
        } };
    }
    function check(result, row, route, attachment, sequence) {
        if (!qualifiedSessionEquals(row, result) || result.route?.wing_id !== route.wing_id ||
            (attachment && (result.attachment_id !== attachment || result.sequence !== sequence))) throw Error('Session response identity mismatch');
        return result;
    }
    function fresh() {
        return active && searchRoute({ ...active.row, routes: [active.route] }, wings(), now());
    }
    async function action(operation, args, route = active.route) {
        return check(await request(route.wing_id, envelope(active.row, operation, args)), active.row, route);
    }
    function attachmentEnvelope(operation, state, extra = {}) {
        return { type: 'session.control', operation, arguments: { attachment_id: state.attachment, sequence: ++state.attachmentSequence,
            origin_wing_id: state.row.origin_wing_id, session_id: state.row.session_id, ...extra } };
    }
    async function attachmentAction(operation, state, extra = {}) {
        const body = attachmentEnvelope(operation, state, extra);
        return check(await request(state.route.wing_id, body), state.row, state.route, state.attachment, body.arguments.sequence);
    }
    async function release(state) {
        if (!state?.attachment) return;
        // Cleanup uses the old capability and selected route, even after selection changes.
        await attachmentAction('fleet_detach', state);
    }
    async function close() {
        const old = active;
        generation++; active = null; busy = false; reading = false;
        update(null);
        try { await release(old); return true; } catch (error) { return false; }
    }
    async function open(row) {
        const cleanup = close();
        const opening = generation;
        const released = await cleanup;
        if (opening !== generation) return;
        owner = user()?.id;
        if (!owner || !row?.origin_wing_id || !row?.session_id) return;
        const ticket = ++generation;
        let pendingPrompt = null, promptStorageError = false;
        try { pendingPrompt = prompts?.get(owner, row) || null; } catch (error) { promptStorageError = true; }
        active = { generation: ticket, owner, attachmentSequence: 0, releaseWarning: !released, row: { ...row }, route: null, events: [], cursor: 0, hasMore: false, terminal: '', mode: 'observe', status: released ? 'Opening session…' : 'Prior hand-back unconfirmed; its idle lease will expire. Opening session…', pendingPrompt, promptStorageError, attachment: null };
        publish();
        try {
            const found = (await inventory(row.session_id)).sessions.filter(candidate => qualifiedSessionEquals(candidate, row));
            if (!valid(ticket)) return;
            if (found.length !== 1) throw Error('Exact session identity unavailable. Refresh inventory.');
            active.row = found[0];
            active.route = searchRoute(found[0], wings(), now());
            if (!active.route) throw Error('No healthy route. Refresh inventory.');
            active.status = pendingPrompt ? `Unconfirmed message · ${pendingPrompt.request_id}. Recheck before sending.` : promptStorageError ? 'Saved message state unavailable. Message submission is disabled.' : active.row.live ? 'Viewing · agent continues running' : 'Viewing recorded session';
            publish();
            await read();
        } catch (error) { if (valid(ticket)) { active.status = error.message; publish(); } }
    }
    async function read() {
        if (!active?.route || reading || busy) return;
        if (owner !== user()?.id) { await close(); return; }
        const ticket = generation;
        const state = active;
        reading = true; publish();
        try {
            if (!state.attachment && !fresh()) throw Error('Route expired. Refresh before acting.');
            const result = await action('session_read', { after_cursor: state.cursor, limit: 200 });
            if (!valid(ticket)) return;
            const view = result.result?.lifecycle;
            if (!view || !Array.isArray(view.events) || !Number.isSafeInteger(view.cursor) || view.cursor < state.cursor) throw Error('Transcript contract mismatch');
            if (view.events.length > 200) throw Error('Transcript page exceeds limit');
            const textEvents = view.events.map(event => ({ sequence: event.sequence, type: event.type, role: event.role, text: typeof event.text === 'string' ? event.text.length > 16384 ? event.text.slice(0, 16384) + '\n[Text truncated]' : event.text : '', reason: typeof event.reason === 'string' ? event.reason.slice(0, 1024) : '' }));
            const nextEvents = [...state.events, ...textEvents];
            state.trimmedEvents = (state.trimmedEvents || 0) + Math.max(0, nextEvents.length - 400);
            state.events = nextEvents.slice(-400);
            state.cursor = view.cursor; state.hasMore = view.has_more === true;
            state.lifecycle = { state: view.state, ready: view.ready, process_alive: view.process_alive };
            state.route = { ...result.route, healthy: true, observed_at: new Date(now()).toISOString() };
            if (state.row.live || state.attachment) {
                if (state.attachment) {
                    const output = await attachmentAction('fleet_read', state);
                    if (!valid(ticket)) return;
                    state.terminal = output.screen || '';
                    if (output.ended) { state.mode = 'lost'; state.status = 'Terminal attachment ended. Refresh and explicitly attach again.'; }
                } else {
                    const output = await action('terminal_read', {});
                    if (!valid(ticket)) return;
                    state.terminal = output.result?.ansi || '';
                }
            }
            if (state.mode !== 'lost' && !state.pendingPrompt && !state.promptStorageError) state.status = state.mode === 'control' ? 'You control terminal input' : state.row.live ? 'Viewing · agent continues running' : 'Viewing recorded session';
        } catch (error) {
            if (valid(ticket)) { state.status = error.message; if (state.attachment) state.mode = 'lost'; }
        } finally { if (valid(ticket)) { reading = false; publish(); } }
    }
    async function mutate(fn) {
        if (!active || busy || reading || owner !== user()?.id) return;
        const ticket = generation;
        busy = true; publish();
        try { await fn(ticket); }
        catch (error) { if (valid(ticket)) active.status = error.message; }
        finally { if (valid(ticket)) { busy = false; publish(); } }
    }
    async function sendMessage(input, reconcile = false) {
        return mutate(async ticket => {
            if (active.promptStorageError) throw Error('Saved message state unavailable. Restore browser storage before sending.');
            if (!active.route || !fresh() || active.attachment) throw Error('Refresh the session and hand back terminal control before sending a message.');
            if (!reconcile && active.pendingPrompt) throw Error('Recheck the original message before submitting another.');
            const prompt = reconcile ? active.pendingPrompt : { request_id: uuid(), input, timeout_seconds: 5, route: { ...active.route } };
            if (!prompt || !prompt.input.trim()) throw Error('Enter a message.');
            if (new TextEncoder().encode(prompt.input).length > 65536) throw Error('Message exceeds 64 KiB.');
            // Save before dispatch. Failure cannot become an untracked mutation.
            prompts?.set(owner, active.row, prompt);
            active.pendingPrompt = prompt; active.receipt = null;
            active.status = 'Sending message…'; publish();
            let response;
            try { response = await action('session_prompt', { request_id: prompt.request_id, input: prompt.input, timeout_seconds: prompt.timeout_seconds }, prompt.route); }
            catch (error) { if (valid(ticket)) active.status = `Message outcome unknown · ${prompt.request_id}. Recheck this message before sending again.`; return; }
            if (!valid(ticket)) return;
            const receipt = response.result?.receipt;
            if (!receipt || receipt.request_id !== prompt.request_id || receipt.session_id !== active.row.session_id) throw Error('Message receipt mismatch; retain the original request ID.');
            active.receipt = receipt;
            active.status = receipt.native_receipt_observed === true ? 'Message text observed by provider' : receipt.definitely_not_sent === true ? 'Message was not sent · provider is not ready or input is owned elsewhere' : `Message unconfirmed · ${prompt.request_id}. Recheck this message before sending again.`;
            if (receipt.native_receipt_observed === true || receipt.definitely_not_sent === true) {
                prompts?.set(owner, active.row, null);
                active.pendingPrompt = null;
            }
        });
    }
    async function takeControl() {
        return mutate(async ticket => {
            if (!fresh() || !active.row.live || active.attachment) throw Error('Refresh this live session before taking control.');
            const state = active;
            state.attachment = uuid(); state.attachmentSequence = 0; state.status = 'Taking terminal control…'; publish();
            try {
                const result = await attachmentAction('fleet_attach', state, { takeover: true });
                if (!valid(ticket)) { try { await release(state); } catch (error) {} return; }
                state.route = result.route; state.mode = 'control'; state.status = 'You control terminal input';
            } catch (error) {
                if (valid(ticket)) { state.mode = 'lost'; state.status = 'Takeover outcome unknown. Hand back or close; do not repeat takeover automatically.'; }
            }
        });
    }
    async function handBack() {
        return mutate(async ticket => {
            const state = active;
            if (!state.attachment) return;
            await release(state);
            if (valid(ticket)) { state.attachment = null; state.mode = 'observe'; state.status = 'Terminal input released · agent continues running'; }
        });
    }
    async function terminalInput(input) {
        return mutate(async ticket => {
            if (!active.attachment || active.mode !== 'control') throw Error('Take control before typing.');
            const state = active;
            if (new TextEncoder().encode(input).length > 16384) throw Error('Terminal input exceeds 16 KiB.');
            try {
                await attachmentAction('fleet_input', state, { input });
                if (valid(ticket)) state.status = 'Terminal input submitted · provider receipt not confirmed';
            } catch (error) { if (valid(ticket)) { state.mode = 'lost'; state.status = 'Input outcome unknown. Read state before deliberately retrying; no input was replayed.'; } }
        });
    }
    return { open, close, read, sendMessage, takeControl, handBack, terminalInput, current: () => active };
}

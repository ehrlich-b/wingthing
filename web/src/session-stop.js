// Request exactly once; verification is read-only and wing-qualified.
export async function stopSessionVerified(session, request, options = {}) {
    const result = await request(session.wing_id, { type: 'pty.kill', session_id: session.id });
    if (!result || result.process_exited !== true) throw new StopVerificationError('Stop requested; process exit unverified. Inspect the session or check status.', false);
    const attempts = options.attempts ?? 5;
    const wait = options.wait || (ms => new Promise(resolve => setTimeout(resolve, ms)));
    for (let index = 0; index < attempts; index++) {
        let inventory;
        try {inventory = await request(session.wing_id, { type: 'sessions.list' });}
        catch(error) {throw new StopVerificationError('Status check failed; process exit receipt retained. '+error.message,true);}
        if (!inventory || !Array.isArray(inventory.sessions)) throw new StopVerificationError('Stop requested; active inventory unavailable. Check status.', true);
        if (!inventory.sessions.some(row => row.session_id === session.id)) return result;
        if (index + 1 < attempts) await wait(250);
    }
    throw new StopVerificationError('Stop requested; session still in active inventory. Inspect before retrying.', true);
}

export class StopVerificationError extends Error {
    constructor(message, processExited) { super(message); this.processExited=processExited===true; }
}
export function stopConfirmationText(session, computer, folder) {
    return `Stop “${session.name || 'unnamed session'}” on ${computer}. Working folder: ${folder || 'not recorded'}.`;
}
export async function checkStoppedSession(session, request, processExited=false) {
    const inventory=await request(session.wing_id,{type:'sessions.list'});
    if (!inventory || !Array.isArray(inventory.sessions)) throw new Error('Active inventory unavailable; process exit remains unverified.');
    const listed=inventory.sessions.some(row=>row.session_id===session.id);
    return {listed,verified:processExited===true && !listed};
}

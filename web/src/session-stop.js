// Request exactly once; verification is read-only and wing-qualified.
export async function stopSessionVerified(session, request, options = {}) {
    const result = await request(session.wing_id, { type: 'pty.kill', session_id: session.id });
    if (!result || result.process_exited !== true) throw new Error('Stop requested; process exit unverified. Update the wing and inspect the session before retrying.');
    const attempts = options.attempts ?? 5;
    const wait = options.wait || (ms => new Promise(resolve => setTimeout(resolve, ms)));
    for (let index = 0; index < attempts; index++) {
        const inventory = await request(session.wing_id, { type: 'sessions.list' });
        if (!inventory || !Array.isArray(inventory.sessions)) throw new Error('Stop requested; active inventory unavailable. Refresh to verify.');
        if (!inventory.sessions.some(row => row.session_id === session.id)) return result;
        if (index + 1 < attempts) await wait(250);
    }
    throw new Error('Stop requested; session still in active inventory. Inspect before retrying.');
}

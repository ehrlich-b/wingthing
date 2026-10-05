export async function requestCanvasStop(entry, request, onChange) {
    if (entry.stopPending) return false;
    if (entry.starting || !entry.id || !entry.wingId) {
        entry.stopError = 'Launching: wait for the session to start before stopping it.';
        if (onChange) onChange();
        return false;
    }
    entry.stopPending = true;
    entry.stopError = '';
    if (onChange) onChange();
    try {
        var result = await request(entry.wingId, { type: 'pty.kill', session_id: entry.id });
        if (!result || (result.ok !== true && result.ok !== 'true')) throw new Error('The wing did not confirm the stop request');
        return true;
    } catch (error) {
        entry.stopError = 'Could not stop session: ' + (error && error.message || 'request failed');
        return false;
    } finally {
        entry.stopPending = false;
        if (onChange) onChange();
    }
}

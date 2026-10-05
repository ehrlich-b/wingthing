// New session links carry the execution wing. Legacy links remain readable but
// are resolved only when their session ID is unambiguous in the inventory.
export function sessionRoute(sessionId, wingId) {
    return '#s/' + encodeURIComponent(sessionId) + (wingId ? '?wing=' + encodeURIComponent(wingId) : '');
}

export function parseSessionRoute(hash) {
    if (!hash || !hash.startsWith('#s/')) return null;
    var parts = hash.substring(3).split('?');
    try {
        var id = decodeURIComponent(parts[0]);
        if (!id) return null;
        var parameters = new URLSearchParams(parts.slice(1).join('?'));
        return { sessionId: id, wingId: parameters.get('wing') || undefined };
    } catch (error) { return null; }
}

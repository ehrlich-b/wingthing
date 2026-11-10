// New links always identify both the machine and session.
export function sessionRoute(sessionId, wingId) {
    return '#session/' + encodeURIComponent(wingId || '') + '/' + encodeURIComponent(sessionId);
}

// Keep old bookmarks readable at this single boundary.
export function parseSessionRoute(hash) {
    const qualified = hash?.match(/^#session\/([^/]+)\/([^/?]+)$/);
    if (qualified) {
        try { return { sessionId: decodeURIComponent(qualified[2]), wingId: decodeURIComponent(qualified[1]) }; } catch { return null; }
    }
    if (!hash || !hash.startsWith('#s/')) return null;
    const parts = hash.substring(3).split('?');
    try {
        const sessionId = decodeURIComponent(parts[0]);
        if (!sessionId) return null;
        return { sessionId, wingId: new URLSearchParams(parts.slice(1).join('?')).get('wing') || undefined };
    } catch { return null; }
}

export function resolveShellRoute(hash = '', inventory = []) {
    let target = hash || '#', focusNeeds = false;
    try {
        let legacy = parseSessionRoute(target);
        if (target.startsWith('#chat/')) legacy = parseSessionRoute('#s/' + target.slice(6));
        if (legacy) {
            const matches = inventory.filter(s => s.id === legacy.sessionId);
            const machine = legacy.wingId || (matches.length === 1 && matches[0].wing_id);
            target = machine ? sessionRoute(legacy.sessionId, machine) : '#sessions';
        }
        const approval = target.match(/^#approve\/([^/]+)\/([^/?]+)$/);
        if (approval) {
            target = sessionRoute(decodeURIComponent(approval[2]), decodeURIComponent(approval[1]));
            focusNeeds = true;
        }
        if (target.startsWith('#w/')) target = '#machine/' + encodeURIComponent(decodeURIComponent(target.slice(3)));
        if (target === '#needs' || target === '#decisions') target = '#';
        let route;
        if (target === '#') route = { view: 'wing' };
        else if (target === '#sessions?state=need') route = { view: 'sessions', state: 'need' };
        else if (/^#(sessions|machines|new|pair|canvas)$/.test(target)) route = { view: target.slice(1) };
        else if (/^#account(?:\/[^/?]+)?$/.test(target)) route = { view: 'account', orgSlug: target.slice(9) || null };
        else {
            const simple = target.match(/^#(new|machine)\/([^/?]+)$/);
            const session = target.match(/^#session\/([^/]+)\/([^/?]+)$/);
            if (simple) route = { view: simple[1], wingId: decodeURIComponent(simple[2]) };
            if (session) route = { view: 'session', wingId: decodeURIComponent(session[1]), sessionId: decodeURIComponent(session[2]) };
        }
        return route ? { hash: target, route, focusNeeds } : { hash: '#', route: { view: 'wing' }, focusNeeds: false };
    } catch { return { hash: '#', route: { view: 'wing' }, focusNeeds: false }; }
}

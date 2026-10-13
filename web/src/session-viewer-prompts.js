// A pending message keeps the original immutable request across route refresh,
// viewer close and reload in the same tab. Browser storage is private to this
// application; content is never placed in URLs, logs or cross-owner cache keys.
export function sessionPromptStore(storage) {
    const key = 'wt_session_viewer_prompts_v1';
    function read(owner) {
        const raw = storage.getItem(key);
        if (!raw) return {};
        const saved = JSON.parse(raw);
        if (saved?.owner !== owner) return {};
        const prompts = saved.prompts;
        if (!prompts || typeof prompts !== 'object' || Array.isArray(prompts) || Object.keys(prompts).length > 32) throw Error('Saved message state is invalid.');
        for (const value of Object.values(prompts)) {
            if (!value || typeof value.request_id !== 'string' || !value.request_id || value.request_id.length > 128 ||
                typeof value.input !== 'string' || new TextEncoder().encode(value.input).length > 65536 ||
                !value.route || typeof value.route.wing_id !== 'string' || !value.route.wing_id || value.timeout_seconds !== 5) throw Error('Saved message receipt is invalid.');
        }
        return prompts;
    }
    function identity(row) { return JSON.stringify([row.origin_wing_id, row.session_id]); }
    return {
        get(owner, row) { return read(owner)[identity(row)] || null; },
        set(owner, row, prompt) {
            const prompts = read(owner);
            const resource = identity(row);
            if (prompt) {
                if (!prompts[resource] && Object.keys(prompts).length >= 32) throw Error('Reconcile pending messages before sending more.');
                prompts[resource] = prompt;
            } else delete prompts[resource];
            storage.setItem(key, JSON.stringify({ owner, prompts }));
        },
        clear() { storage.removeItem(key); },
    };
}

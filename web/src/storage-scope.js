import { CACHE_OWNER_KEY, S } from './state.js';

function removeWingthingStorage(storage) {
    if (!storage) return;
    var keys = [];
    try {
        for (var i = 0; i < storage.length; i++) {
            var key = storage.key(i);
            if (key && key.indexOf('wt_') === 0) keys.push(key);
        }
        keys.forEach(function(key) { storage.removeItem(key); });
    } catch (e) {}
}

// Browser caches contain wing names, project paths, terminal snapshots, TOFU
// pins, and short-lived tunnel grants. The hosted origin can serve more than
// one account in the same browser profile, so bind that state to the current
// authenticated user before rendering any cached data. An unscoped cache from
// an older release is cleared once instead of guessing who owned it.
export function scopeBrowserStateToUser(userId) {
    if (!userId) return false;
    var owner = null;
    try { owner = localStorage.getItem(CACHE_OWNER_KEY); } catch (e) {}
    if (owner === userId) return false;

    // The X25519 identity is ephemeral to this tab and is not an authorization
    // grant. Preserve it so the first scoped reload does not invalidate a new
    // passkey-derived tunnel token; actual grants and cached payloads are reset.
    var identityPublic = null;
    var identityPrivate = null;
    try {
        identityPublic = sessionStorage.getItem('wt_identity_pubkey');
        identityPrivate = sessionStorage.getItem('wt_identity_privkey');
    } catch (e) {}

    removeWingthingStorage(localStorage);
    removeWingthingStorage(sessionStorage);
    try {
        if (identityPublic) sessionStorage.setItem('wt_identity_pubkey', identityPublic);
        if (identityPrivate) sessionStorage.setItem('wt_identity_privkey', identityPrivate);
    } catch (e) {}
    try { localStorage.setItem(CACHE_OWNER_KEY, userId); } catch (e) {}

    S.wingsData = [];
    S.sessionsData = [];
    S.sessionNotifications = {};
    S.wingPastSessions = {};
    S.tunnelKeys = {};
    S.tunnelAuthTokens = {};
    return true;
}

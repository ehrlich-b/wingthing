// Returning from iOS suspension only reads. Coalesce foreground/online/page
// events so they cannot produce overlapping refreshes or resend saved input.
export function createForegroundRefresh(refresh, visible, now = Date.now) {
    var pending = null;
    var last = -Infinity;
    return function() {
        if (!visible()) return Promise.resolve(false);
        if (pending) return pending;
        if (now() - last < 1000) return Promise.resolve(false);
        last = now();
        pending = Promise.resolve().then(refresh).then(function() { return true; }).finally(function() { pending = null; });
        return pending;
    };
}

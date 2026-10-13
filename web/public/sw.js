// Online-only PWA. No request or response is written to a cache.
const offline = '<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="theme-color" content="#1a1a2e"><title>Wingthing · disconnected</title><style>body{margin:0;padding:calc(48px + env(safe-area-inset-top)) 28px;background:#1a1a2e;color:#eee;font:17px/1.6 system-ui}h1{font-size:28px}button{padding:14px 20px;color:#eee;background:#315442;border:0;border-radius:12px;font:inherit}</style><h1>You’re disconnected</h1><p>Reconnect to read your coordinator and sessions. Messages and approvals require a live connection.</p><button onclick="location.reload()">Try again</button></html>';
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', event => {
    event.waitUntil(caches.keys().then(names => Promise.all(names.filter(name => name.startsWith('wt-') || name.startsWith('wingthing')).map(name => caches.delete(name)))).then(() => self.clients.claim()));
});
self.addEventListener('fetch', event => {
    // APIs, auth, files and WebSockets always bypass the worker. A navigation
    // failure returns a constant page with no user/session data and no controls.
    const url = new URL(event.request.url);
    if (event.request.mode !== 'navigate' || url.origin !== self.location.origin || !url.pathname.startsWith('/app/')) return;
    event.respondWith(fetch(event.request, { cache: 'no-store' }).catch(() => new Response(offline, { status: 503, headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } })));
});

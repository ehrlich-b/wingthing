// Online-only PWA. No request or response is written to a cache.
const offline = '<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="theme-color" content="#F3EDE0"><title>Wingthing · disconnected</title><style>body{margin:0;padding:calc(40px + env(safe-area-inset-top)) 24px;background:#F3EDE0;color:#1F1A15;font:18px/1.5 Georgia,serif}h1{font-size:30px;font-weight:600;margin:0 0 12px;padding-bottom:10px;border-bottom:3px double #1F1A15}p{max-width:36em}button{min-height:44px;padding:0 20px;color:#F3EDE0;background:#1F1A15;border:0;border-radius:3px;font:inherit}@media (prefers-color-scheme:dark){body{background:#221C17;color:#EEE4D4}h1{border-color:#EEE4D4}button{background:#EEE4D4;color:#221C17}}</style><h1>You’re disconnected</h1><p>Reconnect to read wing and your sessions. Messages and approvals need a live connection.</p><button onclick="location.reload()">Try again</button></html>';
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

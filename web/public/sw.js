// Only public app assets enter this cache. Navigation, APIs, WebSockets and
// tunnel requests always go straight to the network.
var STATIC_CACHE = 'wt-static-v1';
var APP_URL = new URL('./', self.location.href);

function staticAsset(request) {
    var url = new URL(request.url);
    if (request.method !== 'GET' || url.origin !== APP_URL.origin || url.search || request.headers.has('Authorization')) return false;
    if (!url.pathname.startsWith(APP_URL.pathname)) return false;
    var path = url.pathname.slice(APP_URL.pathname.length);
    return /^(assets\/[A-Za-z0-9_-]+\.(js|css)|fonts\/JetBrainsMono-(Regular|Bold)\.woff2|icons\/wt-(180|192|512)\.png|icons\/wt\.svg|manifest\.json|style\.css)$/.test(path);
}

function publicAsset(response, url) {
    if (!response.ok || response.status !== 200 || response.redirected || response.type !== 'basic') return false;
    if (/private|no-store/i.test(response.headers.get('Cache-Control') || '') || /cookie|authorization|\*/i.test(response.headers.get('Vary') || '')) return false;
    var type = response.headers.get('Content-Type') || '';
    var extension = new URL(url).pathname.split('.').pop();
    var types = { js: /javascript/i, css: /text\/css/i, woff2: /font\/woff2|application\/font-woff/i, png: /image\/png/i, svg: /image\/svg\+xml/i, json: /application\/(manifest\+json|json)/i };
    return !!types[extension] && types[extension].test(type);
}

self.addEventListener('install', function() { self.skipWaiting(); });
self.addEventListener('activate', function(event) {
    event.waitUntil(caches.keys().then(function(names) {
        return Promise.all(names.filter(function(name) { return name.startsWith('wt-static-') && name !== STATIC_CACHE; }).map(function(name) { return caches.delete(name); }));
    }).then(function() { return self.clients.claim(); }));
});
self.addEventListener('fetch', function(event) {
    if (!staticAsset(event.request)) return;
    event.respondWith(caches.open(STATIC_CACHE).then(async function(cache) {
        // Omit credentials even on the allowlisted public static paths.
        try {
            var response = await fetch(new Request(event.request, { credentials: 'omit' }));
            if (publicAsset(response, event.request.url)) await cache.put(event.request, response.clone());
            return response;
        } catch (error) {
            var saved = await cache.match(event.request);
            if (saved) return saved;
            throw error;
        }
    }));
});
self.addEventListener('notificationclick', function(event) {
    event.notification.close();
    var data = event.notification.data || {};
    var url = new URL(APP_URL.href);
    if (typeof data.wingId === 'string' && typeof data.conversationId === 'string' && data.conversationId) {
        url.hash = 'conversation/' + encodeURIComponent(data.conversationId) + '?wing=' + encodeURIComponent(data.wingId);
    } else if (typeof data.wingId === 'string' && typeof data.sessionId === 'string' && data.sessionId) {
        url.hash = 's/' + encodeURIComponent(data.sessionId) + '?wing=' + encodeURIComponent(data.wingId);
    }
    event.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(async function(clients) {
        var client = clients.find(function(client) { return client.url.startsWith(APP_URL.href); });
        if (client) {
            if (client.url !== url.href) await client.navigate(url.href);
            return client.focus();
        }
        return self.clients.openWindow(url.href);
    }));
});

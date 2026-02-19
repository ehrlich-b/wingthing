// Self-destruct: clear all caches and unregister
self.addEventListener('install', function () { self.skipWaiting(); });
self.addEventListener('activate', function (event) {
    event.waitUntil(
        caches.keys().then(function (names) {
            return Promise.all(names.map(function (n) { return caches.delete(n); }));
        }).then(function () { return self.registration.unregister(); })
    );
    self.clients.claim();
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

const source = await readFile(new URL('../public/sw.js', import.meta.url), 'utf8');
function harness() {
    const handlers = {}, writes = [], fetched = [], deleted = [], opened = [];
    let offline = false, headers = { 'Content-Type': 'text/javascript' }, redirected = false, windows = [];
    const saved = new Map();
    const response = () => ({ ok: true, status: 200, type: 'basic', redirected, headers: new Headers(headers), clone: response });
    const context = vm.createContext({ URL, Request, Headers,
        self: { location: { href: 'https://roost.test/app/sw.js' }, addEventListener: (name, handler) => { handlers[name] = handler; }, skipWaiting: () => {},
            clients: { claim: async () => {}, matchAll: async () => windows, openWindow: async url => opened.push(url) } },
        caches: { keys: async () => ['wt-static-old', 'wt-static-v1', 'another-app'], delete: async name => deleted.push(name), open: async () => ({ put: async (request, response) => { writes.push(request.url); saved.set(request.url, response); }, match: async request => saved.get(request.url) }) },
        fetch: async request => { fetched.push(request); if (offline) throw Error('offline'); return response(); },
    });
    vm.runInContext(source, context);
    return { handlers, writes, fetched, deleted, opened, set offline(value) { offline = value; }, set headers(value) { headers = value; }, set redirected(value) { redirected = value; }, set windows(value) { windows = value; }, fetch(url, init) {
        let result;
        handlers.fetch({ request: new Request(url, init), respondWith: promise => { result = promise; } });
        return result;
    } };
}

test('worker never intercepts authenticated APIs, tunnel traffic or navigations', () => {
    const h = harness();
    for (const path of ['/api/app/me', '/api/app/assets/result.js', '/app/', '/app/index.html', '/app/sw.js', '/ws/app', '/ws/tunnel', '/app/tunnel/result.js', '/app/assets/account.json', '/app/assets/index.js?token=secret', '/app/icons/other.png']) {
        assert.equal(h.fetch('https://roost.test' + path), undefined, path);
    }
    assert.equal(h.fetch('https://other.test/app/assets/index.js'), undefined);
    assert.equal(h.fetch('https://roost.test/app/assets/index.js', { headers: { Authorization: 'Bearer secret' } }), undefined);
    assert.equal(h.fetch('https://roost.test/app/assets/index.js', { method: 'POST' }), undefined);
    assert.equal(h.writes.length, 0);
    assert.equal(h.fetched.length, 0);
});

test('worker caches only public static responses and omits credentials', async () => {
    const h = harness(), url = 'https://roost.test/app/assets/index-abcd.js';
    await h.fetch(url);
    assert.deepEqual(h.writes, [url]);
    assert.equal(h.fetched[0].credentials, 'omit');
    h.offline = true;
    assert.ok(await h.fetch(url));
    await assert.rejects(h.fetch('https://roost.test/app/assets/missing.js'), /offline/);
    h.offline = false;
    for (const headers of [{ 'Content-Type': 'application/json' }, { 'Content-Type': 'text/html' }, { 'Content-Type': 'text/javascript', 'Cache-Control': 'private' }, { 'Content-Type': 'text/javascript', 'Cache-Control': 'no-store' }, { 'Content-Type': 'text/javascript', Vary: 'Cookie' }, { 'Content-Type': 'text/javascript', Vary: 'Authorization' }]) {
        h.headers = headers;
        await h.fetch(url);
        assert.equal(h.writes.length, 1);
    }
    h.headers = { 'Content-Type': 'text/javascript' };
    h.redirected = true;
    await h.fetch(url);
    assert.equal(h.writes.length, 1);
});

test('worker removes only its old static caches and opens the exact conversation from attention', async () => {
    const h = harness();
    let completion;
    h.handlers.activate({ waitUntil: promise => { completion = promise; } });
    await completion;
    assert.deepEqual(h.deleted, ['wt-static-old']);
    h.handlers.notificationclick({ notification: { close() {}, data: { wingId: 'mac/one', conversationId: 'root?one' } }, waitUntil: promise => { completion = promise; } });
    await completion;
    const url = 'https://roost.test/app/#conversation/root%3Fone?wing=mac%2Fone';
    assert.deepEqual(h.opened, [url]);
    const navigated = [];
    h.windows = [{ url: 'https://roost.test/app/', navigate: async url => navigated.push(url), focus: async () => navigated.push('focused') }];
    h.handlers.notificationclick({ notification: { close() {}, data: { wingId: 'mac/one', conversationId: 'root?one' } }, waitUntil: promise => { completion = promise; } });
    await completion;
    assert.deepEqual(navigated, [url, 'focused']);
});

test('install metadata and source copies include real SVG and PNG icons', async () => {
    assert.equal(await readFile(new URL('../sw.js', import.meta.url), 'utf8'), source);
    const manifestText = await readFile(new URL('../public/manifest.json', import.meta.url), 'utf8');
    assert.equal(await readFile(new URL('../manifest.json', import.meta.url), 'utf8'), manifestText);
    const manifest = JSON.parse(manifestText);
    assert.equal(manifest.display, 'standalone');
    assert.equal(manifest.scope, '/app/');
    assert.ok(manifest.name && manifest.short_name && manifest.theme_color);
    for (const icon of manifest.icons.filter(icon => icon.type === 'image/png')) {
        const png = await readFile(new URL('../public/' + icon.src, import.meta.url));
        assert.equal(png.subarray(1, 4).toString(), 'PNG');
        assert.equal(png.readUInt32BE(16) + 'x' + png.readUInt32BE(20), icon.sizes);
    }
    const html = await readFile(new URL('../index.html', import.meta.url), 'utf8');
    assert.match(html, /viewport-fit=cover/);
    assert.match(html, /apple-mobile-web-app-capable/);
    assert.match(html, /apple-touch-icon/);
    assert.match(html, /rel="manifest" href="%BASE_URL%manifest.json"/);
});

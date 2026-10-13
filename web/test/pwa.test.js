import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

const source = await readFile(new URL('../public/sw.js', import.meta.url), 'utf8');

test('worker never caches private responses and only supplies a constant disconnected navigation', async () => {
    const listeners = {}, fetched = [];
    const context = { URL, Response, self: { location: { origin: 'https://pwa.example' }, addEventListener: (type, listener) => { listeners[type] = listener; } }, fetch: async (...args) => { fetched.push(args); throw Error('offline'); } };
    vm.runInNewContext(source, context);
    assert.doesNotMatch(source, /caches\.open|cache\.put|cache\.add|match\(/);
    for (const [url, mode] of [['https://pwa.example/api/app/me', 'cors'], ['https://pwa.example/auth/login', 'navigate'], ['https://pwa.example/api/files', 'navigate'], ['https://other.example/app/', 'navigate']]) {
        let intercepted = false;
        listeners.fetch({ request: { url, mode }, respondWith: () => { intercepted = true; } });
        assert.equal(intercepted, false, url);
    }
    let promise;
    listeners.fetch({ request: { url: 'https://pwa.example/app/', mode: 'navigate' }, respondWith: value => { promise = value; } });
    const response = await promise;
    assert.equal(response.status, 503);
    assert.equal(response.headers.get('Cache-Control'), 'no-store');
    assert.match(await response.text(), /You’re disconnected/);
    assert.equal(fetched[0][1].cache, 'no-store');
});

test('worker cleanup preserves other applications on the origin', async () => {
    const listeners = {}, deleted = [];
    let claimed = false;
    vm.runInNewContext(source, { URL, self: { addEventListener: (type, listener) => { listeners[type] = listener; }, clients: { claim: () => { claimed = true; } } }, caches: { keys: async () => ['wt-old', 'wingthing-v1', 'other-app'], delete: async key => { deleted.push(key); } } });
    let promise; listeners.activate({ waitUntil: value => { promise = value; } }); await promise;
    assert.deepEqual(deleted, ['wt-old', 'wingthing-v1']); assert.equal(claimed, true);
});

test('install metadata and source copies include real SVG and PNG icons', async () => {
    assert.equal(await readFile(new URL('../sw.js', import.meta.url), 'utf8'), source);
    const manifestText = await readFile(new URL('../public/manifest.json', import.meta.url), 'utf8');
    assert.equal(await readFile(new URL('../manifest.json', import.meta.url), 'utf8'), manifestText);
    const manifest = JSON.parse(manifestText);
    assert.equal(manifest.display, 'standalone'); assert.equal(manifest.scope, '/app/');
    assert.ok(manifest.name && manifest.short_name && manifest.theme_color);
    for (const icon of manifest.icons.filter(icon => icon.type === 'image/png')) {
        const png = await readFile(new URL('../public/' + icon.src, import.meta.url));
        assert.equal(png.subarray(1, 4).toString(), 'PNG');
        assert.equal(png.readUInt32BE(16) + 'x' + png.readUInt32BE(20), icon.sizes);
    }
    const html = await readFile(new URL('../index.html', import.meta.url), 'utf8');
    assert.match(html, /viewport-fit=cover/); assert.match(html, /apple-mobile-web-app-capable/);
    assert.match(html, /apple-touch-icon/); assert.match(html, /rel="manifest" href="%BASE_URL%manifest.json"/);
});

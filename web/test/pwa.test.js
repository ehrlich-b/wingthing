import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const source = await readFile(new URL('../public/sw.js', import.meta.url), 'utf8');

test('the service worker stays a self-destructing cache cleaner', () => {
    assert.match(source, /unregister/);
    assert.doesNotMatch(source, /addEventListener\('fetch'/);
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

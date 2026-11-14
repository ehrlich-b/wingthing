import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

const base = readFileSync(new URL('../../internal/relay/templates/base.html', import.meta.url), 'utf8');
const partial = base.slice(base.indexOf('{{define "release-info"}}'));
const script = partial.match(/<script>([\s\S]*?)<\/script>/)[1];

async function renderRelease(metadata, ok = true) {
  const label = { textContent: 'version unavailable' };
  const requests = [];
  const pending = [];
  vm.runInNewContext(script, {
    document: { currentScript: { previousElementSibling: { querySelector: () => label } } },
    fetch: (path, options) => {
      requests.push({ path, credentials: options.credentials });
      const response = Promise.resolve({ ok, json: async () => metadata });
      pending.push(response);
      return response;
    },
  });
  await Promise.all(pending);
  await new Promise(resolve => setImmediate(resolve));
  return { text: label.textContent, requests };
}

test('public release display follows actual feed metadata, independent of source version', async () => {
  for (const tag_name of ['v0.149.5', 'v2.3.4']) {
    const result = await renderRelease({ tag_name, draft: false, prerelease: false, assets: [{ name: 'wt-linux-amd64' }] });
    assert.equal(result.text, tag_name);
    assert.deepEqual(result.requests, [{ path: '/dl/latest.json', credentials: 'omit' }]);
  }
  assert.ok(!partial.includes('v0.149.5'));
  assert.ok(partial.includes('Source documentation may describe changes beyond that release'));
});

test('missing, preview, draft or malformed metadata never becomes a release claim', async () => {
  for (const metadata of [null, {}, { tag_name: 'v1.2.3' },
    { tag_name: '<img src=x>', draft: false, prerelease: false, assets: [{}] },
    { tag_name: 'v1.2.3', draft: true, prerelease: false, assets: [{}] },
    { tag_name: 'v1.2.3', draft: false, prerelease: true, assets: [{}] },
    { tag_name: 'v1.2.3', draft: false, prerelease: false, assets: [] }]) {
    assert.equal((await renderRelease(metadata)).text, 'version unavailable');
  }
  assert.equal((await renderRelease({}, false)).text, 'version unavailable');
});

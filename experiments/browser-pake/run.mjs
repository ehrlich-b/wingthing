import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { createServer } from 'node:http';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { resolve } from 'node:path';
import { gzipSync } from 'node:zlib';
import { loadPake } from './loader.mjs';
import { runSuite } from './interop.mjs';

const out = resolve('.scratch/browser-pake');
const chromiumPath = process.env.CHROMIUM_PATH;
if (!chromiumPath) throw new Error('Set CHROMIUM_PATH to a local Chromium executable; browser checks are required');
await mkdir(`${out}/home`, {recursive: true});
// No ambient product/provider/account configuration reaches either child.
const childEnv = {PATH: process.env.PATH, HOME: `${out}/home`, TMPDIR: out,
  XDG_CONFIG_HOME: `${out}/home/config`, XDG_CACHE_HOME: `${out}/home/cache`};
const bytes = await readFile(`${out}/pake.wasm`);
const vocabularySource = await readFile('internal/phonepair/vocabulary.go', 'utf8');
const vocabulary = JSON.parse(vocabularySource.match(/var emojis = \[\]string(\{[^\n]*\})/)[1].replace(/^\{/, '[').replace(/\}$/, ']'));
if (vocabulary.length !== 256) throw new Error('Unexpected emoji vocabulary size');
const peer = spawn('nice', ['-n', '10', `${out}/go-peer`], {env: childEnv, stdio: ['pipe', 'pipe', 'pipe']});
let stderr = '';
peer.stderr.on('data', data => { stderr += data; });
let pending;
createInterface({input: peer.stdout}).on('line', line => {
  if (!pending) return;
  const current = pending; pending = undefined;
  try {
    const reply = JSON.parse(line);
    if (reply.error) current.reject(new Error(reply.error));
    else current.resolve(reply);
  } catch (error) { current.reject(error); }
});
peer.on('error', error => pending?.reject(error));
peer.on('exit', code => pending?.reject(new Error(`Go peer exited ${code}: ${stderr}`)));
function rpc(request) {
  if (pending) throw new Error('Concurrent peer exchange');
  return new Promise((resolve, reject) => {
    pending = {resolve, reject};
    peer.stdin.write(JSON.stringify(request) + '\n');
  });
}

let browser;
let server;
let timer;
let browserStderr = '';
try {
  // Abort only these disposable children on a bounded harness timeout.
  timer = setTimeout(() => {
    pending?.reject(new Error('Interop deadline exceeded'));
    peer.kill(); browser?.kill();
  }, 120000);
  const pake = await loadPake(bytes);
  const node = await runSuite(pake, rpc, vocabulary);
  let complete, fail;
  const browserResult = new Promise((resolve, reject) => { complete = resolve; fail = reject; });
  const modules = new Map([
    ['/loader.mjs', await readFile('experiments/browser-pake/loader.mjs')],
    ['/interop.mjs', await readFile('experiments/browser-pake/interop.mjs')],
  ]);
  const html = `<!doctype html><meta charset="utf-8"><title>Local PAKE interop</title><pre id="result">running</pre>
<script type="module">
import {loadPake} from '/loader.mjs';
import {runSuite} from '/interop.mjs';
try {
  if (!isSecureContext || !crypto.subtle) throw new Error('WebCrypto secure context required');
  const bytes = await (await fetch('/pake.wasm')).arrayBuffer();
  const vocabulary = await (await fetch('/vocabulary.json')).json();
  const rpc = async request => {
    const reply = await (await fetch('/rpc', {method:'POST', body:JSON.stringify(request)})).json();
    if (reply.error) throw new Error(reply.error);
    return reply;
  };
  const result = await runSuite(await loadPake(bytes), rpc, vocabulary);
  result.userAgent = navigator.userAgent; result.secureContext = isSecureContext;
  document.querySelector('#result').textContent = JSON.stringify(result);
  await fetch('/result', {method:'POST', body:JSON.stringify(result)});
} catch(error) {
  await fetch('/result', {method:'POST', body:JSON.stringify({error:String(error), stack:error.stack})});
}
</script>`;
  server = createServer(async (req, res) => {
    res.setHeader('Cache-Control', 'no-store');
    try {
      if (req.url === '/' && req.method === 'GET') {
        res.setHeader('Content-Type', 'text/html'); res.end(html);
      } else if (modules.has(req.url) && req.method === 'GET') {
        res.setHeader('Content-Type', 'text/javascript'); res.end(modules.get(req.url));
      } else if (req.url === '/pake.wasm' && req.method === 'GET') {
        res.setHeader('Content-Type', 'application/wasm'); res.end(bytes);
      } else if (req.url === '/vocabulary.json' && req.method === 'GET') {
        res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(vocabulary));
      } else if (['/rpc', '/result'].includes(req.url) && req.method === 'POST') {
        let body = '';
        for await (const chunk of req) {
          body += chunk;
          if (body.length > 16384) throw new Error('Fixture request too large');
        }
        const data = JSON.parse(body);
        res.setHeader('Content-Type', 'application/json');
        if (req.url === '/rpc') res.end(JSON.stringify(await rpc(data)));
        else {
          res.end('{}');
          if (data.error) fail(new Error(data.error + '\n' + data.stack));
          else complete(data);
        }
      } else { res.statusCode = 404; res.end(); }
    } catch (error) {
      res.statusCode = 500; res.end(JSON.stringify({error: String(error)})); fail(error);
    }
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const flags = ['--headless', '--disable-gpu', '--no-first-run', '--disable-sync',
    '--disable-background-networking', '--disable-component-update', '--disable-default-apps',
    '--disable-extensions', '--password-store=basic', '--no-proxy-server',
    '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1',
    `--user-data-dir=${out}/chromium-profile`, `http://127.0.0.1:${server.address().port}/`];
  if (process.env.CHROMIUM_NO_SANDBOX === '1') flags.unshift('--no-sandbox');
  browser = spawn('nice', ['-n', '10', chromiumPath, ...flags], {env: childEnv, stdio: ['ignore', 'ignore', 'pipe']});
  browser.stderr.on('data', data => { browserStderr += data; });
  browser.on('error', fail);
  browser.on('exit', code => fail(new Error(`Chromium exited ${code}: ${browserStderr}`)));
  const chromium = await browserResult;
  const module = await WebAssembly.compile(bytes);
  const result = {generatedAt: new Date().toISOString(), nodeVersion: process.version,
    wasm: {rawBytes: bytes.length, gzipBytes: gzipSync(bytes, {level: 9}).length,
      sha256: createHash('sha256').update(bytes).digest('hex'),
      imports: WebAssembly.Module.imports(module)},
    timingScope: 'WASM start + finish, including host RNG and C allocation/free; excludes IPC, HKDF, AES and module loading; no warmup excluded',
    chromiumSandboxDisabled: process.env.CHROMIUM_NO_SANDBOX === '1', node, chromium};
  await writeFile(`${out}/result.json`, JSON.stringify(result, null, 2) + '\n');
  console.log(JSON.stringify(result, null, 2));
} finally {
  clearTimeout(timer);
  browser?.kill();
  peer.stdin.end(); peer.kill();
  server?.close();
  await writeFile(`${out}/chromium.stderr`, browserStderr);
}

#!/usr/bin/env node
// Faithful screenshots of built-CLI PTY captures, using the shipped xterm.js.
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const crypto = require('node:crypto');
const { chromium } = require(process.env.WT_PLAYWRIGHT_MODULE || 'playwright');
const [input, output] = process.argv.slice(2);
if (!input || !output) throw new Error('usage: render-storyboard-pty.cjs ABSOLUTE_EVIDENCE_DIR ABSOLUTE_PNG_DIR');
const repo = path.resolve(__dirname, '..');
const xtermRoot = path.join(repo, 'web/node_modules/@xterm/xterm');
const page = `<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="/xterm.css"><style>html,body{margin:0;background:#111917;color:#e6efeb}body{padding:24px}#terminal{display:inline-block;padding:16px;background:#111917}.xterm{padding:0}</style><div id="terminal"></div><script src="/xterm.js"></script>`;
const server = http.createServer((req, res) => {
  const file = req.url === '/xterm.js' ? path.join(xtermRoot, 'lib/xterm.js') : req.url === '/xterm.css' ? path.join(xtermRoot, 'css/xterm.css') : null;
  res.setHeader('Content-Type', req.url === '/xterm.js' ? 'application/javascript' : req.url === '/xterm.css' ? 'text/css' : 'text/html');
  res.end(file ? fs.readFileSync(file) : page);
});
(async () => {
  fs.mkdirSync(output, { recursive: true });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless: true, executablePath: process.env.WT_CHROMIUM_EXECUTABLE || undefined, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  try {
    const tab = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
    await tab.goto(`http://127.0.0.1:${server.address().port}`);
    for (const name of fs.readdirSync(input).filter(n => n.endsWith('.ansi')).sort()) {
      const stem = name.slice(0, -5);
      const metadata = JSON.parse(fs.readFileSync(path.join(input, stem + '.json')));
      if (metadata.cols !== 120 || metadata.rows !== 36 || metadata.pty !== true) throw new Error('invalid PTY dimensions/source');
      const bytes = fs.readFileSync(path.join(input, name));
      const text = await tab.evaluate(async data => {
        if (window.ptyTerminal) window.ptyTerminal.dispose();
        document.querySelector('#terminal').replaceChildren();
        const terminal = window.ptyTerminal = new Terminal({ cols: 120, rows: 36, fontSize: 17, lineHeight: 1.2, fontFamily: 'monospace', cursorBlink: false, theme: { background: '#111917', foreground: '#e6efeb', cursor: '#8ee9c4', selectionBackground: '#3f5148' }, allowProposedApi: true });
        terminal.open(document.querySelector('#terminal'));
        await new Promise(resolve => terminal.write(new Uint8Array(data), resolve));
        await document.fonts.ready;
        return Array.from({ length: 36 }, (_, i) => terminal.buffer.active.getLine(i).translateToString(true)).join('\n');
      }, Array.from(bytes));
      const norm = s => s.replace(/\r/g, '').split('\n').map(l => l.trimEnd()).join('\n').trimEnd();
      const expected = fs.readFileSync(path.join(input, stem + '.txt'), 'utf8');
      if (norm(text) !== norm(expected)) {
        fs.writeFileSync(path.join(input, stem + '.xterm.txt'), text);
        throw new Error(`${stem}: xterm screen differs from the independent PTY emulator`);
      }
      const pngPath = path.join(output, stem + '.png');
      if (fs.existsSync(pngPath)) {
        const backup = path.join(input, 'render-backups'); fs.mkdirSync(backup, {recursive:true});
        fs.copyFileSync(pngPath, path.join(backup, stem + '.png.bak-1012-' + new Date().toISOString().slice(11,16).replace(':','')));
      }
      await tab.locator('#terminal').screenshot({ path: pngPath });
      const png = fs.readFileSync(path.join(output, stem + '.png'));
      const receipt = { ...metadata, ansi_sha256: crypto.createHash('sha256').update(bytes).digest('hex'), png_sha256: crypto.createHash('sha256').update(png).digest('hex'), renderer: 'xterm.js 6 / headless Chromium', emulator_match: true };
      fs.writeFileSync(path.join(input, stem + '.render.json'), JSON.stringify(receipt) + '\n');
      process.stdout.write(`${stem}: 120x36 emulator match\n`);
    }
  } finally { await browser.close(); server.close(); }
})().catch(err => { console.error(err); server.close(); process.exitCode = 1; });

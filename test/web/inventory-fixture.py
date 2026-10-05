#!/usr/bin/env python3
"""Generate the full real-app markup with a synthetic, network-free inventory.

Run from the repository root. Serve with the frontend's normal Vite dev command,
then inspect /app/fixtures/session-inventory.html in an owned browser tab.
This is a visual fixture, not proof of live agent or reconnect behavior.
"""
from pathlib import Path
import re

root = Path(__file__).resolve().parents[2]
source = (root / 'web/index.html').read_text()
source, count = re.subn(r'<script type="module" src="[^\"]*main\.js"></script>', '<script type="module" src="/app/fixtures/session-inventory.js"></script>', source)
if count != 1:
    raise SystemExit('Expected exactly one frontend main entrypoint')
source = source.replace('<div id="wing-status"></div>', '''<p id="fixture-notice" style="font-size:12px;color:#bdc7db;margin-bottom:10px">Synthetic inventory fixture · no live agent control</p>
<button id="fixture-disconnect" class="btn-sm" type="button" style="margin-bottom:12px">simulate WSL disconnect</button>
<button id="fixture-parent-attention" class="btn-sm" type="button" style="margin-bottom:12px">simulate parent needs input</button>
<div id="wing-status"></div>''')
source = source.replace('href="style.css"', 'href="/app/style.css"')
source = source.replace('<title>wingthing</title>', '<title>Wingthing inventory fixture</title>')
output = root / 'web/fixtures/session-inventory.html'
output.write_text(source)
print(output)

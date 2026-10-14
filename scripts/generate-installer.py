#!/usr/bin/env python3
"""Regenerate public installer copies from their shared template and base."""
from pathlib import Path
from urllib.parse import urlsplit
root = Path(__file__).resolve().parents[1]
pkg = root / "internal/downloads"
base = (pkg / "base.txt").read_text().strip()
u = urlsplit(base)
if u.scheme != "https" or not u.netloc or u.username or u.query or u.fragment or any(c in base for c in "\"'$`\\{} \t\r\n"):
    raise SystemExit("Download base must be a shell-safe absolute HTTPS URL")
text = (pkg / "installer.sh.tmpl").read_text().replace("@DOWNLOAD_BASE@", base.rstrip("/"))
for target in ['scripts/install.sh', 'internal/relay/install.sh']:
    (root / target).write_text(text)

#!/bin/sh
set -u
hostname
id
uname -sm
for executable in wt codex claude node npm go git ssh systemctl; do
  command -v "$executable" || true
done
wt --version 2>/dev/null || true
codex --version 2>/dev/null || true
claude --version 2>/dev/null || true
ps -p 1 -o comm=
if timeout 4 bash -c 'exec 3<>/dev/tcp/10.80.1.21/8006' 2>/dev/null; then
  echo lab_tcp=reachable
else
  echo lab_tcp=unreachable
fi
for directory in .codex .claude; do
  test ! -d "$HOME/$directory" || stat -c '%a %U %n' "$HOME/$directory"
done
for file in .codex/auth.json .claude/.credentials.json; do
  test ! -f "$HOME/$file" || stat -c '%a %U %n' "$HOME/$file"
done

#!/bin/sh
# Exercise the real installer against an HTTPS release mirror with no external network.
set -eu
if [ "${1:-}" != inside ]; then
  exec unshare -rn "$0" inside
fi
ip link set lo up
root=$(mktemp -d)
server_pid=
cleanup() { [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || :; rm -rf "$root"; }
trap cleanup EXIT
mkdir -p "$root/dl/v1.2.3" "$root/bin"
cat > "$root/dl/v1.2.3/wt-linux-amd64" <<'BINARY'
#!/bin/sh
echo --https
BINARY
sha256sum "$root/dl/v1.2.3/wt-linux-amd64" | awk '{print $1 "  wt-linux-amd64"}' > "$root/dl/v1.2.3/SHA256SUMS"
printf '%s\n' '{"tag_name":"v1.2.3"}' > "$root/dl/latest.json"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$root/key" -out "$root/cert" -days 1 -subj /CN=localhost -addext subjectAltName=DNS:localhost >/dev/null 2>&1
python3 - "$root" <<'PY' &
import http.server, os, ssl, sys
root = sys.argv[1]
os.chdir(root)
server = http.server.HTTPServer(('127.0.0.1', 18766), http.server.SimpleHTTPRequestHandler)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(root + '/cert', root + '/key')
server.socket = ctx.wrap_socket(server.socket, server_side=True)
server.serve_forever()
PY
server_pid=$!
for n in 1 2 3 4 5; do
  if curl -fsS --cacert "$root/cert" https://localhost:18766/dl/latest.json >/dev/null 2>&1; then break; fi
  sleep 1
done
export CURL_CA_BUNDLE="$root/cert" WT_DOWNLOAD_BASE=https://localhost:18766/dl WT_INSTALL_DIR="$root/bin" HOME="$root"
sh scripts/install.sh > "$root/out" 2> "$root/err"
test -x "$root/bin/wt"
kill "$server_pid"
wait "$server_pid" 2>/dev/null || :
server_pid=
if sh scripts/install.sh > "$root/out" 2> "$root/err"; then echo 'installer unexpectedly succeeded' >&2; exit 1; fi
test "$(cat "$root/err")" = 'Unable to download a verified wt release; please try again later.'
echo 'isolated installer and unavailable mirror: PASS'

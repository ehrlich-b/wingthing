#!/usr/bin/env bash
# Run outside the agent sandbox after make check. All mutable state is disposable.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
model=${1:-gpt-5.6-luna}
if (( $# > 1 )); then
  echo "usage: $0 [model]" >&2
  exit 2
fi
exec nice -n 15 python3 - "$repo" "$model" <<'PY'
import json, os, pathlib, select, shutil, signal, stat, subprocess, sys, tempfile, time

repo = pathlib.Path(sys.argv[1]).resolve()
model = sys.argv[2]
binary = (repo / 'wt').resolve()
if binary.parent != repo or not binary.is_file():
    raise SystemExit('Build this checkout with nice -n 15 make check first.')
scratch = repo / '.scratch'
scratch.mkdir(mode=0o700, exist_ok=True)
root = pathlib.Path(tempfile.mkdtemp(prefix='c-', dir=scratch))
root.chmod(0o700)
home, state, workspace = root / 'home', root / 's', root / 'work'
for path in (home, state, workspace):
    path.mkdir(mode=0o700)
source_home = pathlib.Path(os.environ.get('CODEX_HOME', str(pathlib.Path.home() / '.codex')))
codex_home = home / '.codex'
codex_home.mkdir(mode=0o700)
env = dict(os.environ, HOME=str(home), CODEX_HOME=str(codex_home), WINGTHING_DIR=str(state),
           WT_MCP_CLIENT='', WT_CONVERSATION_EXECUTION_ID='')
run_id = session_id = None

class MCP:
    def __init__(self):
        self.proc = subprocess.Popen([str(binary), 'mcp', 'stdio'], env=env, stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0)
        self.buffer = b''
        self.next_id = 0
        try:
            self.rpc('initialize', {'protocolVersion': '2025-03-26', 'capabilities': {},
                                   'clientInfo': {'name': 'real-codex-canary', 'version': '1'}})
            self.send({'jsonrpc': '2.0', 'method': 'notifications/initialized'})
        except BaseException:
            self.close()
            raise

    def send(self, message):
        self.proc.stdin.write(json.dumps(message).encode() + b'\n')

    def rpc(self, method, params, timeout=30):
        self.next_id += 1
        request_id = self.next_id
        self.send({'jsonrpc': '2.0', 'id': request_id, 'method': method, 'params': params})
        deadline = time.monotonic() + timeout
        while True:
            while b'\n' not in self.buffer:
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not select.select([self.proc.stdout], [], [], remaining)[0]:
                    raise RuntimeError('MCP response timed out')
                chunk = os.read(self.proc.stdout.fileno(), 65536)
                if not chunk:
                    raise RuntimeError('MCP client exited before its response')
                self.buffer += chunk
            line, self.buffer = self.buffer.split(b'\n', 1)
            response = json.loads(line)
            if response.get('id') != request_id:
                continue
            if 'error' in response:
                raise RuntimeError(json.dumps(response['error']))
            return response['result']

    def tool(self, name, arguments, timeout=30):
        result = self.rpc('tools/call', {'name': name, 'arguments': arguments}, timeout)
        if result.get('isError'):
            raise RuntimeError(json.dumps(result))
        return result['structuredContent']

    def close(self):
        self.proc.stdin.close()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()
        self.proc.stdout.close()

def interrupted(signum, frame):
    raise KeyboardInterrupt

signal.signal(signal.SIGTERM, interrupted)
try:
    auth = source_home / 'auth.json'
    if not stat.S_ISREG(auth.lstat().st_mode):
        raise RuntimeError('An existing file-backed Codex login is required; no login is performed by this canary.')
    # Read the existing login once. Codex refreshes only the private scratch copy.
    shutil.copyfile(auth, codex_home / 'auth.json')
    (codex_home / 'auth.json').chmod(0o600)
    (codex_home / 'config.toml').write_text('features.plugins = false\nweb_search = "disabled"\n')
    subprocess.run([str(binary), 'wing', 'start', '--local-only', '--paths', str(workspace)],
                   env=env, check=True, timeout=30, stdout=subprocess.DEVNULL)
    expected = 'WT_CODEX_CANARY_OK'
    prompt = 'Do not use tools, read files, browse, or run commands. Reply with exactly ' + expected + '.'
    first = MCP()
    try:
        admission = first.tool('agent_run', {'agent': 'codex', 'model': model, 'cwd': str(workspace),
                                            'prompt': prompt, 'timeout_seconds': 180})
        run_id, session_id = admission['run_id'], admission['session_id']
        print(json.dumps({'admitted': admission}), flush=True)
    finally:
        first.close()
    # A new stdio process observes the wing-owned run; EOF never owns execution.
    second = MCP()
    try:
        waited = second.tool('agent_wait', {'run_id': run_id, 'timeout_seconds': 240}, timeout=270)
        result = second.tool('agent_result', {'run_id': run_id})
        print(json.dumps({'wait': waited, 'result': result}, ensure_ascii=False), flush=True)
        if result.get('status') != 'done' or result.get('output', '').strip() != expected:
            raise RuntimeError('Real Codex canary did not produce the exact requested result')
    finally:
        second.close()
finally:
    def stop(args, marker):
        try:
            result = subprocess.run([str(binary)] + args, env=env, timeout=30,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            return result.returncode == 0 or not marker.exists()
        except (OSError, subprocess.TimeoutExpired):
            return False

    cleanup_ok = True
    if session_id is not None:
        cleanup_ok = stop(['egg', 'stop', session_id], state / 'eggs' / session_id / 'egg.pid')
    # This WINGTHING_DIR was freshly allocated by this invocation.
    wing_stopped = stop(['wing', 'stop'], state / 'wing.pid')
    cleanup_ok = cleanup_ok and wing_stopped
    if cleanup_ok:
        shutil.rmtree(root)
    else:
        print('Cleanup could not be verified; retained isolated state at ' + str(root), file=sys.stderr)
        raise SystemExit(1)
PY

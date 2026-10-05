#!/usr/bin/python3
"""Disposable native-protocol fixture. This is not an Anthropic model."""
import json
import os
import subprocess
import sys
import termios
import time
import traceback
import tty
import uuid


def fixture_exception(kind, value, trace):
    # Disposable diagnostic artifact, with a scrubbed fixture environment.
    with open(os.path.join(os.getcwd(), '.fixture-error.json'), 'w') as file:
        json.dump({'error': str(value), 'traceback': ''.join(traceback.format_exception(kind, value, trace))}, file)
    sys.__excepthook__(kind, value, trace)


sys.excepthook = fixture_exception


def option(name, argv=None):
    argv = sys.argv if argv is None else argv
    for index, arg in enumerate(argv[:-1]):
        if arg == name:
            return argv[index + 1]
    return None


if '--version' in sys.argv:
    print('Wingthing conversation fixture (not Claude)')
    sys.exit(0)

provider_id = option('--session-id') or option('--resume')
cwd = os.getcwd()
config_home = os.environ.get('CLAUDE_CONFIG_DIR') or os.path.join(os.environ['HOME'], '.claude')
project = os.path.join(config_home, 'projects', cwd.replace('/', '-'))
os.makedirs(project, mode=0o700, exist_ok=True)
transcript = os.path.join(project, provider_id + '.jsonl')
settings = {}
for index, arg in enumerate(sys.argv[:-1]):
    if arg == '--settings':
        try:
            value = json.loads(sys.argv[index + 1])
            settings.update(value.get('hooks', {}))
        except (ValueError, TypeError):
            pass


def hook(name, **fields):
    payload = {'session_id': provider_id, 'hook_event_name': name, **fields}
    for entry in settings.get(name, []):
        for action in entry.get('hooks', []):
            subprocess.run(['/bin/sh', '-c', action['command']], input=json.dumps(payload).encode(), check=True)


def record(role, content):
    with open(transcript, 'a') as file:
        file.write(json.dumps({'uuid': str(uuid.uuid4()), 'type': role, 'sessionId': provider_id, 'message': {'role': role, 'content': content}}) + '\n')
        file.flush()
        os.fsync(file.fileno())


# Like the real TUI: raw input (no line discipline, CR not translated, no
# signal keys) with bracketed paste enabled, set before readiness is announced
# and without flushing input that may already be queued.
fd = sys.stdin.fileno()
input_mode = {'tty': os.isatty(fd)}
if input_mode['tty']:
    tty.setraw(fd, termios.TCSANOW)
    lflag, iflag = termios.tcgetattr(fd)[3], termios.tcgetattr(fd)[0]
    input_mode.update({'icanon': bool(lflag & termios.ICANON), 'echo': bool(lflag & termios.ECHO), 'isig': bool(lflag & termios.ISIG), 'icrnl': bool(iflag & termios.ICRNL)})
    sys.stdout.write('\x1b[?2004h')
    input_mode['bracketed_paste_enabled'] = True
submissions = []


def input_evidence():
    # Disposable per-execution artifact in the fixture workspace.
    with open(os.path.join(cwd, '.fixture-input-' + provider_id + '.json'), 'w') as file:
        json.dump({**input_mode, 'submissions': submissions}, file)


input_evidence()
hook('SessionStart')
print('FIXTURE_NATIVE_READY', flush=True)


class MCPClient:
    """One injected stdio MCP server process, exactly as configured."""

    def __init__(self, configuration):
        env = dict(os.environ)
        env.update(configuration.get('env', {}))
        self.process = subprocess.Popen([configuration['command'], *configuration['args']], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=sys.stderr, text=True, env=env)
        self.sequence = 0

    def request(self, method, params):
        self.sequence += 1
        self.process.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': self.sequence, 'method': method, 'params': params}) + '\n')
        self.process.stdin.flush()
        while True:
            line = self.process.stdout.readline()
            if not line:
                raise RuntimeError('bound MCP exited before response')
            response = json.loads(line)
            if response.get('id') != self.sequence:
                continue
            if response.get('error'):
                raise RuntimeError(str(response['error']))
            return response['result']

    def request_error(self, method, params):
        """A call expected to fail at the protocol layer; returns the error."""
        self.sequence += 1
        self.process.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': self.sequence, 'method': method, 'params': params}) + '\n')
        self.process.stdin.flush()
        while True:
            line = self.process.stdout.readline()
            if not line:
                raise RuntimeError('bound MCP exited before response')
            response = json.loads(line)
            if response.get('id') == self.sequence:
                if not response.get('error'):
                    raise RuntimeError(method + ' unexpectedly succeeded')
                return response['error']

    def initialize(self):
        return self.request('initialize', {'protocolVersion': '2025-11-25', 'capabilities': {}, 'clientInfo': {'name': 'native-protocol-fixture', 'version': '1'}})

    def close(self):
        self.process.stdin.close()
        self.process.wait(timeout=10)


def run_parent(configuration):
    args = configuration['args']
    parent_id = option('--conversation', args)
    transport = 'host_mailbox' if '--host-mailbox' in args else 'direct_stdio'
    calls = [0]
    client = MCPClient(configuration)

    def call(name, arguments):
        calls[0] += 1
        call_id = 'fixture-tool-' + str(calls[0])
        record('assistant', [{'type': 'tool_use', 'id': call_id, 'name': 'wingthing.' + name, 'input': arguments}])
        result = client.request('tools/call', {'name': name, 'arguments': arguments})
        record('user', [{'type': 'tool_result', 'tool_use_id': call_id, 'content': json.dumps(result), 'is_error': bool(result.get('isError'))}])
        return result

    def tool(name, arguments):
        result = call(name, arguments)
        if result.get('isError'):
            raise RuntimeError(name + ': ' + str(result.get('structuredContent')))
        return result['structuredContent']

    def refused(name, arguments, fragment):
        result = call(name, arguments)
        message = str((result.get('structuredContent') or {}).get('error', ''))
        if not result.get('isError') or fragment not in message:
            raise RuntimeError(name + ' was not refused: ' + json.dumps(result))
        return message

    client.initialize()
    record('assistant', [{'type': 'text', 'text': 'FIXTURE_BOUND_MCP_INITIALIZED'}])
    listed = sorted(item['name'] for item in client.request('tools/list', {})['tools'])
    evidence = {'transport': transport, 'parent_conversation_id': parent_id, 'parent_provider_session_id': provider_id, 'tools': listed, 'children': []}
    if transport == 'host_mailbox':
        if 'terminal_send' in listed or 'prompt_run' in listed or 'terminal_start' in listed:
            raise RuntimeError('host mailbox exposed raw input or command tools: ' + str(listed))
        refused('terminal_send', {'session': provider_id, 'input': 'raw'}, 'not available on this connection')
        refused('session_status', {'session': 'execution-outside-tree'}, 'bound task tree')
        # A method outside the bridge is refused before dispatch, and the caller
        # learns that from structured error data rather than prose.
        error = client.request_error('resources/list', {})
        data = error.get('data') or {}
        if data.get('dispatched') != 'no' or data.get('outcome') != 'not_dispatched' or data.get('retry_safe') is not True:
            raise RuntimeError('undispatched refusal lacks structured data: ' + json.dumps(error))
        evidence['structured_refusal'] = data

    prompts = {}
    for suffix in ('a', 'b'):
        launch_args = {'agent': 'claude', 'cwd': cwd, 'label': 'fixture-child-' + suffix, 'request_id': provider_id + '-' + suffix}
        child = tool('agent_start', launch_args)
        replay = tool('agent_start', launch_args)
        if child['session'] != replay['session'] or replay['reused'] is not True:
            raise RuntimeError('launch retry created another execution')
        ready = tool('session_wait', {'session': child['session'], 'state': 'ready', 'timeout_seconds': 30})
        if not ready['matched']:
            raise RuntimeError('child native ready state unavailable')
        before = ready['lifecycle']['head_cursor']
        # Multiline: the child must receive one bracketed paste, not two turns.
        prompt = {'session': child['session'], 'request_id': provider_id + '-prompt-' + suffix, 'input': 'fixture-work-' + suffix + '\nfixture second line ' + suffix, 'timeout_seconds': 30}
        sent = tool('session_prompt', prompt)['receipt']
        if not sent['native_receipt_observed'] or sent.get('retried'):
            raise RuntimeError('child prompt receipt not observed: ' + json.dumps(sent))
        again = tool('session_prompt', prompt)['receipt']
        if not again['native_receipt_observed'] or not again.get('retried') or again['receipt_cursor'] != sent['receipt_cursor']:
            raise RuntimeError('prompt replay did not reconcile without resending: ' + json.dumps(again))
        done = tool('session_wait', {'session': child['session'], 'state': 'completed', 'after_cursor': before, 'timeout_seconds': 30})
        if not done['matched']:
            raise RuntimeError('child native Stop not observed')
        read = tool('session_read', {'session': child['session'], 'after_cursor': 0, 'limit': 200})
        events = read['lifecycle']['events']
        delivered = [event for event in events if event.get('type') == 'message' and event.get('role') == 'user' and event.get('text') == prompt['input']]
        if len(delivered) != 1:
            raise RuntimeError('expected exactly one native delivery, saw %d' % len(delivered))
        if not any(event.get('text') == 'fixture-result:' + prompt['input'] for event in events):
            raise RuntimeError('child result missing from native transcript')
        status = tool('session_status', {'session': child['session']})
        prompts[suffix] = prompt
        evidence['children'].append({'conversation_id': child.get('conversation_id'), 'session': child['session'], 'provider_session_id': read['lifecycle'].get('provider_session_id'), 'prompt_request_id': prompt['request_id'], 'receipt_cursor': sent['receipt_cursor'], 'status_state': (status.get('lifecycle') or {}).get('state')})

    # Reconnect: a second injected client process replays durable tool-level IDs.
    # Neither the launch nor the prompt may run again.
    client.close()
    client = MCPClient(configuration)
    client.initialize()
    first = evidence['children'][0]
    relaunch = tool('agent_start', {'agent': 'claude', 'cwd': cwd, 'label': 'fixture-child-a', 'request_id': provider_id + '-a'})
    if relaunch['session'] != first['session'] or relaunch['reused'] is not True:
        raise RuntimeError('reconnected launch replay created another execution')
    replayed = tool('session_prompt', prompts['a'])['receipt']
    if not replayed.get('retried') or replayed['receipt_cursor'] != first['receipt_cursor']:
        raise RuntimeError('reconnected prompt replay was not reconciled: ' + json.dumps(replayed))
    evidence['reconnect'] = {'launch_reused': True, 'prompt_retried_without_resend': True}

    if transport == 'host_mailbox':
        # Broker restart: the fixture host stops this execution's broker, leaves
        # a stale-epoch request behind and starts a new broker process. The same
        # client then replays durable IDs; neither may run a second time.
        open(os.path.join(cwd, '.fixture-await-restart-' + provider_id), 'w').close()
        deadline = time.time() + 120
        while not os.path.exists(os.path.join(cwd, '.fixture-restarted-' + provider_id)):
            if time.time() > deadline:
                raise RuntimeError('fixture host did not restart the broker')
            time.sleep(0.2)
        second = evidence['children'][1]
        relaunch = tool('agent_start', {'agent': 'claude', 'cwd': cwd, 'label': 'fixture-child-b', 'request_id': provider_id + '-b'})
        if relaunch['session'] != second['session'] or relaunch['reused'] is not True:
            raise RuntimeError('launch replay after broker restart created another execution')
        replayed = tool('session_prompt', prompts['b'])['receipt']
        if not replayed.get('retried') or replayed['receipt_cursor'] != second['receipt_cursor']:
            raise RuntimeError('prompt replay after broker restart was not reconciled: ' + json.dumps(replayed))
        evidence['restart'] = {'launch_reused': True, 'prompt_retried_without_resend': True}

    tree = tool('conversation_read', {'conversation_id': parent_id})
    if len(tree['tasks']) != 3:
        raise RuntimeError('parent task tree missing children')
    tool('conversation_checkpoint', {'conversation_id': parent_id, 'expected_revision': tree['conversation']['revision'], 'after_cursor': tree['next_cursor'], 'checkpoint': 'Fixture received both linked native child results.'})
    record('assistant', [{'type': 'text', 'text': 'FIXTURE_PARENT_ORCHESTRATED_TWO_CHILDREN'}])
    record('assistant', [{'type': 'text', 'text': 'FIXTURE_EVIDENCE:' + json.dumps(evidence, sort_keys=True)}])
    hook('Stop', background_tasks=[])
    client.close()


config_path = option('--mcp-config')
if config_path and '-p' not in sys.argv:
    run_parent(json.load(open(config_path))['mcpServers']['wingthing'])

PASTE_START, PASTE_END = b'\x1b[200~', b'\x1b[201~'


def submit(prompt):
    if not prompt.strip():
        return
    submissions.append(prompt)
    input_evidence()
    hook('UserPromptSubmit', prompt=prompt)
    record('user', [{'type': 'text', 'text': prompt}])
    record('assistant', [{'type': 'tool_use', 'name': 'Read', 'id': 'fixture-read', 'input': {'path': 'disposable-fixture.txt'}}])
    record('user', [{'type': 'tool_result', 'tool_use_id': 'fixture-read', 'content': 'fixture tool evidence'}])
    record('assistant', [{'type': 'text', 'text': 'fixture-result:' + prompt}])
    hook('Stop', background_tasks=[])
    print('FIXTURE_NATIVE_TURN_RECORDED', flush=True)


if '-p' in sys.argv:
    submit(option('-p'))
    sys.exit(0)


# Like an editor composer: a bracketed paste is literal text (newlines included)
# and only a later Enter (raw CR) outside the paste submits it.
buffer, composed, pasting = b'', b'', False
while True:
    try:
        chunk = os.read(fd, 4096)
    except OSError:
        break  # the PTY closed
    if not chunk:
        break
    buffer += chunk
    while buffer:
        if pasting:
            end = buffer.find(PASTE_END)
            if end < 0:
                safe = len(buffer) - (len(PASTE_END) - 1)
                if safe > 0:
                    composed, buffer = composed + buffer[:safe], buffer[safe:]
                break
            composed, buffer, pasting = composed + buffer[:end], buffer[end + len(PASTE_END):], False
        elif buffer.startswith(PASTE_START):
            buffer, pasting = buffer[len(PASTE_START):], True
        elif PASTE_START.startswith(buffer):
            break  # partial paste marker; wait for the rest
        else:
            byte, buffer = buffer[:1], buffer[1:]
            if byte == b'\r':  # LF outside a paste stays composer text
                submit(composed.decode('utf-8', 'replace'))
                composed = b''
            else:
                composed += byte

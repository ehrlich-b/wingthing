# Deterministic OpenSSH fixture: forwards the canonical socket and reports each
# connection to a supervising test. No network, accounts, installed wt or sleeps.
import json, os, shlex, signal, socket, subprocess, sys, threading
args = sys.argv[1:]
root = os.environ['WT_FAKE_SSH_ROOT']
i = args.index('--')
host = args[i+1]
with open(os.path.join(root, host+'.json')) as f: metadata = json.load(f)
if os.path.exists(os.path.join(root, host+'.down')):
    print('fixture host is offline', file=sys.stderr); sys.exit(255)
if os.path.exists(os.path.join(root, host+'.changed-key')):
    print('REMOTE HOST IDENTIFICATION HAS CHANGED!', file=sys.stderr); sys.exit(255)
if '-N' not in args:
    command = shlex.split(args[i+2])
    assert command[1:3] == ['mcp','inspect'], command
    with open(os.path.join(root, host+'.inspect'), 'w') as f: json.dump(args,f)
    if os.environ.get('WT_FAKE_SSH_EXECUTE'):
        env = os.environ.copy()
        env['WINGTHING_DIR'] = metadata['wingthing_dir']
        # Resolve the selected executable as the remote shell would: command
        # names use this fixture's PATH, absolute paths stay absolute.
        sys.exit(subprocess.run(command,env=env).returncode)
    print(json.dumps(metadata)); sys.exit(0)
local, remote = args[args.index('-L')+1].split(':',1)
assert remote == metadata['control_socket']
listener = socket.socket(socket.AF_UNIX)
listener.bind(local); listener.listen()
admin = socket.socket(socket.AF_UNIX)
admin.connect(os.path.join(root,'supervisor.sock'))
admin.sendall((json.dumps({'host':host,'pid':os.getpid(),'args':args,'socket':local})+'\n').encode())
def pump(source, destination):
    try:
        while True:
            data = source.recv(65536)
            if not data: break
            destination.sendall(data)
    except OSError: pass
    finally:
        try: destination.shutdown(socket.SHUT_WR)
        except OSError: pass

def accept():
    while True:
        incoming,_=listener.accept()
        outgoing=socket.socket(socket.AF_UNIX)
        try: outgoing.connect(remote)
        except OSError: incoming.close(); continue
        threading.Thread(target=pump,args=(incoming,outgoing),daemon=True).start()
        threading.Thread(target=pump,args=(outgoing,incoming),daemon=True).start()
threading.Thread(target=accept,daemon=True).start()
# Test commands are explicit barriers. Losing the supervisor also ends the fake.
admin.recv(1)
os._exit(0)

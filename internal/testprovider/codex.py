import json, os, re, subprocess, sys, tty
args=sys.argv[1:]
if args==['--version']:
 print('codex-cli 0.159.3'); sys.exit(0)
hooks={}; notify=None
for i,a in enumerate(args):
 if a=='-c' and i+1<len(args):
  value=args[i+1]
  if value.startswith('hooks.') and 'command=' in value:
   event=value.split('=',1)[0].split('.',1)[1]
   command=re.search(r'command=("(?:\\.|[^"\\])*")',value).group(1)
   hooks[event]=json.loads(command)
  elif value.startswith('notify='): notify=json.loads(value.split('=',1)[1])
assert '--no-daemon' in args and notify and 'SessionStart' in hooks
thread='fake-thread'
def hook(event, **extra):
 data={'hook_event_name':event,'session_id':thread};data.update(extra)
 subprocess.run(['/bin/sh','-c',hooks[event]],input=json.dumps(data).encode(),check=True)
tty.setraw(0)
model=args[args.index('-m')+1]
if model=='fixture-modal':
 os.write(1,b'Update available: private-startup-canary-token\r\n1. Update now 2. Skip\r\n')
 if os.read(0,1): raise RuntimeError('Wingthing typed into a startup modal')
 sys.exit(0)
initial=args[args.index('--')+1] if '--' in args else None
os.write(1,b'OpenAI Codex\r\nAsk Codex to do anything\r\n')
buffer=b''
while True:
 if initial is not None:
  prompt=initial; initial=None
 else:
  chunk=os.read(0,1)
  if not chunk: break
  if chunk!=b'\r': buffer+=chunk;continue
  prompt=buffer.decode().removeprefix('\x1b[200~').removesuffix('\x1b[201~');buffer=b''
 hook('SessionStart',source='startup')
 turn='fake-turn'
 hook('UserPromptSubmit',turn_id=turn,prompt=prompt)
 # Multi-provider fixtures use one gate in each child's granted workspace.
 # The legacy shared gate remains for single-provider fixtures.
 gate_path=os.path.join(os.getcwd(),'.fixture-completion-gate')
 scoped_gate=os.path.exists(gate_path)
 if not scoped_gate: gate_path=os.path.join(os.environ['HOME'],'..','completion-gate')
 with open(gate_path,'rb', buffering=0) as gate:
  if scoped_gate:
   ready=gate_path+'.ready'
   # Readiness is the marker's existence, after the hook and FIFO open above.
   # Publish one persistent entry: kqueue can race with a temporary-file rename.
   os.close(os.open(ready, os.O_CREAT|os.O_EXCL|os.O_WRONLY, 0o600))
  assert gate.read(1)==b'\x01', 'completion gate closed without explicit release'
 payload={'type':'agent-turn-complete','thread-id':thread,'turn-id':turn,'input-messages':[prompt],'last-assistant-message':'Fake Codex '+model+': Ω🙂 '+prompt}
 subprocess.run(notify+[json.dumps(payload)],check=True)
 hook('Stop',turn_id=turn)

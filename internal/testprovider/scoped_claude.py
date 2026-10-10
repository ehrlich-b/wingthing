import json, os, socket, subprocess, sys, tty
args=sys.argv[1:]
if args==['--version']:
 print('fake-claude 1.0'); sys.exit(0)
assert args.count('--mcp-config')==1 and args.count('--strict-mcp-config')==1
assert args[args.index('--model')+1]=='fake-parent'
with open(args[args.index('--mcp-config')+1]) as f: servers=json.load(f)['mcpServers']
assert list(servers)==['wingthing']
server=servers['wingthing']; assert '--host-mailbox' in server['args']
mailbox=server['args'][server['args'].index('--host-mailbox')+1]
control=args[args.index('--fixture-control')+1]
# File access and AF_UNIX connections must both be denied by the final sandbox.
if "--fixture-unconfined" not in args:
 for option in ('--fixture-state-file','--fixture-ssh-key'):
  protected=args[args.index(option)+1]
  try:
   with open(protected,'rb'): pass
  except PermissionError: pass
  else: raise RuntimeError('parent could read protected host file: '+protected)
 probe=socket.socket(socket.AF_UNIX)
 try: probe.connect(control)
 except (PermissionError,OSError): pass
 else: raise RuntimeError('parent received unrestricted control socket')
 finally: probe.close()
tty.setraw(0)
client=None; sequence=0
def rpc(method,params):
 global sequence
 sequence+=1
 client.stdin.write(json.dumps({'jsonrpc':'2.0','id':sequence,'method':method,'params':params})+'\n');client.stdin.flush()
 line=client.stdout.readline()
 if not line: raise RuntimeError('mailbox client ended')
 response=json.loads(line)
 if 'error' in response: raise RuntimeError(str(response))
 return response['result']
def connect():
 global client
 client=subprocess.Popen([server['command']]+server['args'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
 rpc('initialize',{'protocolVersion':'2025-06-18','capabilities':{},'clientInfo':{'name':'fake-parent','version':'1'}})
 catalog=rpc('tools/list',{})['tools']
 names={tool['name'] for tool in catalog}
 assert {'wing_list','agent_run','agent_status','agent_wait','agent_wait_any','agent_result','agent_stop','agent_steer'}<=names
def call(name,args,denied=False):
 result=rpc('tools/call',{'name':name,'arguments':args})
 assert bool(result.get('isError',False))==denied, result
 return result['structuredContent']
def publish(name,data):
 temporary=name+'.tmp'
 with open(temporary,'w') as f: json.dump(data,f);f.flush();os.fsync(f.fileno())
 os.rename(temporary,name)
connect()
wings=call('wing_list',{})['wings'];assert len(wings)==2
receipts=[]
for wing in wings:
 call('wingthing_capabilities',{'wing_id':wing['wing_id']})
 receipt=call('agent_run',{'wing_id':wing['wing_id'],'agent':'codex','model':'fixture-model','cwd':wing['paths'][0],'prompt':wing['name']+' request','timeout_seconds':180,'idempotency_key':wing['name']+'-request'})
 assert receipt['wing_id']==wing['wing_id'] and receipt['run_id'] and receipt['session_id']
 receipts.append(receipt)
call('agent_run',{'wing_id':'ungranted','agent':'codex','cwd':os.getcwd(),'prompt':'denied','idempotency_key':'denied'},True)
call('agent_result',{'wing_id':wings[0]['wing_id'],'run_id':'foreign'},True)
publish('receipts.json',{'receipts':receipts,'mailbox_pid':client.pid})
os.write(1,b'PARENT_READY\r\n')
buffer=b''
while True:
 chunk=os.read(0,1)
 if not chunk: break
 if chunk not in (b'\r',b'\n'): buffer+=chunk;continue
 command=buffer.decode();buffer=b''
 if command=='reconnect':
  client.kill();client.wait();connect();publish('mailbox-reconnected.json',{'pid':client.pid});os.write(1,b'MAILBOX_RECONNECTED\r\n')
 elif command=='recover':
  results=[]
  for receipt in receipts:
   call('agent_wait',{'wing_id':receipt['wing_id'],'run_id':receipt['run_id'],'timeout_seconds':60})
   results.append(call('agent_result',{'wing_id':receipt['wing_id'],'run_id':receipt['run_id']}))
  publish('results.json',results);os.write(1,b'RECOVERED\r\n')
 elif command=='exit':
  client.kill();client.wait();sys.exit(0)

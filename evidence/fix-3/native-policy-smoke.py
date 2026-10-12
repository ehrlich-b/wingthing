#!/usr/bin/env python3
import json, os, subprocess, tempfile, time
from pathlib import Path
root=Path(__file__).resolve().parents[2]
binary=root/'.scratch/storyboard-wt'
with tempfile.TemporaryDirectory(prefix='wt-auth-') as temporary:
 base=Path(temporary)
 for d in ['home','work','state','tmp']: (base/d).mkdir()
 env=dict(os.environ, HOME=str(base/'home'), WINGTHING_DIR=str(base/'state'), TMPDIR=str(base/'tmp'), GOFLAGS='-p=4')
 policy=base/'policy.yaml'
 policy.write_text('base: none\nfs: ["ro:/", "rw:'+str(base/'work')+'", "deny:'+str(base/'home/.ssh')+'"]\nnetwork: none\nenv: [HOME, PATH]\n')
 launch=subprocess.run(['nice','-n','15',str(binary),'terminal','--detach','--json','--cwd',str(base/'work'),'--config',str(policy),'--','/bin/sh','-c','printf NATIVE_POLICY_READY; exec sleep 6'],env=env,capture_output=True,text=True,timeout=20)
 print('launch exit:',launch.returncode,flush=True)
 print(launch.stdout,flush=True)
 if launch.returncode: print(launch.stderr); raise SystemExit(launch.returncode)
 receipt=json.loads(launch.stdout)
 sid=receipt['session']; directory=base/'state/eggs'/sid
 summary=json.loads((directory/'session.authority.json').read_text())
 assert summary['sandboxed'] and summary['workspace']==str(base/'work') and summary['provider_home']==str(base/'home')
 assert summary['network']=='none' and str(base/'home/.ssh') in summary['denied']
 inventory=subprocess.run(['nice','-n','15',str(binary),'session','ps','--json'],env=env,capture_output=True,text=True,timeout=10)
 print('inventory exit:',inventory.returncode,flush=True)
 assert inventory.returncode==0,inventory.stderr
 rows=json.loads(inventory.stdout)
 selected=next(row for row in rows if row['id']==sid)
 assert selected['authority']==summary
 print(json.dumps({'receipt':receipt,'authority':summary,'inventory_authority_matches':True},indent=2),flush=True)
 # The only command is an owned finite sleep; let its native runtime exit.
 time.sleep(7)
 print('native policy persistence/inventory smoke: PASS',flush=True)

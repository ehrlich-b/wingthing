// Branch-built, loopback-only Pair acceptance; no provider or account services.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { deflateRawSync, inflateRawSync } from 'node:zlib';
import { fileURLToPath } from 'node:url';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const revision=()=>({head:spawnSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).stdout.trim(),dirty:!!spawnSync('git',['status','--porcelain'],{cwd:root,encoding:'utf8'}).stdout});
const source=revision(), screenshots=[];
const out = process.env.WT_JOIN_OUT || path.join(root, '.scratch/browser-join');
const shots = process.env.WT_JOIN_SHOTS || path.join(out, 'shots');
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
fs.mkdirSync(out, {recursive:true}); fs.mkdirSync(shots, {recursive:true});
fs.mkdirSync(path.join(root,'.scratch'), {recursive:true});
const state = fs.mkdtempSync('/tmp/wt-join-');
const home = fs.mkdtempSync(path.join(root,'.scratch/join-home-'));
const port = Number(process.env.WT_JOIN_PORT || 18241), base = 'http://127.0.0.1:' + port;
const env = { PATH:process.env.PATH,HOME:home,WINGTHING_DIR:state,WT_BASE_URL:base };
fs.writeFileSync(path.join(state,'wing.yaml'),'label: Studio\n',{mode:0o600});
const log = fs.openSync(path.join(out,'roost.log'),'w');
const startRoost = () => spawn('nice',['-n','10',path.join(root,'wt'),'roost','start','--foreground','--addr','127.0.0.1:'+port],{cwd:root,env,stdio:['ignore',log,log]});
let roost = startRoost(), invitationCount = 0;
const sleep = ms => new Promise(r=>setTimeout(r,ms));
const proof=[]; const pairs=[], fixtureSockets=[];let browser, sessionStarted=false;
const record = (name, fact) => { proof.push({name,pass:true,fact});console.log('PASS '+name+': '+fact); };
const cli = (...args) => {
    const r=spawnSync('nice',['-n','10',path.join(root,'wt'),...args],{cwd:root,env,encoding:'utf8'});
    assert.equal(r.status,0,r.stderr);return r.stdout;
};
const devices = () => cli('devices');
const decode = link => JSON.parse(inflateRawSync(Buffer.from(link.split('#v1z.')[1],'base64url')));
const encode = a => 'wingthing://pair#v1z.'+deflateRawSync(Buffer.from(JSON.stringify(a))).toString('base64url');
async function ready() {
    for(let i=0;i<150;i++){try{if((await fetch(base+'/health')).ok)return;}catch{}if(roost.exitCode!==null)throw Error('roost exited');await sleep(100);}
    throw Error('roost not ready');
}
async function resetRoost() {
    roost.kill('SIGINT'); await once(roost,'exit');
    roost = startRoost(); await ready(); invitationCount = 0;
}
async function pair() {
    // Each process has a bounded eight-invitation window. Restart only our
    // own disposable roost between closed cases rather than filling it.
    if (invitationCount >= 6) await resetRoost();
    invitationCount++;
    const child=spawn('nice',['-n','10',path.join(root,'wt'),'pair','--show-link'],{cwd:root,env,stdio:['pipe','pipe','pipe']});
    pairs.push(child);let output='';child.stdout.on('data',b=>output+=b); // Invitation remains only in memory.
    child.stderr.on('data',()=>{});
    let link;
    for(let i=0;i<100;i++){link=output.match(/wingthing:\/\/pair#v1z\.[A-Za-z0-9_-]+/)?.[0];if(link)break;await sleep(100);}
    assert.ok(link,'pair command did not issue a link');
    return {child,link,output:()=>output};
}
async function oldWing(id, publicKey) {
    const token = 'local-old-wing-' + id;
    const seed = spawnSync('python3', ['-c', 'import sqlite3,sys\nc=sqlite3.connect(sys.argv[1]);c.execute("INSERT INTO device_tokens(token,user_id,device_id,expires_at) VALUES (?, ?, ?, datetime(\'now\',\'+1 hour\'))",(sys.argv[2],"local",sys.argv[3]));c.commit()', path.join(state,'roost.db'), token, id], {encoding:'utf8'});
    assert.equal(seed.status,0,seed.stderr);
    const socket = new WebSocket(base.replace('http:','ws:')+'/ws/wing?token='+encodeURIComponent(token));
    fixtureSockets.push(socket);
    let forwarded = 0;
    await new Promise((resolve,reject)=>{
        const timer=setTimeout(()=>reject(Error('old wing registration timed out')),10000);
        socket.onopen=()=>socket.send(JSON.stringify({type:'wing.register',wing_id:id,public_key:publicKey,hosted_relay:'allow'}));
        socket.onerror=()=>{clearTimeout(timer);reject(Error('old wing registration failed'));};
        socket.onmessage=event=>{const message=JSON.parse(event.data);if(message.type==='registered'){clearTimeout(timer);resolve();}else if(message.type==='tunnel.req')forwarded++;};
    });
    return {socket,forwarded:()=>forwarded};
}
async function newPage(viewport, init, initArg) {
    const context=await browser.newContext({viewport});
    if(init)await context.addInitScript(init, initArg);
    const page=await context.newPage();
    let me=0;page.on('request',r=>{if(r.url().includes('/api/app/me'))me++;});
    await page.goto(base+'/app/#pair');
    await page.getByRole('heading',{name:'Join from this browser'}).waitFor();
    assert.equal(me,0,'Pair fetched account authority');
    return {page,context};
}
async function shot(page,size,name){
    // Label the captured scenario outside the product UI, so identical error
    // screens still carry distinct, honest evidence of the exercised failure.
    await page.evaluate(({size,name})=>{
        document.getElementById('proof-case')?.remove();
        const note=document.createElement('p');note.id='proof-case';
        note.textContent=`Local verification: ${name} · ${size}px`;
        note.style.cssText='margin:24px 32px;color:#626262;font:13px system-ui';
        document.body.append(note);
    },{size,name});
    const file=`${size}-${name}.png`;
    await page.screenshot({path:path.join(shots,file),fullPage:true});
    screenshots.push({file,sha256:createHash('sha256').update(fs.readFileSync(path.join(shots,file))).digest('hex')});
}
async function paste(page,link,name) {
    await page.locator('#bp-link').fill(link);await page.locator('#bp-name').fill(name);
    await page.getByRole('button',{name:'Check invitation'}).click();
}
// Probe wing authorization with the actual persisted, non-exportable browser
// key. No credential, private key, invitation or ciphertext is returned to logs.
async function probe(page,wing) {
    return page.evaluate(async ({base,wing})=>{
        const utf8=new TextEncoder(), text=new TextDecoder();
        const b64=b=>btoa(Array.from(new Uint8Array(b),x=>String.fromCharCode(x)).join(''));
        const unb64=s=>Uint8Array.from(atob(s),c=>c.charCodeAt(0));
        const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('wt-paired-wings-v1');r.onsuccess=()=>resolve(r.result);r.onerror=reject;});
        const saved=await new Promise((resolve,reject)=>{const r=db.transaction('devices').objectStore('devices').get(wing);r.onsuccess=()=>resolve(r.result);r.onerror=reject;});db.close();
        if(saved.signingKey.extractable)throw Error('exportable key');
        let exported=false;try{await crypto.subtle.exportKey('jwk',saved.signingKey);exported=true;}catch{}
        if(exported)throw Error('exportable signing key');
        const keys=await crypto.subtle.generateKey('X25519',false,['deriveBits']);
        const sender=b64(await crypto.subtle.exportKey('raw',keys.publicKey));
        const wingKey=await crypto.subtle.importKey('raw',unb64(saved.key),'X25519',false,[]);
        const shared=await crypto.subtle.deriveBits({name:'X25519',public:wingKey},keys.privateKey,256);
        const ikm=await crypto.subtle.importKey('raw',shared,'HKDF',false,['deriveKey']);
        const derive=info=>crypto.subtle.deriveKey({name:'HKDF',hash:'SHA-256',salt:new Uint8Array(32),info:utf8.encode(info)},ikm,{name:'AES-GCM',length:256},false,['encrypt','decrypt']);
        const carrierKey=await derive('wt-phone-carrier-v1'),deviceKey=await derive('wt-phone-v1');
        const seal=async(key,obj)=>{const iv=crypto.getRandomValues(new Uint8Array(12)),encrypted=new Uint8Array(await crypto.subtle.encrypt({name:'AES-GCM',iv},key,utf8.encode(JSON.stringify(obj))));return b64(Uint8Array.of(...iv,...encrypted));};
        const open=async(key,wire)=>{const data=unb64(wire);return JSON.parse(text.decode(await crypto.subtle.decrypt({name:'AES-GCM',iv:data.slice(0,12)},key,data.slice(12))));};
        const socket=new WebSocket(base.replace('http:','ws:')+'/ws/browser-phone?wing_id='+encodeURIComponent(wing));
        await new Promise((resolve,reject)=>{socket.onopen=resolve;socket.onerror=reject;});
        const callBox=payload=>new Promise((resolve,reject)=>{
            const request_id=crypto.randomUUID(),timer=setTimeout(()=>reject(Error('timeout')),10000);
            socket.onmessage=async e=>{try{const outer=JSON.parse(e.data);if(outer.request_id!==request_id)return;clearTimeout(timer);const box=JSON.parse(outer.payload);if(box.error)throw Error(box.error);resolve(await open(carrierKey,box.payload));}catch(error){reject(error);}};
            socket.send(JSON.stringify({type:'tunnel.req',purpose:'phone',wing_id:wing,request_id,payload}));
        });
        const boxed=async inner=>JSON.stringify({op:'phone.box',sender_pub:sender,payload:await seal(carrierKey,inner)});
        const call=async inner=>callBox(await boxed(inner));
        const helloReply=await call({op:'device.hello',sender_pub:sender});
        const hello=await open(deviceKey,helloReply.payload);
        const message=`wt-device-v1\n${wing}\n${hello.connection_id}\n${hello.challenge}\n${sender}`;
        const raw=new Uint8Array(await crypto.subtle.sign({name:'ECDSA',hash:'SHA-256'},saved.signingKey,utf8.encode(message)));
        const integer=p=>{while(p.length>1&&!p[0])p=p.slice(1);if(p[0]&128)p=Uint8Array.of(0,...p);return Uint8Array.of(2,p.length,...p);};
        const r=integer(raw.slice(0,32)),s=integer(raw.slice(32)),signature=b64(Uint8Array.of(48,r.length+s.length,...r,...s));
        const auth=await call({op:'device.auth',connection_id:hello.connection_id,payload:await seal(deviceKey,{connection_id:hello.connection_id,device_id:saved.device_id,credential:saved.credential,signature})});
        if(auth.error)throw Error(auth.error);
        const errors=[];let seq=0;
        const request=async op=>({op:'device.command',connection_id:hello.connection_id,payload:await seal(deviceKey,{op,input:'{"browser":false,"scope":"control","include_remotes":true}',connection_id:hello.connection_id,sequence:++seq})});
        for(const op of ['launch','input','action','stop','attach','fleet_input']){const reply=await call(await request(op));errors.push([op,reply.error]);}
        const replay=await boxed(await request('sessions'));
        const first=await callBox(replay);if(first.error)throw Error(first.error);
        const repeated=await callBox(replay);socket.close();
        return {errors,replay:repeated.error,keyNonExportable:true,scope:saved.scope};
    },{base,wing});
}
try {
    assert.ok(Buffer.byteLength(state) < 40);
    assert.ok(home.startsWith(root + path.sep));
    record('short-state','socket state uses a short temporary root; fixture HOME stays inside the checkout');
    await ready();
    cli('terminal','--unsandboxed','--detach','--name','join-proof-session','--cwd',home,'--','/bin/sleep','1800');
    sessionStarted=true;
    record('real-session','branch-built CLI started a real local terminal egg running sleep in lane HOME');
    browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||chromium.executablePath(),headless:true,args:['--disable-background-networking','--disable-component-update','--disable-default-apps','--disable-sync','--metrics-recording-only','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1',...(process.env.CHROMIUM_NO_SANDBOX==='1'?['--no-sandbox']:[])]});
    for(const viewport of [{width:390,height:844},{width:1440,height:900}]) {
        const size=viewport.width;
        const {page,context}=await newPage(viewport);
        await shot(page,size,'paste');
        const invitation=await pair(), a=decode(invitation.link), name=process.env.WT_JOIN_CASE==='fragment'?'Browser':'Browser '+size;
        a[2]='Invitation alias';
        const displayLink=encode(a);
        if(process.env.WT_JOIN_CASE==='fragment'){await page.goto('about:blank');await page.goto(base+'/app/'+new URL(displayLink).hash);}else await paste(page,displayLink,name);
        await page.getByRole('button',{name:'Match · Join'}).waitFor();
        await shot(page,size,'compare');
        const fp=await page.locator('.bp-fingerprint').innerText();assert.equal(fp.replaceAll(/\s/g,'').length,64);
        assert.equal((await page.locator('.bp-emoji').innerText()).split(' ').length,4);
        assert.equal((await page.locator('.bp-emoji-labels').innerText()).split(' · ').length,4);
        record('emoji-labels-'+size,'four confirmation glyphs and four readable labels rendered together');
        await page.getByRole('button',{name:'Match · Join'}).click();
        await page.getByText(/Waiting for Enter/).waitFor();await shot(page,size,'waiting');
        assert.ok(!devices().includes(name),'single-sided approval issued device');
        for(let i=0;i<50&&!invitation.output().includes('Enter to allow');i++)await sleep(100);
        assert.ok(invitation.output().includes('Inventory only:'));
        assert.ok(invitation.output().includes(await page.locator('.bp-emoji-labels').innerText()), 'terminal emoji labels differ from browser');
        record('terminal-labels-'+size,'terminal prints the same four emoji names as the browser beside its glyphs');
        assert.ok(invitation.output().includes('Fingerprint: '+fp.replaceAll(/\s+/g,' ')));
        invitation.child.stdin.write('\n');
        await page.getByText('✓ Connected · Inventory only').waitFor({timeout:15000});
        await page.locator('#bp-machines').getByText('Studio',{exact:true}).waitFor();
        await page.locator('#bp-sessions').getByText('join-proof-session',{exact:true}).waitFor();
        assert.equal(await page.locator('#bp-machine-name').innerText(),'Studio');
        assert.ok(!(await page.locator('#bp-machines').innerText()).includes('Invitation alias'));
        record('inventory-'+size,'wing inventory supplies Studio and the real join-proof-session despite invitation name Invitation alias');
        await shot(page,size,'connected');
        assert.ok(devices().includes(name+'\tinventory\t'));
        assert.equal(new URL(page.url()).hash,'#pair');
        if(process.env.WT_JOIN_CASE==='fragment')record('fragment-'+size,'imported invitation fragment stripped before exchange; account lookup absent');
        record('join-'+size,'terminal Enter; devices lists '+name+' with inventory scope; machine and session inventory rendered');
        const p=await probe(page,a[1]);assert.ok(p.errors.every(([,e])=>e==='denied'));assert.equal(p.replay,'replay');assert.equal(p.scope,'inventory');
        record('denials-'+size,'launch, input, action, stop, attach and fleet_input denied at wing');record('replay-'+size,'same encrypted command returned replay');record('key-'+size,'stored P-256 private CryptoKey refuses export');
        // A saved wing pin cannot be silently replaced by an imported link.
        const wrongPin=await pair(), changed=decode(wrongPin.link);changed[3]=Buffer.alloc(32,11).toString('base64');
        await page.reload();await paste(page,encode(changed),'Pin replacement');
        await page.getByRole('heading',{name:'Identity mismatch',exact:true}).waitFor();
        await shot(page,size,'wrong-pin-existing');
        const samePin=await page.evaluate(async ({wing,key})=>{const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('wt-paired-wings-v1');r.onsuccess=()=>resolve(r.result);r.onerror=reject;});const saved=await new Promise((resolve,reject)=>{const r=db.transaction('devices').objectStore('devices').get(wing);r.onsuccess=()=>resolve(r.result);r.onerror=reject;});db.close();return saved.key===key;},{wing:a[1],key:a[3]});
        assert.equal(samePin,true);assert.ok(!devices().includes('Pin replacement'));wrongPin.child.kill('SIGINT');
        record('saved-pin-'+size,'changed invitation pin refused; original persisted pin unchanged');
        await page.reload();await page.getByRole('button',{name:'Open Invitation alias'}).click();await page.getByText('✓ Connected · Inventory only').waitFor();
        record('reload-'+size,'IndexedDB signing key and credential reconnect after reload');
        cli('devices','revoke',name);
        await page.getByRole('heading',{name:'Access ended'}).waitFor({timeout:12000});
        await shot(page,size,'revoked');record('revoked-'+size,'live protected inventory cleared after wt devices revoke');
        await page.getByRole('button',{name:'Forget',exact:true}).click();await page.getByRole('heading',{name:'Join from this browser'}).waitFor();
        assert.equal(await page.locator('#bp-saved button').count(),0);record('forget-'+size,'local key and credential removed');
        await context.close();
        {
            const item=await newPage(viewport), f=await pair(), invitation=decode(f.link);
            invitation[1]='old-wing-'+size;
            const old=await oldWing(invitation[1],invitation[3]);
            await paste(item.page,encode(invitation),'Old wing browser '+size);
            await item.page.getByRole('heading',{name:'Machine needs a newer wingthing',exact:true}).waitFor();
            await item.page.getByText(/Update wingthing on this machine and restart its wing/).waitFor();
            assert.equal(old.forwarded(),0,'browser request reached a wing without scope enforcement');
            await shot(item.page,size,'old-wing');
            record('old-wing-'+size,'real relay closes unsupported wing route with readable update reason; no request forwarded');
            old.socket.close();await item.context.close();f.child.kill('SIGINT');
        }
        {
            const item=await newPage(viewport,()=>{
                if(sessionStorage.getItem('candidate-held'))return;
                sessionStorage.setItem('candidate-held','1');
                const put=IDBObjectStore.prototype.put,send=WebSocket.prototype.send;
                let wrote=false,held=false;
                IDBObjectStore.prototype.put=function(value,...rest){if(value.device_id)wrote=true;return put.call(this,value,...rest);};
                WebSocket.prototype.send=function(value){if(wrote&&!held){held=true;window.__candidateHeld=true;return;}return send.call(this,value);};
            });
            const f=await pair();await paste(item.page,f.link,'Interrupted '+size);
            await item.page.getByRole('button',{name:'Match · Join'}).click();
            for(let i=0;i<50&&!f.output().includes('Enter to allow');i++)await sleep(100);
            assert.ok(f.output().includes('Enter to allow'));f.child.stdin.write('\n');
            await item.page.waitForFunction(()=>window.__candidateHeld===true);
            await item.page.reload();
            await item.page.getByRole('heading',{name:'Join from this browser'}).waitFor();
            const retry=await pair();await paste(item.page,retry.link,'Recovered '+size);
            await item.page.getByRole('heading',{name:'Pairing cancelled',exact:true}).waitFor();
            const forget=item.page.getByRole('button',{name:'Forget incomplete pairing',exact:true});
            await forget.waitFor();await shot(item.page,size,'incomplete-pairing');
            await forget.click();
            await item.page.getByRole('heading',{name:'Join from this browser'}).waitFor();
            await paste(item.page,retry.link,'Recovered '+size);
            await item.page.getByRole('button',{name:'Match · Join'}).click();
            for(let i=0;i<50&&!retry.output().includes('Enter to allow');i++)await sleep(100);
            assert.ok(retry.output().includes('Enter to allow'));retry.child.stdin.write('\n');
            await item.page.getByText('✓ Connected · Inventory only').waitFor();
            await item.page.locator('#bp-sessions').getByText('join-proof-session',{exact:true}).waitFor();
            assert.ok(devices().includes('Recovered '+size+'\tinventory\t'));
            assert.ok(!devices().includes('Interrupted '+size));
            await shot(item.page,size,'recovered-page');
            record('recovered-page-'+size,'actual reload leaves pending record; error offers Forget incomplete pairing; fresh join opens inventory');
            cli('devices','revoke','Recovered '+size);
            await item.context.close();f.child.kill('SIGINT');retry.child.kill('SIGINT');
        }
        for (const requestedScope of ['control','admin']) {
            const item=await newPage(viewport,scope=>{
                const encode=TextEncoder.prototype.encode,decode=TextDecoder.prototype.decode;
                window.__wingErrors=[];
                const send=WebSocket.prototype.send;
                WebSocket.prototype.send=function(value){const request=JSON.parse(value);request.browser_phone=false;return send.call(this,JSON.stringify(request));};
                TextEncoder.prototype.encode=function(value){
                    try { const data=JSON.parse(value);if(data.op==='prove'){data.scope=scope;data.inventory_only=false;value=JSON.stringify(data);} } catch {}
                    return encode.call(this,value);
                };
                TextDecoder.prototype.decode=function(...args){
                    const value=decode.apply(this,args);try{const data=JSON.parse(value);if(data.error)window.__wingErrors.push(data.error);}catch{}
                    return value;
                };
            },requestedScope);
            const f=await pair();
            let route;item.page.on('websocket',socket=>{route=socket.url();});
            await paste(item.page,f.link,'Scope '+requestedScope+' '+size);
            await item.page.getByRole('heading',{name:'Pairing refused',exact:true}).waitFor();
            assert.equal(new URL(route).pathname,'/ws/browser-phone');
            assert.equal(new URL(route).host,new URL(base).host);
            assert.ok(await item.page.evaluate(()=>window.__wingErrors.includes('denied')),'wing did not refuse wider scope');
            assert.ok(!devices().includes('Scope '+requestedScope+' '+size));
            assert.ok(!f.output().includes('Enter to allow'));
            await shot(item.page,size,'scope-'+requestedScope);
            record('scope-'+requestedScope+'-'+size,'app-Origin browser route request refused by wing with denied; no terminal approval or device');
            await item.context.close();f.child.kill('SIGINT');
        }
        // One tab pauses after saving its candidate. A second tab sharing the
        // same IndexedDB must leave that candidate intact, including its pin.
        {
            if (invitationCount > 4) await resetRoost();
            const owner=await newPage(viewport,()=>{
                const put=IDBObjectStore.prototype.put,send=WebSocket.prototype.send;
                let wrote=false,held=false;
                IDBObjectStore.prototype.put=function(value,...rest){if(value.device_id)wrote=true;return put.call(this,value,...rest);};
                WebSocket.prototype.send=function(value){
                    if(wrote&&!held){held=true;window.__heldCandidateCommit={socket:this,value};return;}
                    return send.call(this,value);
                };
                window.__releaseCandidateCommit=()=>{const c=window.__heldCandidateCommit;send.call(c.socket,c.value);};
            });
            const f=await pair();
            await paste(owner.page,f.link,'Tabs '+size);
            await owner.page.getByRole('button',{name:'Match · Join'}).waitFor();
            const other=await owner.context.newPage();
            await other.goto(base+'/app/#pair');
            await other.getByRole('heading',{name:'Join from this browser'}).waitFor();
            const next=await pair();
            await paste(other,next.link,'Other tab '+size);
            await other.getByRole('button',{name:'Match · Join'}).waitFor();
            await owner.page.getByRole('button',{name:'Match · Join'}).click();
            for(let i=0;i<50&&!f.output().includes('Enter to allow');i++)await sleep(100);
            assert.ok(f.output().includes('Enter to allow'));f.child.stdin.write('\n');
            await owner.page.waitForFunction(()=>!!window.__heldCandidateCommit);
            await other.getByRole('button',{name:'Match · Join'}).click();
            for(let i=0;i<50&&!next.output().includes('Enter to allow');i++)await sleep(100);
            assert.ok(next.output().includes('Enter to allow'));next.child.stdin.write('\n');
            await other.getByRole('heading',{name:'Browser storage unavailable',exact:true}).waitFor();
            await other.getByRole('button',{name:'Pair again',exact:true}).click();
            await paste(other,next.link,'Other tab '+size);
            await other.getByRole('heading',{name:'Pairing cancelled',exact:true}).waitFor();
            await shot(other,size,'other-tab-candidate');
            const changed=decode(next.link);changed[3]=Buffer.alloc(32,17).toString('base64');
            await other.getByRole('button',{name:'Pair again',exact:true}).click();
            await paste(other,encode(changed),'Changed pin tab '+size);
            await other.getByRole('heading',{name:'Identity mismatch',exact:true}).waitFor();
            await owner.page.evaluate(()=>window.__releaseCandidateCommit());
            await owner.page.getByText('✓ Connected · Inventory only').waitFor({timeout:15000});
            assert.ok(devices().includes('Tabs '+size+'\tinventory\t'));
            assert.ok(!devices().includes('Other tab '+size));
            record('tabs-'+size,'concurrent tab cannot overwrite or delete another tab’s pending candidate or pin; original tab commits and opens inventory');
            cli('devices','revoke','Tabs '+size);
            await owner.context.close();f.child.kill('SIGINT');next.child.kill('SIGINT');
        }
        for(const failure of ['wrong-pin','wrong-secret','expired','mismatch','cancelled','unreachable','storage','storage-final','storage-readback','storage-activation','cancel-commit','pagehide-commit','unload-commit'].filter(name=>!process.env.WT_JOIN_CASE||name===process.env.WT_JOIN_CASE)) {
            const init=failure==='storage'?()=>{indexedDB.open=()=>{throw new DOMException('Unavailable','QuotaExceededError');};}:failure==='storage-final'?()=>{const put=IDBObjectStore.prototype.put;IDBObjectStore.prototype.put=function(value,...rest){if(value.device_id)throw new DOMException('Full','QuotaExceededError');return put.call(this,value,...rest);};}:failure==='storage-activation'?()=>{const put=IDBObjectStore.prototype.put;IDBObjectStore.prototype.put=function(value,...rest){if(value.ready===true)throw new DOMException('Full','QuotaExceededError');return put.call(this,value,...rest);};}:failure==='storage-readback'?()=>{
                let wrote=false,failed=false;const put=IDBObjectStore.prototype.put,get=IDBObjectStore.prototype.get,send=WebSocket.prototype.send;
                IDBObjectStore.prototype.put=function(value,...rest){if(value.device_id)wrote=true;return put.call(this,value,...rest);};
                IDBObjectStore.prototype.get=function(...args){if(wrote&&!failed){failed=true;window.__joinReadbackFailed=true;throw new DOMException('Read unavailable','UnknownError');}return get.apply(this,args);};
                WebSocket.prototype.send=function(value){if(window.__joinReadbackFailed)throw new Error('Transport interrupted');return send.call(this,value);};
            }:['cancel-commit','pagehide-commit','unload-commit'].includes(failure)?()=>{
                const Native=WebSocket;let wrote=false,held=false;const put=IDBObjectStore.prototype.put;
                IDBObjectStore.prototype.put=function(value,...rest){if(value.device_id)wrote=true;return put.call(this,value,...rest);};
                window.WebSocket=class extends Native{
                    set onmessage(handler){super.onmessage=event=>{if(wrote&&!held){held=true;window.__joinCommitHeld=true;return;}handler(event);};}
                    send(data){if(held)throw new Error('Cancellation transport interrupted');super.send(data);}
                };
            }:null;
            const item=await newPage(viewport,init), f=await pair(), data=decode(f.link);let link=f.link;
            if(failure==='wrong-pin'){data[3]=Buffer.alloc(32,9).toString('base64');link=encode(data);}
            if(failure==='wrong-secret'){data[7]+=' wrong';link=encode(data);}
            if(failure==='expired'){data[8]=Math.floor(Date.now()/1000)-1;link=encode(data);}
            if(failure==='unreachable'){data[1]='unregistered-local-wing';link=encode(data);}
            await paste(item.page,link,'Failure '+size+' '+failure);
            if(['mismatch','cancelled','storage-final','storage-readback','storage-activation','cancel-commit','pagehide-commit','unload-commit'].includes(failure)) {
                await item.page.getByRole('button',{name:'Match · Join'}).waitFor();
                if(failure==='mismatch')await item.page.getByRole('button',{name:'They differ'}).click();
                if(failure==='cancelled')await item.page.getByRole('button',{name:'Cancel',exact:true}).click();
                if(['storage-final','storage-readback','storage-activation','cancel-commit','pagehide-commit','unload-commit'].includes(failure)){await item.page.getByRole('button',{name:'Match · Join'}).click();for(let i=0;i<50&&!f.output().includes('Enter to allow');i++)await sleep(100);f.child.stdin.write('\n');}
                if(['cancel-commit','pagehide-commit','unload-commit'].includes(failure)){
                    await item.page.waitForFunction(()=>window.__joinCommitHeld===true);
                    if(failure==='cancel-commit')await item.page.getByRole('button',{name:'Cancel',exact:true}).click();
                    else if(failure==='unload-commit')await item.page.reload();
                    else await item.page.evaluate(()=>window.dispatchEvent(new Event('pagehide')));
                    await sleep(300);
                    const remaining=await item.page.evaluate(async onlyReady=>{const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('wt-paired-wings-v1');r.onsuccess=()=>resolve(r.result);r.onerror=reject;});const rows=await new Promise((resolve,reject)=>{const r=db.transaction('devices').objectStore('devices').getAll();r.onsuccess=()=>resolve(r.result);r.onerror=reject;});db.close();return rows.filter(r=>r.device_id&&(!onlyReady||r.ready===true)).length;},failure==='unload-commit');
                    assert.equal(remaining,0,'canceled commit retained usable local credential');
                    if(failure==='unload-commit')assert.equal(await item.page.getByRole('button',{name:/^Open Studio/}).count(),0);
                    // The commit was delivered but cancellation could not be.
                    // Revoke that orphaned server grant explicitly in this fixture.
                    cli('devices','revoke','Failure '+size+' '+failure);
                    if(failure==='pagehide-commit')await item.page.reload();
                }
            }
            const heading={ 'wrong-pin':'Identity mismatch','wrong-secret':'Identity mismatch',expired:'Invitation expired',mismatch:'Identity mismatch',cancelled:'Pairing cancelled',unreachable:'Machine unreachable',storage:'Browser storage unavailable','storage-final':'Browser storage unavailable','storage-readback':'Browser storage unavailable','storage-activation':'Browser storage unavailable','cancel-commit':'Pairing cancelled','pagehide-commit':'Join from this browser','unload-commit':'Join from this browser'}[failure];
            await item.page.getByRole('heading',{name:heading,exact:true}).waitFor({timeout:20000});
            await shot(item.page,size,failure);
            if(['storage-final','storage-readback','storage-activation'].includes(failure))await sleep(1000);
            assert.ok(!devices().includes('Failure '+size+' '+failure),'failure granted a device');
            record(failure+'-'+size,['cancel-commit','pagehide-commit','unload-commit'].includes(failure)?(failure==='unload-commit'?'actual reload exposed no usable local Open; orphan grant revoked by fixture CLI':'local credential removed despite lost cancel; orphan grant revoked by fixture CLI'):'state shown; no active device grant');
            await item.context.close();f.child.kill('SIGINT');
        }
    }
    record('accountless','both viewports serve Pair without /api/app/me');
    // Keep the browser clock before expiry only in these fixtures. One real
    // five-minute wait proves both a start rejected by the wing and a commit
    // rejected after Enter, without changing the invitation's server deadline.
    if (!process.env.WT_JOIN_CASE) {
        await resetRoost();
        const expired=await pair(), real=decode(expired.link), pending=[];
        for(const viewport of [{width:390,height:844},{width:1440,height:900}]){
            const f=await pair();
            const item=await newPage(viewport,now=>{
                Date.now=()=>now;
                const put=IDBObjectStore.prototype.put,send=WebSocket.prototype.send,timeout=window.setTimeout;
                let wrote=false,held=false,previous,keepalive;
                // Only the delayed commit needs a longer browser request timer.
                window.setTimeout=(fn,ms,...args)=>timeout(fn,wrote&&ms===12000?360000:ms,...args);
                IDBObjectStore.prototype.put=function(value,...rest){if(value.device_id)wrote=true;return put.call(this,value,...rest);};
                WebSocket.prototype.send=function(value){
                    if(wrote&&!held){
                        held=true;window.__heldCommit={socket:this,value};
                        // Reissue the last encrypted status (never commit) to
                        // keep this bounded relay socket alive during expiry.
                        keepalive=setInterval(()=>{const request=JSON.parse(previous);request.request_id=crypto.randomUUID();send.call(this,JSON.stringify(request));},30000);
                        return;
                    }
                    previous=value;return send.call(this,value);
                };
                window.__releaseCommit=()=>{clearInterval(keepalive);const c=window.__heldCommit;send.call(c.socket,c.value);};
                const decode=TextDecoder.prototype.decode;window.__wingErrors=[];
                TextDecoder.prototype.decode=function(...args){const value=decode.apply(this,args);try{const data=JSON.parse(value);if(data.error)window.__wingErrors.push(data.error);}catch{}return value;};
            },Date.now());
            await paste(item.page,f.link,'Commit expiry '+viewport.width);
            await item.page.getByRole('button',{name:'Match · Join'}).click();
            for(let i=0;i<50&&!f.output().includes('Enter to allow');i++)await sleep(100);
            assert.ok(f.output().includes('Enter to allow'));f.child.stdin.write('\n');
            await item.page.waitForFunction(()=>!!window.__heldCommit);
            pending.push({viewport,f,...item});
        }
        const deadline=Math.max(real[8],...pending.map(item=>decode(item.f.link)[8]))*1000;
        const wait=Math.max(0,deadline-Date.now()+1200);
        console.log('Waiting '+Math.ceil(wait/1000)+' seconds for wing start and commit expiry');
        await sleep(wait);
        for(const item of pending){
            const size=item.viewport.width;
            await item.page.evaluate(()=>window.__releaseCommit());
            await item.page.getByRole('heading',{name:'Invitation expired',exact:true}).waitFor();
            assert.ok(await item.page.evaluate(()=>window.__wingErrors.includes('expired')));
            const remaining=await item.page.evaluate(async()=>{
                const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('wt-paired-wings-v1');r.onsuccess=()=>resolve(r.result);r.onerror=reject;});
                const rows=await new Promise((resolve,reject)=>{const r=db.transaction('devices').objectStore('devices').getAll();r.onsuccess=()=>resolve(r.result);r.onerror=reject;});db.close();return rows.filter(row=>row.device_id).length;
            });
            assert.equal(remaining,0,'failed commit retained ready:false candidate');
            assert.ok(!devices().includes('Commit expiry '+size));
            await shot(item.page,size,'expired-commit');
            record('expired-commit-'+size,'wing refused commit after Enter with expired; no local candidate or server device');
            await item.page.getByRole('button',{name:'Pair again',exact:true}).click();
            const retry=await pair();await paste(item.page,retry.link,'Retry '+size);
            await item.page.getByRole('button',{name:'Match · Join'}).click();
            for(let i=0;i<50&&!retry.output().includes('Enter to allow');i++)await sleep(100);
            retry.child.stdin.write('\n');
            await item.page.getByText('✓ Connected · Inventory only').waitFor();
            await item.page.locator('#bp-sessions').getByText('join-proof-session',{exact:true}).waitFor();
            assert.ok(devices().includes('Retry '+size+'\tinventory\t'));
            await shot(item.page,size,'repaired');
            record('repaired-'+size,'fresh invitation joined successfully after expired commit without Forget');
            cli('devices','revoke','Retry '+size);
            await item.context.close();item.f.child.kill('SIGINT');
        }
        for(const viewport of [{width:390,height:844},{width:1440,height:900}]){
            const {page,context}=await newPage(viewport,now=>{
                Date.now=()=>now;
                const decode=TextDecoder.prototype.decode;window.__wingErrors=[];
                TextDecoder.prototype.decode=function(...args){const value=decode.apply(this,args);try{const data=JSON.parse(value);if(data.error)window.__wingErrors.push(data.error);}catch{}return value;};
            },real[8]*1000-10000);
            let reachedWing=false;page.on('websocket',socket=>socket.on('framesent',()=>{reachedWing=true;}));
            await paste(page,expired.link,'Expired browser');
            await page.getByRole('heading',{name:'Invitation expired',exact:true}).waitFor();
            assert.ok(reachedWing,'browser clock check prevented the wing expiry probe');
            assert.ok(await page.evaluate(()=>window.__wingErrors.includes('expired')),'wing did not refuse expired invitation');
            await shot(page,viewport.width,'expired-real');
            assert.ok(!devices().includes('Expired browser'));
            record('expired-real-'+viewport.width,'unmodified five-minute invitation reached wing with browser clock bypassed; wing returned expired; no grant');
            await context.close();
        }
    }

} catch(error) { console.error(error);fs.writeFileSync(path.join(out,'failure.txt'),error.stack+'\n');process.exitCode=1; }
finally {
    try { if(sessionStarted)cli('session','kill','join-proof-session'); } catch(error) { console.error('Fixture session cleanup: '+error.message);process.exitCode=1; }
    try {
        try { await browser?.close(); }
        finally {
            for(const socket of fixtureSockets)socket.close();
            await Promise.all([...pairs,roost].map(async child=>{
                if(child.exitCode!==null||child.signalCode!==null)return;
                const exited=once(child,'exit');child.kill('SIGINT');
                const timer=setTimeout(()=>child.kill('SIGKILL'),5000);
                try{await exited;}finally{clearTimeout(timer);}
            }));
        }
    } catch(error) { console.error('Fixture cleanup: '+error.message);process.exitCode=1; }
    finally { fs.closeSync(log);fs.rmSync(state,{recursive:true,force:true}); }
    assert.equal(fs.existsSync(state),false);
    record('state-cleanup','temporary wing state removed after fixture processes exit');
    const finish=revision();
    if(finish.head!==source.head||finish.dirty!==source.dirty){console.error('Source revision changed during verification');process.exitCode=1;}
    if(new Set(screenshots.map(shot=>shot.sha256)).size!==screenshots.length){console.error('Duplicate screenshot evidence');process.exitCode=1;}
    fs.writeFileSync(path.join(out,'proof.json'),JSON.stringify({...source,completed:process.exitCode!==1,proof,screenshots},null,2)+'\n');
}

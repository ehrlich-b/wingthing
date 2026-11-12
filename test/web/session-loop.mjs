// Real built roost + its own wing + real PTYs; only provider executables are fake.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { chromium } from '../../web/node_modules/playwright/index.mjs';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const out=process.env.WT_LOOP_OUT; assert.ok(out && path.isAbsolute(out));
const binary=process.env.WT_LOOP_BIN; assert.ok(binary && path.isAbsolute(binary));
const baseline=process.env.WT_LOOP_BASELINE==='1';
const work=fs.mkdtempSync(path.join(process.env.WT_LOOP_SCRATCH,'r'));
// Keep provider socket paths within Linux's 107-byte Unix socket limit.
const home=path.join(work,'h'), state=path.join(work,'s'), bin=path.join(work,'b');
for(const p of [out,home,state,bin]) fs.mkdirSync(p,{recursive:true});
for(const agent of ['claude','codex']) fs.copyFileSync(path.join(root,'test/web/session-loop/provider.py'),path.join(bin,agent));
for(const agent of ['claude','codex']) fs.chmodSync(path.join(bin,agent),0o700);
fs.writeFileSync(path.join(state,'wing.yaml'),'label: Local session proof\n');
const env={PATH:bin+':'+process.env.PATH,HOME:home,WINGTHING_DIR:state,SHELL:'/bin/sh',TERM:'xterm-256color',HTTP_PROXY:'http://127.0.0.1:1',HTTPS_PROXY:'http://127.0.0.1:1',NO_PROXY:'127.0.0.1,localhost',TMPDIR:process.env.WT_LOOP_SCRATCH};
const port=Number(process.env.WT_LOOP_PORT||18273),base='http://127.0.0.1:'+port;
const roostLog=fs.openSync(path.join(out,'roost.log'),'w');
const roost=spawn('nice',['-n','10',binary,'roost','start','--foreground','--addr','127.0.0.1:'+port],{cwd:work,env:{...env,WT_ROOST_READY_FD:'3'},stdio:['ignore',roostLog,roostLog,'pipe']});
const children=[],checks=[],failures=[],dialogs=[];
// HTTP health precedes embedded-wing startup. Wait for the real roost's
// readiness pipe before Chromium or any session client can compete with it.
const roostReady = new Promise((resolve,reject)=>{
 let token='';
 roost.stdio[3].setEncoding('utf8');
 roost.stdio[3].on('data',chunk=>{token+=chunk;});
 roost.stdio[3].on('end',()=>token==='ready\n'?resolve():reject(Error('roost readiness closed with '+JSON.stringify(token)+'; see roost.log')));
 roost.on('error',reject);
});
let browser;
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function until(fn,timeout=10000){const start=Date.now();let last;while(Date.now()-start<timeout){try{if(await fn())return Date.now()-start;}catch(e){last=e;}await sleep(50);}throw Error('deadline '+timeout+' '+(last||''));}
async function check(name,fn){try{const result=await fn();checks.push({name,result});console.log('PASS',name,JSON.stringify(result));}catch(e){failures.push({name,error:e.message});console.log('FAIL',name,e.message);if(!baseline)throw e;}}
try {
 await roostReady;
 await until(async()=> (await fetch(base+'/health')).ok);
 browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_PATH || undefined,args:['--disable-background-networking','--renderer-process-limit=2']});
 const page=await browser.newPage({viewport:{width:1440,height:900}});
 page.on('dialog',dialog=>{dialogs.push(dialog.type());dialog.dismiss();});
 page.on('websocket',socket=>socket.on('framereceived',event=>{try{const msg=JSON.parse(event.payload);if(['error','pty.started'].includes(msg.type)) console.log('PTY',JSON.stringify({type:msg.type,message:msg.message,session:msg.session_id}));}catch{}}));
 page.on('pageerror',e=>console.log('PAGEERROR',e.message));
 await page.route('**/*',route=>new URL(route.request().url()).origin===base?route.continue():route.abort());
 await page.goto(base+'/app/#sessions');
 const list=()=>fs.existsSync(path.join(state,'eggs'))?fs.readdirSync(path.join(state,'eggs')).filter(id=>/^[a-f0-9]{16}$/.test(id)):[];
 for(const agent of ['claude','codex']) {
  const before=list();
  const log=fs.openSync(path.join(out,agent+'-cli.log'),'w');
  const cli=spawn('nice',['-n','10',binary,'egg',agent,'--unsandboxed','--name',agent+'-investigate-login-redirect-preserving-message-order','--cwd',work],{cwd:work,env,stdio:['pipe',log,log]});children.push(cli);
  await until(()=>list().some(id=>!before.includes(id)));
  const sid=list().find(id=>!before.includes(id));
  await until(()=>fs.readFileSync(path.join(out,agent+'-cli.log'),'utf8').includes(agent+' ready'));
  await page.goto(base+'/app/?provider='+agent+'#sessions');
  await page.locator('.pp-srow[href*="/'+sid+'"]').click();
  const inputs=[agent+' direct terminal',agent+' composer first',agent+' takeover line',agent+' composer after handback'];
  if(agent==='codex' && !baseline) { for(const width of [1440,390]) { await page.setViewportSize({width,height:width===390?844:900});await page.waitForTimeout(150); await page.screenshot({path:path.join(out,'codex-zero-'+width+'.png')}); } await page.setViewportSize({width:1440,height:900}); }
  cli.stdin.write(inputs[0]+'\r');
  await check(agent+' direct user and assistant within 3s',async()=>{
   const ms=await until(async()=> (await page.locator('.pp-row-you .pp-say').allTextContents()).includes(inputs[0])&&(await page.locator('.pp-row-session .pp-say').allTextContents()).includes('Reply to '+inputs[0]),3000);return {ms};
  });
  cli.stdin.write('\x02q');
  await until(()=>cli.exitCode!==null);
  await check(agent+' live terminal',async()=>{await until(async()=>(await page.locator('.pp-term').textContent()).includes('INPUT '+inputs[0]),3000);return await page.locator('.pp-term').textContent();});
  for(const width of [1440,390]){await page.setViewportSize({width,height:width===390?844:900});await page.waitForTimeout(150);if(width===390) await page.locator('.pp-terminal').evaluate(el=>{el.open=true;}); await page.screenshot({path:path.join(out,agent+'-'+width+'.png')});}
  await page.setViewportSize({width:1440,height:900});
  await check(agent+' composer takeover handback order',async()=>{
   async function send(text){await until(()=>page.locator('#pp-message').isEnabled());await page.locator('#pp-message').fill(text);await page.locator('.pp-composer [type=submit]').click();await until(async()=>(await page.locator('.pp-row-you .pp-say').allTextContents()).includes(text),3000);}
   await send(inputs[1]);
   await until(()=>page.locator('#pp-message').isEnabled());
   await page.evaluate(()=>{window.keptConversation=document.querySelector('.pp-conversation');window.keptComposer=document.querySelector('#pp-message');});
   await page.locator('#pp-message').fill('Draft stays during terminal control');
   await page.locator('[data-takeover]').click();
   await page.locator('.pp-term-rail .xterm').waitFor({timeout:3000});
   assert.ok(await page.locator('.pp-conversation').isVisible());
   assert.ok(await page.locator('#pp-message').isVisible());
   await until(()=>page.locator('#pp-message').isEnabled());
   assert.equal(await page.locator('#pp-message').inputValue(),'Draft stays during terminal control');
   assert.ok(await page.evaluate(()=>window.keptConversation===document.querySelector('.pp-conversation')&&window.keptComposer===document.querySelector('#pp-message')));
   await page.locator('#pp-message').fill('Editable while terminal has control');
   assert.ok(await page.locator('.pp-side').isVisible());
   await until(async()=>(await page.locator('.pp-term-rail .pp-kick').innerText()).includes('You have control'),3000);
   for(const width of [1440,390]) {await page.setViewportSize({width,height:width===390?844:900});await page.waitForTimeout(150);assert.ok(await page.locator('.pp-conversation').isVisible());assert.ok(await page.locator('#pp-message').isVisible());await page.screenshot({path:path.join(out,agent+'-control-'+width+'.png')});}
   await page.setViewportSize({width:1440,height:900});
   await sleep(1100); // A transcript refresh must keep the mounted terminal usable.
   await page.locator('.pp-live-terminal .xterm-helper-textarea').focus();
   await page.keyboard.type(inputs[2]);await page.keyboard.press('Enter');
   await until(()=>fs.readFileSync(path.join(work,agent+'-pty.jsonl'),'utf8').includes(inputs[2]));
   await page.locator('[data-handback]').click();
   assert.ok(await page.locator('.pp-conversation').isVisible());
   assert.ok(await page.locator('#pp-message').isVisible());
   assert.equal(await page.locator('#pp-message').inputValue(),'Editable while terminal has control');
   assert.ok(await page.evaluate(()=>window.keptConversation===document.querySelector('.pp-conversation')&&window.keptComposer===document.querySelector('#pp-message')));
   await send(inputs[3]);
   await until(async()=>(await page.locator('.pp-term').textContent()).includes(inputs[3]),3000);
   const screen=await page.locator('.pp-term').textContent();
   for(const text of inputs) assert.ok(screen.includes('INPUT '+text));
   const received=fs.readFileSync(path.join(work,agent+'-pty.jsonl'),'utf8').trim().split('\n').map(JSON.parse);
   fs.copyFileSync(path.join(work,agent+'-pty.jsonl'),path.join(out,agent+'-pty.jsonl'));
   assert.deepEqual(received,inputs);
   const rows=await page.locator('.pp-row-you .pp-say').allTextContents();assert.deepEqual(rows,inputs);
   for(const width of [1440,390]){await page.setViewportSize({width,height:width===390?844:900});await page.waitForTimeout(150);if(width===390) await page.locator('.pp-terminal').evaluate(el=>{el.open=true;}); await page.screenshot({path:path.join(out,agent+'-complete-'+width+'.png')});}
   return {received,rows};
  });
  if(agent==='codex' && !baseline) {
   const attach=spawn('nice',['-n','10',binary,'attach',sid],{cwd:work,env,stdio:['pipe','ignore','ignore']}); children.push(attach);
   await sleep(300);
   await check('codex repeated prompts without turn IDs and transient sending',async()=>{
    const repeated='Repeat without a turn ID';
    const before=await page.locator('.pp-row-you .pp-say').allTextContents();
    for(let turn=1;turn<=2;turn++) {
     attach.stdin.write(repeated+'\r');
     await page.locator('.pp-sending').waitFor({timeout:3000});
     assert.equal(await page.locator('.pp-sending').innerText(),'Sending...');
     assert.deepEqual(await page.locator('.pp-row-you .pp-say').allTextContents(),before.concat(Array(turn-1).fill(repeated)));
     if(turn===1) for(const width of [1440,390]) {await page.setViewportSize({width,height:width===390?844:900}); await page.screenshot({path:path.join(out,'codex-sending-'+width+'.png')});}
     await page.setViewportSize({width:1440,height:900});
     fs.writeFileSync(path.join(work,'release-transcript'),String(turn));
     await until(async()=>(await page.locator('.pp-row-you .pp-say').allTextContents()).length===before.length+turn,3000);
     await until(async()=>await page.locator('.pp-sending').count()===0,3000);
    }
    const expected=inputs.concat([repeated,repeated]).flatMap(text=>[text,'Reply to '+text]);
    await until(async()=>(await page.locator('.pp-conversation .pp-say').allTextContents()).length===expected.length,3000);
    assert.deepEqual(await page.locator('.pp-conversation .pp-say').allTextContents(),expected);
    const rows=await page.locator('.pp-row-you .pp-say').allTextContents();
    assert.deepEqual(rows,before.concat([repeated,repeated]));
    const received=fs.readFileSync(path.join(work,'codex-pty.jsonl'),'utf8').trim().split('\n').map(JSON.parse);
    assert.deepEqual(received,inputs.concat([repeated,repeated]));
    fs.copyFileSync(path.join(work,'codex-pty.jsonl'),path.join(out,'codex-repeated-pty.jsonl'));
    return {rows,received};
   });
   for(let i=0;i<6;i++) attach.stdin.write('Long follow-up '+i+' investigating session message order and native transcript replay\r');
   await until(async()=>await page.locator('.pp-conversation .pp-row').count()===24,10000);
   for(const width of [1440,390]) {await page.setViewportSize({width,height:width===390?844:900});await page.waitForTimeout(150); await page.screenshot({path:path.join(out,'codex-limit24-'+width+'.png')});}
   attach.stdin.write('One more turn beyond the message display limit\r');
   await until(async()=>(await page.locator('.pp-conversation').innerText()).includes('+2 earlier messages'),10000);
   assert.equal(await page.locator('.pp-conversation .pp-row').count(),24);
   for(const width of [1440,390]) {await page.setViewportSize({width,height:width===390?844:900});await page.waitForTimeout(150); await page.screenshot({path:path.join(out,'codex-over-limit-'+width+'.png')});}
   attach.stdin.write('\x02q');await until(()=>attach.exitCode!==null);
  }
 }
 // Unknown provider executes an actual /bin/sh, with no transcript adapter.
 const before=list();const shell=spawn('nice',['-n','10',binary,'terminal','--unsandboxed','--name','Plain-shell','--cwd',work],{cwd:work,env,stdio:['pipe','ignore','ignore']});children.push(shell);
 await until(()=>list().some(id=>!before.includes(id)));const sid=list().find(id=>!before.includes(id));
 await sleep(700);shell.stdin.write('echo LIVE_SHELL_SCREEN\r');await sleep(300);shell.stdin.write('\x02q');
 await page.goto(base+'/app/?shell=1#sessions');await page.locator('.pp-srow[href*="/'+sid+'"]').click();
 await check('plain shell live terminal and honest chat note',async()=>{await until(async()=>(await page.locator('.pp-term').textContent()).includes('LIVE_SHELL_SCREEN'),3000);assert.ok((await page.locator('.pp-conversation').innerText()).includes('Chat view is available for Claude and Codex.'));assert.doesNotMatch(await page.locator('#paper').innerText(),/No messages in this session yet/);return 'live screen and availability note';});
 for(const width of [1440,390]){await page.setViewportSize({width,height:width===390?844:900});await page.waitForTimeout(150);if(width===390) await page.locator('.pp-terminal').evaluate(el=>{el.open=true;}); await page.screenshot({path:path.join(out,'shell-'+width+'.png')});}
} finally {
 if(browser)await browser.close();
 for(const cli of children)if(cli.exitCode===null)cli.kill('SIGTERM');
 // Stop only eggs created by this run, using the real CLI.
 if(fs.existsSync(path.join(state,'eggs')))for(const sid of fs.readdirSync(path.join(state,'eggs')))spawnSync(binary,['egg','stop',sid],{env,cwd:work,timeout:5000});
 roost.kill('SIGTERM');
 fs.writeFileSync(path.join(out,'checks.json'),JSON.stringify({baseline,checks,failures,dialogs,work},null,2)+'\n');
}
assert.deepEqual(dialogs,[]);
assert.ok(baseline ? failures.length>0 : failures.length===0);

import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

async function run() {
 const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
 const require=createRequire(resolve(root,'package.json'));
 const {build}=require('esbuild');
 const {chromium}=await import('playwright');
 const fixture=await build({entryPoints:[resolve(root,'test/support/account-fixture.js')],bundle:true,write:false,format:'iife',platform:'browser',logLevel:'silent'});
 const before=process.env.WT_ACCOUNT_BEFORE==='1';
 const dist=resolve(process.env.WT_ACCOUNT_DIST||resolve(root,'dist'));
 const output=resolve(process.env.WT_ACCOUNT_SHOTS||resolve(root,'../../evidence/after'));
 mkdirSync(output,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_PATH||undefined,args:['--disable-background-networking','--renderer-process-limit=2']});
 const results=[];
 try {
 for(const width of [390,1440]) for(const theme of ['light','dark']) for(const state of (process.env.WT_ACCOUNT_STATES||'populated,empty,one,limit,free-purchase,org-pro,roost').split(',')) {
  const context=await browser.newContext({viewport:{width,height:width===390?844:1000},colorScheme:theme,serviceWorkers:'block'});
  await context.addInitScript({content:fixture.outputFiles[0].text});
  const page=await context.newPage(), errors=[], mutations=[], historyChecks=[];
  page.setDefaultTimeout(10000);
  console.log('case',width,theme,state);
  page.on('console',m=>{if(m.type()==='error') console.log(m.text());});
  page.on('dialog',()=>{throw Error('Sign-in methods must never open a dialog');});
  page.on('pageerror',e=>{errors.push(e.message);console.log(e.message);});
  const count=state==='empty'?0:state==='one'?1:8;
  let methods=state==='empty'?[]:[{provider:'github',label:''},...(state==='one'?[]:[{provider:'email',label:'morgan.chen.customer-experience-and-onboarding@example.com'}]),...(state==='limit'?[{provider:'google',label:''}]:[])];
  const initialMethods=methods.length;
  const user={id:'account-owner-technical-id',display_name:'Morgan Chen',email:'morgan.chen@example.com',provider:'google',tier:state.startsWith('free')||state==='empty'?'free':'pro',personal_pro:!['org-pro','roost'].includes(state),has_passkeys:count>0,relay_allowed:true,self_service_plans:['free-purchase','limit'].includes(state),roost_mode:state==='roost'};
  let passkeys=Array.from({length:count},(_,i)=>({id:'passkey-'+i,label:['MacBook Pro — Morgan’s work profile','Security key — backup for travel','iPhone — personal device'][i%3]+' '+(i+1),public_key:'raw-passkey-public-key-'+i,created_at:'2026-10-20T12:00:00Z'}));
  const orgs=Array.from({length:count},(_,i)=>({id:'organization-'+i,name:['Customer experience and onboarding','Platform reliability and infrastructure','Documentation and developer education'][i%3]+' '+(i+1),is_owner:i%2===0,member_count:8,has_subscription:true,plan:'team_yearly',seats_total:8,seats_used:8}));
  await context.route('**/*',async route=>{
   const req=route.request(),url=new URL(req.url());
   assert.equal(url.origin,'http://account.localhost','all requests remain local');
   const json=body=>route.fulfill({contentType:'application/json',body:JSON.stringify(body)});
   if(req.method()!=='GET') {
    mutations.push({path:url.pathname,method:req.method(),body:req.postData()});
    if(req.method()==='DELETE'&&url.pathname.startsWith('/api/app/signin/')) {
     if(methods.length===1) return route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({code:'last_signin_method',error:'This is your only way in'})});
     methods=methods.filter(m=>!url.pathname.endsWith(m.provider));return json({ok:true});
    }
    if(url.pathname==='/api/app/signin/email') return json({sent:true});
    if(url.pathname==='/api/app/signin/google') {
     methods.push({provider:'google',label:''});
     return route.fulfill({contentType:'text/html',body:'<p>Google proof fixture complete</p>'});
    }
    if(req.method()==='DELETE'&&url.pathname.startsWith('/api/app/passkey/')) {passkeys=passkeys.filter(c=>!url.pathname.endsWith(c.id));return json({ok:true});}
    if(url.pathname==='/api/app/upgrade') return json({tier:'pro'});
    if(url.pathname==='/api/app/downgrade') return json({tier:'free'});
    if(url.pathname==='/api/app/passkey/register/begin') return json({publicKey:{challenge:'YQ',user:{id:'YQ'},excludeCredentials:[]}});
    if(url.pathname==='/api/app/passkey/register/finish') return json({ok:true});
    if(url.pathname==='/api/orgs') return json({error:'Choose a different name'});
    if(url.pathname==='/api/app/ntfy/test') return json({ok:true});
    if(url.pathname==='/api/app/ntfy') return json({ok:true});
    if(url.pathname==='/auth/logout') return json({ok:true});
    throw Error('Unexpected mutation '+url.pathname);
   }
   if(url.pathname==='/api/app/me') return json(user);
   if(url.pathname==='/api/app/wings') return json([{wing_id:'machine-a',public_key:await page.evaluate(()=>accountFixture.publicKey),user_id:user.id,online:true}]);
   if(url.pathname==='/api/app/signin') return json({methods,can_add_google:true,can_add_email:true});
   if(url.pathname==='/api/app/passkey') return json(passkeys);
   if(url.pathname==='/api/app/ntfy') return json({enabled:state!=='empty'});
   if(url.pathname==='/api/orgs') return json(orgs);
   if(url.pathname.match(/^\/api\/orgs\/[^/]+\/members$/)) return json({members:Array.from({length:8},(_,i)=>({user_id:'member-'+i,email:'teammate-'+i+'@example.com',role:'member'})),invites:[]});
   const relative=url.pathname==='/app/'||url.pathname==='/'?'index.html':url.pathname.replace(/^\/app\//,'').replace(/^\//,'');
   const file=resolve(dist,relative);
   if(file.startsWith(dist+'/')&&existsSync(file)) return route.fulfill({body:readFileSync(file),contentType:relative.endsWith('.js')?'text/javascript':relative.endsWith('.css')?'text/css':relative.endsWith('.woff2')?'font/woff2':relative.endsWith('.svg')?'image/svg+xml':'text/html'});
   throw Error('Unexpected resource '+url.pathname);
  });
  await page.goto('http://account.localhost/app/#account');
  await page.locator('#ac-passkey-list').getByText(count?passkeys[0].label:'no passkeys registered',{exact:true}).waitFor();
  await page.waitForFunction(()=>document.querySelectorAll('#session-tabs .session-tab').length===24);
  await page.waitForFunction(count=>document.querySelectorAll('.ac-signin-row').length===count,initialMethods);
  const prefix=`account-${width}-${theme}-${state}`;
  await page.screenshot({path:resolve(output,prefix+'-first.png')});
  // Legacy account scrolls internally; expand its scroll surface for a true full-page capture.
  if(before) await page.addStyleTag({content:'body {height:auto;overflow:visible} #app,#main {height:auto;min-height:100vh} #account-section {overflow:visible;flex:none}'});
  await page.screenshot({path:resolve(output,prefix+'-full.png'),fullPage:true,style:before?undefined:'#paper {position:relative} #paper .pp-tabs {position:absolute;bottom:0}'});
  if(before) {results.push({width,theme,state,sessions:24,errors});await context.close();continue;}
  assert.equal(await page.locator('body').evaluate(el=>el.classList.contains('paper-on')),true);
  assert.equal(await page.locator('#sidebar').isVisible(),false);
  assert.equal(await page.getByRole('heading',{name:'Account',exact:true}).count(),1,'one shell Account heading');
  for(const name of ['Account','Current plan','Sign-in methods','Passkeys']) {
   const box=await page.getByRole('heading',{name,exact:true}).boundingBox();
   assert.ok(box&&box.y+box.height<(width===390?844:1000),name+' is identifiable in the first screen');
  }
  assert.equal(await page.getByText(user.id,{exact:true}).isVisible(),false);
  assert.doesNotMatch(await page.locator('#account-content').innerText(),/raw-passkey-public-key/);
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'no horizontal overflow');
  const color=await page.locator('#account-section').evaluate(el=>getComputedStyle(el).color);
  assert.equal(color,await page.locator('#paper').evaluate(el=>getComputedStyle(el).color),'account inherits the current shell palette');
  await page.getByText('Technical details',{exact:true}).click();
  assert.equal(await page.getByText(user.id,{exact:true}).isVisible(),true);
  await page.getByText('Technical details',{exact:true}).click();
  if(count>3) {
   assert.equal(await page.locator('.ac-passkey-row:visible').count(),3);
   await page.locator('#ac-passkey-list summary').click();
   assert.equal(await page.locator('.ac-passkey-row:visible').count(),8);
   await page.locator('#ac-passkey-list summary').click();
   if(state!=='roost') {await page.locator('#ac-org-list summary').click();assert.equal(await page.locator('.ac-org-header:visible').count(),8);await page.locator('#ac-org-list summary').click();}
  }
  assert.equal(await page.locator('.ac-signin-row').count(),initialMethods);
  assert.ok(await page.getByRole('button',{name:'Add Google',exact:true}).isVisible());
  assert.ok(await page.getByRole('button',{name:'Add email link',exact:true}).isVisible());
  assert.equal(await page.locator('#ac-google-add').isDisabled(),state==='limit');
  assert.equal(await page.locator('#ac-email-add').isDisabled(),!['empty','one'].includes(state));
  assert.equal(await page.locator('#ac-google-add').evaluate(el=>[el.form.method,el.form.getAttribute('action')].join(' ')),'post /api/app/signin/google');
  if(state==='one') {
   assert.equal(await page.locator('.ac-signin-row button').isDisabled(),true);
   assert.ok(await page.getByText('This is your only way in',{exact:true}).isVisible());
  }
  if(state==='populated') {
   await page.locator('.ac-signin-row [data-provider="github"]').click();
   await page.waitForFunction(()=>document.querySelectorAll('.ac-signin-row').length===1);
   assert.equal(await page.locator('.ac-signin-row button').isDisabled(),true);
   await page.screenshot({path:resolve(output,prefix+'-removed.png')});
  }
  assert.equal(await page.getByRole('button',{name:'Add passkey',exact:true}).count(),1);
  assert.equal(await page.locator('#account-upgrade').count(),state==='free-purchase'?1:0);
  assert.equal(await page.locator('#account-downgrade').count(),['populated','one','limit'].includes(state)?1:0);
  if(state==='limit') {
   await page.locator('.ac-org-header').first().click();
   await page.locator('#org-members-list-organization-0').getByText('teammate-0@example.com',{exact:false}).waitFor();
   assert.match(await page.locator('#ac-org-detail-organization-0').innerText(),/8\/8 used/);
   assert.equal(await page.locator('#org-add-seats-input-organization-0').getAttribute('min'),'9');
   await page.screenshot({path:resolve(output,prefix+'-expanded-full.png'),fullPage:true,style:'#paper {position:relative} #paper .pp-tabs {position:absolute;bottom:0}'});
  }
  if(state==='empty') {
   assert.equal(await page.getByRole('button',{name:'Create organization',exact:true}).count(),1);
   await page.locator('#ac-create-toggle').click();
   await page.locator('#ac-create-name').fill('Product team');await page.locator('#ac-create-btn').click();
   await page.locator('#ac-create-error').getByText('Choose a different name',{exact:true}).waitFor();
   await page.locator('#ac-ntfy-enable').click();
   assert.match(await page.locator('#ac-ntfy-content').innerText(),/ntfy/);
  }
  if(state==='one') {
   const del=page.locator('.ac-passkey-del').first();await del.click();assert.equal(mutations.length,0);await del.click();
   await page.locator('#ac-passkey-list').getByText('no passkeys registered',{exact:true}).waitFor();
   await page.evaluate(()=>{navigator.credentials.create=async()=>({id:'new-key',rawId:new Uint8Array([1]).buffer,type:'public-key',response:{attestationObject:new Uint8Array([2]).buffer,clientDataJSON:new Uint8Array([3]).buffer}});});
   await page.locator('#ac-passkey-add').click();await page.waitForFunction(()=>document.getElementById('ac-passkey-add').textContent==='Add passkey');
   assert.deepEqual(mutations.map(m=>m.path),['/api/app/passkey/passkey-0','/api/app/passkey/register/begin','/api/app/passkey/register/finish']);
   await page.locator('#account-downgrade').click();await page.waitForFunction(()=>document.getElementById('account-downgrade').textContent.includes('done'));
   assert.equal(mutations.at(-1).path,'/api/app/downgrade');
  }
  if(state==='free-purchase') {await page.locator('#account-upgrade').click();await page.waitForFunction(()=>document.getElementById('account-upgrade').textContent.includes('done'));assert.equal(mutations.at(-1).path,'/api/app/upgrade');}
  if(state==='one') {
   await page.locator('#ac-email-add').click();
   await page.locator('#ac-email-address').fill('backup@example.com');
   await page.getByRole('button',{name:'Send link',exact:true}).click();
   await page.getByRole('status').getByText('Check your email to add this method',{exact:true}).waitFor();
   assert.equal(await page.locator('.ac-signin-row').count(),1,'sending is not verification');
   assert.equal(mutations.at(-1).body,'email=backup%40example.com');
   await page.screenshot({path:resolve(output,prefix+'-email-sent.png')});
   await page.locator('#ac-google-add').click();
   await page.getByText('Google proof fixture complete',{exact:true}).waitFor();
   // Provider navigation is intercepted. Real callback proof is covered by relay tests.
   await page.goto('http://account.localhost/app/#account');
   await page.waitForFunction(()=>document.querySelectorAll('.ac-signin-row').length===2);
   assert.equal(mutations.at(-1).path,'/api/app/signin/google');
   assert.equal(await page.locator('#ac-google-add').isDisabled(),true);
  }
  // Navigation returns to the shell and account without losing either view.
  await page.locator('#ac-back').click();await page.locator('.pp-main').waitFor({state:'visible'});
  await page.goto('http://account.localhost/app/#account');await page.getByRole('heading',{name:'Account',exact:true}).waitFor();
  if(state==='populated') {
   // Enter through the main navigation, then traverse the actual browser history.
   for(const denied of [false,true]) {
    user.relay_allowed=!denied;
    const homeHash=denied?'':'#sessions';
    await page.goto('http://account.localhost/app/'+homeHash);
    await page.locator('.pp-main').waitFor({state:'visible'});
    const navigation=page.locator(width===390?'.pp-tabs':'.pp-side .pp-nav');
    if(denied) assert.deepEqual(await navigation.locator('a').allTextContents(),['Get started','Account']);
    const historyLength=await page.evaluate(()=>history.length);
    await navigation.getByRole('link',{name:'Account',exact:true}).click();
    await page.getByRole('heading',{name:'Account',exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>history.length),historyLength+1,'one entry per Account click');
    if(denied) {
     assert.deepEqual(await navigation.locator('a').allTextContents(),['Get started','Account']);
     assert.equal(await navigation.getByRole('link',{name:'Account',exact:true}).getAttribute('aria-current'),'page');
    }
    await page.goBack();
    await page.locator('.pp-main').waitFor({state:'visible'});
    assert.equal(new URL(page.url()).hash,homeHash,'one Back restores the preceding page');
    assert.equal(await page.locator('body').evaluate(el=>el.classList.contains('paper-on')),true);
    assert.equal(await page.locator('#sidebar').isVisible(),false);
    await page.screenshot({path:resolve(output,prefix+(denied?'-denied':'')+'-back.png')});
    await page.goForward();
    await page.getByRole('heading',{name:'Account',exact:true}).waitFor();
    assert.equal(new URL(page.url()).hash,'#account');
    assert.equal(await page.locator('#sidebar').isVisible(),false);
    assert.equal(await page.locator('body').evaluate(el=>el.classList.contains('paper-on')),true);
    historyChecks.push({denied,homeHash,accountHash:'#account',entriesAdded:1,back:'paper',forward:'account'});
   }
  }
  assert.deepEqual(errors,[]);
  results.push({width,theme,state,initialMethods,finalMethods:methods.length,sessions:24,color,mutations,historyChecks,errors});
  await context.close();
 }
 } finally {await browser.close();}
 writeFileSync(resolve(output,'receipt.json'),JSON.stringify({before,dist,results},null,2)+'\n');
 console.log('account-browser PASS '+results.length+' viewport/theme/state cases');
}
if(!process.env.NODE_TEST_CONTEXT) await run();

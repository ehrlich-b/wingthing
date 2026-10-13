import assert from 'node:assert/strict';
import { mkdir, writeFile, appendFile, copyFile, readFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { execFileSync } from 'node:child_process';
import { cards } from '../src/paid/cards.js';
import { createPaidState, applyPaidAction } from '../src/paid/model.js';
import { escape, cardContent } from '../src/paid/view.js';
async function runPaidUI() {
const base = process.env.PAID_BASE_URL || 'http://127.0.0.1:18407';
const defaultBase = process.env.PAID_DEFAULT_URL;
assert.ok(['127.0.0.1','localhost','[::1]'].includes(new URL(base).hostname),'fixture tests require loopback');
const shots = resolve(process.env.PAID_SHOTS_DIR || '.scratch/paid-shots');
const modulePath = process.env.WT_PLAYWRIGHT_MODULE || '.scratch/playwright/node_modules/playwright/index.mjs';
const { chromium } = await import(pathToFileURL(resolve(modulePath)).href);
const commit = execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim();
if(process.env.PAID_REQUIRE_CLEAN==='1') execFileSync('git',['diff','--exit-code','HEAD','--']);
const binarySha256=execFileSync('sha256sum',[process.env.PAID_BINARY || './wt'],{encoding:'utf8'}).split(' ')[0];
await mkdir(shots,{recursive:true});
const slug = title => title.replace(/^\d+ · /,'').toLowerCase().normalize('NFKD').replace(/[^a-z0-9]+/g,'-').replace(/^-|-$/g,'');
const file = (card,mobile=false) => `${String(card.flow).padStart(2,'0')}-${card.card}-${slug(card.title)}${mobile ? '-mobile' : ''}.png`;
const screenshotMap = new Map();
const errors=[], unexpected=[];
const browser = await chromium.launch({headless:true,executablePath:process.env.PAID_CHROMIUM || undefined,args:['--disable-background-networking','--renderer-process-limit=2']});
try {
 for (const viewport of [{width:1440,height:900},{width:390,height:844},{width:430,height:932}]) {
  const mobile=viewport.width<700;
  const mid=viewport.width===430;
  const context=await browser.newContext({viewport,hasTouch:mobile,deviceScaleFactor:1,acceptDownloads:true});
  // Fulfill nothing: every application resource comes from the actual wt build.
  // Abort all non-loopback traffic and fail the run if the UI tries any.
  await context.route('**/*',route=>{
   const u=new URL(route.request().url());
   if(u.origin!==new URL(base).origin && (!defaultBase || u.origin!==new URL(defaultBase).origin)) {unexpected.push(u.origin+u.pathname); return route.abort();}
   return route.continue();
  });
  const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  await page.goto(`${base}/app/#paid/upgrade/offers`);
  await page.locator('#paid-root').waitFor();
  const state=()=>page.evaluate(()=>JSON.parse(sessionStorage.getItem('wingthing-dormant-paid-fixture-v1')).state);
  const click=async label=>{await page.getByRole('button',{name:label,exact:true}).click();};
  const go=async (group,key)=>{
   if(mobile) await page.locator('#paid-route').selectOption(`#paid/${group}/${key}`);
   else { const nav=page.locator('.paid-nav details').filter({has:page.locator(`a[href="#paid/${group}/${key}"]`)}); if(!await nav.getAttribute('open')) { if(await nav.getAttribute('open')===null) await nav.locator('summary').click(); } await nav.locator(`a[href="#paid/${group}/${key}"]`).click(); }
   const card=cards.find(c=>c.group===group&&c.key===key), title=cardContent(card,await state()).title||card.title; await page.locator('h1').filter({hasText:title}).waitFor();
   assert.equal(await page.locator('h1').innerText(),title);
  };
  const reset=async profile=>{await page.locator('#paid-profile').selectOption(profile);await click('Reset selected fixture');};
  const capture=async (group,key)=>{
   const card=cards.find(c=>c.group===group&&c.key===key);
   assert.equal(await page.locator('h1').innerText(),cardContent(card,await state()).title||card.title);
   assert.ok(await page.getByText('Dormant · dev fixture',{exact:true}).isVisible());
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'no horizontal overflow');
   await page.evaluate(()=>window.scrollTo(0,0));
   const path=resolve(shots,file(card,mobile).replace('.png',mid ? '-430.png' : '.png'));await page.screenshot({path,animations:'disabled'});
   const record=screenshotMap.get(`${card.flow}-${card.card}`)||{card,desktop:null,mobile:null};record[mid?'mid':mobile?'mobile':'desktop']=path;
   if(await page.evaluate(()=>document.documentElement.scrollHeight>innerHeight)){const full=resolve(shots,file(card,mobile).replace('.png',mid ? '-430-full.png' : '-full.png'));await page.screenshot({path:full,fullPage:true,animations:'disabled'});record[mid?'midFull':mobile?'mobileFull':'desktopFull']=full;}
   screenshotMap.set(`${card.flow}-${card.card}`,record);
  };
  const launch=async()=>{await go('cloud','launch');assert.equal((await state()).cloud,null);await page.screenshot({path:resolve(shots,`10-6-before-launch-${viewport.width}.png`),fullPage:true});await click('Create fixture workspace');for(let i=0;i<4;i++)await click('Advance verified boot step');assert.equal((await state()).cloud.status,'ready');};
  const confirmStop=async()=>{await go('cloud','cleanup');await click('Confirm fixture shutdown');assert.equal((await state()).cloud.status,'stopped');};
  await reset('free');
  for(const name of ['Reach a wing anywhere','A private preview','Run with your Mac asleep','Share with Alice'])await page.locator('.paid-tile').filter({has:page.getByRole('heading',{name,exact:true})}).getByRole('button').click();
  assert.equal((await state()).offersSeen.length,4);assert.equal((await state()).cloud,null);
  await capture('upgrade','offers');
  await go('upgrade','price');assert.match(await page.locator('.paid-panel').innerText(),/100 GB/);await click('Continue to fixture checkout');await capture('upgrade','price');
  await go('upgrade','checkout');await click('Simulate return URL');assert.equal((await state()).pro,false);await capture('upgrade','checkout');
  await click('Confirm fixture payment');assert.equal((await state()).pro,true);assert.equal((await state()).cloud,null);
  await go('upgrade','activation');await capture('upgrade','activation');
  await go('upgrade','manage');await click('Cancel renewal');assert.equal((await state()).pro,true);assert.equal((await state()).cancellation,true);await capture('upgrade','manage');
  await launch();await go('upgrade','downgrade');await click('Apply fixture expiry');assert.equal((await state()).cloud.status,'stopping');assert.deepEqual((await state()).localDeviceGrants,['fixture-local-wing']);await capture('upgrade','downgrade');
  await reset('free');
  const paymentBase=applyPaidAction(createPaidState(),{op:'checkout',requestId:'checkout-1'});
  const stage=async value=>{await page.evaluate(value=>sessionStorage.setItem('wingthing-dormant-paid-fixture-v1',JSON.stringify({state:value,profile:'free'})),value);await page.reload();await page.locator('#paid-root').waitFor();};
  for(const outcome of ['pending','declined','abandoned']) {
   const value=outcome==='pending'?paymentBase:applyPaidAction(paymentBase,{op:'payment',checkoutId:'checkout-1',eventId:outcome,outcome});
   await stage(value);await go('upgrade','payment-pending');
   if(outcome==='pending'){await click('Check payment');assert.equal((await state()).checkout.id,'checkout-1');assert.equal((await state()).paymentEvents.length,0);}
   assert.equal((await state()).pro,false);assert.equal((await state()).cloud,null);
   await page.screenshot({path:resolve(shots,`07-7-payment-${outcome}-${viewport.width}.png`),fullPage:true});
  }
  await stage(paymentBase);await go('upgrade','payment-pending');await capture('upgrade','payment-pending');
  assert.equal(await page.locator('a[href="#paid/upgrade/owner"]').count(),0);await reset('operator');await go('upgrade','owner');await click('Record fixture owner grant');assert.equal((await state()).audit.at(-1).reason,'Owner dogfood fixture');await capture('upgrade','owner');
  await reset('active');await go('cloud','terminal');await page.locator('[name="task"]').fill('Review pairing from terminal');await click('Save terminal-origin draft');assert.equal((await state()).cloud,null);await capture('cloud','terminal');
  await go('cloud','dashboard');await page.locator('[name="task"]').fill('Review pairing and open a PR');await click('Save dashboard draft');assert.equal((await state()).draft.task,'Review pairing and open a PR');await capture('cloud','dashboard');
  await go('cloud','phone');await reset('unapproved-phone');await click('Send fixture cloud request');assert.equal((await state()).phoneRequest.status,'awaiting-owner-approval');await reset('phone');await click('Send fixture cloud request');assert.equal((await state()).phoneRequest.status,'queued-for-coordinator');assert.equal((await state()).cloud,null);await capture('cloud','phone');
  await reset('active');await go('cloud','size');await click('Choose Large');assert.equal((await state()).draft.size,'large');await capture('cloud','size');
  await go('cloud','budget');await page.locator('[name="hours"]').fill('2');await page.locator('[name="capUSD"]').fill('5.00');assert.match(await page.locator('[data-cap-preview]').innerText(),/\$5\.00 USD/);await click('Save cloud budget');assert.equal((await state()).draft.capCents,500);await capture('cloud','budget');
  await launch();await capture('cloud','launch');
  await go('cloud','workspace');assert.equal((await state()).cloud.volumeId,'fixture-volume-1');await capture('cloud','workspace');
  await go('cloud','repository');await page.locator('[name="repository"]').selectOption('sample/docs');await page.locator('[name="branch"]').fill('cloud/review-docs');await page.screenshot({path:resolve(shots,`10-8-before-authorize-${viewport.width}.png`),fullPage:true});await click('Authorize selected fixture repository');assert.equal((await state()).draft.repository,'sample/docs');await capture('cloud','repository');
  await go('cloud','git');await click('Record fixture clone');await click('Prepare review-branch PR');assert.match((await state()).draft.gitStatus,/fixture PR ready/);await capture('cloud','git');
  await go('cloud','vault');await page.locator('.paid-tile').filter({hasText:'DEPLOY_TOKEN'}).getByRole('button').click();assert.equal((await state()).draft.vault.DEPLOY_TOKEN,true);assert.equal(await page.locator('#paid-root input').count(),0);await capture('cloud','vault');
  await go('cloud','model');await page.locator('[name="agent"]').selectOption('codex');await click('Confirm BYO model fixture');assert.equal((await state()).draft.agent,'codex');await capture('cloud','model');
  await go('cloud','network');await click('Approve extra fixture destination');assert.ok((await state()).draft.network.includes('api.example.com'));await capture('cloud','network');
  await go('cloud','tools');await click('Record reviewed Rust image step');assert.ok((await state()).draft.tools.includes('Rust (reviewed image step)'));await capture('cloud','tools');
  await go('cloud','attach');await click('Confirm separate fixture cloud pairing');await click('Attach exact fixture session');assert.equal((await state()).cloud.attached,true);await capture('cloud','attach');
  await go('cloud','meter');await click('Record 18 ready minutes');assert.equal((await state()).cloud.elapsedSeconds,1080);await capture('cloud','meter');
  await go('cloud','idle');await click('Simulate idle warning');await click('Check idle policy');assert.match((await state()).message,/Idle warning/);await capture('cloud','idle');
  await click('Keep working');assert.equal((await state()).cloud.idleMinutes,0);await click('Simulate 15 minutes idle');await click('Check idle policy');await confirmStop();
  await go('cloud','resume');await click('Consent to disk-backed restart');for(let i=0;i<3;i++)await click('Advance verified readiness');assert.equal((await state()).cloud.previousRunId,'fixture-run-1');assert.equal((await state()).cloud.status,'ready');await capture('cloud','resume');
  await go('cloud','results');await click('Grant fixture preview to Alice');assert.equal((await state()).cloud.previewRecipient,'Alice');
  let downloadPromise=page.waitForEvent('download');await click('Download fixture artifacts');assert.equal((await downloadPromise).suggestedFilename(),'dormant-fixture-artifacts.json');await capture('cloud','results');
  await go('cloud','cleanup');await click('Stop compute');await click('Confirm fixture shutdown');await click('Prepare credential-free export');await click('Review workspace deletion');assert.ok((await state()).exportManifest.excludes.includes('vault values'));await capture('cloud','cleanup');
  await page.locator('[name="volumeId"]').fill('wrong-volume');assert.equal(await page.getByRole('button',{name:'Delete fixture workspace',exact:true}).isDisabled(),true);await page.screenshot({path:resolve(shots,`10-19-delete-empty-${viewport.width}.png`),fullPage:true});await page.locator('[name="volumeId"]').fill('fixture-volume-1');assert.equal(await page.getByRole('button',{name:'Delete fixture workspace',exact:true}).isDisabled(),false);await page.screenshot({path:resolve(shots,`10-19-delete-match-${viewport.width}.png`),fullPage:true});await click('Delete fixture workspace');assert.equal((await state()).cloud.status,'deleted');
  await reset('active');await go('cloud','launch');await click('Create fixture workspace');await go('cloud','launch-failed');await click('Fail allocation');assert.equal((await state()).cloud.status,'failed');await capture('cloud','launch-failed');
  await click('Reconcile and retry original allocation');await go('cloud','launch');for(let i=0;i<3;i++)await click('Advance verified boot step');assert.equal((await state()).cloud.status,'ready');
  await go('cloud','recovery');await click('Simulate disk');await click('Simulate network');await click('Simulate restore');await capture('cloud','recovery');await click('Consent to cold recovery');assert.equal((await state()).fault,null);
  await go('cloud','limits');await click('Try a second launch');assert.match(await page.locator('#paid-feedback').innerText(),/resume|live VM/);await capture('cloud','limits');
  await go('cloud','cleanup');await click('Stop compute');await click('Confirm fixture shutdown');
  await go('billing','usage');await page.locator('[name="capUSD"]').fill('5.00');assert.match(await page.locator('[data-cap-preview]').innerText(),/\$5\.00 USD/);await click('Save account cap');assert.equal((await state()).accountCapCents,500);await capture('billing','usage');
  await go('billing','invoices');await click('Finalize settled fixture invoice');assert.equal((await state()).invoice.status,'finalized');
  downloadPromise=page.waitForEvent('download');await click('Download fixture invoice');const invoice=await downloadPromise;assert.equal(invoice.suggestedFilename(),'dormant-fixture-invoice.json');const invoiceJSON=JSON.parse(await readFile(await invoice.path(),'utf8'));assert.equal(invoiceJSON.fixture,true);await capture('billing','invoices');
  await go('billing','notifications');await page.locator('[data-preference="attention"]').check();await click('Prepare support report');assert.equal((await state()).notifications.attention,true);assert.ok(!JSON.stringify((await state()).report).includes('DEPLOY_TOKEN'));await capture('billing','notifications');
  await stage(applyPaidAction(await state(),{op:'billing-fail',invoiceId:'fixture-invoice-1'}));await go('billing','payment-failed');assert.equal((await state()).billingBlocked,true);await capture('billing','payment-failed');await click('Update payment method');assert.equal((await state()).accountCapCents,500);
  await go('billing','reconcile');await click('Mark meter stale');assert.equal((await state()).meterFresh,false);await capture('billing','reconcile');await click('Record fixture reconciliation');await click('Deduplicate finalized invoice');assert.equal((await state()).invoiceEvents.length,1);
  await go('teams','later');await page.locator('[name="role"]').selectOption('phone');await page.locator('[name="resource"]').selectOption('billing');await click('Review fixture resource access');assert.match((await state()).teamDecision,/denied/);await capture('teams','later');
  // The dormant route survives reload and browser back without entering terminal lifecycle.
  await page.reload();await page.locator('#paid-root').waitFor();assert.match(await page.locator('h1').innerText(),/Orgs/);
  await go('billing','reconcile');await page.goBack();await page.locator('h1').filter({hasText:'Orgs'}).waitFor();
  await context.close();
 }
 if(defaultBase){
  const context=await browser.newContext({viewport:{width:1440,height:900}});const page=await context.newPage();
  await page.goto(`${defaultBase}/app/#paid/upgrade/price`);await page.locator('#user-info').filter({hasText:'local'}).waitFor();
  assert.equal(await page.locator('#paid-root').count(),0);assert.equal(await page.locator('#paid-fixture-entry').count(),0);
  const me=await (await context.request.get(`${defaultBase}/api/app/me`)).json();assert.equal(me.paid_fixture,false);
  await page.screenshot({path:resolve(shots,'paid-default-off.png')});await context.close();
 }
 assert.deepEqual(errors,[],'no runtime browser errors');assert.deepEqual(unexpected,[],'no outward requests');
 assert.equal(screenshotMap.size,36);
 const jsonl=resolve(shots,'paid.jsonl');if(existsSync(jsonl))await copyFile(jsonl,jsonl+'.bak-1012-'+new Date().toISOString().replace(/[^0-9]/g,'').slice(8,12));
 await writeFile(jsonl,'');
 for(const {card,desktop,mobile,desktopFull,mobileFull} of screenshotMap.values()){
  assert.ok(desktop&&mobile);await appendFile(jsonl,JSON.stringify({flow:card.flow,card:card.card,title:card.title,status:'dormant',png:desktop,mobile_png:mobile,full_png:desktopFull || null,mobile_full_png:mobileFull || null,surface:'web',test:`web/test/paid/model.test.js (${String(card.flow).padStart(2,'0')}-${card.card}); make test-paid-ui`,commit,notes:'Real built wt local SPA; default-OFF development fixture; proposed prices; no payment/account/provider/VM calls. Phone viewport is web, not native iPhone.'})+'\n');
 }
 const supplementalScreenshots=[...screenshotMap.values()].reduce((n,v)=>n+Number(!!v.mobileFull)+Number(!!v.desktopFull),0);
 await writeFile(resolve(shots,'paid-browser-receipt.json'),JSON.stringify({commit,binarySha256,supplementalScreenshots,sourceThrough:'2026-10-11 storyboards; current fixture source',generatedAt:new Date().toISOString(),cards:36,screenshots:108,viewports:[{width:1440,height:900},{width:390,height:844},{width:430,height:932}],defaultOffChecked:!!defaultBase,errors,unexpected,status:'passed'},null,2)+'\n');
 const gallery=resolve(shots,'paid.html');if(existsSync(gallery))await copyFile(gallery,gallery+'.bak-1012-'+new Date().toISOString().replace(/[^0-9]/g,'').slice(8,12));
 await writeFile(gallery,`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Dormant paid UI review</title><style>body{font:15px/1.5 system-ui;background:#101713;color:#deeadf;max-width:1400px;margin:auto;padding:24px}a{color:#a5edbe}section{margin:24px 0;padding:20px;background:#19231c;border:1px solid #3c5142;border-radius:12px}img{max-width:100%;border:1px solid #3c5142}figure{margin:0}.shots{display:grid;grid-template-columns:3fr 1fr;gap:16px}small{color:#afc3b5}@media(max-width:700px){.shots{grid-template-columns:1fr}}</style><h1>36 dormant paid cards</h1><p>Real local wt build · 1440×900 and 390×844 · no money, accounts, providers or VMs.</p><p>Candidate <code>${commit}</code> · binary SHA256 <code>${binarySha256}</code></p><p>Fixture source through 2026-10-12T21:00:00Z; generated ${new Date().toISOString()}. Native iPhone / live payment / production cloud acceptance remain unrun.</p>${[...screenshotMap.values()].map(({card,desktopFull,mobileFull})=>`<section><h2>${escape(card.title)}</h2><p><small>Flow ${card.flow} · card ${card.card} · dormant · unit and Chromium journey passed</small></p><div class="shots"><figure><a href="${file(card)}"><img loading="lazy" src="${file(card)}" alt="${escape(card.title)} desktop"></a>${desktopFull?`<p><a href="${file(card).replace('.png','-full.png')}">Complete desktop page</a></p>`:''}</figure><figure><a href="${file(card,true)}"><img loading="lazy" src="${file(card,true)}" alt="${escape(card.title)} mobile web"></a>${mobileFull?`<p><a href="${file(card,true).replace('.png','-full.png')}">Complete mobile page</a></p>`:''}</figure></div></section>`).join('')}</html>`);
 console.log(JSON.stringify({status:'passed',cards:36,screenshots:108,supplementalScreenshots,defaultOffChecked:!!defaultBase,commit,manifest:jsonl}));
}finally{await browser.close();}

}
if (!process.env.NODE_TEST_CONTEXT) await runPaidUI();

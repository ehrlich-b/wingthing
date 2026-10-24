import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { filterSessionInventory } from '../src/session-inventory.js';

async function run() {
 const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
 const require = createRequire(resolve(root, 'package.json'));
 const { build } = require('esbuild');
 const { chromium } = require('playwright');
 const snapshots = JSON.parse(readFileSync(process.env.WT_SHELL_SNAPSHOTS, 'utf8'));
 const output = process.env.WT_SHELL_SHOTS;
 assert.ok(output, 'retain screenshots outside the checkout');
 mkdirSync(output, { recursive: true });
 const fixture = await build({ entryPoints: [resolve(root, 'test/support/presence-fixture.js')], bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
 const origin = 'http://shell-fixture.localhost';
 const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
 const normalize = rows => rows.map(row => ({...row, wing_id:'machine-0', swept:true, lifecycle:{status:row.status,state:row.state,state_source:row.state_source}}));
 for (const [phase, expected] of [['waiting',3],['acknowledged',2],['finished',0]]) assert.equal(filterSessionInventory(normalize(snapshots[phase]),[{wing_id:'machine-0',online:true}],{},{status:'attention'}).length, expected);
 try {
  for (const width of [390,1440]) {
   for (const count of [0,1,8,12]) {
    const rows = Array.from({length:count},(_,i)=>({...snapshots.waiting[i%3],id:'volume-shell-'+i,name:'release-compatibility-customer-migration-validation-task-'+(i+1)}));
    const context = await browser.newContext({viewport:{width,height:width===390?844:1000}});
    await context.addInitScript({content:'window.presenceCount=1; window.shellRows='+JSON.stringify(rows)+';\n'+fixture.outputFiles[0].text});
    const page = await context.newPage(), errors=[];
    page.on('pageerror',e=>errors.push(e.message));
    page.setDefaultTimeout(20000);
    await context.route('**/*', async route => {
     const url=new URL(route.request().url());assert.equal(url.origin,origin);
     const json=body=>route.fulfill({contentType:'application/json',body:JSON.stringify(body)});
     if(url.pathname==='/api/app/me')return json({id:'fixture-owner',display_name:'You',relay_allowed:true,has_passkeys:true});
     if(url.pathname==='/api/app/passkey')return json([{credential_id:'Zml4dHVyZQ'}]);
     if(url.pathname==='/api/app/wings')return json(await page.evaluate(()=>presenceFixture.discovery()));
     const relative=url.pathname==='/app/'?'index.html':url.pathname.replace(/^\/app\//,'');
     const path=resolve(root,'dist',relative);
     if(path.startsWith(resolve(root,'dist')+'/')&&existsSync(path))return route.fulfill({body:readFileSync(path),contentType:relative.endsWith('.js')?'text/javascript':relative.endsWith('.css')?'text/css':relative.endsWith('.html')?'text/html':'image/svg+xml'});
     throw Error('Unexpected resource '+url.pathname);
    });
    await page.goto(origin+'/app/#sessions');
    await page.waitForFunction(()=>presenceFixture.calls.some(c=>c.type==='sessions.list'));
    if(count) {
     await page.locator('.pp-group').filter({hasText:'Needs you'}).first().waitFor();
     assert.equal(await page.locator('.pp-srow:visible').count(),Math.min(count,8));
     assert.match(await page.locator('.pp-srow').first().innerText(),/Needs you · reported/);
     if(count>8)await page.getByText('+4 more',{exact:true}).waitFor();
    }
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'no horizontal overflow');
    await page.screenshot({path:resolve(output,`shell-${count}-${width}.png`),fullPage:true});
    if(count===12) {
     await page.locator('#pp-filter').fill('task-12');
     assert.equal(await page.locator('.pp-srow:visible').count(),1,'search reveals matching overflow row');
     await page.locator('#pp-filter').fill('');
     await page.locator('.pp-more-sessions').evaluate(el=>el.open=false);
     await page.getByText('+4 more',{exact:true}).click();
     assert.equal(await page.locator('.pp-srow:visible').count(),12);
     // Replay actual CLI-generated inventory snapshots through the existing
     // encrypted sessions.list transport, one refresh per phase.
     for(const [phase,expected] of [['waiting',3],['acknowledged',2],['finished',0]]) {
      const before=await page.evaluate(()=>presenceFixture.calls.filter(c=>c.type==='sessions.list').length);
      await page.evaluate(rows=>presenceFixture.setShellRows(rows),snapshots[phase]);
      await page.waitForFunction(n=>presenceFixture.calls.filter(c=>c.type==='sessions.list').length>n,before);
      await page.waitForFunction(expected=>document.querySelectorAll('.pp-srow .pp-st-need').length===expected,expected);
      assert.equal(await page.evaluate(()=>presenceFixture.calls.filter(c=>c.type==='sessions.list').length),before+1,'one inventory refresh');
      await page.screenshot({path:resolve(output,`transition-${phase}-${width}.png`),fullPage:true});
     }
    }
    assert.deepEqual(errors,[]);
    await context.close();
    console.log(JSON.stringify({width,count,oneRefresh:true,overflow:false}));
   }
  }
 } finally {await browser.close();}
}
if (!process.env.NODE_TEST_CONTEXT) await run();

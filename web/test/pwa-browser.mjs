import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createRequire } from 'node:module';
import { x25519 } from '@noble/curves/ed25519.js';

async function run() {
    const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
    const require = createRequire(resolve(root, 'package.json'));
    const { build } = require('esbuild');
    const fixture = await build({entryPoints:[resolve(root,'test/support/pwa-fixture.js')],bundle:true,write:false,format:'iife',platform:'browser',logLevel:'silent'});
    const publicKey = Buffer.from(x25519.getPublicKey(new Uint8Array(32).fill(37))).toString('base64');
    const types = { '.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml','.png':'image/png','.json':'application/json','.html':'text/html','.woff2':'font/woff2' };
    let fixtureOffline = false;
    const server = createServer((request,response) => {
        // Browser offline emulation can leave the worker transport online.
        // Drop the real fixture transport too, so navigation must use fallback.
        if (fixtureOffline) { response.destroy(); return; }
        const path = new URL(request.url,'http://localhost').pathname;
        response.setHeader('Cache-Control','no-store');
        const json = value => {response.setHeader('Content-Type','application/json');response.end(JSON.stringify(value));};
        if (path==='/api/app/me') return json({id:'fixture-owner',display_name:'You',relay_allowed:true,has_passkeys:true,release_channel:'preview'});
        if (path==='/api/app/wings') return json(['machine-a','machine-b'].map(wing_id=>({wing_id,public_key:publicKey,user_id:'fixture-owner',owner:'You',online:true})));
        const relative = path==='/app/'||path==='/' ? 'index.html' : path.replace(/^\/app\//,'');
        const file = resolve(root,'dist',relative);
        if (!file.startsWith(resolve(root,'dist')+'/') || !existsSync(file)) {response.writeHead(404);response.end();return;}
        response.setHeader('Content-Type',types['.'+relative.split('.').at(-1)]||'application/octet-stream');
        response.end(readFileSync(file));
    });
    await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
    const base=`http://127.0.0.1:${server.address().port}`;
    const output=resolve(process.env.WT_PWA_SHOTS || resolve(root,'../.scratch/pwa-shots'));
    mkdirSync(output,{recursive:true});
    const { chromium }=await import(pathToFileURL(resolve(process.env.WT_PLAYWRIGHT_MODULE)).href);
    const browser=await chromium.launch({headless:true,executablePath:process.env.WT_CHROMIUM_EXECUTABLE||undefined,args:['--disable-background-networking','--renderer-process-limit=2']});
    const results=[];
    try {
        for (const viewport of [{width:390,height:844},{width:430,height:932},{width:1440,height:900}]) {
            const context=await browser.newContext({viewport,hasTouch:viewport.width<600,timezoneId:'America/New_York'});
            await context.addInitScript({content:fixture.outputFiles[0].text});
            const page=await context.newPage(),errors=[];
            page.on('pageerror',error=>{errors.push(error.message);console.log('Browser error: '+error.message);});
            page.on('console', message=>{if(message.type()==='error') console.log('Browser console: '+message.text());});
            await page.goto(base+'/app/');
            console.log('Boot '+viewport.width);
            await page.locator('#coordinator-home .thread-bubble').filter({hasText:'The test suite passed.'}).waitFor();
            await page.locator('.fleet-machine').filter({hasText:'Studio'}).waitFor();
            assert.equal(await page.locator('.search-session').count(),2,'qualified same IDs merge only by origin');
            assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
            console.log('Rendered '+viewport.width);
            await page.waitForFunction(()=>navigator.serviceWorker.controller,{},{timeout:15000});
            console.log('Worker active '+viewport.width);
            await page.reload();
            await page.waitForFunction(()=>Boolean(navigator.serviceWorker.controller));
            const manifest=await page.evaluate(async()=>await (await fetch('/app/manifest.json')).json());
            assert.equal(manifest.scope,'/app/');assert.equal(manifest.display,'standalone');
            await page.locator('#coordinator-home .thread-bubble').filter({hasText:'The test suite passed.'}).waitFor();
            await page.screenshot({path:resolve(output,`pwa-ready-${viewport.width}x${viewport.height}.png`)});
            assert.deepEqual(await page.evaluate(()=>caches.keys()),[],'private app data is never put in Cache Storage');
            const callCount=await page.evaluate(()=>coordinatorFixture.calls.length);
            fixtureOffline = true;
            await context.setOffline(true);
            await page.locator('#pwa-connection').waitFor({state:'visible'});
            await page.reload({waitUntil:'domcontentloaded'});
            await page.getByRole('heading',{name:'You’re disconnected'}).waitFor();
            assert.equal(await page.locator('form,textarea').count(),0,'offline navigation has no mutation controls');
            await page.screenshot({path:resolve(output,`pwa-offline-${viewport.width}x${viewport.height}.png`)});
            fixtureOffline = false;
            await context.setOffline(false);
            await page.getByRole('button',{name:'Try again'}).click();
            await page.locator('#coordinator-home .thread-bubble').filter({hasText:'The test suite passed.'}).waitFor();
            assert.deepEqual(await page.evaluate(()=>caches.keys()),[]);
            await page.evaluate(()=>{window.pwaInventoryFailure=true;});
            await page.locator('#session-search-form button').click();
            await page.waitForFunction(()=>document.getElementById('session-search-status').textContent.includes('coverage incomplete'));
            assert.equal(await page.locator('#session-search-results').isVisible(),true);
            assert.equal(await page.locator('.search-session').count(),0);
            await page.screenshot({path:resolve(output,`pwa-inventory-unavailable-${viewport.width}x${viewport.height}.png`)});
            await page.evaluate(()=>{window.pwaInventoryFailure=false;});
            await page.locator('#session-search-form button').click();
            await page.waitForFunction(()=>document.querySelectorAll('.search-session').length===2);
            assert.deepEqual(errors,[]);
            results.push({viewport,manifest:true,workerControls:true,offlineConstantPage:true,recovery:true,cacheEntries:0,encryptedFixtureCalls:callCount});
            await context.close();
        }
    } finally { await browser.close(); await new Promise(resolve=>server.close(resolve)); }
    writeFileSync(resolve(output,'pwa-receipt.json'),JSON.stringify({build:'web/dist',transport:'real HTTP server; encrypted fixture wing peers',results},null,2)+'\n');
    console.log('PWA browser PASS '+JSON.stringify(results));
}
if (!process.env.NODE_TEST_CONTEXT) await run();

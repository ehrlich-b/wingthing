import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

async function run() {
    const [base, fixture, localOrigin, remoteOrigin, sessionID] = process.argv.slice(2);
    assert.equal(process.env.WT_TAKEOVER_EXPECT_REAL_ROOST, '1');
    assert.ok(base.startsWith('http://127.0.0.1:'));
    const { chromium } = await import(pathToFileURL(resolve(process.env.WT_PLAYWRIGHT_MODULE)).href);
    const shots = process.env.WT_TAKEOVER_SHOTS || resolve('.scratch/takeover-shots');
    mkdirSync(shots, { recursive: true });
    const browser = await chromium.launch({ headless: true, executablePath: process.env.WT_CHROMIUM_EXECUTABLE || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    try {
        const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        await page.route('**/*', route => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
        let holdNext = false, held = null, captured = null, captureNext = false;
        let heldReady, heldAnswered, replayAnswered;
        let capturedReplies = 0;
        await page.routeWebSocket(base.replace('http:', 'ws:') + '/ws/relay?*', route => {
            const server = route.connectToServer();
            route.onMessage(message => {
                const wire = typeof message === 'string' ? JSON.parse(message) : {};
                if (wire.type === 'tunnel.req' && holdNext) { holdNext = false; held = { server, message, id: wire.request_id }; heldReady?.(); return; }
                if (wire.type === 'tunnel.req' && captureNext) { captureNext = false; captured = { server, message, id: wire.request_id }; capturedReplies = 0; }
                server.send(message);
            });
            server.onMessage(message => {
                route.send(message);
                const wire = typeof message === 'string' ? JSON.parse(message) : {};
                if (held && wire.request_id === held.id) heldAnswered?.();
                if (captured && wire.request_id === captured.id && ++capturedReplies === 2) replayAnswered?.();
            });
        });
        await page.goto(base);
        await page.waitForFunction(() => document.querySelectorAll('.wing-box').length === 2);
        const search = async () => {
            await page.locator('#session-search-input').fill('DNS helper');
            await page.locator('#session-search-form button').click();
            await page.waitForFunction(() => document.querySelectorAll('.search-session').length === 2);
        };
        const liveInventory = page.locator(`.search-session[data-origin="${remoteOrigin}"][data-session="${sessionID}"]`);
        await liveInventory.waitFor({ state: 'visible' });
        await page.locator('#coordinator-home .thread-bubble').filter({hasText:'The DNS helper is ready.'}).waitFor();
        for (const viewport of [{width:390,height:844},{width:430,height:932},{width:1440,height:900}]) {
            await page.setViewportSize(viewport);
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
            assert.equal(await page.locator('.fleet-machine').count(),2);
            assert.ok((await liveInventory.textContent()).includes('via Laptop'));

            const position = await page.evaluate(() => ({pin:document.getElementById('coordinator-home').getBoundingClientRect().top, other:document.getElementById('conversation-inventory').getBoundingClientRect().top,otherHidden:getComputedStyle(document.getElementById('conversation-inventory')).display==='none'}));
            assert.ok(position.otherHidden || position.pin < position.other, 'pinned thread renders above coordinator creation after refresh');
            assert.doesNotMatch(await page.locator('#coordinator-home').innerText(),/Heartbeat report is stale/);assert.notEqual(await page.locator('#coordinator-home [data-presence]').getAttribute('data-presence'),'unknown');await page.screenshot({path:resolve(shots, `pwa-home-${viewport.width}x${viewport.height}.png`)});
            await page.locator('#session-search-form').scrollIntoViewIfNeeded();
            await page.screenshot({path:resolve(shots, `pwa-inventory-${viewport.width}x${viewport.height}.png`)});
            await page.locator('#coordinator-home').scrollIntoViewIfNeeded();

        }

        await page.locator('#session-search-form').evaluate(form=>window.scrollTo(0,form.getBoundingClientRect().top+scrollY-12));
        assert.equal(await page.locator('#session-viewer').isVisible(),false);
        const groups=await page.locator('.fleet-machine').allTextContents();assert.equal(groups.length,2);
        const inventoryBounds=await page.locator('.session-search').boundingBox();for(const row of await page.locator('.search-session').all()){const box=await row.boundingBox();assert.ok(box&&box.y>=inventoryBounds.y&&box.y+box.height<=inventoryBounds.y+inventoryBounds.height,'both inventory rows fit the focused capture');}
        const inventoryText=await page.locator('.session-search').innerText();assert.doesNotMatch(inventoryText,/\.scratch|\/work\/lanes|Matched:/);assert.match(inventoryText,/dns-helper/);assert.doesNotMatch(inventoryText,new RegExp(localOrigin+'|'+remoteOrigin+'|'+sessionID));
        await page.locator('.session-search').screenshot({ path: resolve(shots, '13-5-takeover-inventory-desktop.png') });
        await liveInventory.getByRole('button', { name: 'Open session', exact: true }).click();
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Desktop DNS helper is ready'));
        assert.equal(await page.locator('#session-viewer [data-route]').textContent(), 'direct');
        await page.locator('#session-viewer [data-close]').click();
        await search();
        const rows = page.locator('.search-session');
        const target = rows.filter({ hasText: remoteOrigin });
        assert.equal(await rows.count(), 2);
        assert.equal(await target.count(), 1);
        assert.ok((await target.textContent()).includes('via Laptop'));
        assert.ok((await target.textContent()).includes('direct'));
        await target.getByRole('button', { name: 'Open session', exact: true }).click();
        const dialog = page.locator('#session-viewer');
        await dialog.waitFor({ state: 'visible' });
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Desktop DNS helper is ready'));
        assert.ok((await dialog.locator('[data-identity]').textContent()).includes(remoteOrigin + ' / ' + sessionID));
        assert.equal(await dialog.locator('[data-route]').textContent(), 'direct');
        await page.screenshot({ path: resolve(shots, '13-5-takeover-transcript-desktop.png') });
        await page.setViewportSize({ width: 430, height: 932 });
        await page.screenshot({ path: resolve(shots, 'pwa-transcript-430x932.png') });
        await page.setViewportSize({ width: 390, height: 844 });
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-transcript-mobile.png') });
        await dialog.locator('#viewer-message').fill('Private unsent remote draft');
        await dialog.locator('[data-close]').click();
        await rows.filter({ hasText: localOrigin }).getByRole('button', { name: 'Open session', exact: true }).click();
        await page.waitForFunction(origin => document.querySelector('[data-identity]').textContent.includes(origin), localOrigin);
        assert.equal(await dialog.locator('#viewer-message').inputValue(), '');
        await dialog.locator('[data-close]').click();
        await target.getByRole('button', { name: 'Open session', exact: true }).click();
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Desktop DNS helper'));
        await dialog.locator('#viewer-message').fill('Check the new DNS record first.');
        await dialog.locator('[data-send]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent === 'Message appeared in session');
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Checked the DNS helper: Check the new DNS record first.'));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-message-mobile.png') });
        async function messageFrame(kind) {
            for(const viewport of [{width:390,height:844},{width:430,height:932},{width:1440,height:900}]){
                await page.setViewportSize(viewport);await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));await dialog.locator('[data-latest]').evaluate(button=>{if(!button.hidden)button.click();});
                const send=await dialog.locator('[data-send]').boundingBox();assert.ok(send&&send.y>=0&&send.y+send.height<=viewport.height,'Send message stays in viewport');
                const box=await dialog.locator('[data-viewer-scroll]').boundingBox(), latest=await dialog.locator('[data-transcript] .thread-bubble').last().boundingBox();
                assert.ok(latest&&box&&latest.y>=box.y&&latest.y+latest.height<=box.y+box.height+1,'latest reply stays visible '+JSON.stringify({viewport,box,latest}));
                await page.screenshot({path:resolve(shots,`13-5-takeover-${kind}-${viewport.width}.png`)});
            }
        }
        await messageFrame('message');await page.setViewportSize({width:390,height:844});
        await dialog.locator('[data-tab="terminal"]').click();
        await dialog.locator('[data-take]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent === 'You control terminal input');
        await dialog.locator('[data-input]').fill('Old delayed terminal input');
        const paused = new Promise(resolve => { heldReady = resolve; });
        // Pause background browser reads so the held encrypted request is the input under test.
        await page.evaluate(()=>Object.defineProperty(document,'visibilityState',{configurable:true,value:'hidden'}));
        await page.waitForFunction(()=>!document.querySelector('[data-terminal-send]').disabled);
        holdNext = true;
        await dialog.locator('[data-terminal-send]').click();
        await paused;
        await dialog.locator('[data-close]').click();
        await target.getByRole('button', { name: 'Open session', exact: true }).click();
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Desktop DNS helper'));
        await dialog.locator('[data-tab="terminal"]').click();
        await dialog.locator('[data-take]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent === 'You control terminal input');
        assert.equal(await dialog.locator('[data-input]').inputValue(), '');
        await dialog.locator('[data-input]').fill('New selection terminal draft');
        const answered = new Promise(resolve => { heldAnswered = resolve; });
        held.server.send(held.message);
        await answered;
        await page.waitForTimeout(50);
        assert.equal(await dialog.locator('[data-input]').inputValue(), 'New selection terminal draft');
        assert.equal(await dialog.locator('[data-status]').textContent(), 'You control terminal input');
        captureNext = true;
        await dialog.locator('[data-input]').fill('Human takeover input');
        await dialog.locator('[data-terminal-send]').click();
        await page.waitForFunction(()=>document.querySelector('#session-viewer [data-status]').textContent.includes('Terminal input submitted')).catch(async error=>{await page.screenshot({path:resolve(shots,'input-read-failure.png')});throw Error(error.message+'; status: '+await dialog.locator('[data-status]').textContent());});
        await page.evaluate(()=>Object.defineProperty(document,'visibilityState',{configurable:true,value:'visible'}));
        await page.waitForFunction(() => document.querySelector('[data-terminal] .xterm-accessibility')?.textContent.includes('Human takeover input'));
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Human takeover input'));
        const replayed = new Promise(resolve => { replayAnswered = resolve; });
        captured.server.send(captured.message);
        await replayed;
        await page.evaluate(()=>Object.defineProperty(document,'visibilityState',{configurable:true,value:'visible'}));

        for(const width of [390,430]){await page.setViewportSize({width,height:width===390?844:932});await dialog.locator('[data-terminal-scroll]').waitFor({state:'visible'});await dialog.locator('[data-terminal-right]').click();assert.ok(await dialog.locator('[data-terminal]').evaluate(el=>el.scrollLeft>0));await dialog.locator('[data-terminal-left]').click();await page.screenshot({path:resolve(shots,`13-5-takeover-control-${width}.png`)});}
        await page.setViewportSize({width:390,height:844});await page.screenshot({ path: resolve(shots, '13-5-takeover-control-mobile.png') });
        await page.setViewportSize({ width: 1440, height: 900 });
        await page.screenshot({ path: resolve(shots, '13-5-takeover-control-desktop.png') });
        await dialog.locator('[data-back]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent.includes('Terminal input released'));
        assert.equal(await dialog.locator('[data-input]').isDisabled(), true);
        assert.equal(await dialog.locator('[data-take]').isVisible(), true);
        await page.waitForFunction(()=>document.querySelector('[data-terminal] .xterm-accessibility')?.textContent.includes('Human takeover input'));
        await page.waitForFunction(()=>document.querySelector('[data-terminal-status]').textContent.includes('Current read-only'));
        await page.waitForFunction(()=>document.querySelector('[data-terminal] .xterm-accessibility')?.textContent.includes('Human takeover input'));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-handback-desktop.png') });
        await dialog.locator('[data-close]').click();
        // Real direct transport is dropped; the same exact live egg is reachable via gateway.
        assert.equal((await fetch(fixture + '/direct-down', { method: 'POST' })).status, 204);
        await search();
        await target.getByRole('button', { name: 'Open session', exact: true }).click();
        await page.waitForFunction(() => document.querySelector('[data-route]').textContent === 'via Laptop');
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Human takeover input'));
        const artifact=dialog.locator('[data-artifact-path]');assert.equal(await artifact.isDisabled(),true);assert.match(await dialog.locator('[data-artifact-route]').textContent(),/direct connection to Desktop/);assert.equal(await dialog.locator('[data-artifact-refresh]').isVisible(),true);
        await page.screenshot({ path: resolve(shots, '13-5-takeover-gateway-desktop.png') });
        await artifact.scrollIntoViewIfNeeded();await page.screenshot({path:resolve(shots,'13-5-takeover-gateway-artifact-desktop.png')});
        await page.setViewportSize({ width: 390, height: 844 });
        await dialog.locator('[data-tab="terminal"]').click();
        await dialog.locator('[data-take]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent === 'You control terminal input');
        await dialog.locator('[data-input]').fill('Gateway takeover input');
        await dialog.locator('[data-terminal-send]').click();
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Gateway takeover input'));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-gateway-mobile.png') });
        await page.setViewportSize({width:430,height:932});await dialog.locator('[data-terminal-scroll]').waitFor({state:'visible'});await page.screenshot({path:resolve(shots,'13-5-takeover-gateway-430.png')});await page.setViewportSize({width:390,height:844});
        await dialog.locator('[data-back]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent.includes('Terminal input released'));
        await dialog.locator('[data-tab="transcript"]').click();
        await dialog.locator('#viewer-message').fill('Show a permission attention event.');
        await dialog.locator('[data-send]').click();
        await dialog.locator('[data-attention]').waitFor({ state: 'visible' });
        assert.ok((await dialog.locator('[data-attention]').textContent()).includes('Approval must be answered in terminal'));
        await page.waitForFunction(()=>document.querySelector('[data-transcript]').textContent.includes('Checked the DNS helper: Show a permission attention event.'));
        await messageFrame('attention');await page.setViewportSize({width:390,height:844});
        await page.screenshot({ path: resolve(shots, '13-5-takeover-attention-mobile.png') });
        await dialog.locator('[data-attention-terminal]').click();assert.equal(await dialog.locator('[data-terminal-panel]').isVisible(),true);
        await dialog.locator('[data-close]').click();
        await page.locator('#session-search-input').fill('');
        await page.waitForFunction(() => document.querySelectorAll('.search-session').length === 2);
        await page.locator('#coordinator-home .thread-bubble').filter({hasText:'Checked the DNS helper: Show a permission attention event.'}).waitFor();
        assert.ok((await page.locator('#coordinator-home').textContent()).includes('wing'));
        await page.screenshot({path:resolve(shots,'pwa-home-via-390x844.png')});
        assert.deepEqual(errors, []);
        console.log(JSON.stringify({ checks: ['session-list-entry', 'cross-session-draft-reset', 'delayed-input-selection-fence', 'encrypted-browser-input-replay-denied', 'two-qualified-origins', 'direct-via-merge', 'live-transcript', 'native-message-text-receipt', 'real-PTY-takeover', 'hand-back-keeps-agent', 'direct-down-via-up', 'gateway-PTY-input', 'honest-approval-gap', 'desktop-mobile-layout'], machines: 2, real_build: true, real_encrypted_tunnels: true, fixture_provider: true, fixture_ssh: true, native_iphone: false }));
    } finally { await browser.close(); }
}
if (!process.env.NODE_TEST_CONTEXT) await run();

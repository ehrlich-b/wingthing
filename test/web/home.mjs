// R4 acceptance through the existing look harness, real roost and encrypted wings.
import assert from 'node:assert/strict';
import fs from 'node:fs';

export async function runHome({pages, machines, sockets, machine, row, events, at, go, shoot, checked, base}) {
    const suffix = p => '-' + p.viewportSize().width;
    const verify = async (page, name, heading, primary) => {
        assert.equal(await page.locator('.pp-hdr h1').innerText(), 'Home');
        if (heading) assert.ok(await page.getByRole('heading', {name:heading,exact:true}).isVisible());
        if (primary) assert.ok(await page.getByRole(primary === 'Copy' || primary === 'Retry' ? 'button' : 'link', {name:primary,exact:true}).isVisible());
        const text = await page.locator('#paper').innerText();
        assert.doesNotMatch(text, /has not reported|\b0\b/i);
        assert.equal(await page.locator('.pp-strip').evaluate(e=>e.getBoundingClientRect().height), 32);
        assert.ok(await page.locator(page.viewportSize().width === 390 ? '.pp-tabs' : '.pp-side').isVisible());
        assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth));
        checked(name+suffix(page), {heading, primary, text});
        await shoot(page, name, suffix(page));
    };
    const loads = new Map(), inventories = new Map();
    for (const page of pages) {
        loads.set(page,0); inventories.set(page,0);
        page.on('framenavigated', frame=>{ if (frame===page.mainFrame()) loads.set(page,loads.get(page)+1); });
        page.on('request', req=>{if (new URL(req.url()).pathname==='/api/app/wings') inventories.set(page,inventories.get(page)+1);});
        await go(page,'#');
        await verify(page,'home-no-machine','Connect a machine','Copy');
        await page.evaluate(()=>{window.step2=document.querySelector('[data-k="step-2"]');Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:text=>{window.copied=text;return Promise.resolve();}}});});
        await page.getByRole('button',{name:'Copy',exact:true}).click();
        assert.equal(await page.evaluate(()=>window.copied),'curl -fsSL https://wingthing.ai/install.sh | sh\nwt login\nwt start');
    }
    const navigations = pages.map(p=>loads.get(p));
    machines.studio.rows=[];
    const connectedAt=Date.now(); await machine('studio');
    await Promise.all(pages.map(async (page,i)=>{
        await page.waitForFunction(()=>document.querySelector('[data-step-title="2"]')?.textContent==='Studio connected' && document.querySelector('[data-k="step-2"] .home-tick')?.textContent==='✓',null,{timeout:4900});
        const elapsed=Date.now()-connectedAt;
        assert.ok(elapsed<5000, 'Connected step exceeded five seconds: '+elapsed);
        assert.equal(loads.get(page),navigations[i]);
        assert.ok(await page.evaluate(()=>window.step2===document.querySelector('[data-k="step-2"]')));
        checked('home-live-connect'+suffix(page), {elapsedMs:elapsed,navigations:loads.get(page)});
        await verify(page,'home-machine-no-session','Studio connected','Start a session');
    }));
    machines.studio.rows=[row('migrate','migrate-sessions-table','claude','working','wingthing')];
    for (const page of pages) {
        await page.locator('.home-row').waitFor();
        await verify(page,'home-sessions','Working');
        assert.equal(await page.locator('.home-row').count(),1);
        assert.equal(await page.locator('#paper [data-composer]').count(),0);
        const setup=page.getByRole('link',{name:'Set one up',exact:true});
        assert.ok(await setup.isVisible());
        const href=await setup.getAttribute('href');
        assert.equal(href,'/docs#wing-setup');
        const docs=fs.readFileSync('internal/relay/templates/docs.html','utf8');
        assert.ok(docs.includes('id="'+href.split('#')[1]+'"'));
        checked('home-setup-anchor'+suffix(page),{href,anchorExists:true});
        await shoot(page,'home-volume-1',suffix(page));
    }
    // The pinned wing has its own composer and never counts as work or Recent.
    machines.studio.rows.push({...row('wing','wing','claude','working','wingthing'),pinned:true,role:'coordinator'});
    for (const status of ['working','idle']) {
        machines.studio.rows[1].lifecycle.status=status;
        for (const page of pages) {
            await go(page,'#');
            await page.locator('#paper [data-composer]').waitFor();
            assert.equal(await page.locator('.pp-meta').innerText(),'1 working');
            assert.equal(await page.locator('.pp-nav-sessions small').first().innerText(),'1');
            assert.equal(await page.locator('.home-row').count(),1);
            assert.equal(await page.locator('.home-row[data-k="studio/wing"]').count(),0);
            assert.equal(await page.locator('[data-k="recent"]').count(),0);
            checked('home-wing-excluded-'+status+suffix(page),'One work session, one composer, no wing work or Recent row');
        }
    }
    machines.studio.rows.push(row('redirect','fix-login-redirect','claude','blocked','wingthing'));
    // A fresh load reads the changed fixture without waiting for the normal 30 s poll.
    for (const page of pages) {
        // Hold the real encrypted session read until the initial card is measured.
        let release;
        const held=new Promise(resolve=>{release=resolve;});
        machines.studio.beforeSessionRead=id=>id==='redirect'?held:undefined;
        // Reload discards any read the other viewport received while this one ran.
        await page.reload();
        await page.locator('.home-need').waitFor();
        assert.equal(await page.locator('.home-need').count(),1);
        const command=page.locator('.home-need .home-commands');
        assert.equal(await command.count(),1);
        assert.equal(await command.getAttribute('aria-hidden'),'true');
        assert.equal(await command.textContent(),'');
        await page.getByRole('button',{name:'Answer',exact:true}).focus();
        const snapshot=()=>page.evaluate(()=>({
            boxes:[...document.querySelectorAll('.home-need, .home-need .pp-btn, .home-section > h2, .home-row')].map(el=>el.getBoundingClientRect().toJSON()),
            focus:document.activeElement.textContent,
        }));
        const before=await snapshot();
        await shoot(page,'home-request-loading',suffix(page));
        release();
        await page.waitForFunction(()=>document.querySelector('.home-need pre')?.textContent.includes('./scripts/deploy.sh staging'));
        assert.deepEqual(await snapshot(),before);
        assert.equal(await command.getAttribute('aria-hidden'),'false');
        // A longer, multiline reason must stay in the same command slot too.
        const longCommand='./scripts/'+('extended-workspace-migration/').repeat(20)+'deploy.sh\n--runInBand';
        events.redirect.push({...events.redirect.at(-1),sequence:6,reason:'Run '+longCommand});
        await page.evaluate(()=>{const now=Date.now;Date.now=()=>now()+9000;});
        await page.waitForFunction(()=>document.querySelector('.home-need pre')?.textContent.includes('extended-workspace-migration/'));
        assert.deepEqual(await snapshot(),before);
        assert.equal(await command.getAttribute('title'),longCommand);
        assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
        checked('home-request-loading-stable'+suffix(page),{before,after:await snapshot(),longMultilineCommand:true});
        await shoot(page,'home-request-loaded',suffix(page));
        events.redirect.pop();
        delete machines.studio.beforeSessionRead;
        const answer=page.getByRole('button',{name:'Answer',exact:true});
        assert.equal(await answer.count(),1);
        await page.locator('.home-need .home-commands').waitFor();
        await shoot(page,'home-blocked',suffix(page));
        await answer.click();
        await page.waitForFunction(()=>location.hash==='#session/studio/redirect' && document.querySelector('#paper .pp-live .xterm')?.contains(document.activeElement)).catch(async error=>{console.log('ANSWER-FOCUS',JSON.stringify(await page.evaluate(()=>({hash:location.hash,active:document.activeElement.tagName+'.'+document.activeElement.className,terminal:!!document.querySelector('.pp-live .xterm'),composer:!!document.querySelector('#pp-message')}))));throw error;});
        checked('home-answer-focus'+suffix(page),await page.evaluate(()=>({hash:location.hash,focus:document.activeElement.className,terminal:!!document.querySelector('#paper .pp-live').contains(document.activeElement)})));
    }
    // Realistic long names, 8+ rows, and the six-machine plan boundary.
    machines.studio.rows=[
        {...row('wing','wing','claude','idle','wingthing'),pinned:true,role:'coordinator'},
        row('migrate','migrate-sessions-table','claude','working','wingthing'),
        ...Array.from({length:8},(_,i)=>row('work-'+i,'Migrate organization invitation recovery and interrupted workspace settings '+(i+1),'claude','working','wingthing')),
        ...Array.from({length:7},(_,i)=>row('recent-'+i,'Review extended release notes for organization migration '+(i+1),'codex',i%2?'idle':'completed','wingthing')),
        row('redirect','fix-login-redirect','claude','blocked','wingthing'),
        ...Array.from({length:3},(_,i)=>row('need-'+i,'Confirm staged workspace migration for the extended organization '+(i+1),'claude','blocked','wingthing')),
    ];
    machines.studio.rows.find(s=>s.session_id==='work-0').name='Needs you copy audit for extended organization migrations';
    events['work-1']=[{sequence:1,type:'message',role:'assistant',text:'Reviewing the Needs you copy in the settings page',timestamp:at(1)}];
    // The plan boundary includes five machines that were connected previously.
    // Seed their persisted inventory exactly as the offline fixture does; only
    // Studio is awake, so this Home test does not exercise tunnel-pool capacity.
    const remembered = ['macbook','mini','research-1','research-2','research-3'].map(id=>({wing_id:id,hostname:'Research workstation for extended organization migrations '+id,last_seen:at(45),agents:['claude','codex']}));
    const openHome = async page => {
        await page.goto(base+'/app/');
        await page.waitForFunction(()=>document.querySelector('.pp-nav-sessions small')?.textContent==='20' && document.querySelector('.pp-nav-machines small')?.textContent==='6' && document.querySelector('#paper [data-composer]'));
    };
    for (const page of pages) {
        await page.evaluate(extra=>{const cache=JSON.parse(localStorage.getItem('wt_wings') || '[]');localStorage.setItem('wt_wings',JSON.stringify([...cache,...extra]));},remembered);
        await openHome(page);
        await page.locator('#paper [data-composer]').waitFor();
        assert.equal(await page.locator('.home-need').count(),3);
        assert.equal(await page.locator('[data-k="working"] .home-row').count(),8);
        assert.equal(await page.locator('[data-k="recent"] .home-row').count(),5);
        assert.equal(await page.locator('.pp-meta').innerText(),'9 working · 4 needs you');
        assert.equal(await page.locator('.home-row[data-k="studio/wing"]').count(),0);
        assert.ok(await page.locator('[data-home-needs]').isVisible());
        assert.equal(await page.locator('[data-home-needs]').innerText(),'+1 more');
        for (const height of await page.locator('.home-row').evaluateAll(rows=>rows.map(r=>r.getBoundingClientRect().height))) assert.equal(height,page.viewportSize().width===390?56:44);
        await verify(page,'home-volume-plan-limit-6','Needs you');
        await page.locator('[data-home-needs]').click();
        await page.locator('#pp-filter').waitFor();
        assert.equal(await page.evaluate(()=>location.hash),'#sessions?state=need');
        assert.equal(await page.locator('#pp-filter').inputValue(),'');
        await page.waitForFunction(()=>document.querySelector('.pp-srow[data-k="studio/work-1"] .pp-sline')?.textContent.includes('Needs you'));
        assert.equal(await page.locator('.pp-group:not([hidden])').count(),1);
        const waiting=['redirect','need-0','need-1','need-2'].map(id=>'studio/'+id).sort();
        const visible = () => page.locator('.pp-group:not([hidden]) .pp-srow:not([hidden])').evaluateAll(rows=>rows.map(r=>r.dataset.k).sort());
        assert.deepEqual(await visible(),waiting);
        await page.locator('#pp-filter').fill('staged');
        assert.deepEqual(await visible(),waiting.filter(id=>!id.endsWith('/redirect')));
        await page.locator('#pp-filter').fill('no-such-session');
        await page.getByRole('button',{name:'Clear search',exact:true}).click();
        assert.deepEqual(await visible(),waiting);
        await page.reload();
        await page.locator('.pp-group:not([hidden]) .pp-srow').first().waitFor();
        assert.deepEqual(await visible(),waiting);
        assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth));
        await shoot(page,'home-needs-filter',suffix(page));
        await page.getByRole('link',{name:'All sessions',exact:true}).click();
        await page.waitForFunction(()=>location.hash==='#sessions' && document.querySelectorAll('.pp-group:not([hidden]) .pp-srow:not([hidden])').length===20);
        checked('home-needs-overflow-filter'+suffix(page),{waiting,searchAndReload:true,allSessions:20,falseMatchesExcluded:['work-0 name','work-1 latest line']});
    }
    // Stability: no membership changes; new report lines/times every two seconds.
    // Both viewports sample the actual production DOM every 500 ms for 60 seconds.
    const previous = events.migrate.slice();
    const originalLines = new Map();
    for (const page of pages) {
        await openHome(page);
        await page.locator('#pp-message:not([disabled])').waitFor();
        await page.waitForTimeout(1200);
        originalLines.set(page, await page.locator('.home-row[data-k="studio/migrate"] .home-line').textContent());
        await page.evaluate(()=>{
            window.homeStable=[...document.querySelectorAll('[data-stable]')].map(el=>({el,box:JSON.stringify(el.getBoundingClientRect().toJSON())}));
            window.homeChanges=[];window.homeShift=0;window.homeSamples=0;
            const textarea=document.querySelector('#pp-message');textarea.value='Review this migration carefully';textarea.dispatchEvent(new Event('input',{bubbles:true}));textarea.focus();textarea.setSelectionRange(7,11);
            window.homeReader=textarea;window.homeScroll=document.querySelector('.pp-main');window.homeScroll.scrollTop=40;window.homeScrollTop=window.homeScroll.scrollTop;
            // Viewport coordinates account for the reader's deliberate scroll.
            window.homeStable.forEach(x=>x.box=JSON.stringify(x.el.getBoundingClientRect().toJSON()));
            window.homeObserver=new PerformanceObserver(list=>{for(const e of list.getEntries()) if(!e.hadRecentInput)window.homeShift+=e.value;});window.homeObserver.observe({type:'layout-shift'});
            window.homeSampler=setInterval(()=>{window.homeSamples++;for(const x of window.homeStable)if(JSON.stringify(x.el.getBoundingClientRect().toJSON())!==x.box)window.homeChanges.push(x.el.outerHTML.slice(0,100));},500);
        });
    }
    const counts=pages.map(p=>inventories.get(p));
    for(let i=0;i<30;i++) {
        events.migrate.push({sequence:10+i,type:'message',role:'assistant',text:'Reviewing workspace migration batch '+(i+1)+' with longer summary text',timestamp:at(i%2?2:1)});
        await pages[0].waitForTimeout(2000);
    }
    events.migrate=previous;
    for(const [i,page] of pages.entries()) {
        const stable=await page.evaluate(()=>{
            clearInterval(window.homeSampler);window.homeObserver.disconnect();
            const reader=window.homeReader;
            return {samples:window.homeSamples,changes:window.homeChanges,shift:window.homeShift,focus:document.activeElement===reader,selection:[reader.selectionStart,reader.selectionEnd],scroll:window.homeScroll.scrollTop,expectedScroll:window.homeScrollTop};
        });
        assert.ok(stable.samples>=120);assert.deepEqual(stable.changes,[]);assert.equal(stable.shift,0);assert.ok(stable.focus);assert.deepEqual(stable.selection,[7,11]);assert.equal(stable.scroll,stable.expectedScroll);
        // Ordinary Home polling is every 30 s. No onboarding cadence remains.
        assert.ok(inventories.get(page)-counts[i]<=3);
        const updatedLine = await page.locator('.home-row[data-k="studio/migrate"] .home-line').textContent();
        assert.notEqual(updatedLine, originalLines.get(page));
        assert.match(updatedLine, /Reviewing workspace migration batch/);
        checked('home-stable-60s'+suffix(page),{...stable,originalLine:originalLines.get(page),updatedLine});
    }
    // Disconnect actual fixture sockets; cached session rows must stay.
    for (const socket of sockets.values()) socket.close();
    await pages[0].waitForTimeout(500);
    for (const page of pages) {
        // Reload retains the browser's cached inventory while API reports offline.
        await page.goto(base+'/app/');
        await page.waitForFunction(()=>document.querySelector('.pp-strip')?.textContent.includes('No machines are connected. Sessions are kept.'));
        await verify(page,'home-offline','Working','Retry');
        assert.equal(await page.locator('.home-row').count(),13);
        assert.equal(await page.locator('.home-row:not(.home-offline)').count(),0);
        const offlineColors = await page.locator('.home-offline .pp-st,.pp-sq-offline i').evaluateAll(elements=>elements.map(el=>({color:getComputedStyle(el).color,border:getComputedStyle(el).borderColor})));
        for (const color of offlineColors) { assert.notEqual(color.color,'rgb(255, 138, 61)');assert.notEqual(color.border,'rgb(255, 138, 61)'); }
        assert.equal(await page.locator('.pp-away').count(),0);
        assert.equal(await page.locator('.home-need').count(),0);
        assert.equal(await page.locator('#paper [data-composer]').count(),0);
    }
    for (const page of pages) {
        await page.route('**/api/app/me',async route=>{
            const response=await route.fetch();const me=await response.json();
            await route.fulfill({response,json:{...me,relay_allowed:false}});
        });
        await page.goto(base+'/app/');
        await page.locator('.home-access').waitFor();
        await verify(page,'home-browser-access','Use wingthing with your agent','See plans');
        assert.equal(await page.locator('.home-access pre').count(),2);
        assert.equal(await page.locator('#paper .home-row,#paper [data-composer],#paper .home-steps').count(),0);
    }
}

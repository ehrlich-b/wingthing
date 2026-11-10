// R4 acceptance through the existing look harness, real roost and encrypted wings.
import assert from 'node:assert/strict';

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
        assert.ok(await page.getByRole('link',{name:'Set one up',exact:true}).isVisible());
        await shoot(page,'home-volume-1',suffix(page));
    }
    machines.studio.rows.push(row('redirect','fix-login-redirect','claude','blocked','wingthing'));
    // A fresh load reads the changed fixture without waiting for the normal 30 s poll.
    for (const page of pages) {
        await go(page,'#');
        assert.equal(await page.locator('.home-need').count(),1);
        const answer=page.getByRole('button',{name:'Answer',exact:true});
        assert.equal(await answer.count(),1);
        await page.locator('.home-need .home-commands').waitFor();
        await shoot(page,'home-blocked',suffix(page));
        await answer.click();
        await page.waitForFunction(()=>location.hash==='#session/studio/redirect' && document.querySelector('#paper .pp-live .xterm')?.contains(document.activeElement));
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
        assert.ok(await page.locator('[data-home-needs]').isVisible());
        assert.equal(await page.locator('[data-home-needs]').innerText(),'+1 more');
        for (const height of await page.locator('.home-row').evaluateAll(rows=>rows.map(r=>r.getBoundingClientRect().height))) assert.equal(height,page.viewportSize().width===390?56:44);
        await verify(page,'home-volume-plan-limit-6','Needs you');
        await page.locator('[data-home-needs]').click();
        await page.locator('#pp-filter').waitFor();
        assert.equal(await page.locator('#pp-filter').inputValue(),'Needs you');
        assert.equal(await page.locator('.pp-group:not([hidden])').count(),1);
        checked('home-needs-overflow-filter'+suffix(page),'All four waiting sessions reachable in filtered Sessions');
    }
    // Stability: no membership changes; new report lines/times every two seconds.
    // Both viewports sample the actual production DOM every 500 ms for 60 seconds.
    const previous = events.wing.slice();
    const originalLines = new Map();
    for (const page of pages) {
        await openHome(page);
        await page.locator('#pp-message:not([disabled])').waitFor();
        await page.waitForTimeout(1200);
        originalLines.set(page, await page.locator('.home-row[data-k="studio/wing"] .home-line').textContent());
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
        events.wing.push({sequence:10+i,type:'message',role:'assistant',text:'Reviewing workspace migration batch '+(i+1)+' with longer summary text',timestamp:at(i%2?2:1)});
        await pages[0].waitForTimeout(2000);
    }
    events.wing=previous;
    for(const [i,page] of pages.entries()) {
        const stable=await page.evaluate(()=>{
            clearInterval(window.homeSampler);window.homeObserver.disconnect();
            const reader=window.homeReader;
            return {samples:window.homeSamples,changes:window.homeChanges,shift:window.homeShift,focus:document.activeElement===reader,selection:[reader.selectionStart,reader.selectionEnd],scroll:window.homeScroll.scrollTop,expectedScroll:window.homeScrollTop};
        });
        assert.ok(stable.samples>=120);assert.deepEqual(stable.changes,[]);assert.equal(stable.shift,0);assert.ok(stable.focus);assert.deepEqual(stable.selection,[7,11]);assert.equal(stable.scroll,stable.expectedScroll);
        // Ordinary Home polling is every 30 s. No onboarding cadence remains.
        assert.ok(inventories.get(page)-counts[i]<=3);
        const updatedLine = await page.locator('.home-row[data-k="studio/wing"] .home-line').textContent();
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

// Sixty-second refresh proof on Home, Sessions, Session and Machine using the look fixture.
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
const sleep = ms => new Promise(resolve=>setTimeout(resolve,ms));
export async function assertSessionStability({pages,machines,events,screens,row,msg,go,checked,sent,shoot,toggleConnection}) {
    const fixture=row('stable','Keep organization invitation recovery readable during extended workspace migrations','claude','idle','wingthing');
    machines.studio.rows=[fixture];
    events.stable=Array.from({length:32},(_,i)=>msg(i+1,i%2?'assistant':'user','Long transcript row for interrupted organization invitation recovery '+i,32-i));
    screens.stable='\x1b[32mAll 41 tests pass\x1b[0m\r\n';
    const coordinator = {...row('stable-wing','wing','claude','idle','wingthing'), pinned:true, role:'coordinator'};
    const additional = Array.from({length:24}, (_,i)=>row('stable-'+i,'Workspace migration recovery with a long organization invitation name '+i,'claude',i<3?'blocked':i<13?'working':'idle','wingthing'));
    machines.studio.rows = [coordinator, fixture, ...additional];
    for (const entry of [coordinator, ...additional]) events[entry.session_id] = [msg(1,'assistant','Migration progress before refresh',0)];
    const stamp = new Date().toISOString();
    for (const list of Object.values(events)) for (const event of list) event.timestamp=stamp;
    const routes = ['#session/studio/stable','#','#sessions','#machine/studio'];
    const views = ['Session','Home','Sessions','Machine'];
    for (const [index,page] of pages.entries()) {
        const view=views[Math.floor(index/2)];
        await go(page,routes[Math.floor(index/2)]);
        if (view==='Session' || view==='Home') {
            await page.waitForFunction(()=>document.querySelector('#pp-message') && !document.querySelector('#pp-message').disabled);
            await page.locator('#pp-message').fill('Keep this draft and selection intact.');
        } else if (view==='Sessions') {
            await page.locator('#pp-filter').fill('migration');
        } else {
            await page.locator('.wd-session-row').first().waitFor();
        }
        // Allow the first native reads and fonts to settle before measuring refreshes.
        await page.waitForTimeout(1800);
        await page.evaluate(async view=>{
            const input=document.querySelector('#pp-message, #pp-filter');
            const target=input || document.querySelector('.wd-session-row');
            const transcript=document.querySelector('.pp-conversation') || document.querySelector('.pp-main');
            for (const disclosure of document.querySelectorAll('.wd-more, .pp-more-sessions')) disclosure.open=true;
            target.focus({preventScroll:true});
            if(input) input.setSelectionRange(2,5);
            const maximum=transcript.scrollHeight-transcript.clientHeight;
            if (maximum <= 2) throw Error(view+' fixture must have a scrollable list');
            transcript.scrollTop=Math.min(120,maximum/2);
            // The deliberate focus/scroll setup must paint before observing refreshes.
            await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
            window.startStability = () => {
            const rects=()=>[...document.querySelectorAll('[data-stable]')].map(node=>{
                const {x,y,width,height}=node.getBoundingClientRect(); return {x,y,width,height};
            });
            window.stability={initial:rects(),nodes:[...document.querySelectorAll('[data-stable]')],samples:0,moves:[],shifts:0,sources:[],input,target,transcript,scroll:transcript.scrollTop,value:input?.value};
            window.stability.observer=new PerformanceObserver(list=>{for(const entry of list.getEntries()){window.stability.shifts+=entry.value;window.stability.sources.push({value:entry.value,sources:entry.sources.map(s=>({tag:s.node?.tagName,className:s.node?.className,previous:s.previousRect,current:s.currentRect}))});}});
            window.stability.observer.observe({type:'layout-shift',buffered:false});
            window.stability.timer=setInterval(()=>{
                const s=window.stability; s.samples++;
                const current=rects(); if(JSON.stringify(current)!==JSON.stringify(s.initial))s.moves.push({sample:s.samples,differences:current.map((box,i)=>JSON.stringify(box)!==JSON.stringify(s.initial[i])?{index:i,initial:s.initial[i],current:box}:null).filter(Boolean)});
                if(s.nodes.some((node,i)=>node!==document.querySelectorAll('[data-stable]')[i]))s.moves.push({sample:s.samples,replaced:true});
                if(s.samples === 120) { clearInterval(s.timer); s.observer.disconnect(); }
                if(document.activeElement!==target || input && (input.selectionStart!==2 || input.selectionEnd!==5 || input.value!==s.value) || transcript.scrollTop!==s.scroll)s.moves.push({sample:s.samples,readerChanged:true,scroll:transcript.scrollTop});
            },500);
            };
        },view);
        await shoot(page,'stability-'+view.toLowerCase(),'-'+page.viewportSize().width);
    }
    // Start every viewport after setup, so each full minute sees live changes.
    await Promise.all(pages.map(page => page.evaluate(() => window.startStability())));
    for (let tick=1;tick<=30;tick++) {
        await sleep(2000);
        toggleConnection(tick);
        machines.studio.rows.reverse(); // Incoming order must not reorder surviving rows.
        for (const page of pages) await page.evaluate(()=>window.dispatchEvent(new Event('online')));
        const timestamp=new Date().toISOString();
        for (const entry of machines.studio.rows) {
            const list=events[entry.session_id];
            list.push({...msg(list.length+1,'assistant','Native progress update '+tick+' for organization invitation recovery',0),timestamp});
            entry.lifecycle.state_observed_at=timestamp;
        }
        screens.stable='\x1b[32mAll 41 tests pass\x1b[0m\r\nProgress '+tick+'\r\n';
        if(tick%5===0)console.log('STABILITY '+(tick*2)+'s on four routes at both sizes');
    }
    for (const [index,page] of pages.entries()) {
        const summary=await page.evaluate(()=>{
            const s=window.stability;
            return {samples:s.samples,shifts:s.shifts,firstMoves:s.moves.slice(0,2)};
        });
        console.log('MEASURE '+views[Math.floor(index/2)]+' '+page.viewportSize().width+' '+JSON.stringify(summary));
    }
    for (const [index,page] of pages.entries()) {
        const view=views[Math.floor(index/2)];
        const result=await page.evaluate(()=>{
            const s=window.stability;clearInterval(s.timer);s.observer.disconnect();
            return {samples:s.samples,moves:s.moves,shifts:s.shifts,initial:s.initial,sources:s.sources,credentialCalls:window.credentialCalls,scroll:s.scroll};
        });
        assert.equal(result.samples,120,JSON.stringify(result)); assert.deepEqual(result.moves,[],view); assert.equal(result.shifts,0,JSON.stringify({view,...result})); assert.equal(result.credentialCalls,0);
        assert.ok(result.initial.length>=12,view+' must measure header, strip, section heads and ten rows');
        checked(view.toLowerCase()+'-stable-60s-'+page.viewportSize().width,result);
        if(view==='Session') {
            if(page.viewportSize().width===390) await page.locator('[data-session-panel=terminal]').click();
            await page.waitForFunction(()=>/Progress (2[0-9]|30)/.test(document.querySelector('.pp-screen-snapshot .xterm-rows')?.textContent));
            checked('screen-keeps-up-'+page.viewportSize().width,await page.locator('.pp-screen-snapshot .xterm-rows').textContent());
        } else if(view!=='Machine') {
            assert.match(await page.locator('.pp-main').textContent(),/Native progress update/);
            checked(view.toLowerCase()+'-refresh-observed-'+page.viewportSize().width,'Native progress text updated during measurement');
        }
    }
    // Deliberate insertion is measured separately from refresh stability.
    const listPages=pages.slice(4,6), beforeInsertion=[];
    const boxes=page=>page.locator('.pp-srow').evaluateAll(rows=>rows.map(node=>{
        const {x,y,width,height}=node.getBoundingClientRect();return {key:node.dataset.key,x,y,width,height};
    }));
    for(const page of listPages) {
        await page.locator('.pp-main').evaluate(node=>{node.scrollTop=0;});
        beforeInsertion.push(await boxes(page));
    }
    const inserted=row('inserted-migration','Workspace migration inserted deliberately before idle rows','claude','blocked','wingthing');
    machines.studio.rows.splice(4,0,inserted);
    events[inserted.session_id]=[msg(1,'assistant','Deliberately inserted fixture row',0)];
    for(const [index,page] of listPages.entries()) {
        await page.locator('.pp-srow[href="#session/studio/inserted-migration"]').waitFor();
        const current=await boxes(page), insertion=current.find(box=>box.key?.endsWith('/inserted-migration'));
        assert.ok(insertion);
        const moved=[];
        const insertionTop=await page.locator('.pp-group').filter({has:page.locator('.pp-srow[href="#session/studio/inserted-migration"]')}).evaluate(node=>node.getBoundingClientRect().y);
        let displacement;
        for(const previous of beforeInsertion[index]) {
            const next=current.find(box=>box.key===previous.key);
            assert.ok(next);
            assert.equal(next.x,previous.x);assert.equal(next.width,previous.width);assert.equal(next.height,previous.height);
            if(next.y!==previous.y) {
                assert.ok(previous.y>=insertionTop,'only following rows may move');
                displacement ??= next.y-previous.y;
                assert.ok(displacement>0);
                assert.equal(next.y-previous.y,displacement);
                moved.push({key:next.key,from:previous.y,to:next.y});
            }
        }
        assert.ok(moved.length>0);
        checked('deliberate-session-insertion-'+page.viewportSize().width,{insertion,moved});
    }
    // Keep the existing two independent session queue/reload probes.
    pages=pages.slice(0,2);
    machines.studio.rows=[fixture];
    // A working agent keeps the composer on, reserves one queued draft, then
    // dispatches it exactly once when the native reader reports readiness.
    fixture.lifecycle={status:'working',state:'working',state_source:'claude_hook'};
    for(const page of pages) {
        await page.reload();
        await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='working');
        await page.locator('#pp-message').fill('Queued follow-up '+page.viewportSize().width);
        await page.getByRole('button',{name:'Send',exact:true}).click();
        if (page.viewportSize().width===1440) {
            await page.evaluate(()=>{location.hash='#sessions';});
            await page.locator('#pp-filter').waitFor();
        } else {
            await page.reload();
            await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='working' && document.querySelector('.pp-queue-note')?.textContent.includes("Sends when it's ready"));
        }
    }
    assert.equal(sent.length,0);
    fixture.lifecycle={status:'idle',state:'idle',state_source:'claude_hook'};
    for(let i=0;i<30 && sent.length<2;i++)await sleep(100);
    assert.deepEqual(sent.map(s=>s.input).sort(),['Queued follow-up 1440','Queued follow-up 390']);
    await sleep(1700); assert.equal(sent.length,2);
    checked('queued-messages-delivered-once-after-navigation-and-reload',sent.slice());
    // Restore the queue while another route is open, without reopening its session.
    fixture.lifecycle={status:'working',state:'working',state_source:'claude_hook'};
    const page=pages[0];
    await go(page,'#session/studio/stable');
    await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='working');
    await page.locator('#pp-message').fill('Restore while elsewhere');
    await page.getByRole('button',{name:'Send',exact:true}).click();
    await page.evaluate(()=>{location.hash='#sessions';}); await page.locator('#pp-filter').waitFor();
    await page.reload(); await page.locator('#pp-filter').waitFor();
    fixture.lifecycle={status:'idle',state:'idle',state_source:'claude_hook'};
    for(let i=0;i<50 && sent.length<3;i++)await sleep(100);
    assert.equal(sent.length,3);
    assert.equal(sent[2].input,'Restore while elsewhere');
    assert.equal(await page.evaluate(()=>location.hash),'#sessions');
    await sleep(1700); assert.equal(sent.length,3);
    checked('queued-message-restored-on-another-route',sent[2]);
    await go(page,'#session/studio/stable');
    await page.waitForFunction(()=>!document.querySelector('#pp-message')?.disabled);
    await page.locator('#pp-message').fill('Send immediately once');
    await page.getByRole('button',{name:'Send',exact:true}).click();
    await page.waitForFunction(()=>document.querySelector('#pp-message')?.value==='');
    for(let i=0;i<30 && sent.length<4;i++)await sleep(100);
    assert.equal(sent.length,4); assert.equal(sent[3].input,'Send immediately once');
    checked('ready-message-clears-only-after-reservation',sent[3]);

    await assertHomeDeliveryStability({pages,machines,events,row,msg,go,checked,sent,shoot});
    await assertMachineUpdateStability({pages,machines,go,checked,shoot});
}

export async function assertHomeDeliveryStability({pages,machines,events,row,msg,go,checked,sent,shoot}) {
    // A native delivery transition must fit the Home rail's existing slots.
    // Keep the full saved message and the same-request recovery action usable.
    const coordinator={...row('delivery-wing','wing','claude','idle','wingthing'),pinned:true,role:'coordinator'};
    machines.studio.rows=[coordinator,row('delivery-work','Review the extended organization migration','claude','idle','wingthing')];
    events[coordinator.session_id]=[msg(1,'assistant','Ready for your next message',0)];
    const pendingText=Array.from({length:12},(_,i)=>'Saved message line '+i+' '+ 'long-message-content '.repeat(8)).join('\n');
    for (const page of pages) {
        await go(page,'#');
        // Reload the inventory before measuring the new Home coordinator.
        await page.reload();
        await page.waitForFunction(()=>document.querySelector('#pp-message') && !document.querySelector('#pp-message').disabled);
        await page.waitForTimeout(1800);
        await page.evaluate(()=>{
            const nodes=[...document.querySelectorAll('[data-stable], .pp-delivery, .pp-pending-text, [data-check-receipt]')];
            const boxes=()=>nodes.map(node=>{
                const {x,y,width,height}=node.getBoundingClientRect();return {x,y,width,height};
            });
            const observer=new PerformanceObserver(list=>{for(const entry of list.getEntries())window.deliveryStability.shifts+=entry.value;});
            window.deliveryStability={nodes,boxes,initial:boxes(),shifts:0,observer,scroll:document.querySelector('.pp-main').scrollTop};
            observer.observe({type:'layout-shift',buffered:false});
        });
        const measure=async state=>{
            await page.waitForTimeout(100);
            const result=await page.evaluate(()=>{
                const s=window.deliveryStability;
                return {initial:s.initial,current:s.boxes(),connected:s.nodes.every(node=>node.isConnected),shifts:s.shifts,scroll:document.querySelector('.pp-main').scrollTop,initialScroll:s.scroll};
            });
            assert.deepEqual(result.current,result.initial,state+' boxes');
            assert.equal(result.connected,true,state+' retains nodes');
            assert.equal(result.scroll,result.initialScroll,state+' scroll');
            assert.equal(result.shifts,0,state+' layout shifts');
            checked('home-delivery-'+state+'-'+page.viewportSize().width,result);
            await shoot(page,'home-delivery-'+state,'-'+page.viewportSize().width);
        };
        await measure('ready');
        coordinator.receiptMode='unconfirmed';
        await page.locator('#pp-message').fill(pendingText);
        // Submit in place so an intentional mobile scroll is not part of the probe.
        await page.locator('#paper [data-composer]').dispatchEvent('submit');
        await page.waitForFunction(()=>document.querySelector('.pp-delivery')?.textContent.includes('Delivery unconfirmed'));
        assert.equal(await page.locator('.pp-pending-text').textContent(),pendingText);
        const request=await page.locator('.pp-delivery small').textContent();
        assert.match(request,/Request .+/);
        await page.locator('.pp-pending-text').evaluate(node=>{node.scrollTop=node.scrollHeight;});
        assert.ok(await page.locator('.pp-pending-text').evaluate(node=>node.scrollTop)>0,'saved message remains scrollable');
        await measure('pending');
        coordinator.receiptMode='confirmed';coordinator.promptDelayMs=800;
        const sentBefore=sent.length;
        await page.locator('#paper [data-check-receipt]').dispatchEvent('click');
        await page.waitForFunction(()=>document.querySelector('.pp-delivery')?.textContent.includes('Checking delivery'));
        assert.equal(await page.locator('#paper [data-check-receipt]').isDisabled(),true);
        await measure('checking');
        await page.waitForFunction(()=>document.querySelector('.pp-delivery')?.textContent.includes('Delivered'));
        assert.equal(sent.length,sentBefore+1);
        assert.equal('Request '+sent.at(-1).request_id,request,'receipt check reuses the saved request');
        assert.equal(sent.at(-1).input,pendingText);
        assert.equal(await page.locator('.pp-delivery small').textContent(),request);
        assert.equal(await page.locator('#paper [data-check-receipt]').isVisible(),false);
        await measure('delivered');
        await page.evaluate(()=>window.deliveryStability.observer.disconnect());
        delete coordinator.promptDelayMs;
    }
}

export async function assertMachineUpdateStability({pages,machines,go,checked,shoot}) {
    machines.studio.version='0.1.0';
    for (const page of pages) {
        await go(page,'#machine/studio');
        await page.locator('.wd-name').waitFor();
        // Supply the same version metadata a cached machine can carry; keep
        // the built application's update handler and scheduled callback intact.
        await page.evaluate(()=>{
            const wings=JSON.parse(localStorage.getItem('wt_wings'));
            Object.assign(wings.find(w=>w.wing_id==='studio'),{version:'0.1.0',latest_version:'0.2.0'});
            localStorage.setItem('wt_wings',JSON.stringify(wings));
        });
        await page.reload();
        await page.locator('#wd-update').waitFor({state:'attached'});
        await page.locator('[data-detail=technical]').evaluate(node=>{node.open=true;});
        await page.evaluate(()=>{
            const original=window.setTimeout;
            window.updateCallbacks=0;
            window.setTimeout=function(callback,delay,...args) {
                return original.call(this,delay===3000 ? (...args)=>{window.updateCallbacks++;callback(...args);} : callback,delay,...args);
            };
        });
        await page.locator('#wd-update').click();
        await page.getByRole('status').filter({hasText:'Updating…'}).waitFor();
        await page.locator('#wd-back').focus();
        await page.evaluate(async ()=>{
            await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
            const region=document.querySelector('#wing-detail-content');
            const nodes=[...region.querySelectorAll('*')];
            const boxes=()=>nodes.map(node=>{
                const {x,y,width,height}=node.getBoundingClientRect();return {x,y,width,height};
            });
            const observer=new MutationObserver(list=>{
                for(const entry of list) window.machineUpdate.removed+=entry.removedNodes.length;
            });
            window.machineUpdate={nodes,boxes,initial:boxes(),focused:document.activeElement,removed:0,shifts:0,observer};
            observer.observe(region,{childList:true,subtree:true});
            const shifts=new PerformanceObserver(list=>{
                for(const entry of list.getEntries()) window.machineUpdate.shifts+=entry.value;
            });
            window.machineUpdate.shiftsObserver=shifts;
            shifts.observe({type:'layout-shift',buffered:false});
        });
        await shoot(page,'machine-updating','-'+page.viewportSize().width);
    }
    // At least two real three-second callbacks must run in each viewport.
    await Promise.all(pages.map(page=>page.waitForFunction(()=>window.updateCallbacks>=2)));
    for(const page of pages) {
        const result=await page.evaluate(()=>{
            const s=window.machineUpdate;s.observer.disconnect();s.shiftsObserver.disconnect();
            return {callbacks:window.updateCallbacks,replaced:s.nodes.some(node=>!node.isConnected),
                focusLost:document.activeElement!==s.focused,removed:s.removed,shifts:s.shifts,
                initial:s.initial,current:s.boxes(),credentialCalls:window.credentialCalls};
        });
        assert.ok(result.callbacks>=2);
        assert.equal(result.replaced,false);assert.equal(result.focusLost,false);
        assert.equal(result.removed,0);assert.equal(result.shifts,0);assert.equal(result.credentialCalls,0);
        assert.deepEqual(result.current,result.initial);
        checked('machine-update-retains-focus-and-nodes-'+page.viewportSize().width,result);
        // Expire the wait using Date only; the production three-second timer
        // still runs normally and restores the ordinary details page.
        await page.clock.setFixedTime(Date.now()+61000);
        await page.locator('.wd-name').waitFor();
        const recovery=await page.evaluate(()=>({
            sameLink:document.querySelector('#wd-back')===window.machineUpdate.focused,
            focused:document.activeElement===window.machineUpdate.focused,
            hash:location.hash,
        }));
        assert.deepEqual(recovery,{sameLink:true,focused:true,hash:'#machine/studio'});
        checked('machine-update-expiry-keeps-navigation-'+page.viewportSize().width,recovery);
        await page.locator('#wd-back').click();
        await page.waitForFunction(()=>location.hash==='#machines');
    }
    assert.equal(machines.studio.updates,pages.length,'each viewport used the production update action once');
    checked('machine-update-actions',machines.studio.updates);
}

// Run directly as well as through the existing look harness.
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('./look.mjs', import.meta.url))], {
        env: {...process.env, WT_LOOK_SHELL: 'session-stable'}, stdio: 'inherit',
    });
    if (result.error) throw result.error;
    process.exitCode = result.status ?? 1;
}

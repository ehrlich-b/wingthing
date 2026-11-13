// State and interaction assertions use look.mjs's real roost and encrypted fixtures.
import assert from 'node:assert/strict';

export async function assertSessionStates({pages,machines,events,screens,row,msg,go,shoot,checked,assertShell,sent,checkSessionLayout}) {
    const specs = [
        ['reading','Connecting…','Connecting to Studio…'],
        ['live','Read-only',''],
        ['working','Read-only','Claude is working…'],
        ['waiting','Read-only · waiting for you','Answer in the terminal'],
        ['control',"You're typing",'Messages wait while you have control.'],
        ['unsupported','Read-only',"wingthing can't read this agent's conversation, so there's no chat here. The terminal on the right is the whole session."],
        ['empty','Read-only',"No messages yet. What you send goes to the agent's prompt."],
        ['offline','Last screen, 6:12 PM','Studio is offline.'],
        ['locked','Locked','Unlock Studio to read this session.'],
        ['ended','Ended','This session ended at 5:40 PM.'],
        ['older','Read-only','Update wt on Studio to see its terminal here:'],
    ];
    const today = new Date(); today.setHours(17,40,0,0);
    const seen = new Date(); seen.setHours(18,12,0,0);
    const fixtureRow = row('state','Fix login redirect when organization invitations expire during workspace migration','claude','idle','wingthing');
    machines.studio.rows = [fixtureRow, ...Array.from({length:8}, (_,i)=>row('long-'+i, 'A long organization invitation recovery project and branch name '+i, 'claude', 'idle', 'workspace-migration'))];
    screens.state='\x1b[38;2;42;168;255m❯\x1b[0m ./scripts/deploy.sh staging\r\n\x1b[32mTests pass (41/41).\x1b[0m\r\nDo you want to proceed?\r\n';
    const conversation = Array.from({length:12},(_,i)=>msg(i+1,i%2?'assistant':'user','Review organization invitation recovery for interrupted workspace migrations, including long project and branch names '+(i+1),12-i));
    conversation[conversation.length-1].text += '\n' + 'Long invitation recovery details with an-unbroken-identifier-'.repeat(30) + 'FINAL WORDS';
    events.state=conversation.map(event=>({...event}));
    for (const page of pages) {
        const width=page.viewportSize().width;
        let baseline;
        await page.clock.install({time:seen});
        await page.clock.setFixedTime(seen);
        for (const [name,kicker,sentence] of specs) {
            Object.assign(fixtureRow,{reading:false,readingHold:false,older:false,agent:'claude',process_alive:true,ended_at:undefined,lifecycle:{status:'idle',state:'idle',state_source:'claude_hook'}});
            await page.clock.setFixedTime(seen);
            machines.studio.flags={}; events.state=conversation.map(event=>({...event}));
            if (name==='reading') {fixtureRow.reading=true;fixtureRow.readingHold=true;}
            if (name==='working') Object.assign(fixtureRow.lifecycle,{status:'working',state:'working'});
            if (name==='waiting') { Object.assign(fixtureRow.lifecycle,{status:'blocked',state:'needs_input',reason:'Run ./scripts/deploy.sh staging'}); }
            if (name==='unsupported') { fixtureRow.agent='shell'; fixtureRow.lifecycle.state_source='unsupported'; events.state=[]; }
            if (name==='empty') events.state=[];
            if (name==='ended') { fixtureRow.process_alive=false; fixtureRow.ended_at=today.toISOString(); fixtureRow.lifecycle.status='exited'; fixtureRow.lifecycle.state_observed_at=today.toISOString(); }
            if (name==='older') fixtureRow.older=true;
            // Open connected first to retain a real cached transcript and screen.
            if (page.url().startsWith('http')) await page.evaluate(() => localStorage.clear());
            await page.goto('about:blank');
            await go(page,'#session/studio/state');
            if (name!=='reading') await page.waitForFunction(() => !['reading'].includes(document.querySelector('.pp-session')?.dataset.state));
            if (name==='offline' || name==='locked') {
                machines.studio.flags=name==='offline'?{offline:true}:{locked:true};
                await page.clock.setFixedTime(new Date(seen.getTime()+2000));
                await page.evaluate(()=>document.querySelector('#phone-refresh').click());
            }
            if (name==='control') {
                if (width===390) await page.locator('[data-session-panel=terminal]').click();
                const before=await page.evaluate(()=>({hash:location.hash,sidebar:document.querySelector('.pp-side').innerHTML}));
                assert.equal(await page.locator('#pp-message').isEnabled(),true);
                await page.getByRole('button',{name:'Take over',exact:true}).click();
                await page.getByRole('button',{name:'Hand back',exact:true}).waitFor();
                assert.equal(await page.locator('#pp-message').isDisabled(),true);
                await page.locator('.pp-screen-control .xterm').waitFor({state:'visible'});
                await page.waitForFunction(()=>document.activeElement?.classList.contains('xterm-helper-textarea'));
                assert.deepEqual(await page.evaluate(()=>({hash:location.hash,sidebar:document.querySelector('.pp-side').innerHTML})),before);
                checked('takeover-unchanged-frame-'+width,before);
                await page.keyboard.type('Typed in the terminal '+width);await page.keyboard.press('Enter');
            }
            if (name==='waiting') { assert.equal(await page.locator('#pp-message').isEnabled(),true); assert.equal(await page.locator('.pp-need button').textContent(),'Answer in the terminal'); }
            if (name==='reading') {
                await page.locator('.pp-reading').waitFor({state:'visible'});
                const heights=await page.locator('.pp-reading > div').evaluateAll(rows=>rows.map(row=>row.getBoundingClientRect().height));
                assert.deepEqual(heights,Array(3).fill(width===390?56:44));
            }
            await page.waitForFunction(name=>document.querySelector('.pp-session')?.dataset.state===name,name);
            if (name==='offline') await page.waitForFunction(()=>document.querySelector('[data-terminal-kicker]').textContent.includes('6:12'));
            await page.waitForFunction(expected=>document.querySelector('[data-terminal-kicker]')?.textContent===expected,kicker);
            assert.equal(await page.locator('[data-terminal-kicker]').innerText(),kicker);
            if (sentence) assert.ok((await page.locator('.pp-session').textContent()).includes(sentence),name+': '+sentence);
            if (width===390) {
                assert.equal(await page.locator('[data-session-panel=messages]').count(),1);
                assert.equal(await page.locator('[data-session-panel=terminal]').count(),1);
                assert.equal(await page.locator('.pp-session').getAttribute('data-panel'),['waiting','control'].includes(name)?'terminal':'messages');
                await page.locator('[data-session-panel=terminal]').click();
            }
            const bounds=await page.locator('.pp-session').evaluate(node=>{
                const rect=el=>{const {x,y,width,height}=el.getBoundingClientRect();return {x,y,width,height};};
                return {messages:rect(node.querySelector('.pp-session-messages')),terminal:rect(node.querySelector('.pp-session-terminal')),screen:rect(node.querySelector('.pp-session-screen'))};
            });
            if (width===1440) { baseline ||= bounds; assert.deepEqual(bounds,baseline,name+' fixed columns and pane'); assert.ok(bounds.messages.x<bounds.terminal.x); }
            else {
                const tabs=await page.locator('.pp-tabs').boundingBox();
                assert.ok(bounds.screen.x>=0 && bounds.screen.x+bounds.screen.width<=390 && bounds.screen.y>=0 && bounds.screen.y+bounds.screen.height<=tabs.y,JSON.stringify({name,bounds,tabs}));
                for (const button of await page.locator('.pp-terminal-actions .pp-btn').all()) { const r=await button.boundingBox(); assert.ok(r && r.y+r.height<=tabs.y,JSON.stringify({name,r,tabs})); }
            }
            if (name==='live') {
                assert.equal(await page.locator('#pp-message').isEnabled(),true);
                const colours=await page.locator('.pp-screen-snapshot .xterm-rows').evaluate(node=>[...node.querySelectorAll('span')].map(span=>getComputedStyle(span).color));
                assert.ok(colours.includes('rgb(42, 168, 255)') && colours.includes('rgb(54, 214, 154)'),JSON.stringify(colours));
                checked('coloured-read-only-screen-'+width,colours);
                if(width===390) await page.locator('[data-session-panel=messages]').click();
                const turn=page.locator('.pp-say').last(), summary=turn.locator('summary');
                await summary.focus(); await page.keyboard.press('Enter');
                assert.equal(await turn.getAttribute('open'),'');
                const full=turn.locator('.pp-message-full');
                assert.ok((await full.innerText()).endsWith('FINAL WORDS'));
                assert.equal(await full.evaluate(node=>node.scrollWidth<=node.clientWidth),true);
                assert.ok((await full.boundingBox()).height>100);
                // A native disclosure remains open and focused across refreshes.
                await page.waitForTimeout(1700);
                assert.equal(await turn.getAttribute('open'),'');
                assert.equal(await summary.evaluate(node=>node===document.activeElement),true);
                await full.evaluate(node=>{node.parentElement.parentElement.parentElement.scrollTop=100000;});
                await shoot(page,'session-full-message','-'+width);
                await summary.focus(); await page.keyboard.press('Enter');
                assert.equal(await turn.getAttribute('open'),null);
                checked('full-message-keyboard-and-refresh-'+width,{ending:'FINAL WORDS',wrapped:true});
                if(width===390) {
                    await summary.tap();
                    assert.equal(await turn.getAttribute('open'),'');
                    assert.ok((await full.innerText()).endsWith('FINAL WORDS'));
                    await summary.tap();
                    assert.equal(await turn.getAttribute('open'),null);
                    checked('full-message-touch-'+width,{ending:'FINAL WORDS'});
                }
                if(width===390) await page.locator('[data-session-panel=terminal]').click();
            }
            if (name==='unsupported' || name==='locked') {
                assert.equal(await page.locator('.pp-conversation .pp-row').count(),0);
                assert.equal(await page.locator('#pp-message').isDisabled(),true);
            }
            if (width===390 && !['waiting','control'].includes(name)) {
                await page.locator('[data-session-panel=messages]').click();
                const composer=await page.locator('.pp-composer').boundingBox(), tabs=await page.locator('.pp-tabs').boundingBox();
                assert.ok(composer.y>=0 && composer.y+composer.height<=tabs.y,JSON.stringify({name,composer,tabs}));
            }
            if (name==='working') {
                assert.equal(await page.locator('#pp-message').isEnabled(),true);
                const invalid='x'.repeat(65537);
                await page.locator('#pp-message').fill(invalid);
                await page.getByRole('button',{name:'Send',exact:true}).click();
                assert.equal(await page.locator('#pp-message').inputValue(),invalid);
                assert.match(await page.locator('.pp-delivery').textContent(),/64 KiB/);
                checked('invalid-queued-input-keeps-draft-'+width,{bytes:invalid.length});
                await page.locator('#pp-message').fill('Queue this follow-up');
                await page.getByRole('button',{name:'Send',exact:true}).click();
                assert.ok((await page.locator('.pp-composer').textContent()).includes("Sends when it's ready"));
                // State fixtures are isolated; a new browser below checks delivery.
            }
            assert.equal(await page.evaluate(()=>window.credentialCalls),0);
            await assertShell(page,'session-'+name+'-'+width);
            await shoot(page,'session-'+name,'-'+width);
            if(name==='reading') fixtureRow.readingHold=false;
            checked('state-'+name+'-'+width,{bounds,kicker,sentence});
            if (width===390 && !['waiting','control'].includes(name)) {
                await page.locator('[data-session-panel=terminal]').click();
                await shoot(page,'session-'+name+'-terminal','-'+width);
                await page.locator('[data-session-panel=messages]').click();
            }
            if (width===390 && ['waiting','control'].includes(name)) {
                await page.locator('[data-session-panel=messages]').click();
                assert.equal(await page.locator('.pp-session').getAttribute('data-panel'),'messages');
                await shoot(page,'session-'+name+'-messages','-'+width);
                await page.locator('[data-session-panel=terminal]').click();
            }
            if (name==='waiting') {
                if (width===390) await page.locator('[data-session-panel=messages]').click();
                const before=sent.length, hash=await page.evaluate(()=>location.hash);
                await page.getByRole('button',{name:'Answer in the terminal',exact:true}).click();
                await page.getByRole('button',{name:'Hand back',exact:true}).waitFor();
                await page.waitForFunction(()=>document.activeElement?.classList.contains('xterm-helper-textarea'));
                await page.keyboard.type('Yes from terminal '+width); await page.keyboard.press('Enter');
                for(let i=0;i<30 && !events.state.some(event=>event.text==='Yes from terminal '+width);i++) await page.waitForTimeout(50);
                await page.getByRole('button',{name:'Hand back',exact:true}).click();
                await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='working' && !document.querySelector('#pp-message')?.disabled);
                assert.equal(await page.evaluate(()=>location.hash),hash);
                assert.equal(sent.length,before,'waiting input never uses the composer transport');
                assert.ok(events.state.some(event=>event.role==='user' && event.text==='Yes from terminal '+width));
                checked('terminal-only-waiting-focus-and-answer-'+width,{composerSends:0,hash,terminalInput:'Yes from terminal '+width});
            }
            if (name==='control') {
                const hash=await page.evaluate(()=>location.hash), side=await page.locator('.pp-side').innerHTML();
                await page.locator('.pp-screen-control .xterm-helper-textarea').focus();
                await page.keyboard.type('Terminal message '+width); await page.keyboard.press('Enter');
                for(let i=0;i<30 && !events.state.some(event=>event.text==='Terminal message '+width);i++) await page.waitForTimeout(50);
                await page.getByRole('button',{name:'Hand back',exact:true}).click();
                await page.getByRole('button',{name:'Take over',exact:true}).waitFor();
                assert.equal(await page.evaluate(()=>location.hash),hash);
                assert.equal(await page.locator('.pp-side').innerHTML(),side);
                await page.waitForFunction(()=>!document.querySelector('#pp-message')?.disabled);
                assert.ok(events.state.some(event=>event.role==='user' && event.text==='Terminal message '+width));
                await page.getByRole('button',{name:'Take over',exact:true}).click();
                await page.getByRole('button',{name:'Hand back',exact:true}).click();
                assert.equal(await page.evaluate(()=>location.hash),hash);
                checked('handback-unchanged-frame-'+width,{hash,side});
                await page.getByRole('button',{name:'Take over',exact:true}).click();
                await page.getByRole('button',{name:'Hand back',exact:true}).waitFor();
                assert.equal(await page.evaluate(()=>location.hash),hash);
                assert.equal(await page.locator('.pp-side').innerHTML(),side);
                assert.equal(await page.locator('#pp-message').isDisabled(),true);
                await page.getByRole('button',{name:'Hand back',exact:true}).click();
                checked('second-takeover-in-place-'+width,{hash,side});
            }
        }
        // A saved working follow-up cannot answer a later waiting prompt.
        machines.studio.flags={}; Object.assign(fixtureRow,{older:false,process_alive:true,lifecycle:{status:'working',state:'working',state_source:'claude_hook'}});
        await page.evaluate(()=>localStorage.clear()); await page.goto('about:blank');
        await go(page,'#session/studio/state');
        await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='working' && !document.querySelector('#pp-message')?.disabled);
        const before=sent.length, followUp='Inspect the tests after this task '+width, answer='Yes in terminal '+width;
        await page.locator('#pp-message').fill(followUp);
        await page.getByRole('button',{name:'Send',exact:true}).click();
        Object.assign(fixtureRow.lifecycle,{status:'blocked',state:'needs_input',reason:'Run ./scripts/deploy.sh staging'});
        await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='waiting' && document.querySelector('#pp-message') && !document.querySelector('#pp-message').disabled);
        assert.equal(sent.length,before);
        fixtureRow.readDelayMs=4000;
        await page.reload();
        await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='waiting' && document.querySelector('#pp-message') && !document.querySelector('#pp-message').disabled);
        delete fixtureRow.readDelayMs;
        if(width===390) await page.locator('[data-session-panel=messages]').click();
        await page.locator('#pp-message').fill(answer);
        await page.getByRole('button',{name:'Send',exact:true}).click();
        await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='working' && !document.querySelector('#pp-message')?.disabled);
        if(width===390) await page.locator('[data-session-panel=messages]').click();
        assert.equal(await page.locator('#pp-message').isEnabled(),true);
        assert.deepEqual(sent.slice(before).map(p=>p.input),[answer]);
        checked('typed-answer-working-restores-composer-'+width,{answer,composerEnabled:true,queuedFollowUp:followUp,delayedReadMs:4000});
        await shoot(page,'session-waiting-with-follow-up','-'+width);
        Object.assign(fixtureRow.lifecycle,{status:'idle',state:'idle'});
        await page.waitForFunction(text=>document.querySelector('.pp-conversation')?.textContent.includes(text),followUp);
        await page.waitForTimeout(1700);
        assert.deepEqual(sent.slice(before).map(p=>p.input),[answer,followUp]);
        assert.equal(sent[before].answer_to_state_cursor,1);
        assert.equal(sent[before+1].answer_to_state_cursor,undefined);
        await page.reload();
        await page.waitForFunction(()=>document.querySelector('.pp-session')?.dataset.state==='live');
        await page.waitForTimeout(1700);
        assert.deepEqual(sent.slice(before).map(p=>p.input),[answer,followUp]);
        checked('typed-answer-follow-up-once-after-reload-'+width,sent.slice(before));
        machines.studio.flags={}; Object.assign(fixtureRow,{older:false,process_alive:true,lifecycle:{status:'idle',state:'idle',state_source:'claude_hook'}}); events.state=conversation.map(event=>({...event}));
        await go(page,'#session/studio/state');
        if (width===390) await page.locator('[data-session-panel=messages]').click();
        // Additional transcript volumes pin the empty, one, limit and overflow pictures.
        for (const count of [0,1,3,8,24,32]) {
            events.state=Array.from({length:count},(_,i)=>msg(i+1,i%2?'assistant':'user','A long migration task name with organization invitation recovery details '+i,1));
            await page.evaluate(()=>localStorage.clear());
            await page.goto('about:blank');
            await go(page,'#session/studio/state');
            await page.waitForFunction(expected=>document.querySelectorAll('.pp-conversation .pp-row').length===expected,Math.min(count,24));
            if (count>24) assert.equal(await page.locator('[data-session-earlier]').innerText(),'+'+(count-24)+' more');
            await checkSessionLayout(page,'session-volume-'+count+'-'+width,count===3);
            await shoot(page,'session-volume-'+count,'-'+width);
            if (count>24) { await page.locator('[data-session-earlier]').click(); assert.equal(await page.locator('.pp-conversation .pp-row').count(),count); }
        }
        await go(page,'#sessions');
        await page.waitForFunction(()=>document.querySelectorAll('.pp-srow').length>=8);
        await shoot(page,'session-long-name-items','-'+width);
    }
}

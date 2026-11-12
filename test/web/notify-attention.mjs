// Production attention handling with an isolated shell and no relay or account.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { chromium } from '../../web/node_modules/playwright/index.mjs';
const stubs = {
    '/src/state.js': 'export const S = window.state; export const DOM = {};',
    '/src/render.js': 'export const renderSidebar = () => {}, renderDashboard = () => {};',
    '/src/chat-view.js': 'export const chatSnapshot = () => null;',
    '/src/session-completion.js': 'export const COMPLETION_STORAGE_PREFIX = "completion:"; export const acknowledgeSessionCompletions = (storage,user,rows) => window.seen.push({user,rows});',
    '/src/storage-scope.js': 'export const browserLocalStorage = () => localStorage; export const userStorageKey = (prefix,user) => prefix+user;',
};
const server = createServer(async (req,res) => {
    try {
        if (req.url === '/') { res.setHeader('Content-Type','text/html'); res.end('<div id="paper"><main class="pp-main"></main></div>'); }
        else {
            res.setHeader('Content-Type','text/javascript');
            if (stubs[req.url]) res.end(stubs[req.url]);
            else if (/^\/src\/[a-z/-]+\.js$/.test(req.url)) res.end(await readFile(new URL('../../web'+req.url,import.meta.url)));
            else res.writeHead(404).end();
        }
    } catch (e) { res.writeHead(500).end(String(e)); }
});
let browser;
try {
    server.listen(0,'127.0.0.1'); await once(server,'listening');
    const base='http://127.0.0.1:'+server.address().port;
    browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_PATH,args:['--disable-background-networking']});
    const page=await browser.newPage(); const errors=[];
    page.on('pageerror',e=>errors.push(e.message));
    page.on('dialog',()=>assert.fail('Attention reading must not open a dialog'));
    await page.route('**/*',r=>new URL(r.request().url()).origin===base?r.continue():r.abort());
    await page.goto(base);
    const result=await page.evaluate(async()=>{
        const key=(wing,id)=>JSON.stringify([wing,id]);
        window.state={activeView:'home', currentUser:{id:'reader'}, sessionsData:[{id:'same',wing_id:'one'},{id:'same',wing_id:'two'}],sessionNotifications:{}};
        window.seen=[]; window.visible=true;
        Object.defineProperty(document,'visibilityState',{get:()=>window.visible?'visible':'hidden'});
        Object.defineProperty(document,'hidden',{get:()=>!window.visible});
        Object.defineProperty(window,'Notification',{value:{permission:'denied'}});
        const {setNotification,acknowledgeViewedSession,initNotifyListeners}=await import('/src/notify.js');
        const assert=(ok,msg)=>{if(!ok)throw Error(msg);};
        history.replaceState({},'','#session/one/same');
        const main=document.querySelector('.pp-main');main.dataset.hash=location.hash;main.dataset.view='session';
        state.sessionNotifications[key('one','same')]=true;
        state.sessionNotifications[key('two','same')]=true;
        document.title='(!) wingthing';
        acknowledgeViewedSession();
        assert(!state.sessionNotifications[key('one','same')],'Opening clears exact paper attention');
        assert(state.sessionNotifications[key('two','same')],'Other wing stays unread');
        setNotification('same','one');
        assert(!state.sessionNotifications[key('one','same')],'Visible session stays acknowledged');
        main.dataset.hash='#sessions';
        setNotification('same','one');
        assert(state.sessionNotifications[key('one','same')],'Unrendered route must stay unread');
        main.dataset.hash=location.hash;window.visible=false;
        acknowledgeViewedSession();
        assert(state.sessionNotifications[key('one','same')],'Hidden session stays unread');
        initNotifyListeners();window.visible=true;document.dispatchEvent(new Event('visibilitychange'));
        assert(!state.sessionNotifications[key('one','same')],'Visibility return clears attention');
        assert(state.sessionNotifications[key('two','same')],'Visibility return respects wing');
        state.activeView='account';state.sessionNotifications[key('one','same')]=true;
        acknowledgeViewedSession();
        assert(state.sessionNotifications[key('one','same')],'Account never acknowledges paper route');
        state.activeView='home';delete state.sessionNotifications[key('two','same')];acknowledgeViewedSession();
        assert(document.title==='wingthing','Reading last attention restores title');
        assert(seen.every(entry=>entry.user==='reader'&&entry.rows[0].wing_id==='one'),'Completion acknowledgments remain scoped');
        if(state.titleFlashTimer)clearInterval(state.titleFlashTimer);
        return {seen:seen.length,notifications:state.sessionNotifications,title:document.title};
    });
    assert.deepEqual(errors,[]);assert.deepEqual(result.notifications,{});
    console.log('PASS paper-session attention: open, visible events, hidden/read return, exact wing, account guard, title and scoped completion',JSON.stringify(result));
} finally { await browser?.close(); await new Promise(resolve=>server.close(resolve)); }

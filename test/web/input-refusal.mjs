import assert from 'node:assert/strict';
import fs from 'node:fs';

// Run the installed send function with a delayed encryptor and the real notice
// renderer in a browser, over the normal Sessions page and its live refreshes.
export async function assertInputRefusal({page, go, shoot, checked}) {
    const source = fs.readFileSync(new URL('../../web/src/terminal.js', import.meta.url), 'utf8');
    const sendBody = source.slice(source.indexOf('function refusePTYInput('))
        .replace('export function sendPTYInput', 'function sendPTYInput');
    const renderer = fs.readFileSync(new URL('../../web/src/input-refusal.js', import.meta.url), 'utf8');
    await go(page, '#sessions');
    const result = await page.evaluate(async ({sendBody, renderer}) => {
        const {showInputRefusal} = await import('data:text/javascript,' + encodeURIComponent(renderer));
        const sent = [], cleared = [];
        const S = {ptySessionId:'migrate', ptyWingId:'studio', e2eKey:{}, ptyControllerId:'old',
            ptyWs:{readyState:1, send:raw=>sent.push(raw)}};
        const DOM = {ptyStatus:{textContent:'new terminal'}};
        let finish;
        const send = new Function('S','DOM','e2eEncrypt','terminalReferenceMatches','sendViaDC',
            'clearNotification','WebSocket','showInputRefusal', sendBody + '\nreturn sendPTYInput;')(
            S, DOM, () => new Promise(resolve=>{finish=resolve;}),
            (state,id,wing,socket)=>state.ptySessionId===id && state.ptyWingId===wing && state.ptyWs===socket,
            ()=>false, (...args)=>cleared.push(args), {OPEN:1}, showInputRefusal);
        const pending = send('do not send to the next session');
        S.ptySessionId = 'notes'; S.ptyWingId = 'macbook';
        finish('encrypted'); await pending;
        return {sent, cleared, status:DOM.ptyStatus.textContent};
    }, {sendBody, renderer});
    assert.deepEqual(result, {sent:[],cleared:[],status:'new terminal'});
    const notice = page.locator('#input-refusals [role="alert"]');
    await notice.waitFor();
    assert.match(await notice.textContent(), /Input not sent: terminal connection changed/);
    assert.equal(await notice.locator('a').getAttribute('href'), '#session/studio/migrate');
    const contrast = await notice.evaluate(node => {
        const luminance = value => {
            const rgb = value.match(/[\d.]+/g).slice(0,3).map(Number).map(x => {
                x /= 255; return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
            });
            return rgb[0]*0.2126 + rgb[1]*0.7152 + rgb[2]*0.0722;
        };
        const style = getComputedStyle(node);
        const a = luminance(style.color), b = luminance(style.backgroundColor);
        return (Math.max(a,b)+0.05)/(Math.min(a,b)+0.05);
    });
    assert.ok(contrast >= 4.5, 'Refusal text must remain readable: ' + contrast);
    await page.waitForTimeout(2200);
    assert.equal(await notice.count(), 1, 'refresh must retain the refusal until dismissal');
    await shoot(page, 'input-refusal-navigation', '-' + page.viewportSize().width);
    await notice.locator('a').click();
    await page.waitForFunction(()=>location.hash==='#session/studio/migrate');
    assert.equal(await notice.count(), 1, 'opening the origin must retain the warning');
    await notice.getByRole('button', {name:'Dismiss input warning'}).click();
    assert.equal(await page.locator('#input-refusals').count(), 0);
    await page.evaluate(async renderer => {
        const {showInputRefusal} = await import('data:text/javascript,' + encodeURIComponent(renderer));
        for (let i=0;i<8;i++) showInputRefusal('same-session', 'wing-'+i, 'Input not sent: disconnected.');
        showInputRefusal('same-session', 'wing-0', 'Input not sent: terminal unavailable.');
    }, renderer);
    assert.equal(await notice.count(), 8, 'Same-session IDs on different wings have independent warnings');
    assert.equal(await notice.filter({visible:true}).count(), 3);
    assert.equal(await page.locator('#input-refusals summary').textContent(), '+5 more');
    await page.locator('#input-refusals summary').click();
    assert.equal(await notice.filter({visible:true}).count(), 8);
    for (let i=0;i<8;i++) await notice.first().getByRole('button', {name:'Dismiss input warning'}).click();
    assert.equal(await page.locator('#input-refusals').count(), 0);
    checked('input-refusal-navigation-' + page.viewportSize().width, result);
}

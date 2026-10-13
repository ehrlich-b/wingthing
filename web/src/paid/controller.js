import { cards } from './cards.js';
import { createPaidState, applyPaidAction, fixtureDownload, FIXTURE_VERSION } from './model.js';
import { parseUSD } from './currency.js';
import { renderPaidView, visiblePaidCards } from './view.js';
import './paid.css';
const STORAGE = 'wingthing-dormant-paid-fixture-v1';
export function paidFixtureAllowed(user) { return user?.paid_fixture === true; }
export function mountPaidFixture(user) {
    if (!paidFixtureAllowed(user)) return false;
    if (document.getElementById('paid-root')) return true;
    let s = createPaidState(), profile = 'free', error = '';
    try {
        const saved = JSON.parse(sessionStorage.getItem(STORAGE));
        if (saved?.state?.fixture === true && saved.state.version === FIXTURE_VERSION) { s = saved.state; profile = saved.profile; }
    } catch { /* Fresh tab / invalid dev fixture state. */ }
    const root = document.createElement('div'); root.id = 'paid-root'; document.body.appendChild(root);
    document.getElementById('app').hidden = true;
    document.body.classList.add('paid-active');
    function save() { try { sessionStorage.setItem(STORAGE, JSON.stringify({state:s,profile})); } catch { /* Read-only/storage-denied browser remains usable. */ } }
    function card() { const visible=visiblePaidCards(s); return visible.find(c => location.hash === `#paid/${c.group}/${c.key}`) || visible[0]; }
    function render(focus = false) {
        root.innerHTML = renderPaidView(card(),s,error);
        document.getElementById('paid-profile').value = profile;
        if (focus) { root.querySelector('h1').focus({preventScroll:true}); window.scrollTo(0,0); }
    }
    function dispatch(action) {
        try { s = applyPaidAction(s,action); error = ''; save(); }
        catch (e) { error = e.message; }
        render();
    }
    function download(kind) {
        try {
            const value = fixtureDownload(s,kind), blob = new Blob([value], {type:'application/json'});
            const url = URL.createObjectURL(blob), a = document.createElement('a'); a.href=url; a.download=`dormant-fixture-${kind}.json`; a.click();
            setTimeout(()=>URL.revokeObjectURL(url),0); error=''; s.message='Fixture download prepared locally. No external request.';
        } catch(e) { error=e.message; }
        render();
    }
    root.addEventListener('click', event => {
        const target = event.target.closest('button,a'); if (!target) return;
        if (target.hasAttribute('data-exit')) { event.preventDefault(); location.href='/app/#account'; location.reload(); return; }
        if (target.id==='paid-reset') { profile=document.getElementById('paid-profile').value; s=createPaidState(profile); error=''; save(); render(); return; }
        if (target.dataset.action) dispatch(JSON.parse(target.dataset.action));
        if (target.dataset.download) download(target.dataset.download);
    });
    root.addEventListener('change', event => {
        if (event.target.id === 'paid-route') location.hash = event.target.value;
        if (event.target.dataset.preference) dispatch({op:'notification',name:event.target.dataset.preference,enabled:event.target.checked});
    });
    root.addEventListener('input', event => {
        const form=event.target.closest('form'); if (!form) return;
        if (event.target.name==='capUSD') {
            const output=form.querySelector('[data-cap-preview]'), submit=form.querySelector('[type="submit"]');
            try { const cents=parseUSD(event.target.value); output.textContent='$'+(cents/100).toFixed(2)+' USD'; submit.disabled=false; }
            catch(e) { output.textContent=e.message; submit.disabled=true; }
        }
        if (event.target.name==='volumeId') form.querySelector('[type="submit"]').disabled=!(s.deletionReview && s.cloud?.status==='stopped' && event.target.value===s.cloud.volumeId);
    });
    root.addEventListener('submit', event => {
        if (!event.target.dataset.operation) return;
        event.preventDefault(); const op=event.target.dataset.operation, values = Object.fromEntries(new FormData(event.target));
        if ('capUSD' in values) {
            try { values.capCents=parseUSD(values.capUSD); delete values.capUSD; }
            catch(e) { error=e.message; render(); return; }
        }
        if ('hours' in values) values.hours=Number(values.hours);
        for (const name of ['overage','consent','revoked']) if (event.target.querySelector(`[name="${name}"][type="checkbox"]`)) values[name]=!!event.target.querySelector(`[name="${name}"]`).checked;
        dispatch({op,...values});
    });
    window.addEventListener('hashchange', () => { if (!location.hash.startsWith('#paid/')) { location.reload(); return; } error=''; render(true); });
    render(); return true;
}
export function installPaidFixtureEntry(user) {
    if (!paidFixtureAllowed(user)) return false;
    if (location.hash.startsWith('#paid/')) return mountPaidFixture(user);
    const host=document.getElementById('header-right');
    const entry=document.createElement('a'); entry.className='btn-sm'; entry.id='paid-fixture-entry'; entry.href='#paid/upgrade/offers'; entry.textContent='paid · dormant fixture'; host.prepend(entry);
    entry.addEventListener('click',()=> { setTimeout(()=>mountPaidFixture(user),0); });
    return false;
}

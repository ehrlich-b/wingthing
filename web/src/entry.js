// Pair is accountless and loads without the account app's identity/storage setup.
const pairRoute = () => location.hash === '#pair' || location.hash.startsWith('#v1z.');
async function start() {
if (pairRoute()) {
    const { initBrowserPair } = await import('./browser-pair.js');
    await initBrowserPair();
} else {
    window.addEventListener('hashchange', () => { if (pairRoute()) location.reload(); });
    await import('./main.js');
}

}
start().catch(() => { document.body.textContent = "Pair page unavailable. Reload to try again."; });

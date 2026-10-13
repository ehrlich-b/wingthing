export function initPWA() {
    let prompt = null;
    const button = document.getElementById('pwa-install');
    const banner = document.getElementById('pwa-connection');
    function connection() { if (banner) banner.hidden = navigator.onLine !== false; }
    window.addEventListener('offline', connection);
    window.addEventListener('online', connection);
    connection();
    window.addEventListener('beforeinstallprompt', event => {
        event.preventDefault(); prompt = event;
        if (button) button.hidden = false;
    });
    window.addEventListener('appinstalled', () => { prompt = null; if (button) button.hidden = true; });
    button?.addEventListener('click', async () => {
        if (!prompt) return;
        const current = prompt; prompt = null; button.hidden = true;
        try { await current.prompt(); await current.userChoice; } catch { /* Safari's Share menu remains available. */ }
    });
    if ('serviceWorker' in navigator && window.isSecureContext) {
        navigator.serviceWorker.register(import.meta.env.BASE_URL + 'sw.js', { scope: import.meta.env.BASE_URL, updateViaCache: 'none' }).catch(() => { /* Online use still works without installation. */ });
    }
}

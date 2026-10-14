const DB = 'wt-paired-wings-v1';
const STORE = 'devices';

async function open() {
    if (!globalThis.isSecureContext || !crypto.subtle || !globalThis.indexedDB) throw new Error('storage');
    return new Promise((resolve, reject) => {
        const request = indexedDB.open(DB, 1);
        request.onupgradeneeded = () => {
            const store = request.result.createObjectStore(STORE, { keyPath: 'wing_id' });
            store.createIndex('pin', 'key');
            store.createIndex('device_id', 'device_id', { unique: true });
        };
        request.onsuccess = () => resolve(request.result);
        request.onerror = request.onblocked = () => reject(new Error('storage'));
    }).catch(() => { throw new Error('storage'); });
}
async function transaction(mode, work) {
    const db = await open();
    try {
        return await new Promise((resolve, reject) => {
            const tx = db.transaction(STORE, mode);
            let result;
            tx.oncomplete = () => resolve(result?.result);
            tx.onerror = tx.onabort = () => reject(new Error('storage'));
            try { result = work(tx.objectStore(STORE)); } catch { tx.abort(); reject(new Error('storage')); }
        });
    } catch { throw new Error('storage'); } finally { db.close(); }
}
export const loadDevice = wing => transaction('readonly', store => store.get(wing));
export const listDevices = () => transaction('readonly', store => store.getAll());
export const forgetDevice = (wing, deviceID) => transaction('readwrite', store => {
    if (!deviceID) return store.delete(wing);
    const request = store.get(wing);
    request.onsuccess = () => { if (request.result?.device_id === deviceID) store.delete(wing); };
    return request;
});
export async function saveDevice(device) {
    if (device.signingKey?.extractable !== false || device.signingKey.type !== 'private' || device.scope !== 'inventory') throw new Error('storage');
    try {
        await transaction('readwrite', store => store.put(device));
        const saved = await loadDevice(device.wing_id);
        if (!saved || saved.device_id !== device.device_id || saved.signingKey?.extractable !== false) throw new Error('storage');
    } catch {
        // No commit is sent after any storage failure. Also remove any partial
        // local record; an unavailable store still cannot activate the grant.
        try { await forgetDevice(device.wing_id, device.device_id); } catch {}
        throw new Error('storage');
    }
}
export async function activateDevice(device) {
    await transaction('readwrite', store => {
        const request = store.get(device.wing_id);
        request.onsuccess = () => {
            const saved = request.result;
            if (!saved || saved.device_id !== device.device_id) { request.transaction.abort(); return; }
            saved.ready = true;
            store.put(saved);
        };
        return request;
    });
}
export async function prepareSigningKey() {
    if (!globalThis.isSecureContext || !crypto.subtle) throw new Error('storage');
    const keys = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']);
    // Verify that this origin can durably clone a non-exportable CryptoKey
    // before asking either person to approve. No invitation data is stored.
    const probe = { wing_id: 'probe:' + crypto.randomUUID(), signingKey: keys.privateKey };
    try {
        await transaction('readwrite', store => store.put(probe));
        const saved = await loadDevice(probe.wing_id);
        if (saved?.signingKey?.extractable !== false) throw new Error('storage');
        await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, saved.signingKey, new Uint8Array(1));
    } catch { throw new Error('storage'); }
    finally { await forgetDevice(probe.wing_id); }
    return { signingKey: keys.privateKey, publicKey: new Uint8Array(await crypto.subtle.exportKey('raw', keys.publicKey)) };
}

// WebCrypto ECDSA returns IEEE P1363 r||s; Go verifies ASN.1 DER.
export function signatureDER(raw) {
    const bytes = new Uint8Array(raw);
    if (bytes.length !== 64) throw new Error('bad_proof');
    const integer = part => {
        let start = 0;
        while (start < part.length - 1 && part[start] === 0) start++;
        part = part.slice(start);
        if (part[0] & 128) part = Uint8Array.of(0, ...part);
        return Uint8Array.of(2, part.length, ...part);
    };
    const r = integer(bytes.slice(0, 32)), s = integer(bytes.slice(32));
    return Uint8Array.of(48, r.length + s.length, ...r, ...s);
}

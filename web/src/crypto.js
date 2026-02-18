import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';
import { b64ToBytes, bytesToB64 } from './helpers.js';
import { S } from './state.js';

// Browser identity key (sessionStorage — ephemeral per tab, provides PFS)
var IDENTITY_PUBKEY_KEY = 'wt_identity_pubkey';
var IDENTITY_PRIVKEY_KEY = 'wt_identity_privkey';

function getOrCreateIdentityKey() {
    try {
        var storedPub = sessionStorage.getItem(IDENTITY_PUBKEY_KEY);
        var storedPriv = sessionStorage.getItem(IDENTITY_PRIVKEY_KEY);
        if (storedPub && storedPriv) return { pub: storedPub, priv: b64ToBytes(storedPriv) };
        var priv = x25519.utils.randomSecretKey();
        sessionStorage.setItem(IDENTITY_PRIVKEY_KEY, bytesToB64(priv));
        var pub = bytesToB64(x25519.getPublicKey(priv));
        sessionStorage.setItem(IDENTITY_PUBKEY_KEY, pub);
        return { pub: pub, priv: priv };
    } catch (e) { return { pub: '', priv: null }; }
}

export var identityKey = getOrCreateIdentityKey();
export var identityPubKey = identityKey.pub;

// Pure JS HKDF + AES-GCM — works without crypto.subtle / secure context
export async function deriveE2EKey(wingPublicKeyB64) {
    if (!identityKey.priv) return null;
    var wingPubBytes = b64ToBytes(wingPublicKeyB64);
    var shared = x25519.getSharedSecret(identityKey.priv, wingPubBytes);
    var salt = new Uint8Array(32);
    var enc = new TextEncoder();
    return hkdf(sha256, shared, salt, enc.encode('wt-pty'), 32);
}

export async function e2eEncrypt(plaintext) {
    if (!S.e2eKey) return btoa(unescape(encodeURIComponent(plaintext)));
    var enc = new TextEncoder();
    var iv = crypto.getRandomValues(new Uint8Array(12));
    var cipher = gcm(S.e2eKey, iv);
    var ciphertext = cipher.encrypt(enc.encode(plaintext));
    var result = new Uint8Array(iv.length + ciphertext.length);
    result.set(iv);
    result.set(ciphertext, iv.length);
    return bytesToB64(result);
}

export async function e2eDecrypt(encoded) {
    if (!S.e2eKey) {
        var binary = atob(encoded);
        var bytes = new Uint8Array(binary.length);
        for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
        return bytes;
    }
    var data = b64ToBytes(encoded);
    var iv = data.slice(0, 12);
    var ciphertext = data.slice(12);
    var cipher = gcm(S.e2eKey, iv);
    var plaintext = cipher.decrypt(ciphertext);
    return new Uint8Array(plaintext);
}

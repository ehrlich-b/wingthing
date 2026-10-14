// Shared acceptance suite runs unchanged in Node and in a real browser.
const utf8 = new TextEncoder();
const text = new TextDecoder();
export const toBase64 = bytes => btoa(String.fromCharCode(...bytes));
export const fromBase64 = value => Uint8Array.from(atob(value), c => c.charCodeAt(0));
const assert = (ok, message) => { if (!ok) throw new Error(message); };

async function pairKey(shared, q, alice, bob) {
  const transcript = `wt-pair-v1\n${q.wing_id}\n${q.key}\n${q.id}\n${alice}\n${bob}`;
  const salt = await crypto.subtle.digest('SHA-256', utf8.encode(transcript));
  const ikm = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveBits']);
  const key = new Uint8Array(await crypto.subtle.deriveBits({name: 'HKDF', hash: 'SHA-256',
    salt, info: utf8.encode('wt-pair-v1')}, ikm, 256));
  return key;
}

async function encrypt(key, plaintext) {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const imported = await crypto.subtle.importKey('raw', key, 'AES-GCM', false, ['encrypt']);
  const sealed = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, imported, utf8.encode(plaintext)));
  const wire = new Uint8Array(iv.length + sealed.length);
  wire.set(iv); wire.set(sealed, iv.length);
  return toBase64(wire);
}

async function decrypt(key, wire) {
  const bytes = fromBase64(wire);
  const imported = await crypto.subtle.importKey('raw', key, 'AES-GCM', false, ['decrypt']);
  return text.decode(await crypto.subtle.decrypt({name: 'AES-GCM', iv: bytes.subarray(0, 12)}, imported, bytes.subarray(12)));
}

async function emoji(key, vocabulary) {
  const imported = await crypto.subtle.importKey('raw', key, {name: 'HMAC', hash: 'SHA-256'}, false, ['sign']);
  const digest = new Uint8Array(await crypto.subtle.sign('HMAC', imported, utf8.encode('wt-emoji-v1')));
  return Array.from(digest.subarray(0, 4), b => vocabulary[b]).join(' ');
}

export async function runSuite(pake, rpc, vocabulary, count = 200) {
  const timings = [];
  const seen = new Set();
  let success = 0;
  const cases = [];
  async function exchange(alice, fault = '') {
    const go = await rpc({op: 'start', alice: !alice});
    let secret = go.invitation.secret;
    let wingIdentity = go.identity;
    if (fault === 'secret') secret += ' wrong';
    if (fault === 'wing-identity') wingIdentity += ':wrong';
    const names = fault === 'swapped-identities'
      ? {myIdentity: alice ? wingIdentity : 'wt-phone-v1', peerIdentity: alice ? 'wt-phone-v1' : wingIdentity}
      : fault === 'phone-identity'
        ? (alice ? {myIdentity: 'wt-phone-v2'} : {peerIdentity: 'wt-phone-v2'}) : {};
    let ex;
    let shared;
    try {
      const before = performance.now();
      ex = pake.start({alice, secret, wingIdentity, ...names});
      const startTime = performance.now() - before;
      assert(ex.message.length === 32, 'message size');
      const message = toBase64(ex.message);
      const finishBefore = performance.now();
      shared = ex.finish(fromBase64(go.message));
      const elapsed = startTime + performance.now() - finishBefore;
      assert(shared.length === 64, 'shared size');
      let reused = false;
      try { ex.finish(fromBase64(go.message)); } catch { reused = true; }
      assert(reused, 'single-use context');
      const am = alice ? message : go.message;
      const bm = alice ? go.message : message;
      const key = await pairKey(shared, go.invitation, am, bm);
      try {
        const proof = await encrypt(key, 'browser-pake proof');
        const result = await rpc({op: 'finish', message, proof});
        const equal = toBase64(shared) === result.shared;
        let confirmed = false;
        try { confirmed = await decrypt(key, result.sealed) === 'go-pake proof'; } catch { /* wrong key */ }
        if (fault) {
          assert(!equal && !result.proofOK && !confirmed, `accepted ${fault}`);
          cases.push(`${alice ? 'wasm-alice' : 'wasm-bob'}:${fault}:rejected`);
        } else {
          assert(equal && result.proofOK && confirmed, 'shared key or AES-GCM confirmation differs');
          assert(await emoji(key, vocabulary) === result.emoji, 'emoji mismatch');
          assert(!seen.has(message) && !seen.has(go.message), 'repeated randomized message');
          seen.add(message); seen.add(go.message);
          success++;
          timings.push(elapsed);
        }
      } finally { key.fill(0); }
    } finally {
      ex?.close(); shared?.fill(0);
      await rpc({op: 'close'});
      assert(pake.liveContexts() === 0, 'WASM context leaked');
    }
  }
  // 200 randomized successes per role, using the real native peer each time.
  for (const alice of [true, false]) {
    for (let i = 0; i < count; i++) await exchange(alice);
    for (const fault of ['secret', 'wing-identity', 'phone-identity', 'swapped-identities']) await exchange(alice, fault);
  }
  // Explicit cancellation, malformed peer and RNG exception release their state.
  const fixture = {alice: true, secret: 'local cancellation fixture', wingIdentity: 'local-fixture'};
  const cancelled = pake.start(fixture);
  cancelled.close(); cancelled.close();
  const offCurve = new Uint8Array(32); offCurve[0] = 2;
  for (const message of [new Uint8Array(31), new Uint8Array(33), offCurve]) {
    const ex = pake.start(fixture);
    let refused = false;
    try { ex.finish(message); } catch { refused = true; }
    finally { ex.close(); }
    assert(refused && pake.liveContexts() === 0, 'malformed peer accepted or leaked');
  }
  const original = crypto.getRandomValues;
  try {
    crypto.getRandomValues = () => { throw new Error('RNG unavailable'); };
    let refused = false;
    try { pake.start(fixture); } catch { refused = true; }
    assert(refused && pake.liveContexts() === 0, 'RNG error accepted or leaked');
  } finally { crypto.getRandomValues = original; }
  assert(pake.errors() === 0, 'allocation diagnostic errors');
  timings.sort((a, b) => a - b);
  return {success, perRole: count, negative: cases.length, cases,
    messageBytes: 32, sharedBytes: 64, uniqueMessages: seen.size,
    confirmation: 'Go phonepair.PairKey AES-GCM both directions and emoji',
    liveContexts: pake.liveContexts(), memoryBytes: pake.memoryBytes(),
    timingMs: {mean: timings.reduce((a, b) => a + b, 0) / timings.length,
      p50: timings[Math.floor(timings.length / 2)], p95: timings[Math.floor(timings.length * .95)]}};
}

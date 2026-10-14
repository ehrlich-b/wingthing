// BoringSSL SPAKE2 with browser entropy and single-use, explicitly freed exchanges.
const utf8 = new TextEncoder();

export async function loadPake(bytes) {
  let wasm;
  let entropy;
  const module = await WebAssembly.compile(bytes);
  for (const entry of WebAssembly.Module.imports(module)) {
    if (entry.module !== 'host' || !['random', 'abort'].includes(entry.name)) {
      throw new Error(`Unexpected WASM import: ${entry.module}.${entry.name}`);
    }
  }
  const instance = await WebAssembly.instantiate(module, { host: {
    random(ptr, length) {
      // SPAKE2 requests exactly 64 bytes once. Fetch them before entering WASM
      // so an unavailable host RNG cannot unwind the module's C shadow stack.
      if (!entropy || length !== 64) throw new Error('Unexpected SPAKE2 entropy request');
      new Uint8Array(wasm.memory.buffer, ptr, length).set(entropy);
      entropy.fill(0); entropy = undefined;
    },
    abort() { throw new Error('BoringSSL aborted'); },
  } });
  wasm = instance.exports;
  wasm._initialize();
  const view = () => new Uint8Array(wasm.memory.buffer);
  function alloc(data) {
    const p = wasm.pake_alloc(data.length || 1);
    if (!p) throw new Error('WASM allocation failed');
    view().set(data, p);
    return p;
  }
  function scoped(data, fn) {
    const ptr = alloc(data);
    try { return fn(ptr); } finally { wasm.pake_free(ptr); }
  }
  return {
    start({ alice, secret, wingIdentity, myIdentity, peerIdentity }) {
      const mine = utf8.encode(myIdentity ?? (alice ? 'wt-phone-v1' : wingIdentity));
      const peer = utf8.encode(peerIdentity ?? (alice ? wingIdentity : 'wt-phone-v1'));
      const password = utf8.encode(secret);
      let ctx = 0;
      try {
        entropy = globalThis.crypto.getRandomValues(new Uint8Array(64));
        const message = scoped(mine, m => scoped(peer, p => {
          ctx = wasm.pake_new(alice ? 1 : 0, m, mine.length, p, peer.length);
          if (!ctx) throw new Error('SPAKE2 initialization failed');
          return scoped(password, s => scoped(new Uint8Array(32), out => {
            if (!wasm.pake_generate(ctx, s, password.length, out)) throw new Error('SPAKE2 generation failed');
            return view().slice(out, out + 32);
          }));
        }));
        const close = () => {
          if (ctx) { wasm.pake_close(ctx); ctx = 0; }
        };
        return {
          message,
          close,
          finish(message) {
            if (!ctx) throw new Error('SPAKE2 context used');
            try {
              return scoped(message, p => scoped(new Uint8Array(64), out => {
                if (!wasm.pake_finish(ctx, p, message.length, out)) throw new Error('SPAKE2 peer message refused');
                return view().slice(out, out + 64);
              }));
            } finally { close(); }
          },
        };
      } catch (error) {
        if (ctx) wasm.pake_close(ctx);
        throw error;
      } finally {
        password.fill(0); entropy?.fill(0); entropy = undefined;
      }
    },
    liveContexts: () => wasm.pake_live(),
    errors: () => wasm.pake_errors(),
    memoryBytes: () => wasm.memory.buffer.byteLength,
  };
}

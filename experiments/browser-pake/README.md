# Browser SPAKE2 interoperability experiment

This standalone experiment compiles the same BoringSSL SPAKE2 that
`internal/pake` uses to wasm32. It is separate from the web application and
does not enroll devices or communicate with a relay.

The build uses the eight portable sources listed in `build.sh`, unmodified,
with linker garbage collection. `wrapper.c` provides a C ABI, a cryptographic
host entropy hook, assertion termination and allocation diagnostics. The
single-thread module imports only `host.random` and `host.abort`; no WASI
filesystem, networking, environment or RNG interface is exposed. The loader
obtains a fresh 64-byte `crypto.getRandomValues` sample before each generation.
There is no deterministic RNG fallback. Call `close()` on abandoned exchanges;
`finish()` releases state on success or failure and refuses reuse.

## Build and run

Requires clang/clang++ 19, wasm-ld 19, Go with CGO and a native C++ compiler,
Node with WebCrypto, and a local headless Chromium executable. No new npm
dependencies are used. Use a disposable HOME for all native peers and tests.

From the repository root, download the pinned WASI sysroot and compiler builtins:

```sh
mkdir -p .scratch
curl -fL --retry 2 -o .scratch/wasi-sdk.tar.gz \
  https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-25/wasi-sdk-25.0-x86_64-linux.tar.gz
printf '%s\n' '52640dde13599bf127a95499e61d6d640256119456d1af8897ab6725bcf3d89c  .scratch/wasi-sdk.tar.gz' | sha256sum -c -
nice -n 10 tar -xzf .scratch/wasi-sdk.tar.gz -C .scratch
export WASI_SDK_PATH="$PWD/.scratch/wasi-sdk-25.0-x86_64-linux"
make browser-pake
```

Set `CHROMIUM_PATH` to your installed Chromium binary, then run:

```sh
mkdir -p .scratch/browser-pake/home
export HOME="$PWD/.scratch/browser-pake/home"
export GOFLAGS=-p=2 GOMAXPROCS=2
make check-browser-pake GO='nice -n 10 go' NODE='nice -n 10 node'
make test-pairing GO='nice -n 10 go'
```

Outputs are in `.scratch/browser-pake`: `pake.wasm`, compiler dependency files,
`pake.map`, `sources.txt`, `result.json` and browser diagnostics. Chromium's
profile and HOME are disposable, and the fixture server binds only to loopback.
Chromium's sandbox is enabled by default. A host that cannot run it can explicitly
set `CHROMIUM_NO_SANDBOX=1`; the result records that condition. Missing Chromium
or any failed browser assertion fails the target.

## What the check proves

`go-peer` calls the real `internal/pake.New/Finish`, not translated Go or JS
arithmetic. Each runtime performs 200 fresh successful exchanges as Alice and
200 as Bob against that native peer. Every success checks all 64 shared bytes,
authenticated AES-GCM proofs in both directions using Go's real
`phonepair.PairKey`, and the exact emoji sequence from `EmojiFromShared`.
The JS side uses WebCrypto for HKDF, AES-GCM and HMAC; the emoji table is read
directly from the repository's ordered vocabulary.

Wrong secret, wrong wing identity, wrong phone identity and swapped identities
must produce unequal keys and failed encrypted confirmation, in both roles.
SPAKE2 itself does not report wrong-password failure; callers must confirm the
key before authorizing anything. Cancellation, malformed messages, RNG failure,
single-use enforcement and zero remaining contexts are also checked.

The headless Chromium run executes the same suite in an actual secure-context
browser page, exchanging messages with the native peer via a local fixture
server. Browser background network services are disabled and name resolution
is restricted to loopback. It is a cryptographic interoperability test, with no
Pair page, relay route, IndexedDB, device credential or capability changes.

`result.json` reports raw bytes, gzip level-9 bytes, module SHA-256, imports,
runtime versions and mean/p50/p95 time. Timing sums one WASM side's `start` and
`finish`, including host entropy and allocation/free; it excludes native peer
time, IPC, WebCrypto confirmation and module loading. Both roles are included,
and no warmup samples are excluded. These are local measurements, not an
enrollment latency claim.

The vendored sources and their licenses are described in
[`internal/pake/README.md`](../../internal/pake/README.md). This build uses the
portable non-FIPS configuration; interoperability does not establish browser
constant-time behavior or production enrollment acceptance.

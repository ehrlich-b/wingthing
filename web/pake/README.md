# Browser PAKE asset

The Pair page imports `web/src/pake/pake.wasm` through Vite. Rebuild with
`WASI_SDK_PATH=/path/to/wasi-sdk-25.0 make browser-pake`. The checked-in asset
keeps ordinary web/release builds independent of the WASI toolchain.
`make check-browser-pake` rebuilds it and checks native Go interoperability in
Node and Chromium. See `experiments/browser-pake/README.md` for prerequisites.

`build.sh` compiles eight unmodified portable translation units from the
vendored BoringSSL tree, plus `wrapper.c`. Their provenance and preserved
licenses are in `internal/pake/README.md`. The only host imports are secure
randomness and abort. The loader frees each exchange, including failures.
No filesystem, network or environment interfaces are imported by WASM.

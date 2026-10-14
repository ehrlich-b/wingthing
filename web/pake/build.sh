#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"
: "${WASI_SDK_PATH:?Set WASI_SDK_PATH to the extracted wasi-sdk-25.0 directory}"
cxx=${WASM_CXX:-clang++-19}
cc=${WASM_CC:-clang-19}
out=.scratch/browser-pake
src=internal/pake/boringssl
mkdir -p "$out"
# Exactly the portable SPAKE2 dependency closure; no upstream source edits.
sources='crypto/curve25519/spake25519.cc
crypto/curve25519/curve25519.cc
crypto/fipsmodule/bn/bn.cc.inc
crypto/fipsmodule/bn/generic.cc.inc
crypto/sha/sha512.cc
crypto/fipsmodule/sha/sha512.cc.inc
crypto/mem.cc
crypto/bytestring/cbs.cc'
flags="--target=wasm32-wasi --sysroot=$WASI_SDK_PATH/share/wasi-sysroot -Oz -DOPENSSL_NO_ASM -DOPENSSL_NO_THREADS_CORRUPT_MEMORY_AND_LEAK_SECRETS_IF_THREADED -ffunction-sections -fdata-sections -I$src/include"
# Sequential compilation is bounded below the two-job budget.
i=0
for file in $sources; do
  i=$((i + 1))
  nice -n 10 "$cxx" $flags -isystem "$WASI_SDK_PATH/share/wasi-sysroot/include/c++/v1" \
    -std=c++17 -fno-exceptions -fno-rtti -MMD -MF "$out/$i.d" \
    -x c++ -c "$src/$file" -o "$out/$i.o"
done
nice -n 10 "$cc" $flags -c web/pake/wrapper.c -o "$out/wrapper.o"
nice -n 10 "$cc" --target=wasm32-wasi --sysroot="$WASI_SDK_PATH/share/wasi-sysroot" \
  -resource-dir "$WASI_SDK_PATH/lib/clang/19" \
  -mexec-model=reactor -Wl,--gc-sections -Wl,--strip-all \
  -Wl,--initial-memory=2097152 -Wl,--max-memory=16777216 \
  -Wl,--export=pake_new -Wl,--export=pake_generate -Wl,--export=pake_finish -Wl,--export=pake_close \
  -Wl,--export=pake_alloc -Wl,--export=pake_free -Wl,--export=pake_live \
  -Wl,--export=pake_errors -Wl,-Map,"$out/pake.map" \
  "$out"/*.o -o "$out/pake.wasm"
printf '%s\n' "$sources" > "$out/sources.txt"

cp "$out/pake.wasm" web/src/pake/pake.wasm
chmod 644 web/src/pake/pake.wasm

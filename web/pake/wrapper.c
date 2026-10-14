// Experimental C ABI around the repository's unmodified BoringSSL SPAKE2.
#include <CCryptoBoringSSL_curve25519.h>
#include <CCryptoBoringSSL_err.h>
#include <CCryptoBoringSSL_mem.h>
#include <CCryptoBoringSSL_rand.h>
#include <stdlib.h>

__attribute__((import_module("host"), import_name("random")))
extern void host_random(uint8_t *out, size_t len);
__attribute__((import_module("host"), import_name("abort"), noreturn))
extern void host_abort(void);

// This is the only entropy path. Host exceptions abort generation; SPAKE2's
// upstream RAND_bytes call expects success or termination, never partial output.
int RAND_bytes(uint8_t *out, size_t len) {
  host_random(out, len);
  return 1;
}

// Single-instance diagnostic counter avoids linking a native thread error queue.
// BoringSSL's allocation failures still return NULL and the ABI propagates them.
static unsigned errors;
void ERR_put_error(int library, int unused, int reason, const char *file, unsigned line) {
  (void)library; (void)unused; (void)reason; (void)file; (void)line;
  errors++;
}
unsigned pake_errors(void) { return errors; }
void abort(void) { host_abort(); }
// Preserve assertion failure without importing a filesystem/stdio runtime.
void __assert_fail(const char *expr, const char *file, int line, const char *func) {
  (void)expr; (void)file; (void)line; (void)func;
  host_abort();
}

static unsigned live;
unsigned pake_live(void) { return live; }

SPAKE2_CTX *pake_new(int alice, const uint8_t *mine, size_t mine_len,
                    const uint8_t *peer, size_t peer_len) {
  SPAKE2_CTX *ctx = SPAKE2_CTX_new(alice ? spake2_role_alice : spake2_role_bob,
                                mine, mine_len, peer, peer_len);
  if (!ctx) return NULL;
  live++;
  return ctx;
}

int pake_generate(SPAKE2_CTX *ctx, const uint8_t *secret, size_t secret_len,
                  uint8_t *message) {
  size_t n = 0;
  return SPAKE2_generate_msg(ctx, message, &n, 32, secret, secret_len) == 1 && n == 32;
}

void pake_close(SPAKE2_CTX *ctx) {
  if (ctx) {
    SPAKE2_CTX_free(ctx);
    live--;
  }
}

int pake_finish(SPAKE2_CTX *ctx, const uint8_t *message, size_t message_len,
                uint8_t *key) {
  size_t n = 0;
  int ok = SPAKE2_process_msg(ctx, key, &n, 64, message, message_len);
  return ok == 1 && n == 64;
}

void *pake_alloc(size_t n) { return OPENSSL_malloc(n); }
void pake_free(void *p) { OPENSSL_free(p); }

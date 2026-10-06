package egg

// OpenCode 1.18.13 calls event synchronously without awaiting its promise, and
// awaits dispose. Publish synchronously so events cannot overtake each other.
// No SDK imports, dependency installation, client calls or config writes.
const openCodeLifecyclePlugin = `import { readFileSync, writeFileSync, linkSync, unlinkSync } from 'node:fs';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';

export const WingthingLifecycle = async () => {
  const spool = __SPOOL__;
  let sessionID = __PROVIDER_ID__, sequence = 0, state = 'idle', failed = false;
  const pending = new Set();
  let restored = false;
  try {
    const bound = JSON.parse(readFileSync(join(spool, 'binding'), 'utf8')).session_id;
    if (typeof bound === 'string' && bound && (!sessionID || sessionID === bound)) {
      sessionID = bound; restored = true; state = 'unknown';
    }
  } catch {}
  function emit(name, extra = {}) {
    if (!sessionID) return;
    if (name === 'SessionStart') {
      try { writeFileSync(join(spool, 'binding'), JSON.stringify({session_id: sessionID}), {mode: 0o600, flag: 'wx'}); } catch {}
    }
    const temp = join(spool, 'event.' + randomUUID());
    try {
      writeFileSync(temp, JSON.stringify({session_id: sessionID, hook_event_name: name, ...extra}), {mode: 0o600, flag: 'wx'});
      for (;;) {
        const file = join(spool, 'seq.' + String(++sequence).padStart(20, '0') + '.json');
        try { linkSync(temp, file); break; }
        catch (error) { if (error.code !== 'EEXIST') throw error; }
      }
    } catch {} finally { try { unlinkSync(temp); } catch {} }
  }
  function activity(name = 'PostToolUse') {
    if (pending.size) return;
    state = 'working'; emit(name);
  }
  function end() {
    emit(state === 'idle' && !failed ? 'SessionEnd' : 'Interrupt');
  }
  if (sessionID) emit(restored ? 'ProviderReloaded' : 'SessionStart');
  return {
    'chat.message'(input) {
      // Fresh roots bind from session.created, resumed sessions use their exact
      // invocation ID. A child's prompt can never select the parent identity.
      if (input.sessionID !== sessionID) return;
      failed = false;
      activity('UserPromptSubmit');
    },
    event({event}) {
      const p = event.properties;
      if (event.type === 'session.created' && !p.info.parentID && !sessionID) {
        sessionID = p.info.id; state = 'idle'; emit('SessionStart');
      }
      if (!sessionID || (p.sessionID ?? p.info?.id) !== sessionID) return;
      switch (event.type) {
        case 'session.status':
          if (p.status.type === 'idle') { pending.clear(); state = failed ? 'failed' : 'idle'; emit(failed ? 'StopFailure' : 'Stop'); }
          else if (p.status.type === 'busy' || p.status.type === 'retry') activity();
          break;
        case 'permission.asked': case 'question.asked':
          pending.add(p.id); state = 'blocked'; emit('PermissionRequest'); break;
        case 'permission.replied': case 'question.replied': case 'question.rejected':
          pending.delete(p.requestID); activity(); break;
        case 'session.error': failed = true; state = 'failed'; emit('StopFailure'); break;
        case 'session.deleted': end(); break;
      }
    },
    dispose() { state = 'unknown'; emit('ProviderDisposed'); }
  };
};
`

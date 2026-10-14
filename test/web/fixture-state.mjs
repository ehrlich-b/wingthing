// Teardown for disposable wing state: wait for every process still using the
// directory (roost, wing, eggs it spawned), then delete it and confirm it stays gone.
import fs from 'node:fs';
import path from 'node:path';

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const inside = (dir, target) => target === dir || target.startsWith(dir + path.sep);

// Linux only: on other platforms this finds nothing and removal relies on the
// tracked children having exited.
export function stateProcesses(dir) {
    const found = [];
    let pids = [];
    try { pids = fs.readdirSync('/proc').filter(name => /^\d+$/.test(name) && Number(name) !== process.pid); } catch { return found; }
    for (const pid of pids) {
        const proc = path.join('/proc', pid);
        try {
            const environ = fs.readFileSync(path.join(proc, 'environ'), 'utf8').split('\0');
            const cmdline = fs.readFileSync(path.join(proc, 'cmdline'), 'utf8').split('\0');
            let uses = environ.some(entry => entry.includes('=') && inside(dir, entry.slice(entry.indexOf('=') + 1))) || cmdline.some(arg => inside(dir, arg));
            if (!uses) { try { uses = inside(dir, fs.readlinkSync(path.join(proc, 'cwd'))); } catch {} }
            if (!uses) {
                try { uses = fs.readdirSync(path.join(proc, 'fd')).some(fd => { try { return inside(dir, fs.readlinkSync(path.join(proc, 'fd', fd))); } catch { return false; } }); } catch {}
            }
            if (uses) found.push(Number(pid));
        } catch {} // Exited, or not ours to read.
    }
    return found;
}

const alive = pid => { try { process.kill(pid, 0); return true; } catch (error) { return error.code === 'EPERM'; } };

// Interrupt, then kill, anything still using dir; resolves once none remain.
export async function stopStateProcesses(dir, { graceMs = 5000, timeoutMs = 30000 } = {}) {
    const started = Date.now();
    const signalled = new Map();
    for (;;) {
        const pids = stateProcesses(dir).filter(alive);
        if (!pids.length) return;
        if (Date.now() - started > timeoutMs) throw Error('processes still using ' + dir + ': ' + pids.join(' '));
        for (const pid of pids) {
            const first = signalled.get(pid);
            try {
                if (first === undefined) { process.kill(pid, 'SIGINT'); signalled.set(pid, Date.now()); }
                else if (Date.now() - first > graceMs) process.kill(pid, 'SIGKILL');
            } catch {}
        }
        await sleep(100);
    }
}

// Wait for users of dir to exit, delete it, and confirm nothing recreated it.
export async function removeState(dir, options) {
    await stopStateProcesses(dir, options);
    fs.rmSync(dir, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
    await sleep(500);
    await stopStateProcesses(dir, options);
    fs.rmSync(dir, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
    if (fs.existsSync(dir)) throw Error('state directory remained after cleanup: ' + dir);
}

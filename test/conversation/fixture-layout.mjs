// Disposable layouts for the ordinary stdio and protected host-mailbox proofs.
import { mkdtemp, mkdir, realpath, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';

const inside = (parent, child) => child === parent || child.startsWith(parent + path.sep);

export async function createFixtureLayout(stateRoot = '') {
    let runtimeRoot;
    if (stateRoot) {
        if (!process.env.XDG_RUNTIME_DIR) throw new Error('protected fixtures require XDG_RUNTIME_DIR');
        const runtime = await realpath(process.env.XDG_RUNTIME_DIR);
        const selected = await realpath(stateRoot);
        if (!inside(runtime, selected)) throw new Error('protected fixture root must be inside XDG_RUNTIME_DIR');
        const temporary = await Promise.all(['/tmp', '/private/tmp', tmpdir()].map(dir => realpath(dir).catch(() => dir)));
        if (temporary.some(dir => inside(dir, selected))) throw new Error('protected fixture root must be outside temporary write directories');
        runtimeRoot = await mkdtemp(path.join(selected, 'wt'));
    }
    let scratch;
    try {
        scratch = await realpath(await mkdtemp('/tmp/wt'));
        const workspace = path.join(scratch, 'workspace');
        const bin = path.join(scratch, 'bin');
        // Protected state has its own disposable HOME, outside both /tmp and
        // the writable workspace. Never use the account's real HOME or state.
        const home = runtimeRoot || path.join(scratch, 'provider-home');
        const state = runtimeRoot ? path.join(home, 's') : path.join(workspace, 'preview-state');
        const endpoint = path.join(state, 'eggs', '0'.repeat(16), 'egg.sock');
        if (Buffer.byteLength(endpoint) > 103) throw new Error(`fixture socket exceeds 103 bytes: ${endpoint}`);
        for (const dir of [workspace, bin, home, state]) await mkdir(dir, { recursive: true, mode: 0o700 });
        return { scratch, workspace, bin, home, state, runtimeRoot };
    } catch (error) {
        if (scratch) await rm(scratch, { recursive: true, force: true });
        if (runtimeRoot) await rm(runtimeRoot, { recursive: true, force: true });
        throw error;
    }
}

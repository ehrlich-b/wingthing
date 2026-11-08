import assert from 'node:assert/strict';
import { test } from 'node:test';
import { realpath, rm, stat } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { createFixtureLayout } from './fixture-layout.mjs';

test('stdio and protected mailbox layouts preserve their actual policy boundary', async () => {
    const runtime = await realpath(process.env.XDG_RUNTIME_DIR || process.env.HOME);
    const ordinary = await createFixtureLayout();
    const protectedLayout = await createFixtureLayout(runtime);
    try {
        assert.equal(path.dirname(ordinary.state), ordinary.workspace);
        assert.equal(path.dirname(ordinary.scratch), await realpath('/tmp'));
        assert.equal(path.dirname(protectedLayout.runtimeRoot), runtime);
        assert.equal(protectedLayout.home, protectedLayout.runtimeRoot);
        assert.equal(path.dirname(protectedLayout.state), protectedLayout.home);
        assert.ok(!protectedLayout.state.startsWith(ordinary.scratch + path.sep));
        assert.ok(!protectedLayout.state.startsWith(protectedLayout.scratch + path.sep));
        assert.notEqual(protectedLayout.home, await realpath(process.env.HOME));
        for (const layout of [ordinary, protectedLayout]) {
            assert.ok(Buffer.byteLength(path.join(layout.state, 'eggs', '0'.repeat(16), 'egg.sock')) <= 103);
            assert.equal((await stat(layout.home)).mode & 0o777, 0o700);
        }
        await assert.rejects(createFixtureLayout('/tmp'), /XDG_RUNTIME_DIR|temporary write/);
        // Runtime storage may never be selected from a writable temp grant,
        // even when the caller labels that grant as its runtime directory.
        const saved = process.env.XDG_RUNTIME_DIR;
        try {
            process.env.XDG_RUNTIME_DIR = '/tmp';
            await assert.rejects(createFixtureLayout('/tmp'), /temporary write/);
        } finally {
            if (saved === undefined) delete process.env.XDG_RUNTIME_DIR;
            else process.env.XDG_RUNTIME_DIR = saved;
        }
    } finally {
        await rm(ordinary.scratch, { recursive: true, force: true });
        await rm(protectedLayout.scratch, { recursive: true, force: true });
        await rm(protectedLayout.runtimeRoot, { recursive: true, force: true });
    }
});

test('macOS mailbox caller works without runtime environment variables', async () => {
    // Model a standalone account using a private, non-temporary fixture root.
    const root = await realpath(process.env.XDG_RUNTIME_DIR || process.env.HOME);
    const env = { ...process.env, HOME: root };
    delete env.XDG_RUNTIME_DIR;
    delete env.CONVERSATION_FIXTURE_ROOT;
    const output = execFileSync('make', ['-n', 'e2e-mac'], { env, encoding: 'utf8' });
    const caller = output.trim().split('\n').at(-1);
    assert.ok(caller.includes(`--state-root "${root}"`), caller);
    // Follow the actual caller's selected root in a fresh process, so ambient
    // XDG_RUNTIME_DIR cannot accidentally make this standalone check pass.
    execFileSync(process.execPath, ['--input-type=module', '-e', `
        import assert from 'node:assert/strict';
        import { realpath, rm, stat } from 'node:fs/promises';
        import path from 'node:path';
        import { createFixtureLayout } from './test/conversation/fixture-layout.mjs';
        const root = await realpath(process.env.HOME);
        const layout = await createFixtureLayout(root);
        try {
            assert.equal(path.dirname(layout.runtimeRoot), root);
            assert.equal(layout.home, layout.runtimeRoot);
            assert.equal(path.dirname(layout.state), layout.home);
            assert.notEqual(layout.home, root);
            assert.equal((await stat(layout.home)).mode & 0o777, 0o700);
            assert.ok(Buffer.byteLength(path.join(layout.state, 'eggs', '0'.repeat(16), 'egg.sock')) <= 103);
            await assert.rejects(createFixtureLayout('/tmp'), /temporary write/);
        } finally {
            await rm(layout.scratch, { recursive: true, force: true });
            await rm(layout.runtimeRoot, { recursive: true, force: true });
        }
    `], { env, stdio: 'pipe' });
});

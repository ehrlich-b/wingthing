#!/usr/bin/env node
// Closed, local JSON fixture driver. Deliberate intermediate typed parity;
// never invokes wt, a payment SDK, provider, GitHub or production API.
import { createPaidState, applyPaidAction } from '../web/src/paid/model.js';
import { readFileSync } from 'node:fs';
if (process.argv[2] !== '--fixture-json') {
    process.stderr.write('Usage: node scripts/paid-fixture.mjs --fixture-json < actions.json\n');
    process.exit(2);
}
try {
    const request = JSON.parse(readFileSync(0, 'utf8'));
    if (!request || Object.keys(request).some(k => !['scenario','actions'].includes(k)) || !Array.isArray(request.actions) || request.actions.length > 100) throw new Error('Invalid bounded fixture request');
    let state = createPaidState(request.scenario || 'free');
    for (const action of request.actions) state = applyPaidAction(state, action);
    process.stdout.write(JSON.stringify({fixture:true,state})+'\n');
} catch (e) { process.stderr.write(e.message+'\n'); process.exitCode=1; }

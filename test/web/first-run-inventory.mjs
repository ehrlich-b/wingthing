// Execute unchanged candidate functions with a fake clock and isolated I/O.
// Regression for the first-run fixture: retain the production polling and render callers.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const source = rel => fs.readFileSync(path.join(root, rel), 'utf8');
const data = source('web/src/data.js');
const home = source('web/src/shell/views/home.js');
const state = source('web/src/paper-state.js');
const newView = source('web/src/shell/views/new.js');

// All extracted functions retain their entire original bodies.
const loadHome = data.slice(data.indexOf('var _loadHomeLock = false;'));
assert.match(loadHome, /async function _loadHomeInner\(\)/);
assert.ok(loadHome.trimEnd().endsWith('}'));
const poll = home.slice(home.indexOf('let firstRunActive ='), home.indexOf('\nfunction connection()'));
const machine = state.slice(state.indexOf('export function machineState'), state.indexOf('\nexport function machineLabel'));
const view = newView.slice(newView.indexOf('function viewNew'), newView.indexOf('\nexport { viewNew }'));

const results = [];
for (const inventory of ['missing', 'complete', 'empty']) {
    const apiOK = inventory !== 'missing';
    let now = 0;
    const wings = Array.from({length: 9}, (_, i) => ({wing_id: `machine-${i}`,
        wing_label: `Research workstation for extended migration projects ${i + 1}`,
        online: true, agents: [], public_key: 'synthetic'}));
    const fetches = [], cancelled = [];
    class Clock extends Date {
        constructor(...args) { super(...(args.length ? args : [now])); }
        static now() { return now; }
    }
    const noop = () => {};
    const context = vm.createContext({
        Date: Clock, Promise, console,
        S: {currentUser: {relay_allowed: true}, wingsData: wings, sessionsData: [], activeView: 'home'},
        DOM: {commandPalette: {style: {display: 'none'}}},
        fetch: async url => {fetches.push(url); return {ok: apiOK, json: async () => inventory === 'complete' ? wings.map(w => ({wing_id: w.wing_id})) : []};},
        getCachedWings: () => [], getCachedSessions: () => [],
        sortWingsByOrder: value => value, sortSessionsByOrder: value => value,
        setCachedSessions: noop, rebuildAgentLists: noop, updateHeaderStatus: noop,
        renderSidebar: noop, refreshSessionFilesButton: noop, renderDashboard: noop,
        renderWingDetailPage: noop, saveWingCache: noop, updatePaletteState: noop,
        cancelWingProbe: id => cancelled.push(id), probeWing: async () => {},
        fetchWingSessions: async () => [], mergeWingSessions: noop,
        render: noop, sessions: () => [],
        ui: {launchWingId: 'machine-8'}, machines: () => wings,
        machineName: w => w.wing_label, esc: value => value,
        head: () => '', viewEmpty: () => '', needsPasskey: () => false,
        installedAgents: w => w.agents, agentName: value => value,
        agentSetup: w => `<h2>No agents detected</h2>${w.wing_label}`,
    });
    vm.runInContext(loadHome.replace('export async function loadHome', 'async function loadHome') + '\n' +
        poll.replaceAll('export function ', 'function ') + '\n' +
        machine.replace('export function ', 'function ') + '\n' + view, context);
    vm.runInContext('syncHomePolling(true)', context);
    const before = vm.runInContext("viewNew({wingId: 'machine-8'})", context);
    assert.match(before, /No agents detected[\s\S]*Research workstation.*9/);
    now = 2999;
    vm.runInContext('pollHomeData()', context);
    assert.equal(fetches.length, 0);
    now = 3000;
    vm.runInContext('pollHomeData()', context);
    await vm.runInContext('inventoryRead', context);
    const after = vm.runInContext("viewNew({wingId: 'machine-8'})", context);
    assert.deepEqual(fetches, ['/api/app/wings']);
    if (inventory === 'complete') {
        assert.ok(wings.every(w => w.online === true), 'complete inventory keeps all nine machines online');
        assert.match(after, /No agents detected[\s\S]*Research workstation.*9/);
        assert.equal(cancelled.length, 0);
    } else {
        assert.match(after, /Machine unavailable/);
        assert.equal(cancelled.length, 9);
        assert.ok(wings.every(w => w.online === false));
    }
    results.push({inventory, api_status: apiOK ? 200 : 404, poll_at_ms: now,
        machine8: vm.runInContext('machineState(S.wingsData[8])', context),
        cancelled_probes: cancelled.length, expected_no_agents: /No agents detected/.test(after)});
}
console.log(JSON.stringify({kind: 'isolated unmodified function replay', results}, null, 2));

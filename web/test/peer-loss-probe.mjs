// Regression probe for asynchronous peer cleanup; production bodies only.
import assert from 'node:assert/strict';
import fs from 'node:fs';
const s = fs.readFileSync(new URL('../src/webrtc.js', import.meta.url), 'utf8');
const start = s.indexOf('        dc.onclose = function() {');
const body = s.slice(start + '        dc.onclose = function() {'.length, s.indexOf('\n        };', start));
const cleanupSource = s.match(/export function cleanupPeer\(wingId\) \{[^]*?^\}/m)[0].replace('export function', 'function');
let reports = 0;
const dc = { close() { queueMicrotask(() => onclose()); } };
const dataChannels = { k: dc }, dcActive = { k: true }, sessionWings = { k: 'wing' }, dcBuffers = {}, peers = { wing: { close() {} } };
const refusalSource = s.slice(s.indexOf('        dc._unconfirmedInputs = 0;'), s.indexOf('        dc.onclose = function() {'));
new Function('dc', 'dcActive', 'key', 'wingId', 'sessionId', 'handleDCMessage', refusalSource)(dc, dcActive, 'k', 'wing', 'session', () => reports++);
// Model one input queued locally before the peer fails.
dc._unconfirmedInputs = 1;
const onclose = new Function('dataChannels', 'dcActive', 'sessionWings', 'dcBuffers', 'dc', 'key', 'wingId', 'sessionId', 'handleDCMessage', 'return function(){' + body + '}')(dataChannels, dcActive, sessionWings, dcBuffers, dc, 'k', 'wing', 'session', () => reports++);
const cleanup = new Function('peers', 'dataChannels', 'dcActive', 'sessionWings', 'dcBuffers', cleanupSource + ';return cleanupPeer')(peers, dataChannels, dcActive, sessionWings, dcBuffers);
cleanup('wing');
await new Promise(resolve => setImmediate(resolve));
console.log('active channel peer failure: visible reports=' + reports);
assert.equal(reports, 1, 'active peer failure must visibly refuse input exactly once');
assert.equal(dc._unconfirmedInputs, 0);
assert.equal(dc._refusedInputs, 1);
onclose();
assert.equal(reports, 1, 'late close must not report twice');

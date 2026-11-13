import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = ['common', 'views/sessions', 'views/session'].map(name => readFileSync(new URL('../src/shell/' + name + '.js', import.meta.url), 'utf8')).join('\n');
const functionBody = name => source.slice(source.indexOf(`function ${name}(`), source.indexOf('\n}', source.indexOf(`function ${name}(`)) + 2);

test('A6: session copy is brief and recovery retains its permission guidance', () => {
    const view = functionBody('viewSessions');
    assert.doesNotMatch(view, /cach|polling|refresh interval|protocol|state ID/i);
    assert.match(view, />Machine controls<\/a>/);
    assert.match(view, />Clear search<\/button>/);
    assert.match(view, /Last seen on /);
    assert.match(functionBody('accessPanel'), /machineGuidance\(w\)/);
    assert.match(functionBody('machineActions'), />Machine access<\/a>/);
    assert.match(functionBody('viewSession'), /Answer in the terminal/);
});

test('A7: session presentation keeps the existing machine display boundaries', () => {
    assert.match(source, /const machineLimit = 5[,;]/);
    assert.match(source, /machineListLimit = 6;/);
    assert.match(functionBody('viewSessions'), /more machines/);
    assert.doesNotMatch(functionBody('viewSessions'), /\$\d|Upgrade|Subscribe|entitlement|self_service_plans/i);
});

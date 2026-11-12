import test from 'node:test';
import assert from 'node:assert/strict';
import { sessionRoute, resolveShellRoute } from '../src/session-route.js';

test('all old bookmarks resolve to canonical shell routes', () => {
    for (const [hash, target] of [
        ['#s/a%2Fb?wing=machine%2Fone','#session/machine%2Fone/a%2Fb'],
        ['#chat/a?wing=m','#session/m/a'], ['#approve/m/a','#session/m/a'],
        ['#w/m%2Fone','#machine/m%2Fone'], ['#needs','#'], ['#decisions','#'],
        ['#bogus','#'], ['#session/%broken/a','#'], ['#new/%broken','#'],
    ]) assert.equal(resolveShellRoute(hash).hash, target);
    assert.equal(resolveShellRoute('#approve/m/a').focusNeeds, true);
    assert.equal(resolveShellRoute('#s/id',[{id:'id',wing_id:'m'}]).hash,'#session/m/id');
    assert.equal(resolveShellRoute('#s/id',[{id:'id',wing_id:'m'},{id:'id',wing_id:'n'}]).hash,'#sessions');
});

test('every route in the table stays inside the shell', () => {
    for (const hash of ['#','#sessions','#machines','#machine/m','#session/m/id','#new','#new/m','#account','#pair','#canvas']) {
        assert.equal(resolveShellRoute(hash).hash, hash);
    }
    assert.equal(sessionRoute('a/b','m?'),'#session/m%3F/a%2Fb');
});

test('the Home overflow route selects waiting sessions independently of search text', () => {
    assert.deepEqual(resolveShellRoute('#sessions?state=need').route, { view: 'sessions', state: 'need' });
    assert.deepEqual(resolveShellRoute('#sessions').route, { view: 'sessions' });
});

test('logical conversation bookmarks retain both identities across shell navigation', () => {
    assert.deepEqual(resolveShellRoute('#conversation/task-123?wing=studio').route,
        { view: 'conversation', conversationId: 'task-123', wingId: 'studio' });
    assert.deepEqual(resolveShellRoute('#conversation/task%2F123?wing=studio%2Fone').route,
        { view: 'conversation', conversationId: 'task/123', wingId: 'studio/one' });
    for (const hash of ['#conversation/task?wing=', '#conversation/%broken?wing=studio'])
        assert.equal(resolveShellRoute(hash).hash, '#');
});

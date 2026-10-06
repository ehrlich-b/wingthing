import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { sessionResourceKey } from '../src/session-reference.js';

const source = readFileSync(new URL('../src/render.js', import.meta.url), 'utf8')
    .replace(/^import .*;\n/gm, '').replace(/^export /gm, '');

function dragHarness() {
    const timers = new Map(), saved = [];
    let timerId = 0, target = null;
    const grids = [grid(['a', 'b', 'c']), grid(['d', 'e'])];
    function grid(ids) {
        const grid = { listeners: {}, children: [],
            querySelectorAll: () => grid.children.slice(),
            addEventListener(type, callback) { this.listeners[type] = callback; },
            insertBefore(card, reference) {
                if (reference && reference.parentNode !== this) throw Error('NotFoundError');
                card.parentNode.children.splice(card.parentNode.children.indexOf(card), 1);
                this.children.splice(reference ? this.children.indexOf(reference) : this.children.length, 0, card);
                card.parentNode = this;
            },
        };
        grid.children = ids.map(id => {
            const classes = new Set();
            const card = { dataset: { sid: id, wingId: 'mac' }, parentNode: grid, listeners: {},
                classList: { add: name => classes.add(name), remove: name => classes.delete(name), contains: name => classes.has(name) },
                addEventListener(type, callback) { this.listeners[type] = callback; }, setAttribute() {},
                closest: selector => selector === '.egg-box' ? card : selector === '.egg-grid' ? grid : null,
                compareDocumentPosition(other) {
                    const cards = grids.flatMap(grid => grid.children);
                    return cards.indexOf(card) < cards.indexOf(other) ? 4 : 2;
                },
                get nextSibling() { return this.parentNode.children[this.parentNode.children.indexOf(this) + 1] || null; },
            };
            return card;
        });
        return grid;
    }
    const context = vm.createContext({
        DOM: { sessionsList: { querySelectorAll: selector => selector === '.egg-grid' ? grids : grids.flatMap(grid => grid.children) } },
        S: { sessionsData: grids.flatMap(grid => grid.children).map(card => ({ id: card.dataset.sid, wing_id: 'mac' })) },
        window: { ontouchstart: null }, navigator: { maxTouchPoints: 1 }, Node: { DOCUMENT_POSITION_FOLLOWING: 4 },
        document: { elementFromPoint: () => target }, sessionResourceKey,
        setTimeout(callback) { timers.set(++timerId, callback); return timerId; }, clearTimeout: id => timers.delete(id),
        setEggOrder: order => saved.push(Array.from(order)),
    });
    vm.runInContext(source, context, { filename: 'render.js' });
    context.setupEggDrag();
    const event = { touches: [{ clientX: 0, clientY: 0 }], changedTouches: [{ clientX: 0, clientY: 0 }], preventDefault() {} };
    function start(card, longPress = true) {
        card.listeners.touchstart({ target: card });
        if (longPress) { timers.forEach(callback => callback()); timers.clear(); }
    }
    return { context, grids, saved, event, start, target(card) { target = card; } };
}

test('touch drops cannot highlight or move cards across project grids', () => {
    for (const index of [0, 1]) {
        const h = dragHarness(), [source, other] = h.grids, card = source.children[0], target = other.children[index];
        h.start(card);
        h.target(target);
        source.listeners.touchmove(h.event);
        assert.equal(target.classList.contains('drag-over'), false);
        assert.doesNotThrow(() => source.listeners.touchend(h.event));
        assert.deepEqual(source.children.map(card => card.dataset.sid), ['a', 'b', 'c']);
        assert.deepEqual(other.children.map(card => card.dataset.sid), ['d', 'e']);
        assert.equal(card.classList.contains('dragging'), false);
        assert.deepEqual(h.saved, []);
    }
});

test('touch reordering stays functional within the source project', () => {
    const h = dragHarness(), grid = h.grids[0];
    h.start(grid.children[0]);
    h.target(grid.children[1]);
    grid.listeners.touchend(h.event);
    assert.deepEqual(grid.children.map(card => card.dataset.sid), ['b', 'a', 'c']);
    h.start(grid.children[2]);
    h.target(grid.children[0]);
    grid.listeners.touchend(h.event);
    assert.deepEqual(grid.children.map(card => card.dataset.sid), ['c', 'b', 'a']);
    assert.equal(h.saved.length, 2);
});

test('touch drag state clears even when hit testing, insertion or order persistence throws', () => {
    for (const failure of ['hit test', 'insert', 'save']) {
        const h = dragHarness(), grid = h.grids[0], card = grid.children[0];
        h.start(card);
        h.target(grid.children[1]);
        grid.listeners.touchmove(h.event);
        const fail = () => { throw Error(failure); };
        if (failure === 'hit test') h.context.document.elementFromPoint = fail;
        if (failure === 'insert') grid.insertBefore = fail;
        if (failure === 'save') h.context.setEggOrder = fail;
        assert.throws(() => grid.listeners.touchend(h.event), new RegExp(failure));
        assert.equal(card.classList.contains('dragging'), false);
        assert.equal(grid.children.some(card => card.classList.contains('drag-over')), false);
        let active = false;
        assert.doesNotThrow(() => grid.listeners.touchmove({ ...h.event, preventDefault() { active = true; } }));
        assert.equal(active, false, 'a failed drop must release the touch source');
    }
});

test('touch cancellation releases an active drag and cancels a pending long press', () => {
    for (const longPress of [false, true]) {
        const h = dragHarness(), grid = h.grids[0], card = grid.children[0];
        h.start(card, longPress);
        grid.listeners.touchcancel(h.event);
        assert.equal(card.classList.contains('dragging'), false);
        h.target(grid.children[1]);
        grid.listeners.touchend(h.event);
        assert.deepEqual(h.saved, []);
    }
});

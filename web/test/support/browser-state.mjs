import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import * as inventory from '../../src/session-inventory.js';
import * as helpers from '../../src/helpers.js';
import * as references from '../../src/session-reference.js';

// Run the real module bodies with only their transport/UI dependencies replaced.
export function moduleContext(name, bindings) {
    const source = readFileSync(new URL(`../../src/${name}.js`, import.meta.url), 'utf8')
        .replace(/^import .*;\n/gm, '').replace(/^export /gm, '');
    const context = vm.createContext({ ...helpers, ...references, ...inventory, ...bindings });
    vm.runInContext(source, context, { filename: `${name}.js` });
    return context;
}

export function memoryStorage() {
    const values = new Map();
    return {
        get length() { return values.size; },
        key: index => [...values.keys()][index] ?? null,
        getItem: key => values.get(key) ?? null,
        setItem: (key, value) => values.set(key, String(value)),
        removeItem: key => values.delete(key),
        values,
    };
}

export function sidebarHarness() {
    const actions = [], renames = [];
    const doc = { activeElement: null, getElementById: () => null };
    function element(dataset = {}, parent = null, tag = 'tab') {
        return {
            dataset, parentNode: parent, tag, listeners: {}, children: [],
            addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); },
            removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter(item => item !== fn); },
            contains(target) { return target === this || this.children.some(child => child.contains(target)); },
            closest(selector) {
                if ((selector === '.session-tab' || selector === '[data-sid]') && this.tag === 'tab') return this;
                if ((selector === 'button, input, .forking' || selector === '.session-rename-btn') && this.tag === 'button') return this;
                return this.parentNode?.closest(selector) || null;
            },
            querySelector(selector) { return selector === '.session-rename-btn' ? this.children.find(child => child.tag === 'button') || null : null; },
            querySelectorAll(selector) { return selector === '[data-session-action]' ? [] : this.children; },
            focus() { doc.activeElement = this; },
            dispatch(type, key) {
                const event = { target: this, key, prevented: false, stopped: false,
                    preventDefault() { this.prevented = true; }, stopPropagation() { this.stopped = true; } };
                for (let node = this; node; node = node.parentNode) {
                    for (const fn of node.listeners[type] || []) fn(event);
                    if (event.stopped) break;
                }
                return event;
            },
        };
    }
    const tabs = element({}, null, 'container');
    Object.defineProperty(tabs, 'innerHTML', { set(html) {
        tabs.children.forEach(child => { child.parentNode = null; });
        tabs.children = [...html.matchAll(/data-sid="([^"]*)" data-wing-id="([^"]*)"/g)].map(match => {
            const tab = element({ sid: match[1], wingId: match[2] }, tabs);
            // Fixtures use owner sessions with the rename capability.
            tab.children = [element({}, tab, 'button')];
            return tab;
        });
    } });
    const S = { sessionsData: [], wingsData: [{ wing_id: 'mac', online: true, capabilities: ['session.rename'] }],
        currentUser: { id: 'owner' }, sessionNotifications: {}, activeView: 'home', ptySessionId: null, ptyWingId: null };
    const context = moduleContext('render', {
        S, DOM: { sessionTabs: tabs }, document: doc, refreshParentDot() {},
        switchToSession: (id, _, wingId) => actions.push([wingId, id]),
    });
    context.beginSessionRename = (tab, session) => renames.push([session.wing_id, session.id]);
    return { S, context, tabs, doc, actions, renames, render: () => context.renderSidebar() };
}

export function session(id = 'same-id', wing_id = 'mac', swept = true) {
    return { id, wing_id, swept, name: id, agent: 'codex', user_id: 'owner' };
}

export function deferred() {
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    return { promise, resolve, reject };
}

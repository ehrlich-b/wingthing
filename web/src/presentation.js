import { escapeMarkup } from './security.js';

// Display identity never changes the qualified resource used for a request.
export function workTitle(row) {
    if (row?.pinned && row.role === 'coordinator') return 'wing';
    const value = row?.name || row?.title;
    return typeof value === 'string' && value.trim() && !/[\\/]/.test(value) && !/^[a-f\d]{16,}$/i.test(value.trim()) ? value.trim() : 'Untitled session';
}
export function projectLabel(row) {
    const name = row?.project_name || row?.project?.name;
    if (typeof name === 'string' && name.trim() && !/[\\/]/.test(name)) return name;
    const parts = typeof row?.cwd === 'string' ? row.cwd.split(/[\\/]/).filter(Boolean) : [];
    const leaf = parts.at(-1);
    return leaf && !leaf.startsWith('.') && !/^[a-f\d]{16,}$/i.test(leaf) ? leaf : 'Project not named';
}
const states = {
    working: ['Working', '●', 'info'], blocked: ['Needs you', '△', 'warning'], needs_input: ['Needs you', '△', 'warning'],
    idle: ['Idle', '○', 'neutral'], done: ['Done', '✓', 'success'], exited: ['Ended', '○', 'neutral'],
    stopped: ['Ended', '○', 'neutral'], failed: ['Failed', '⊗', 'error'], unknown: ['Unknown', '?', 'neutral'],
};
export function statusMarkup(status, lastKnown = false) {
    const [label, icon, tone] = states[status] || states.unknown;
    return '<span class="wt-status" data-tone="' + tone + '"><span aria-hidden="true">' + icon + '</span>' + (lastKnown ? 'Last seen · ' : '') + label + '</span>';
}
export function matchExcerpt(text, query) {
    if (typeof text !== 'string') return '';
    const excerpt = text.slice(0, 1024);
    const words = String(query || '').trim().split(/\s+/).filter(Boolean).map(word => word.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
    if (!words.length) return escapeMarkup(excerpt);
    const matches = new RegExp('(' + words.join('|') + ')', 'gi');
    return excerpt.split(matches).map((part, i) => i % 2 ? '<mark>' + escapeMarkup(part) + '</mark>' : escapeMarkup(part)).join('');
}

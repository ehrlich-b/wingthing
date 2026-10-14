import { escapeMarkup } from './security.js';

export function safeActivityLink(value) {
    try {
        const url = new URL(value);
        return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password ? url.href : '';
    } catch { return ''; }
}

export function activityTime(value) {
    const ms = typeof value === 'number' ? value : Date.parse(value);
    return Number.isFinite(ms) && ms > 0 ? new Date(ms).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }) : 'Time not recorded';
}

export function reportChips(report) {
    const artifacts = Array.isArray(report.artifacts) ? report.artifacts.slice(0, 8) : [];
    const links = Array.isArray(report.links) ? report.links.slice(0, 8) : [];
    return '<div class="activity-chips">' + artifacts.filter(item => typeof item.path === 'string' && item.path.trim() && !item.path.includes('\0')).map(item =>
        '<button type="button" class="activity-file" data-artifact-path="' + escapeMarkup(item.path) + '"><span aria-hidden="true">▤</span> ' + escapeMarkup(item.label || item.path) + '</button>').join('') +
        links.map(item => { const href = safeActivityLink(item.url); return href ? '<a href="' + escapeMarkup(href) + '" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer">' + escapeMarkup(item.label || 'Open link') + ' ↗</a>' : ''; }).join('') + '</div>';
}

const labels = { session_ready: 'Ready', prompt_submitted: 'Woke for a message', turn_completed: 'Turn completed', turn_failed: 'Turn failed', input_requested: 'Approval / input requested', session_exit: 'Process stopped', provider_session_end: 'Provider session ended', provider_warning: 'Provider warning' };

export function sessionActivity(messages) {
    const items = [], seen = new Set();
    for (const message of messages || []) {
        const key = `${message.sequence}:${message.itemIndex || 0}:${message.type}`;
        if (seen.has(key)) continue;
        seen.add(key);
        if (message.type === 'activity') {
            if (message.report) items.push({ key, kind: 'update', title: 'Coordinator update', content: message.report.update || '', timestamp: message.timestamp, report: message.report });
            else items.push({ key, kind: message.eventType === 'input_requested' || message.state === 'needs_input' ? 'approval' : 'state',
                title: labels[message.eventType] || 'State changed', content: [({needs_input:'Input requested',working:'Working',idle:'Idle',stopped:'Stopped',completed:'Completed'})[message.state] || message.state, message.reason].filter(Boolean).join(' · '), timestamp: message.timestamp });
        } else if ((message.type === 'user' || message.type === 'assistant') && message.content) {
            items.push({ key, kind: 'message', title: message.type === 'user' ? 'You sent a message' : 'Agent update', content: message.content.slice(0, 4096), timestamp: message.timestamp });
        }
    }
    return items;
}

export function timelineMarkup(messages, limit = 200) {
    const items = sessionActivity(messages).slice(-limit).reverse();
    return items.length ? '<ol class="session-timeline">' + items.map(item => '<li data-activity-key="' + escapeMarkup(item.key) + '" data-kind="' + item.kind + '"><span class="activity-marker" aria-hidden="true"></span><div><div class="activity-heading"><strong>' + escapeMarkup(item.title) + '</strong><time>' + escapeMarkup(activityTime(item.timestamp)) + '</time></div><p>' + escapeMarkup(item.content) + '</p>' + (item.report ? reportChips(item.report) : '') + (item.kind === 'approval' ? '<small>Answer in terminal · this is a recorded request; inspect its current status there.</small>' : '') + '</div></li>').join('') + '</ol>' : '<p class="activity-empty">No recorded activity yet.</p>';
}

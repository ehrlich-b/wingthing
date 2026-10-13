import { escapeMarkup } from './security.js';
import { activityTime, reportChips } from './session-activity.js';

// Reports are messages from the coordinator; native observations remain evidence,
// never fabricated replies, read receipts or timer ticks.
export function coordinatorThreadMarkup(messages, summary, limit = 6) {
    const rows = (messages || []).filter(message =>
        ['user', 'assistant'].includes(message.type) && message.content || message.report?.update).slice(-limit);
    if (!rows.length) rows.push({ type: 'assistant', content: summary.update, timestamp: summary.reportedAt, report: summary });
    let day = '';
    return '<div class="coordinator-thread" aria-label="Recent coordinator messages">' + rows.map(message => {
        const date = new Date(message.timestamp || 0);
        const nextDay = date.getTime() > 0 ? date.toLocaleDateString([], { month: 'short', day: 'numeric' }) : '';
        const separator = nextDay && nextDay !== day ? '<div class="thread-day">' + escapeMarkup(nextDay) + '</div>' : '';
        day = nextDay;
        const user = message.type === 'user';
        return separator + '<div class="thread-message ' + (user ? 'thread-you' : 'thread-wing') + '"><div class="thread-bubble">' +
            escapeMarkup(message.report?.update || message.content || '') + (message.report ? reportChips(message.report) : '') +
            '</div><time>' + escapeMarkup(activityTime(message.timestamp)) + '</time></div>';
    }).join('') + '</div>';
}

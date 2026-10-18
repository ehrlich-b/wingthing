// chat.js — Parse agent JSONL into common chat message format

// Common message shapes:
// {type: "user", content: "the prompt text"}
// {type: "assistant", content: "response text", thinking: "...", toolCalls: [...]}
// {type: "tool_result", name: "Read", content: "..."}

export function parseJSONL(text, agent) {
    if (!text) return [];
    var lines = text.split('\n').filter(function(l) { return l.trim(); });
    var messages = [];
    for (var i = 0; i < lines.length; i++) {
        try {
            var raw = JSON.parse(lines[i]);
            var parsed = agent === 'claude' ? parseClaude(raw) : agent === 'codex' ? parseCodex(raw) : parseGeneric(raw);
            if (parsed) messages.push(parsed);
        } catch (e) {
            // skip malformed lines
        }
    }
    return messages;
}

function parseClaude(raw) {
    // Skip non-message types
    if (raw.type === 'file-history-snapshot' || raw.type === 'summary') return null;
    if (!raw.message) return null;

    var role = raw.message.role;
    if (role === 'user') {
        var userBlocks = raw.message.content;
        if (Array.isArray(userBlocks) && userBlocks.some(function(b) { return b.type === 'tool_result'; })) {
            return { type: 'tool_result', name: userBlocks.map(function(b) { return b.tool_use_id || ''; }).filter(Boolean).join(', '), content: extractText(userBlocks) };
        }
        return { type: 'user', content: extractText(raw.message.content) };
    }
    if (role === 'assistant') {
        var content = '';
        var thinking = '';
        var toolCalls = [];
        var blocks = raw.message.content;
        if (typeof blocks === 'string') {
            content = blocks;
        } else if (Array.isArray(blocks)) {
            for (var i = 0; i < blocks.length; i++) {
                var b = blocks[i];
                if (b.type === 'text') {
                    content += (content ? '\n' : '') + b.text;
                } else if (b.type === 'thinking') {
                    thinking += (thinking ? '\n' : '') + b.thinking;
                } else if (b.type === 'tool_use') {
                    toolCalls.push({ name: b.name, input: b.input, id: b.id });
                } else if (b.type === 'tool_result') {
                    toolCalls.push({ name: b.tool_use_id || 'tool', result: extractText(b.content), isError: b.is_error });
                }
            }
        }
        var msg = { type: 'assistant', content: content };
        if (thinking) msg.thinking = thinking;
        if (toolCalls.length > 0) msg.toolCalls = toolCalls;
        return msg;
    }
    // tool_result at top level (Claude format has these as separate messages sometimes)
    if (role === 'tool') {
        return { type: 'tool_result', content: extractText(raw.message.content) };
    }
    return null;
}

// Native v2 item records are retained by internal/egg/codex_native.go.
// Only complete items are transcript text; deltas and future shapes remain
// inspectable. Lifecycle readiness always comes from session_read, never text.
function parseCodex(raw) {
    if (!raw || typeof raw !== 'object') return providerRecord(raw);
    var item = raw.method === 'item/completed' && raw.params && raw.params.item;
    if (item && item.type === 'userMessage' && Array.isArray(item.content) && item.content.every(b => b && b.type === 'text' && typeof b.text === 'string')) {
        return { type: 'user', content: item.content.map(b => b.text).join('\n') };
    }
    if (item && item.type === 'agentMessage' && typeof item.text === 'string') {
        return { type: 'assistant', content: item.text };
    }
    if (item && item.type === 'mcpToolCall' && typeof item.tool === 'string') {
        return { type: 'assistant', content: '', toolCalls: [{ name: (item.server ? item.server + ' · ' : '') + item.tool, id: item.id, input: item.arguments },
            ...(item.result !== undefined || item.error != null ? [{ name: item.tool, result: displayJSON(item.error ?? item.result), isError: item.error != null }] : [])] };
    }
    // Retain generic role records supported by earlier builds.
    return (!raw.method && parseGeneric(raw)) || providerRecord(raw);
}

function displayJSON(value) {
    return typeof value === 'string' ? value : JSON.stringify(value, null, 2);
}

function providerRecord(raw) {
    return { type: 'provider_record', name: raw && (raw.method || raw.type) || 'provider record', content: displayJSON(raw) };
}

function parseGeneric(raw) {
    // Basic fallback for role-based records
    if (raw.role === 'user' || (raw.message && raw.message.role === 'user')) {
        var c = raw.content || (raw.message && raw.message.content) || '';
        return { type: 'user', content: typeof c === 'string' ? c : JSON.stringify(c) };
    }
    if (raw.role === 'assistant' || (raw.message && raw.message.role === 'assistant')) {
        var c = raw.content || (raw.message && raw.message.content) || '';
        return { type: 'assistant', content: typeof c === 'string' ? c : JSON.stringify(c) };
    }
    return null;
}

function extractText(content) {
    if (typeof content === 'string') return content;
    if (Array.isArray(content)) {
        return content.map(function(b) {
            if (typeof b === 'string') return b;
            if (b.type === 'text') return b.text;
            if (b.type === 'tool_result') return b.content ? extractText(b.content) : '';
            return '';
        }).filter(Boolean).join('\n');
    }
    return '';
}

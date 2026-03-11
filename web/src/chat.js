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
            var parsed = agent === 'claude' ? parseClaude(raw) : parseGeneric(raw);
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

function parseGeneric(raw) {
    // Basic fallback for codex/opencode
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

// Apply only fields actually sent by the relay. Older nodes omit newer wing
// capabilities, so absence must preserve the last inventory/probe value.
export function applyWingEventMetadata(wing, event) {
    if (event.public_key) wing.public_key = event.public_key;
    if (event.locked !== undefined) wing.locked = !!event.locked;
    if (event.allowed_count !== undefined) wing.allowed_count = event.allowed_count;
    if (event.purpose_binding !== undefined) wing.purpose_binding = !!event.purpose_binding;
    if (event.direct_mcp !== undefined) wing.direct_mcp = !!event.direct_mcp;
    if (event.hosted_relay !== undefined) wing.hosted_relay = event.hosted_relay;
    return wing;
}

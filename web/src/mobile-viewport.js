export function conversationViewport(viewport, layoutHeight, mobileConversation) {
    if (!viewport || (viewport.scale && viewport.scale !== 1) || !Number.isFinite(viewport.height) || viewport.height <= 0) return null;
    var top = mobileConversation ? Math.max(0, viewport.offsetTop || 0) : 0;
    return { height: viewport.height, top: top, keyboard: mobileConversation && layoutHeight - viewport.height - top > 150 };
}

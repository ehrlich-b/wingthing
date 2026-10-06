import { S } from './state.js';
import { wingDisplayName } from './helpers.js';
import { showHome } from './nav.js';
import { openConversationReference, cachedConversationTask } from './conversation-view.js';
import { parentDotStorageKey, readParentSelection, saveParentSelection, selectParentConversation, activateParentSelection, parentDotPresentation } from './parent-dot-state.js';

var selection = null;
var selectionUser = '';
var initialized = false;
var opening = false;
var storageError = '';
var openError = '';
var openErrorAt = 0;

function browserStorage() {
    try { return window.localStorage; } catch (error) { return null; }
}

function restoreSelection() {
    selectionUser = S.currentUser ? S.currentUser.id : '';
    var restored = readParentSelection(browserStorage(), selectionUser);
    selection = restored.selection;
    storageError = restored.error;
    openError = '';
}

function chooseParent() {
    showHome();
    var inventory = document.getElementById('conversation-inventory');
    if (inventory) inventory.scrollIntoView({ block: 'start' });
}

export function refreshParentDot() {
    var button = document.getElementById('parent-dot');
    if (!button) return;
    if (S.currentUser && selectionUser !== S.currentUser.id) restoreSelection();
    // A shortcut back to the coordinator this user opened last. Choosing one
    // happens in the Home inventory, so nothing renders before a selection or
    // on a wing without coordinators. The stored selection is kept either way.
    var selectedWing = selection && S.wingsData.find(function(item) { return item.wing_id === selection.wingId; });
    button.hidden = !S.currentUser || S.currentUser.relay_allowed === false || !selectedWing ||
        selectedWing.tunnel_error === 'not_allowed' || !Array.isArray(selectedWing.capabilities) ||
        !selectedWing.capabilities.includes('conversation.personal.v1');
    var message = document.getElementById('parent-dot-message');
    if (button.hidden) { if (message) message.hidden = true; return; }
    var evidence = cachedConversationTask(selection);
    if (openError && evidence && !evidence.readError && evidence.observedAt > openErrorAt) openError = '';
    if (openError && selection) evidence = { ...(evidence || {}), reference: selection, readError: openError };
    var presentation = parentDotPresentation(selection, S.wingsData, S.sessionsData, evidence);
    var description = 'coordinator “' + presentation.title + '” on ' + (wingDisplayName(selectedWing) || selection.wingId) + ' — ' + presentation.label;
    button.dataset.state = presentation.state;
    button.disabled = opening;
    button.title = opening ? 'Opening coordinator…' : 'Open ' + description;
    button.setAttribute('aria-label', 'Open ' + description);
    button.querySelector('.parent-dot-name').textContent = 'coordinator: ' + presentation.title;
    button.querySelector('.parent-dot-state').textContent = opening ? 'opening…' : presentation.label;
    if (message) {
        message.textContent = [storageError, openError].filter(Boolean).join(' ');
        message.hidden = !message.textContent;
    }
}

export function initParentDot() {
    if (initialized || !document.getElementById('parent-dot')) return;
    initialized = true;
    restoreSelection();
    document.getElementById('parent-dot').addEventListener('click', async function() {
        if (opening) return;
        openError = '';
        var userId = S.currentUser ? S.currentUser.id : '';
        var selected = selection;
        opening = true;
        refreshParentDot();
        try {
            await activateParentSelection(selected, userId, openConversationReference, chooseParent);
        } catch (error) {
            if (selectionUser === userId && selection === selected) {
                openError = 'Parent could not open: ' + (error.message || 'Reconnect to its execution wing.');
                openErrorAt = Date.now();
            }
        } finally {
            opening = false;
            refreshParentDot();
        }
    });
    window.addEventListener('wingthing:conversation-selected', function(event) {
        var userId = S.currentUser ? S.currentUser.id : '';
        var selected = selectParentConversation(userId, selection, event.detail);
        if (!selected) return;
        selectionUser = userId;
        selection = selected;
        storageError = saveParentSelection(browserStorage(), userId, selected) ? '' : 'Parent selection could not be saved in this browser.';
        openError = '';
        refreshParentDot();
    });
    window.addEventListener('wingthing:conversation-inventory-updated', refreshParentDot);
    window.addEventListener('storage', function(event) {
        if (event.key === parentDotStorageKey(S.currentUser ? S.currentUser.id : '')) {
            restoreSelection();
            refreshParentDot();
        }
    });
    // Age the display locally on every view; this timer starts no provider work
    // and makes no extra network requests.
    setInterval(refreshParentDot, 15000);
    refreshParentDot();
}

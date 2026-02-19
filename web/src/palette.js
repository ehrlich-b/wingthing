import { S, DOM } from './state.js';
import { escapeHtml, wingDisplayName, shortenPath, nestedRepoCount, agentWithIcon, dirParent } from './helpers.js';
import { sendTunnelRequest } from './tunnel.js';
import { getLastTermAgent, setLastTermAgent } from './data.js';
import { showTerminal } from './nav.js';
import { connectPTY } from './pty.js';

var paletteWingIndex = 0;
var paletteAgentIndex = 0;
var paletteSelectedIndex = 0;
var dirListTimer = null;
var dirListPending = false;
var dirListCache = [];
var dirListCacheDir = '';
var dirListQuery = '';
var homeDirCache = [];

export function onlineWings() {
    return S.wingsData.filter(function(w) { return w.online !== false; });
}

function currentPaletteWing() {
    var online = onlineWings();
    if (online.length === 0) return null;
    return online[paletteWingIndex % online.length];
}

function currentPaletteAgents() {
    var wing = currentPaletteWing();
    if (!wing || !wing.agents || wing.agents.length === 0) return ['claude'];
    return wing.agents;
}

function currentPaletteAgent() {
    var agents = currentPaletteAgents();
    return agents[paletteAgentIndex % agents.length];
}

export function cyclePaletteAgent(dir) {
    var agents = currentPaletteAgents();
    if (agents.length <= 1) return;
    var step = dir || 1;
    paletteAgentIndex = (paletteAgentIndex + step + agents.length) % agents.length;
    renderPaletteStatus();
}

export function showPalette() {
    DOM.commandPalette.style.display = '';
    DOM.paletteSearch.value = '';
    DOM.paletteSearch.focus();
    updatePaletteState();
    var wing = currentPaletteWing();
    if (wing && homeDirCache.length === 0) {
        sendTunnelRequest(wing.wing_id, { type: 'dir.list', path: '~/' }).then(function(data) {
            var entries = data.entries || [];
            if (Array.isArray(entries)) {
                homeDirCache = entries.map(function(e) {
                    return { name: e.name, path: e.path, isDir: e.is_dir };
                });
            }
        }).catch(function() {});
    }
}

export function updatePaletteState(isOpen) {
    var online = onlineWings();
    var alive = online.length > 0;
    var wasWaiting = DOM.paletteDialog.classList.contains('palette-waiting');

    DOM.paletteSearch.disabled = !alive;
    DOM.paletteDialog.classList.toggle('palette-waiting', !alive);

    if (alive) {
        if (wasWaiting) {
            DOM.paletteDialog.classList.add('palette-awake');
            setTimeout(function() { DOM.paletteDialog.classList.remove('palette-awake'); }, 800);
            DOM.paletteSearch.focus();
        }
        if (!isOpen) {
            var agents = currentPaletteAgents();
            var last = getLastTermAgent();
            var idx = agents.indexOf(last);
            paletteAgentIndex = idx >= 0 ? idx : 0;
        }
        renderPaletteStatus();
        if (!isOpen) {
            renderPaletteResults(DOM.paletteSearch.value);
        }
    } else {
        DOM.paletteStatus.innerHTML = '<span class="palette-waiting-text">no wings online</span>';
        DOM.paletteResults.innerHTML = '<div class="palette-waiting-msg">' +
            '<div class="waiting-dot"></div>' +
            '<div>no wings online</div>' +
            '<div class="palette-waiting-hint"><a href="https://wingthing.ai/install" target="_blank">install wt</a>, then <code>wt login</code> and <code>wt start</code></div>' +
        '</div>';
    }
}

export function hidePalette() {
    DOM.commandPalette.style.display = 'none';
    if (dirListTimer) { clearTimeout(dirListTimer); dirListTimer = null; }
    dirListPending = false;
    dirListCache = [];
    dirListCacheDir = '';
    dirListQuery = '';
    homeDirCache = [];
}

export function cyclePaletteWing() {
    var online = onlineWings();
    if (online.length <= 1) return;
    paletteWingIndex = (paletteWingIndex + 1) % online.length;
    paletteAgentIndex = 0;
    renderPaletteStatus();
    renderPaletteResults('');
    DOM.paletteSearch.value = '';
}

function renderPaletteStatus() {
    var wing = currentPaletteWing();
    var wingName = wing ? wingDisplayName(wing) : '?';
    var agent = currentPaletteAgent();
    DOM.paletteStatus.innerHTML = '<span class="accent">' + escapeHtml(wingName) + '</span> &middot; ' +
        'terminal &middot; <span class="accent">' + agentWithIcon(agent) + '</span>';
}

function renderPaletteResults(filter) {
    var wing = currentPaletteWing();
    var wingId = wing ? wing.wing_id : '';
    var wingProjects = wingId
        ? S.allProjects.filter(function(p) { return p.wingId === wingId; })
        : S.allProjects;

    var items = [];

    var lower = filter ? filter.toLowerCase() : '';
    var filtered = lower
        ? wingProjects.filter(function(p) {
            return p.name.toLowerCase().indexOf(lower) !== -1 ||
                   p.path.toLowerCase().indexOf(lower) !== -1;
        })
        : wingProjects.slice();

    filtered.sort(function(a, b) {
        var ca = nestedRepoCount(a.path, wingProjects);
        var cb = nestedRepoCount(b.path, wingProjects);
        if (ca !== cb) return cb - ca;
        return a.name.localeCompare(b.name);
    });

    filtered.forEach(function(p) {
        items.push({ name: p.name, path: p.path, isDir: true });
    });

    var seenPaths = {};
    items.forEach(function(it) { seenPaths[it.path] = true; });
    var homeExtras = homeDirCache.filter(function(e) {
        if (seenPaths[e.path]) return false;
        return !lower || e.name.toLowerCase().indexOf(lower) !== -1 ||
               e.path.toLowerCase().indexOf(lower) !== -1;
    });
    homeExtras.sort(function(a, b) {
        var ca = nestedRepoCount(a.path, wingProjects);
        var cb = nestedRepoCount(b.path, wingProjects);
        if (ca !== cb) return cb - ca;
        return a.name.localeCompare(b.name);
    });
    homeExtras.forEach(function(e) {
        items.push({ name: e.name, path: e.path, isDir: e.isDir });
    });

    renderPaletteItems(items);
}

function renderPaletteItems(items) {
    paletteSelectedIndex = 0;

    if (items.length === 0) {
        DOM.paletteResults.innerHTML = '';
        return;
    }

    DOM.paletteResults.innerHTML = items.map(function(item, i) {
        var icon = item.isDir ? '/' : '';
        return '<div class="palette-item' + (i === 0 ? ' selected' : '') + '" data-path="' + escapeHtml(item.path) + '" data-index="' + i + '">' +
            '<span class="palette-name">' + escapeHtml(item.name) + icon + '</span>' +
            (item.path ? '<span class="palette-path">' + escapeHtml(shortenPath(item.path)) + '</span>' : '') +
        '</div>';
    }).join('');

    DOM.paletteResults.querySelectorAll('.palette-item').forEach(function(item) {
        item.addEventListener('click', function() {
            launchFromPalette(item.dataset.path);
        });
        item.addEventListener('mouseenter', function() {
            DOM.paletteResults.querySelectorAll('.palette-item').forEach(function(el) { el.classList.remove('selected'); });
            item.classList.add('selected');
            paletteSelectedIndex = parseInt(item.dataset.index);
        });
    });
}

function filterCachedItems(prefix) {
    var items = dirListCache;
    if (prefix) {
        items = items.filter(function(e) {
            return e.name.toLowerCase().indexOf(prefix) === 0;
        });
    }
    return items;
}

export function debouncedDirList(value) {
    if (dirListTimer) clearTimeout(dirListTimer);

    if (!value || (value.charAt(0) !== '/' && value.charAt(0) !== '~')) {
        dirListPending = false;
        dirListCache = [];
        dirListCacheDir = '';
        renderPaletteResults(value);
        return;
    }

    dirListQuery = value;
    var parsed = dirParent(value);

    if (dirListCacheDir && dirListCacheDir === parsed.dir) {
        dirListPending = false;
        renderPaletteItems(filterCachedItems(parsed.prefix));
        return;
    }

    if (dirListCache.length > 0) {
        renderPaletteItems(filterCachedItems(parsed.prefix));
    }

    dirListPending = true;
    dirListTimer = setTimeout(function() { fetchDirList(parsed.dir); }, 150);
}

function fetchDirList(dirPath) {
    var wing = currentPaletteWing();
    if (!wing) { dirListPending = false; return; }

    var fetchId = crypto.randomUUID();
    dirListQuery = fetchId;
    dirListPending = true;

    sendTunnelRequest(wing.wing_id, { type: 'dir.list', path: dirPath }).then(function(data) {
        dirListPending = false;

        if (dirListQuery !== fetchId) return;

        var entries = data.entries || [];

        var currentParsed = dirParent(DOM.paletteSearch.value);
        if (currentParsed.dir !== dirPath) return;

        if (!entries || !Array.isArray(entries)) {
            dirListCache = [];
            dirListCacheDir = '';
            renderPaletteItems([]);
            return;
        }
        var items = entries.map(function(e) {
            return { name: e.name, path: e.path, isDir: e.is_dir };
        });
        items.sort(function(a, b) {
            if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
            var ca = nestedRepoCount(a.path, S.allProjects);
            var cb = nestedRepoCount(b.path, S.allProjects);
            if (ca !== cb) return cb - ca;
            return a.name.localeCompare(b.name);
        });
        var absDirPath = dirPath;
        if (items.length > 0 && items[0].path) {
            absDirPath = items[0].path.replace(/\/[^\/]+$/, '');
        }
        var dirLabel = shortenPath(absDirPath).replace(/\/$/, '') || absDirPath;
        items.unshift({ name: dirLabel, path: absDirPath, isDir: true });

        dirListCache = items;
        dirListCacheDir = dirPath;

        renderPaletteItems(filterCachedItems(currentParsed.prefix));
    }).catch(function() {
        dirListPending = false;
    });
}

export function navigatePalette(dir) {
    var items = DOM.paletteResults.querySelectorAll('.palette-item');
    if (items.length === 0) return;
    items[paletteSelectedIndex].classList.remove('selected');
    paletteSelectedIndex = (paletteSelectedIndex + dir + items.length) % items.length;
    items[paletteSelectedIndex].classList.add('selected');
    items[paletteSelectedIndex].scrollIntoView({ block: 'nearest' });
}

export function tabCompletePalette() {
    var selected = DOM.paletteResults.querySelector('.palette-item.selected');
    if (!selected) return;
    var path = selected.dataset.path;
    if (!path) return;
    var short = shortenPath(path);
    var nameEl = selected.querySelector('.palette-name');
    var isDir = nameEl && nameEl.textContent.slice(-1) === '/';
    if (isDir) {
        DOM.paletteSearch.value = short + '/';
        debouncedDirList(DOM.paletteSearch.value);
    } else {
        DOM.paletteSearch.value = short;
    }
}

export function launchFromPalette(cwd) {
    if (onlineWings().length === 0) return;
    var wing = currentPaletteWing();
    if (!wing) return;
    var agent = currentPaletteAgent();
    var wingId = wing.wing_id;
    var validCwd = (cwd && cwd.charAt(0) === '/') ? cwd : '';
    hidePalette();
    setLastTermAgent(agent);
    showTerminal();
    connectPTY(agent, validCwd, wingId);
}

export function isDirListPending() {
    return dirListPending;
}

// State
let token = localStorage.getItem('wt_token');
let deviceId = localStorage.getItem('wt_device_id');
let tasks = [];
let selectedTaskId = null;
let pollTimer = null;
let refreshTimer = null;

// DOM refs
const connectionStatus = document.getElementById('connection-status');
const loginSection = document.getElementById('login-section');
const loginForm = document.getElementById('login-form');
const loginPending = document.getElementById('login-pending');
const userCodeEl = document.getElementById('user-code');
const submitSection = document.getElementById('submit-section');
const taskForm = document.getElementById('task-form');
const taskInput = document.getElementById('task-input');
const statusSection = document.getElementById('status-section');
const pendingCount = document.getElementById('pending-count');
const runningCount = document.getElementById('running-count');
const tokensToday = document.getElementById('tokens-today');
const timelineSection = document.getElementById('timeline-section');
const timelineList = document.getElementById('timeline-list');
const threadSection = document.getElementById('thread-section');
const threadContent = document.getElementById('thread-content');

// Init
function init() {
    if (!deviceId) {
        deviceId = crypto.randomUUID();
        localStorage.setItem('wt_device_id', deviceId);
    }

    if (token) {
        showApp();
        loadTasks();
        refreshTimer = setInterval(loadTasks, 10000);
    } else {
        showLogin();
    }

    loginForm.addEventListener('submit', function (e) {
        e.preventDefault();
        startDeviceAuth();
    });

    taskForm.addEventListener('submit', function (e) {
        e.preventDefault();
        var what = taskInput.value.trim();
        if (what) {
            submitTask(what);
            taskInput.value = '';
        }
    });
}

// UI state management
function showLogin() {
    loginSection.style.display = '';
    loginForm.style.display = '';
    loginPending.style.display = 'none';
    submitSection.style.display = 'none';
    statusSection.style.display = 'none';
    timelineSection.style.display = 'none';
    threadSection.style.display = 'none';
}

function showApp() {
    loginSection.style.display = 'none';
    submitSection.style.display = '';
    statusSection.style.display = '';
    timelineSection.style.display = '';
    threadSection.style.display = '';
    updateConnectionStatus('connected');
}

// Connection status
function updateConnectionStatus(status) {
    connectionStatus.textContent = status;
    connectionStatus.className = status;
}

// API helpers
function api(method, path, body) {
    var opts = {
        method: method,
        headers: {
            'Authorization': 'Bearer ' + token,
            'Content-Type': 'application/json'
        }
    };
    if (body) opts.body = JSON.stringify(body);
    return fetch(path, opts).then(function (resp) {
        if (resp.status === 401) {
            token = null;
            localStorage.removeItem('wt_token');
            showLogin();
            throw new Error('unauthorized');
        }
        return resp.json();
    });
}

// Load tasks from server
function loadTasks() {
    api('GET', '/api/tasks').then(function (data) {
        if (Array.isArray(data)) {
            tasks = data;
            renderTimeline();
            updateStats();
            if (selectedTaskId) renderThread();
        }
    }).catch(function () {});
}

function updateStats() {
    var pending = 0, running = 0;
    for (var i = 0; i < tasks.length; i++) {
        if (tasks[i].status === 'pending') pending++;
        if (tasks[i].status === 'running') running++;
    }
    pendingCount.textContent = pending;
    runningCount.textContent = running;
}

// Task submission
function submitTask(prompt) {
    api('POST', '/api/tasks', { prompt: prompt }).then(function (data) {
        if (data.task_id) {
            selectedTaskId = data.task_id;
            loadTasks();
            streamTask(data.task_id);
        }
    }).catch(function (err) {
        console.error('submit error:', err);
    });
}

// SSE streaming
function streamTask(taskId) {
    var es = new EventSource('/api/tasks/' + taskId + '/stream?token=' + encodeURIComponent(token));
    var output = '';

    es.onmessage = function (e) {
        output += e.data;
        // Update task in local list
        for (var i = 0; i < tasks.length; i++) {
            if (tasks[i].id === taskId) {
                tasks[i].output = output;
                tasks[i].status = 'running';
                break;
            }
        }
        if (selectedTaskId === taskId) renderThread();
    };

    es.addEventListener('done', function (e) {
        es.close();
        for (var i = 0; i < tasks.length; i++) {
            if (tasks[i].id === taskId) {
                tasks[i].status = e.data;
                break;
            }
        }
        renderTimeline();
        if (selectedTaskId === taskId) renderThread();
        // Refresh from server to get final state
        setTimeout(loadTasks, 500);
    });

    es.onerror = function () {
        es.close();
        setTimeout(loadTasks, 1000);
    };
}

// Rendering
function renderTimeline() {
    timelineList.innerHTML = '';
    if (tasks.length === 0) {
        timelineList.innerHTML = '<div class="timeline-item"><span class="task-what" style="color:var(--text-dim)">no tasks yet</span></div>';
        return;
    }
    for (var i = 0; i < tasks.length; i++) {
        var task = tasks[i];
        var el = document.createElement('div');
        el.className = 'timeline-item ' + task.status;
        el.setAttribute('data-id', task.id);

        var whatSpan = document.createElement('span');
        whatSpan.className = 'task-what';
        whatSpan.textContent = task.prompt || task.skill || task.id;

        var statusSpan = document.createElement('span');
        statusSpan.className = 'task-status';
        statusSpan.textContent = task.status;

        el.appendChild(whatSpan);
        el.appendChild(statusSpan);

        el.addEventListener('click', (function (id) {
            return function () {
                selectedTaskId = id;
                renderThread();
            };
        })(task.id));

        timelineList.appendChild(el);
    }
}

function renderThread() {
    if (!selectedTaskId) {
        threadContent.innerHTML = '<span class="empty">select a task to view details</span>';
        return;
    }
    var task = null;
    for (var i = 0; i < tasks.length; i++) {
        if (tasks[i].id === selectedTaskId) { task = tasks[i]; break; }
    }
    if (!task) {
        threadContent.innerHTML = '<span class="empty">task not found</span>';
        return;
    }

    var html = '<pre>';
    html += 'task: ' + escapeHtml(task.prompt || task.skill || task.id) + '\n';
    html += 'status: ' + escapeHtml(task.status) + '\n';
    if (task.agent) html += 'agent: ' + escapeHtml(task.agent) + '\n';
    if (task.wing_id) html += 'wing: ' + escapeHtml(task.wing_id) + '\n';
    if (task.output) {
        html += '\n--- output ---\n' + escapeHtml(task.output);
    }
    if (task.error) {
        html += '\n--- error ---\n' + escapeHtml(task.error);
    }
    html += '</pre>';
    threadContent.innerHTML = html;
}

// Auth flow
function startDeviceAuth() {
    loginForm.style.display = 'none';
    loginPending.style.display = '';

    fetch('/auth/device', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ machine_id: deviceId })
    })
    .then(function (resp) { return resp.json(); })
    .then(function (data) {
        if (data.error) {
            loginForm.style.display = '';
            loginPending.style.display = 'none';
            return;
        }
        userCodeEl.textContent = data.user_code;
        pollForToken(data.device_code, data.interval || 5);
    })
    .catch(function () {
        loginForm.style.display = '';
        loginPending.style.display = 'none';
    });
}

function pollForToken(deviceCode, interval) {
    if (pollTimer) clearInterval(pollTimer);

    pollTimer = setInterval(function () {
        fetch('/auth/token', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ device_code: deviceCode })
        })
        .then(function (resp) { return resp.json(); })
        .then(function (data) {
            if (data.token) {
                clearInterval(pollTimer);
                pollTimer = null;
                token = data.token;
                localStorage.setItem('wt_token', token);
                showApp();
                loadTasks();
                refreshTimer = setInterval(loadTasks, 10000);
            }
            if (data.error && data.error !== 'authorization_pending') {
                clearInterval(pollTimer);
                pollTimer = null;
                showLogin();
            }
        })
        .catch(function () {});
    }, interval * 1000);
}

// Helpers
function escapeHtml(str) {
    var div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

// Service worker
if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('sw.js').catch(function () {});
}

// Boot
init();

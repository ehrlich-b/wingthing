// State
let token = localStorage.getItem('wt_token');
let deviceId = localStorage.getItem('wt_device_id');
let tasks = [];
let selectedTaskId = null;
let pollTimer = null;

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
    } else {
        showLogin();
    }

    loginForm.addEventListener('submit', function (e) {
        e.preventDefault();
        startDeviceAuth();
    });

    taskForm.addEventListener('submit', function (e) {
        e.preventDefault();
        const what = taskInput.value.trim();
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
    renderTimeline();
    renderThread();
}

// Connection status
function updateConnectionStatus(status) {
    connectionStatus.textContent = status;
    connectionStatus.className = status;
}

// Task management
function addTask(id, what) {
    tasks.unshift({
        id,
        what,
        status: 'pending',
        output: '',
        error: '',
        timestamp: Date.now()
    });
    renderTimeline();
}

function updateTask(taskId, updates) {
    for (let i = 0; i < tasks.length; i++) {
        if (tasks[i].id === taskId) {
            Object.assign(tasks[i], updates);
            renderTimeline();
            if (selectedTaskId === taskId) {
                renderThread();
            }
            return;
        }
    }
}

function submitTask(what) {
    const id = crypto.randomUUID();
    addTask(id, what);
    selectedTaskId = id;
    renderThread();
    // TODO: POST to API when task submission endpoint exists
}

// Rendering
function renderTimeline() {
    timelineList.innerHTML = '';
    if (tasks.length === 0) {
        timelineList.innerHTML = '<div class="timeline-item"><span class="task-what" style="color:var(--text-dim)">no tasks yet</span></div>';
        return;
    }
    for (const task of tasks) {
        const el = document.createElement('div');
        el.className = 'timeline-item ' + task.status;
        el.setAttribute('data-id', task.id);

        const whatSpan = document.createElement('span');
        whatSpan.className = 'task-what';
        whatSpan.textContent = task.what;

        const statusSpan = document.createElement('span');
        statusSpan.className = 'task-status';
        statusSpan.textContent = task.status;

        el.appendChild(whatSpan);
        el.appendChild(statusSpan);

        el.addEventListener('click', () => {
            selectedTaskId = task.id;
            renderThread();
        });

        timelineList.appendChild(el);
    }
}

function renderThread() {
    if (!selectedTaskId) {
        threadContent.innerHTML = '<span class="empty">select a task to view details</span>';
        return;
    }
    const task = tasks.find(t => t.id === selectedTaskId);
    if (!task) {
        threadContent.innerHTML = '<span class="empty">task not found</span>';
        return;
    }

    let html = '<pre>';
    html += 'task: ' + escapeHtml(task.what) + '\n';
    html += 'status: ' + escapeHtml(task.status) + '\n';
    if (task.progress) {
        html += 'progress: ' + task.progress + '%\n';
    }
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
    .then(resp => resp.json())
    .then(data => {
        if (data.error) {
            loginForm.style.display = '';
            loginPending.style.display = 'none';
            return;
        }
        userCodeEl.textContent = data.user_code;
        pollForToken(data.device_code, data.interval || 5);
    })
    .catch(() => {
        loginForm.style.display = '';
        loginPending.style.display = 'none';
    });
}

function pollForToken(deviceCode, interval) {
    if (pollTimer) clearInterval(pollTimer);

    pollTimer = setInterval(() => {
        fetch('/auth/token', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ device_code: deviceCode })
        })
        .then(resp => resp.json())
        .then(data => {
            if (data.token) {
                clearInterval(pollTimer);
                pollTimer = null;
                token = data.token;
                localStorage.setItem('wt_token', token);
                showApp();
            }
            if (data.error && data.error !== 'authorization_pending') {
                clearInterval(pollTimer);
                pollTimer = null;
                showLogin();
            }
        })
        .catch(() => {
            // Network error — keep trying
        });
    }, interval * 1000);
}

// Helpers
function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

// Service worker
if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('sw.js').catch(() => {});
}

// Boot
init();

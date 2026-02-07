(function () {
    'use strict';

    // State
    var ws = null;
    var token = localStorage.getItem('wt_token');
    var deviceId = localStorage.getItem('wt_device_id');
    var tasks = []; // {id, what, status, output, error, timestamp}
    var selectedTaskId = null;
    var pollTimer = null;

    // DOM refs
    var connectionStatus = document.getElementById('connection-status');
    var loginSection = document.getElementById('login-section');
    var loginForm = document.getElementById('login-form');
    var loginPending = document.getElementById('login-pending');
    var userCodeEl = document.getElementById('user-code');
    var submitSection = document.getElementById('submit-section');
    var taskForm = document.getElementById('task-form');
    var taskInput = document.getElementById('task-input');
    var statusSection = document.getElementById('status-section');
    var pendingCount = document.getElementById('pending-count');
    var runningCount = document.getElementById('running-count');
    var tokensToday = document.getElementById('tokens-today');
    var timelineSection = document.getElementById('timeline-section');
    var timelineList = document.getElementById('timeline-list');
    var threadSection = document.getElementById('thread-section');
    var threadContent = document.getElementById('thread-content');

    // Init
    function init() {
        if (!deviceId) {
            deviceId = crypto.randomUUID();
            localStorage.setItem('wt_device_id', deviceId);
        }

        if (token) {
            showApp();
            connect();
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
        renderTimeline();
        renderThread();
    }

    // Connection status
    function updateConnectionStatus(status) {
        connectionStatus.textContent = status;
        connectionStatus.className = status;
    }

    // WebSocket
    function connect() {
        if (!token) return;

        var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        var wsUrl = proto + '//' + location.host + '/ws/client?token=' + token;
        ws = new WebSocket(wsUrl);

        ws.onopen = function () {
            updateConnectionStatus('connected');
        };

        ws.onclose = function () {
            updateConnectionStatus('disconnected');
            ws = null;
            setTimeout(connect, 3000);
        };

        ws.onerror = function () {
            // onclose will fire after this
        };

        ws.onmessage = function (event) {
            try {
                var msg = JSON.parse(event.data);
                handleMessage(msg);
            } catch (e) {
                // ignore malformed messages
            }
        };
    }

    function handleMessage(msg) {
        var payload;
        if (typeof msg.payload === 'string') {
            try { payload = JSON.parse(msg.payload); } catch (e) { payload = msg.payload; }
        } else {
            payload = msg.payload;
        }

        switch (msg.type) {
            case 'task_result':
                updateTask(payload.task_id, {
                    status: payload.status,
                    output: payload.output || '',
                    error: payload.error || ''
                });
                break;
            case 'task_status':
                updateTask(payload.task_id, {
                    status: payload.status,
                    progress: payload.progress
                });
                break;
            case 'status':
                pendingCount.textContent = payload.pending || 0;
                runningCount.textContent = payload.running || 0;
                tokensToday.textContent = payload.tokens_today || 0;
                break;
            case 'error':
                // Show error in thread section briefly
                threadContent.innerHTML = '<pre>' + escapeHtml(payload.message || 'unknown error') + '</pre>';
                break;
        }
    }

    // Task management
    function addTask(id, what) {
        tasks.unshift({
            id: id,
            what: what,
            status: 'pending',
            output: '',
            error: '',
            timestamp: Date.now()
        });
        renderTimeline();
    }

    function updateTask(taskId, updates) {
        for (var i = 0; i < tasks.length; i++) {
            if (tasks[i].id === taskId) {
                for (var key in updates) {
                    if (updates.hasOwnProperty(key)) {
                        tasks[i][key] = updates[key];
                    }
                }
                renderTimeline();
                if (selectedTaskId === taskId) {
                    renderThread();
                }
                return;
            }
        }
        // Task not found locally — create a placeholder
        tasks.unshift({
            id: taskId,
            what: '(remote task)',
            status: updates.status || 'unknown',
            output: updates.output || '',
            error: updates.error || '',
            timestamp: Date.now()
        });
        renderTimeline();
    }

    function submitTask(what) {
        if (!ws || ws.readyState !== WebSocket.OPEN) return;

        var id = crypto.randomUUID();
        var payload = { what: what, type: 'prompt' };

        ws.send(JSON.stringify({
            type: 'task_submit',
            id: id,
            payload: JSON.stringify(payload),
            timestamp: Date.now()
        }));

        addTask(id, what);
        selectedTaskId = id;
        renderThread();
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
            whatSpan.textContent = task.what;

            var statusSpan = document.createElement('span');
            statusSpan.className = 'task-status';
            statusSpan.textContent = task.status;

            el.appendChild(whatSpan);
            el.appendChild(statusSpan);

            el.addEventListener('click', (function (taskId) {
                return function () {
                    selectedTaskId = taskId;
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
            if (tasks[i].id === selectedTaskId) {
                task = tasks[i];
                break;
            }
        }
        if (!task) {
            threadContent.innerHTML = '<span class="empty">task not found</span>';
            return;
        }

        var html = '<pre>';
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
                    connect();
                }
                // If error is authorization_pending, keep polling
                // If error is expired_code or invalid_code, stop
                if (data.error && data.error !== 'authorization_pending') {
                    clearInterval(pollTimer);
                    pollTimer = null;
                    showLogin();
                }
            })
            .catch(function () {
                // Network error — keep trying
            });
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
        navigator.serviceWorker.register('/app/sw.js').catch(function () {
            // SW registration failed — not critical
        });
    }

    // Boot
    init();
})();

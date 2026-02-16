// localStorage keys
export var CACHE_KEY = 'wt_sessions';
export var WINGS_CACHE_KEY = 'wt_wings';
export var LAST_TERM_KEY = 'wt_last_term_agent';
export var TERM_BUF_PREFIX = 'wt_termbuf_';
export var WING_ORDER_KEY = 'wt_wing_order';
export var EGG_ORDER_KEY = 'wt_egg_order';
export var TERM_THUMB_PREFIX = 'wt_termthumb_';
export var WING_SESSIONS_PREFIX = 'wt_wing_sessions_';

// Mutable state — all modules import S and mutate properties
export var S = {
    ptyWs: null,
    ptySessionId: null,
    ptyWingId: null,
    term: null,
    fitAddon: null,
    serializeAddon: null,
    saveBufferTimer: null,
    ctrlActive: false,
    currentUser: null,
    e2eKey: null,
    availableAgents: [],
    allProjects: [],
    wingsData: [],
    sessionsData: [],
    sessionNotifications: {},
    activeView: 'home',
    titleFlashTimer: null,
    appWs: null,
    appWsBackoff: 1000,
    latestVersion: '',
    ptyReconnecting: false,
    ptyBandwidthExceeded: false,
    currentWingId: null,
    wingPastSessions: {},
    tunnelKeys: {},
    tunnelAuthTokens: {},
    accountExpandSlug: null,
};

// DOM refs — populated by initDOM(), called once from main.js init
export var DOM = {};
export function initDOM() {
    DOM.detailOverlay = document.getElementById('detail-overlay');
    DOM.detailBackdrop = document.getElementById('detail-backdrop');
    DOM.detailDialog = document.getElementById('detail-dialog');
    DOM.sessionTabs = document.getElementById('session-tabs');
    DOM.newSessionBtn = document.getElementById('new-session-btn');
    DOM.homeBtn = document.getElementById('home-btn');
    DOM.headerLogo = document.getElementById('header-logo');
    DOM.headerTitle = document.getElementById('header-title');
    DOM.userInfo = document.getElementById('user-info');
    DOM.homeSection = document.getElementById('home-section');
    DOM.wingStatusEl = document.getElementById('wing-status');
    DOM.sessionsList = document.getElementById('sessions-list');
    DOM.emptyState = document.getElementById('empty-state');
    DOM.terminalSection = document.getElementById('terminal-section');
    DOM.terminalContainer = document.getElementById('terminal-container');
    DOM.ptyStatus = document.getElementById('pty-status');
    DOM.sessionCloseBtn = document.getElementById('session-close-btn');
    DOM.chatSection = document.getElementById('chat-section');
    DOM.wingDetailSection = document.getElementById('wing-detail-section');
    DOM.wingDetailContent = document.getElementById('wing-detail-content');
    DOM.accountSection = document.getElementById('account-section');
    DOM.accountContent = document.getElementById('account-content');
    DOM.commandPalette = document.getElementById('command-palette');
    DOM.paletteBackdrop = document.getElementById('palette-backdrop');
    DOM.paletteDialog = document.getElementById('palette-dialog');
    DOM.paletteSearch = document.getElementById('palette-search');
    DOM.paletteResults = document.getElementById('palette-results');
    DOM.paletteStatus = document.getElementById('palette-status');
    DOM.paletteHints = document.getElementById('palette-hints');
}

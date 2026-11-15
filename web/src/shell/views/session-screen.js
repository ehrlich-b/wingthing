import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { terminalTheme } from '../../terminal-theme.js';
import { terminal } from '../common.js';

let screen;
export function updateSessionScreen(main, session, visible) {
    const mount = main.querySelector('[data-engine="session-screen"]');
    if (!mount || screen?.mount !== mount) {
        screen?.observer.disconnect(); screen?.term.dispose(); screen = null;
        if (!mount) return;
        const term = new Terminal({ disableStdin: true, cursorBlink: false, cursorInactiveStyle: 'none',
            fontSize: 12, fontFamily: "'JetBrains Mono', monospace", theme: terminalTheme, scrollback: 2000 });
        const fit = new FitAddon(); term.loadAddon(fit); term.open(mount);
        const observer = new ResizeObserver(() => { if (mount.clientWidth && mount.clientHeight) fit.fit(); });
        observer.observe(mount); screen = { mount, term, fit, observer, text: null, key: '' };
    }
    if (!session) return;
    const key = session.wing_id + '/' + session.id, text = visible ? terminal(session).text : '';
    if (screen.key !== key || screen.text !== text) {
        screen.term.reset(); screen.term.write(text); screen.text = text; screen.key = key;
    }
}

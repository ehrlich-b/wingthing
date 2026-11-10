import { head } from '../frame.js';
import { wingOf, machineName, machineSessions } from '../select.js';
import { escapeMarkup as esc } from '../../security.js';
export function viewMachine(route) {
    return head(esc(machineName(wingOf(route.wingId))), machineSessions(route.wingId).length + ' sessions') + '<div data-engine="machine"></div>';
}

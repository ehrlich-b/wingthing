import { head } from '../frame.js';
export function viewAccount() {
    return head('Account', '') + '<div data-engine="account"></div>';
}

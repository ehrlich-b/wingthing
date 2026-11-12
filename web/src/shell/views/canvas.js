import { head } from '../frame.js';
export function viewCanvas() {
    return head('Canvas', '') + '<div data-engine="canvas"></div>';
}

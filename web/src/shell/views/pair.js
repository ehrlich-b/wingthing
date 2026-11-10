import { head } from '../frame.js';
import {  } from '../select.js';
import {  } from '../common.js';
function viewPair() {
    return head('Pair a machine', 'On the computer') + '<p>Open the Pair page to join this machine.</p><a href="#pair" onclick="location.reload()">Open Pair</a>';
}

export { viewPair };

import test from 'node:test';
import assert from 'node:assert/strict';
import {wingDisplayName} from '../src/helpers.js';
test('same-named machines retain distinct identities and get unique visible suffixes',()=>{
 const peers=[{wing_id:'abcdef12-one',hostname:'Build'},{wing_id:'abcdef12-two',hostname:'Build'},{wing_id:'other',hostname:'Laptop'}];
 assert.equal(wingDisplayName(peers[0],peers),'Build · abcdef12-o');assert.equal(wingDisplayName(peers[1],peers),'Build · abcdef12-t');assert.equal(wingDisplayName(peers[2],peers),'Laptop');assert.equal(peers[0].wing_id,'abcdef12-one');
});

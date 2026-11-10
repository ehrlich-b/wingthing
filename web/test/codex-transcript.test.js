import test from 'node:test';
import assert from 'node:assert/strict';
import { emptyConversationState, applyConversationRead } from '../src/conversation-state.js';
test('Codex transcript replaces prompt hook fallbacks once each and keeps repeated turns in native order',()=>{
 const hook=(sequence)=>({sequence,source:'codex_hook',type:'prompt_submitted',text:'repeat'});
 const msg=(sequence,role,text)=>({sequence,source:'codex_transcript',type:'message',role,text});
 const view=events=>({session_id:'ours',agent:'codex',provider_session_id:'thread',cursor:events.at(-1).sequence,events});
 let state=applyConversationRead(emptyConversationState(),view([hook(1),hook(2)]));
 state=applyConversationRead(state,view([msg(3,'user','repeat'),msg(4,'assistant','first'),msg(5,'user','repeat'),msg(6,'assistant','second')]));
 assert.deepEqual(state.messages.filter(m=>['user','assistant'].includes(m.type)).map(m=>[m.type,m.content]),[['user','repeat'],['assistant','first'],['user','repeat'],['assistant','second']]);
 state=applyConversationRead(state,view([msg(5,'user','repeat'),msg(6,'assistant','second')]));
 assert.equal(state.messages.filter(m=>m.type==='user').length,2);
});

test('a delayed Codex prompt hook cannot duplicate an already imported native user turn',()=>{
 const view=events=>({session_id:'ours',agent:'codex',provider_session_id:'thread',cursor:events.at(-1).sequence,events});
 let state=applyConversationRead(emptyConversationState(),view([{sequence:1,source:'codex_transcript',type:'message',role:'user',text:'same'}]));
 state=applyConversationRead(state,view([{sequence:2,source:'codex_hook',type:'prompt_submitted',text:'same'}]));
 state=applyConversationRead(state,view([{sequence:3,source:'codex_hook',type:'prompt_submitted',text:'same'},{sequence:4,source:'codex_transcript',type:'message',role:'user',text:'same'}]));
 assert.equal(state.messages.filter(m=>m.type==='user').length,2);
});

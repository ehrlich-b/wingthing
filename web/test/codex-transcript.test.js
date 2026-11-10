import test from 'node:test';
import assert from 'node:assert/strict';
import { emptyConversationState, applyConversationRead, restoredConversationState } from '../src/conversation-state.js';

for (const identified of [false, true]) {
 for (const chunked of [false, true]) {
  test(`resumed history imported after a repeated hook preserves the pending turn (IDs ${identified}, chunked ${chunked})`, () => {
   const hook = {sequence:1,source:'codex_hook',type:'prompt_submitted',text:'repeat',turn_id:identified ? 'new' : ''};
   const history = [
    {sequence:2,source:'codex_transcript',type:'message',role:'user',text:'repeat',turn_id:identified ? 'old' : ''},
    {sequence:3,source:'codex_transcript',type:'message',role:'assistant',text:'old reply'},
   ];
   const view = events => ({agent:'codex',cursor:events.at(-1).sequence,events});
   const rows = state => state.messages.filter(m=>['user','assistant'].includes(m.type)).map(m=>m.content);
   let state = applyConversationRead(emptyConversationState(), view(chunked ? [hook] : [hook,...history]));
   if (chunked) {
    state = applyConversationRead(state, view(history.slice(0,1)));
    assert.deepEqual(rows(state), ['repeat','repeat']);
    state = restoredConversationState(JSON.parse(JSON.stringify(state)));
    state = applyConversationRead(state, view(history.slice(1)));
   }
   assert.deepEqual(rows(state), ['repeat','old reply','repeat']);
   state = restoredConversationState(JSON.parse(JSON.stringify(state)));
   state = applyConversationRead(state, view([hook,...history]));
   assert.deepEqual(rows(state), ['repeat','old reply','repeat']);
   if (identified) {
    state = applyConversationRead(state, view([
     {sequence:4,source:'codex_transcript',type:'message',role:'user',text:'repeat',turn_id:'new'},
     {sequence:5,source:'codex_transcript',type:'message',role:'assistant',text:'new reply'},
    ]));
    assert.deepEqual(rows(state), ['repeat','old reply','repeat','new reply']);
   }
  });
 }
}

test('an older open native turn cannot consume a new hook with equal text', () => {
 const state = applyConversationRead(emptyConversationState(), {agent:'codex',events:[
  {sequence:1,source:'codex_transcript',type:'message',role:'user',text:'repeat',turn_id:'old'},
  {sequence:2,source:'codex_hook',type:'prompt_submitted',text:'repeat',turn_id:'new'},
 ]});
 assert.equal(state.messages.filter(m=>m.type==='user').length,2);
});

test('a hook delayed past its native answer matches the same turn once', () => {
 const state = applyConversationRead(emptyConversationState(), {agent:'codex',events:[
  {sequence:1,source:'codex_transcript',type:'message',role:'user',text:'repeat',turn_id:'same'},
  {sequence:2,source:'codex_transcript',type:'message',role:'assistant',text:'reply',turn_id:'same'},
  {sequence:3,source:'codex_hook',type:'prompt_submitted',text:'repeat',turn_id:'same'},
 ]});
 assert.deepEqual(state.messages.filter(m=>['user','assistant'].includes(m.type)).map(m=>m.content),['repeat','reply']);
});

for (const chunked of [false, true]) {
 test(`a repeated prompt after resumed history stays visible during transcript catch-up (${chunked ? 'separate reads' : 'one read'})`, () => {
  const events = [
   {sequence:1,source:'codex_transcript',type:'message',role:'user',text:'repeat'},
   {sequence:2,source:'codex_transcript',type:'message',role:'assistant',text:'old reply'},
   {sequence:3,source:'codex_hook',type:'prompt_submitted',text:'repeat',turn_id:'new'},
   {sequence:4,source:'codex_transcript',type:'message',role:'user',text:'repeat',turn_id:'new'},
   {sequence:5,source:'codex_transcript',type:'message',role:'assistant',text:'new reply'},
  ];
  const view = events => ({agent:'codex',cursor:events.at(-1).sequence,events});
  const rows = state => state.messages.filter(m=>['user','assistant'].includes(m.type)).map(m=>m.content);
  let state = emptyConversationState();
  if (chunked) state = applyConversationRead(state, view(events.slice(0,2)));
  state = applyConversationRead(state, view(events.slice(chunked ? 2 : 0,3)));
  assert.deepEqual(rows(state), ['repeat','old reply','repeat']);
  state = restoredConversationState(JSON.parse(JSON.stringify(state)));
  state = applyConversationRead(state, view(events.slice(0,3)));
  assert.deepEqual(rows(state), ['repeat','old reply','repeat']);
  state = applyConversationRead(state, view(events.slice(3)));
  assert.deepEqual(rows(state), ['repeat','old reply','repeat','new reply']);
 });
}

test('a new prompt cannot match an older native user behind another open turn', () => {
 const state = applyConversationRead(emptyConversationState(), {agent:'codex',cursor:4,events:[
  {sequence:1,source:'codex_transcript',type:'message',role:'user',text:'repeat'},
  {sequence:2,source:'codex_transcript',type:'message',role:'assistant',text:'old reply'},
  {sequence:3,source:'codex_transcript',type:'message',role:'user',text:'different'},
  {sequence:4,source:'codex_hook',type:'prompt_submitted',text:'repeat'},
 ]});
 assert.deepEqual(state.messages.filter(m=>['user','assistant'].includes(m.type)).map(m=>m.content), ['repeat','old reply','different','repeat']);
});

for (const second of ['second', 'first']) {
 for (const chunked of [false, true]) {
  test(`Codex partial transcript stays before the pending ${second === 'first' ? 'repeated' : 'distinct'} prompt (${chunked ? 'separate reads' : 'one read'})`, () => {
   const events = [
    {sequence:1,source:'codex_hook',type:'prompt_submitted',text:'first',turn_id:'first'},
    {sequence:2,source:'codex_hook',type:'prompt_submitted',text:second,turn_id:'second'},
    {sequence:3,source:'codex_transcript',type:'message',role:'user',text:'first',turn_id:'first'},
    {sequence:4,source:'codex_transcript',type:'message',role:'assistant',text:'reply first'},
    {sequence:5,source:'codex_transcript',type:'message',role:'user',text:second,turn_id:'second'},
    {sequence:6,source:'codex_transcript',type:'message',role:'assistant',text:'reply second'},
   ];
   const view = events => ({agent:'codex',cursor:events.at(-1).sequence,events});
   const rows = state => state.messages.filter(m=>['user','assistant'].includes(m.type)).map(m=>[m.type,m.content]);
   let state = emptyConversationState();
   if (chunked) {
    state = applyConversationRead(state, view(events.slice(0,2)));
    assert.deepEqual(rows(state), [['user','first'],['user',second]]);
    state = applyConversationRead(state, view(events.slice(2,3)));
    assert.deepEqual(rows(state), [['user','first'],['user',second]]);
    state = applyConversationRead(state, view(events.slice(3,4)));
   } else state = applyConversationRead(state, view(events.slice(0,4)));
   assert.deepEqual(rows(state), [['user','first'],['assistant','reply first'],['user',second]]);
   assert.equal(state.cursor, 4);
   const partial = JSON.parse(JSON.stringify(state));
   state = restoredConversationState(partial);
   state = applyConversationRead(state, view(events.slice(0,4)));
   assert.deepEqual(rows(state), rows(partial));
   state = applyConversationRead(state, view(events.slice(4)));
   assert.deepEqual(rows(state), [['user','first'],['assistant','reply first'],['user',second],['assistant','reply second']]);
   assert.equal(state.cursor, 6);
  });
 }
}

test('Codex transcript replaces prompt hook fallbacks once each and keeps repeated turns in native order',()=>{
 const hook=(sequence)=>({sequence,source:'codex_hook',type:'prompt_submitted',text:'repeat',turn_id:String(sequence)});
 const msg=(sequence,role,text)=>({sequence,source:'codex_transcript',type:'message',role,text,turn_id:sequence<5?'1':'2'});
 const view=events=>({session_id:'ours',agent:'codex',provider_session_id:'thread',cursor:events.at(-1).sequence,events});
 let state=applyConversationRead(emptyConversationState(),view([hook(1),hook(2)]));
 state=applyConversationRead(state,view([msg(3,'user','repeat'),msg(4,'assistant','first'),msg(5,'user','repeat'),msg(6,'assistant','second')]));
 assert.deepEqual(state.messages.filter(m=>['user','assistant'].includes(m.type)).map(m=>[m.type,m.content]),[['user','repeat'],['assistant','first'],['user','repeat'],['assistant','second']]);
 state=applyConversationRead(state,view([msg(5,'user','repeat'),msg(6,'assistant','second')]));
 assert.equal(state.messages.filter(m=>m.type==='user').length,2);
});

test('a delayed Codex prompt hook cannot duplicate an already imported native user turn',()=>{
 const view=events=>({session_id:'ours',agent:'codex',provider_session_id:'thread',cursor:events.at(-1).sequence,events});
 let state=applyConversationRead(emptyConversationState(),view([{sequence:1,source:'codex_transcript',type:'message',role:'user',text:'same',turn_id:'first'}]));
 state=applyConversationRead(state,view([{sequence:2,source:'codex_hook',type:'prompt_submitted',text:'same',turn_id:'first'}]));
 state=applyConversationRead(state,view([{sequence:3,source:'codex_hook',type:'prompt_submitted',text:'same',turn_id:'second'},{sequence:4,source:'codex_transcript',type:'message',role:'user',text:'same',turn_id:'second'}]));
 assert.equal(state.messages.filter(m=>m.type==='user').length,2);
});

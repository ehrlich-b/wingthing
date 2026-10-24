// Local encrypted peer with realistic account and session volume.
import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';
const bytes = s => Uint8Array.from(atob(s), c => c.charCodeAt(0));
const b64 = v => btoa(String.fromCharCode(...v));
const privateKey = new Uint8Array(32).fill(37);
const publicKey = b64(x25519.getPublicKey(privateKey));
const sessions = Array.from({length:24}, (_,i) => ({session_id:'session-'+i, name:['Prepare the customer onboarding release checklist','Investigate long-running accessibility regressions','Update the documentation and migration examples'][i%3]+' '+(i+1),agent:i%2?'codex':'claude',user_id:'account-owner-technical-id',cwd:'/fixture/product',lifecycle:{session_id:'session-'+i,status:'working',state:'working',process_alive:true,events:[],cursor:0,head_cursor:0}}));
if (typeof window !== 'undefined') {
window.accountFixture = {publicKey, sessions, calls:[]};
class FixtureSocket {
 static OPEN=1;
 constructor(url) { this.url=url; this.readyState=0; setTimeout(()=>{this.readyState=1;this.onopen?.({});},0); }
 close() { this.readyState=3; this.onclose?.({}); }
 send(value) {
  const outer=JSON.parse(value);
  if(outer.type!=='tunnel.req') throw Error('Unexpected socket message '+outer.type);
  const key=hkdf(sha256,x25519.getSharedSecret(privateKey,bytes(outer.sender_pub)),new Uint8Array(32),new TextEncoder().encode('wt-tunnel'),32);
  const payload=bytes(outer.payload);
  const inner=JSON.parse(new TextDecoder().decode(gcm(key,payload.slice(0,12)).decrypt(payload.slice(12))));
  accountFixture.calls.push(inner);
  let result;
  if(inner.type==='wing.info') result={hostname:'Product development workstation',platform:'linux',agents:['claude','codex']};
  else if(inner.type==='sessions.list') result={sessions};
  else if(inner.type==='sessions.inventory'||inner.type==='sessions.search') result={version:'wingthing.session-inventory.v1',wing_id:'machine-a',sessions:[],complete:true,sources:[],generated_at:new Date().toISOString()};
  else throw Error('Unexpected peer request '+inner.type);
  const iv=crypto.getRandomValues(new Uint8Array(12));
  const cipher=gcm(key,iv).encrypt(new TextEncoder().encode(JSON.stringify(result)));
  const answer=new Uint8Array(iv.length+cipher.length);answer.set(iv);answer.set(cipher,iv.length);
  setTimeout(()=>this.onmessage?.({data:JSON.stringify({type:'tunnel.res',request_id:outer.request_id,payload:b64(answer)})}),0);
 }
}
window.WebSocket=FixtureSocket;
}

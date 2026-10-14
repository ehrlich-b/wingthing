import test from 'node:test';
import assert from 'node:assert/strict';
import { parseUSD } from '../../src/paid/currency.js';
test('USD entry uses exact minor units and rejects ambiguous formats',()=>{
 for(const [text,cents] of [['0',0],['0.01',1],['5.00',500],['19.99',1999],['200.00',20000],[' 5.0 ',500]])assert.equal(parseUSD(text),cents);
 for(const text of ['',null,5,'-1','+1','1e2','1.001','200.01','1,000','005.00','5.','$5','NaN','Infinity'])assert.throws(()=>parseUSD(text));
});

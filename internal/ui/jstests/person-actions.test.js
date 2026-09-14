import {test} from 'node:test';
import assert from 'node:assert/strict';
import {JSDOM} from 'jsdom';
import {createPersonActions} from '../static/views/person-actions.js';
test('person menu hides unavailable actions and supports keyboard navigation and dismissal',()=>{
 const dom=new JSDOM('<body><button id="outside">Outside</button></body>');const d=dom.window.document;
 const actions=['Reset password','Sign out everywhere','Delete person'].map(text=>{const b=d.createElement('button');b.textContent=text;return b;});actions[0].hidden=true;
 const menu=createPersonActions(d,'person',actions);d.body.append(menu.element);
 const toggle=menu.element.querySelector('button'),list=menu.element.querySelector('[role="menu"]');
 assert.equal(list.hidden,true);assert.equal(list.children.length,2);
 toggle.dispatchEvent(new dom.window.KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));
 assert.equal(d.activeElement.textContent,'Sign out everywhere');assert.equal(toggle.getAttribute('aria-expanded'),'true');
 d.activeElement.dispatchEvent(new dom.window.KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));assert.equal(d.activeElement.textContent,'Delete person');
 d.activeElement.dispatchEvent(new dom.window.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));assert.equal(list.hidden,true);assert.equal(d.activeElement,toggle);
 toggle.click();d.getElementById('outside').click();assert.equal(list.hidden,true);
 toggle.click();menu.close();assert.equal(list.hidden,true);
});
test('menu activation runs the action once and returns focus to its trigger',()=>{
 const d=new JSDOM('<body></body>').window.document;let calls=0;
 const action=d.createElement('button');action.textContent='Sign out everywhere';action.onclick=()=>calls++;
 const menu=createPersonActions(d,'person',[action]);d.body.append(menu.element);const toggle=menu.element.querySelector('button');
 toggle.click();action.click();assert.equal(calls,1);assert.equal(d.activeElement,toggle);assert.equal(toggle.getAttribute('aria-expanded'),'false');
});

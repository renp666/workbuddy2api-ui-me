// Run with: node --test console/web_test.cjs (no frontend dependencies).
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const vm = require('node:vm');

test('malformed SSE cancels the live request and restores controls', async () => {
  const elements = new Map();
  function element() {
    return {value:'', hidden:false, disabled:false, textContent:'', handlers:{},
      addEventListener(name, fn) { this.handlers[name] = fn; },
      append(){}, replaceChildren(){}, scrollIntoView(){}, querySelector(){return null;}};
  }
  function get(id) {
    if (!elements.has(id)) elements.set(id, element());
    return elements.get(id);
  }
  let signal;
  const ctx = {
    document: {getElementById:get, querySelectorAll:()=>[], createElement:element},
    location:{origin:'http://console.test'}, AbortController, TextDecoder, Option:function(text,value){return {textContent:text,value};},
    setInterval(){}, clearTimeout(){},
    fetch:async (url, options) => {
      if (url !== '/admin/chat') throw new Error('not used');
      signal = options.signal;
      return {ok:true, status:200, body:{getReader:()=>({read:async()=>({done:false, value:new TextEncoder().encode('data: {invalid}\n\n')})})}};
    }
  };
  vm.runInNewContext(readFileSync(__dirname+'/web/app.js','utf8'), ctx);
  get('prompt').value = 'hello';
  get('model').value = 'cn:glm-5.2';
  await get('chat-form').handlers.submit({preventDefault(){}});
  assert.ok(signal.aborted, 'parser failure left upstream running');
  assert.equal(get('send-chat').disabled, false);
  assert.equal(get('stop-chat').hidden, true);
});

function logoutFixture(fetch) {
  const elements = new Map();
  const element = () => ({value:'', type:'password', hidden:false, textContent:'', children:[], handlers:{}, classList:{toggle(){},add(){},remove(){}},
    addEventListener(name, fn){this.handlers[name]=fn;},
    replaceChildren(...children){this.children=children;}, select(){}});
  const get = id => {if(!elements.has(id))elements.set(id,element());return elements.get(id);};
  const controller = new AbortController();
  const ctx = vm.createContext({document:{getElementById:get,querySelectorAll:()=>[]},
    location:{origin:'http://console.test'},setInterval(){},clearTimeout(){},fetch,controller});
  vm.runInContext(readFileSync(__dirname+'/web/app.js','utf8'),ctx);
  vm.runInContext("csrf='active-csrf';history=[{role:'user',content:'private chat'}];activeRequest=controller;flowID='active-flow';",ctx);
  get('messages').children=['private chat'];get('api-key').value='revealed-api-key';get('api-key').type='text';
  get('admin-key').value='private-admin-key';get('login-view').hidden=true;get('console-view').hidden=false;
  return {get,ctx,controller};
}

test('task reward distinguishes unknown from confirmed zero', () => {
  const {ctx}=logoutFixture(()=>new Promise(()=>{}));
  assert.equal(vm.runInContext('formatTaskReward(null)',ctx),'未确认');
  assert.equal(vm.runInContext('formatTaskReward(undefined)',ctx),'未确认');
  assert.equal(vm.runInContext('formatTaskReward(0)',ctx),'0');
  assert.equal(vm.runInContext('formatTaskReward(5)',ctx),'5');
});

test('usage statistics distinguish unknown tokens and credit from zero', () => {
  const {ctx}=logoutFixture(()=>new Promise(()=>{}));
  assert.equal(vm.runInContext('formatUsageTokens(-1)',ctx),'—');
  assert.equal(vm.runInContext('formatUsageTokens(0)',ctx),'0');
  assert.equal(vm.runInContext('formatUsageTokens(123)',ctx),'123');
  assert.equal(vm.runInContext('formatUsageCredit(null)',ctx),'—');
  assert.equal(vm.runInContext('formatUsageCredit(undefined)',ctx),'—');
  assert.equal(vm.runInContext('formatUsageCredit(0)',ctx),'0');
  assert.equal(vm.runInContext('formatUsageCredit(1.5)',ctx),'1.5');
});

test('task request id uses secure random bytes when randomUUID is unavailable', () => {
  const {ctx}=logoutFixture(()=>new Promise(()=>{}));
  ctx.crypto={getRandomValues(bytes){for(let i=0;i<bytes.length;i++)bytes[i]=i;return bytes;}};
  assert.equal(vm.runInContext('createTaskRequestID()',ctx),'000102030405060708090a0b0c0d0e0f');
  ctx.crypto={randomUUID(){return '11111111-2222-4333-8444-555555555555';},getRandomValues(){throw new Error('fallback used');}};
  assert.equal(vm.runInContext('createTaskRequestID()',ctx),'11111111-2222-4333-8444-555555555555');
});

test('task truth and all run statuses are formatted without guessing', () => {
  const {ctx}=logoutFixture(()=>new Promise(()=>{}));
  assert.equal(vm.runInContext("formatTaskAvailability({enabled:false,next_at:null})",ctx),'已禁用');
  assert.equal(vm.runInContext("formatTaskAvailability({enabled:true,next_at:null})",ctx),'暂不可用 / 未知');
  for(const [status,label] of Object.entries({running:'运行中',success:'成功',partial_failure:'部分失败',failed:'失败',skipped:'已跳过',interrupted:'已中断（结果未确认）',unknown:'结果未确认'})) {
    ctx.testStatus=status;
    assert.equal(vm.runInContext('formatRunStatus(testStatus)',ctx),label,status);
  }
});

function taskFixture(fetch, cryptoImpl={randomUUID:()=> '11111111-2222-4333-8444-555555555555'}) {
  const elements=new Map();
  function element(tag='div') {
    return {tagName:tag.toUpperCase(),value:'',type:'',hidden:false,disabled:false,textContent:'',className:'',children:[],dataset:{},handlers:{},classList:{toggled:{},toggle(name,on){this.toggled[name]=on;},add(name){this.toggled[name]=true;},remove(name){delete this.toggled[name];}},
      addEventListener(name,fn){this.handlers[name]=fn;},append(...children){this.children.push(...children);},appendChild(child){this.children.push(child);return child;},
      replaceChildren(...children){this.children=children;},setAttribute(name,value){this[name]=value;},insertBefore(child){this.children.unshift(child);},querySelector(){return null;},scrollIntoView(){},select(){}};
  }
  const get=id=>{if(!elements.has(id))elements.set(id,element());return elements.get(id);};
  const protocolButtons=['access-openai','access-anthropic','chat-openai','chat-anthropic'].map(id=>{const button=get(id);button.dataset.protocol=id.split('-')[1];return button;});
  const opened=[];
  const ctx=vm.createContext({document:{getElementById:get,querySelectorAll:selector=>selector==='[data-protocol]'?protocolButtons:[],createElement:element,hidden:false,handlers:{},addEventListener(name,fn){this.handlers[name]=fn;}},
    location:{origin:'http://console.test'},AbortController,TextDecoder,TextEncoder,Option:function(text,value){return {textContent:text,value};},
    setInterval(){},clearTimeout(){},setTimeout(){},fetch,crypto:cryptoImpl,navigator:{clipboard:{writeText:async()=>{}}},window:{open(url){const w={closed:false,location:{href:url||''},close(){this.closed=true;}};opened.push(w);return w;}}});
  vm.runInContext(readFileSync(__dirname+'/web/app.js','utf8'),ctx);
  vm.runInContext("csrf='active-csrf';page='tasks';taskState={items:[{id:'checkin',enabled:true,hours:[9,21],timezone:'Asia/Shanghai',next_at:null}],active_run:null,latest_runs:[]};",ctx);
  return {ctx,get,opened};
}

test('rapid task activation sends once and an unknown response retries the same intent id', async () => {
  let firstReject, postCount=0;
  const bodies=[];
  const response=(status,body)=>({ok:status<400,status,json:async()=>body});
  const {ctx}=taskFixture((url,options={})=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/tasks/checkin/runs') {
      postCount++;bodies.push(options.body);
      if(postCount===1)return new Promise((_,reject)=>{firstReject=reject;});
      return response(202,{id:'run-one',task_id:'checkin',status:'running',accounts:[]});
    }
    if(url==='/admin/tasks')return response(200,{items:[{id:'checkin',enabled:true,hours:[9,21],timezone:'Asia/Shanghai',next_at:null}],active_run:null,latest_runs:[]});
    if(url==='/admin/task-runs/run-one')return response(200,{id:'run-one',task_id:'checkin',status:'success',accounts:[],duration_ms:0,log:''});
    if(url==='/admin/task-runs?limit=20')return response(200,{items:[],next_before:null});
    throw new Error('unexpected '+url);
  });
  const first=vm.runInContext("triggerTask('checkin')",ctx);
  await Promise.resolve();
  await vm.runInContext("triggerTask('checkin')",ctx);
  assert.equal(postCount,1,'rapid repeat issued a second POST');
  firstReject(new TypeError('connection lost after request'));
  await first;
  await vm.runInContext("triggerTask('checkin')",ctx);
  assert.equal(postCount,2);
  assert.equal(JSON.parse(bodies[0]).request_id,JSON.parse(bodies[1]).request_id,'retry changed request intent id');
});

test('logout clears task UI state without aborting an accepted-start request', async () => {
  let rejectStart, startSignal;
  const {ctx}=taskFixture((url,options={})=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/tasks/checkin/runs'){startSignal=options.signal;return new Promise((_,reject)=>{rejectStart=reject;});}
    throw new Error('unexpected '+url);
  });
  const pending=vm.runInContext("triggerTask('checkin')",ctx);
  await Promise.resolve();
  ctx.document.getElementById('task-log').textContent='private task log';
  vm.runInContext('signedOut()',ctx);
  assert.equal(startSignal,undefined,'page cleanup attached cancellation to the accepted-start request');
  assert.equal(vm.runInContext('taskStarting',ctx),false,'logout left task controls stuck busy');
  assert.equal(ctx.document.getElementById('task-log').textContent,'','logout retained task detail');
  rejectStart(new TypeError('browser logged out'));
  await pending;
});

test('a transient active-task state failure schedules another visible-page poll', async () => {
  let retry;
  const {ctx}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/tasks')return Promise.reject(new TypeError('temporary network failure'));
    throw new Error('unexpected '+url);
  });
  ctx.setTimeout=fn=>{retry=fn;return 1;};
  vm.runInContext("taskState.active_run={id:'run-active',task_id:'checkin',status:'running'}",ctx);
  await vm.runInContext('loadTaskState()',ctx);
  assert.equal(typeof retry,'function','transient poll failure stopped automatic updates');
});

test('poll refreshes the former active run after catalog turns inactive', async () => {
  let catalogRead=false;
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/tasks'){catalogRead=true;return response({items:[{id:'checkin',enabled:true,hours:[9,21],timezone:'Asia/Shanghai',next_at:null}],active_run:null,latest_runs:[]});}
    if(url==='/admin/task-runs/run-active')return response({id:'run-active',task_id:'checkin',status:catalogRead?'success':'running',accounts:[],duration_ms:1,log:''});
    if(url==='/admin/task-runs?limit=20')return response({items:[],next_before:null});
    throw new Error('unexpected '+url);
  });
  vm.runInContext("taskState.active_run={id:'run-active',task_id:'checkin',status:'running'}",ctx);
  await vm.runInContext("loadTaskDetail('run-active')",ctx);
  let scrolls=0;get('task-detail').scrollIntoView=()=>scrolls++;
  await vm.runInContext('pollTaskRun()',ctx);
  assert.match(get('task-detail-title').textContent,/成功/,'final detail was left at running');
  assert.equal(scrolls,0,'background completion scrolled the page');
});

test('visible idle polls detect external history changes without losing loaded pages', async () => {
  let newest=null, historyReads=0, timer;
  const response=body=>({ok:true,status:200,json:async()=>body});
  const old={id:'old',task_id:'checkin',status:'success',accounts:[]};
  const older={id:'older',task_id:'travel',status:'success',accounts:[]};
  const {ctx,get}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/tasks')return response({items:[],active_run:newest?.status==='running'?newest:null,latest_runs:newest?[newest]:[]});
    if(url==='/admin/task-runs?limit=20'){historyReads++;return response({items:newest?[newest,old]:[old],next_before:'old'});}
    if(url==='/admin/task-runs?limit=20&before=old')return response({items:[older],next_before:'older'});
    throw new Error('unexpected '+url);
  });
  ctx.setTimeout=(fn,ms)=>{timer={fn,ms};return 1;};
  await vm.runInContext('loadTaskPage();',ctx);
  assert.equal(typeof timer?.fn,'function','idle page stopped polling');
  assert.ok(timer.ms>=2000,'idle poll is unbounded');
  await vm.runInContext('loadTaskHistory()',ctx);
  const row=get('task-history-body').children[0];
  await vm.runInContext('pollTaskRun()',ctx);
  assert.equal(historyReads,1,'unchanged idle tick reloaded history');
  assert.equal(get('task-history-body').children[0],row,'unchanged tick replaced rows');
  newest={id:'external',task_id:'activity',status:'running',accounts:[]};
  await vm.runInContext('pollTaskRun()',ctx);
  assert.equal(vm.runInContext('taskHistory.map(run=>run.id).join(",")',ctx),'external,old,older');
  assert.equal(vm.runInContext('taskBefore',ctx),'older');
  newest={...newest,status:'partial_failure'};
  await vm.runInContext('pollTaskRun()',ctx);
  assert.equal(vm.runInContext('taskHistory[0].status',ctx),'partial_failure');
  assert.equal(vm.runInContext('taskHistory.length',ctx),3);
  assert.equal(historyReads,3);
  ctx.document.hidden=true;ctx.document.handlers.visibilitychange();
  ctx.document.hidden=false;ctx.document.handlers.visibilitychange();
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(vm.runInContext('taskHistory.length',ctx),3,'returning to visible page discarded loaded history');
  assert.equal(historyReads,3,'visibility resume reloaded unchanged history');
});

test('active polling preserves closed and historical detail ownership', async () => {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const active={id:'active',task_id:'travel',status:'running',accounts:[]};
  const {ctx,get}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/tasks')return response({items:[],active_run:active,latest_runs:[active]});
    if(url==='/admin/task-runs?limit=20')return response({items:[active],next_before:null});
    if(url.startsWith('/admin/task-runs/'))return response(url.endsWith('/old')?{id:'old',task_id:'cat',status:'success',accounts:[]}:active);
    throw new Error('unexpected '+url);
  });
  await vm.runInContext("loadTaskState();",ctx);
  await vm.runInContext("loadTaskDetail('active')",ctx);
  get('task-detail-close').handlers.click();
  await vm.runInContext('pollTaskRun()',ctx);
  assert.equal(get('task-detail').hidden,true,'poll reopened closed detail');
  await vm.runInContext("loadTaskDetail('old')",ctx);
  let scrolls=0;get('task-detail').scrollIntoView=()=>scrolls++;
  await vm.runInContext('pollTaskRun()',ctx);
  assert.match(get('task-detail-title').textContent,/夜猫子/,'poll replaced historical selection');
  assert.equal(scrolls,0);
});

test('pagination guards the cursor and a reset invalidates a late earlier page', async () => {
  const pending=[];
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    return new Promise(resolve=>pending.push({url,resolve}));
  });
  vm.runInContext("taskBefore='cursor';taskHistory=[{id:'cursor',task_id:'cat'}]",ctx);
  const first=vm.runInContext('loadTaskHistory()',ctx);
  const duplicate=vm.runInContext('loadTaskHistory()',ctx);
  assert.equal(pending.length,1,'same cursor requested concurrently');
  await duplicate;
  assert.equal(get('task-more').disabled,true);
  const reset=vm.runInContext('loadTaskHistory(true)',ctx);
  pending[1].resolve(response({items:[{id:'new',task_id:'travel'}],next_before:'new'}));await reset;
  pending[0].resolve(response({items:[{id:'stale',task_id:'cat'}],next_before:null}));await first;
  assert.equal(vm.runInContext('taskHistory.map(run=>run.id).join(",")',ctx),'new');
  assert.equal(vm.runInContext('taskBefore',ctx),'new');
  assert.equal(get('task-more').disabled,false);
});

test('closing detail invalidates a pending response and newest detail wins', async () => {
  const pending=new Map();
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url.startsWith('/admin/task-runs/'))return new Promise(resolve=>pending.set(url,resolve));
    throw new Error('unexpected '+url);
  });
  const closed=vm.runInContext("loadTaskDetail('run-close')",ctx);
  await Promise.resolve();get('task-detail-close').handlers.click();
  pending.get('/admin/task-runs/run-close')(response({id:'run-close',task_id:'checkin',status:'success',accounts:[],duration_ms:1,log:''}));
  await closed;
  assert.equal(get('task-detail').hidden,true,'closed detail reopened from a late response');

  const older=vm.runInContext("loadTaskDetail('run-a')",ctx);await Promise.resolve();
  const newer=vm.runInContext("loadTaskDetail('run-b')",ctx);await Promise.resolve();
  pending.get('/admin/task-runs/run-b')(response({id:'run-b',task_id:'travel',status:'success',accounts:[],duration_ms:1,log:''}));await newer;
  pending.get('/admin/task-runs/run-a')(response({id:'run-a',task_id:'checkin',status:'failed',accounts:[],duration_ms:1,log:''}));await older;
  assert.match(get('task-detail-title').textContent,/猫猫旅行/,'older detail overwrote the latest selection');

  const navigated=vm.runInContext("loadTaskDetail('run-nav')",ctx);await Promise.resolve();
  vm.runInContext("page='overview';stopTaskReads(true)",ctx);
  pending.get('/admin/task-runs/run-nav')(response({id:'run-nav',task_id:'checkin',status:'success',accounts:[],duration_ms:1,log:''}));await navigated;
  assert.equal(get('task-detail').hidden,true,'navigation allowed a pending detail to reopen');
});

test('equivalent task refresh preserves card nodes for in-flight clicks', () => {
  const {ctx,get}=taskFixture(()=>new Promise(()=>{}));
  vm.runInContext('renderTasks()',ctx);const card=get('task-list').children[0];
  vm.runInContext('taskState=JSON.parse(JSON.stringify(taskState));renderTasks()',ctx);
  assert.equal(get('task-list').children[0],card,'equivalent refresh replaced the clickable card');
});

test('task detail keeps untrusted logs as text and preserves unknown duration', () => {
  const {ctx,get}=taskFixture(()=>new Promise(()=>{}));
  ctx.runFixture={id:'run-x',task_id:'activity',status:'interrupted',started_at:'2026-09-15T10:00:00Z',finished_at:'2026-09-15T10:01:00Z',duration_ms:null,accounts:[{uid:'u1',status:'unknown',detail:'result_unconfirmed',before:null,after:{value:0,observed_at:'2026-09-15T10:00:30Z'},reward:null}],log:'<img src=x onerror=alert(1)>',log_truncated:true};
  vm.runInContext('renderTaskDetail(runFixture)',ctx);
  assert.equal(get('task-log').textContent,'<img src=x onerror=alert(1)>');
  assert.match(get('task-detail-meta').textContent,/耗时：未知/);
  assert.match(get('task-detail-meta').textContent,/观察时间/);
  assert.match(get('task-accounts').textContent,/已确认奖励：未确认/);
  assert.match(get('task-accounts').textContent,/余额 0/);
  assert.equal(get('task-log-truncated').hidden,false);
});

test('logout 503 immediately clears browser secrets and shows cleanup error on login', async () => {
  let finishLogout;
  const {get,ctx,controller} = logoutFixture((url,options) => {
    if(url==='/admin/session')return new Promise(()=>{});
    assert.equal(url,'/admin/logout');
    assert.equal(options.headers['X-CSRF-Token'],'active-csrf');
    return new Promise(resolve=>{finishLogout=resolve;});
  });
  const pending=get('logout').handlers.click();
  assert.equal(get('api-key').value,'','logout left API key visible while cleanup pending');
  assert.equal(get('api-key').type,'password');
  assert.equal(get('admin-key').value,'');
  assert.deepEqual(get('messages').children,[]);
  assert.equal(vm.runInContext('csrf',ctx),'');
  assert.equal(vm.runInContext('history.length',ctx),0);
  assert.equal(vm.runInContext('flowID',ctx),undefined);
  assert.ok(controller.signal.aborted);
  assert.equal(get('console-view').hidden,true);
  assert.equal(get('login-view').hidden,false);
  finishLogout({ok:false,status:503,json:async()=>({error:'核心授权流程取消失败，管理会话已退出'})});
  await pending;
  assert.equal(get('login-error').textContent,'核心授权流程取消失败，管理会话已退出');
  assert.equal(get('api-key').value,'');
});

test('an access response started before logout cannot restore the revealed key', async () => {
  let finishAccess;
  const {get} = logoutFixture(url => {
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/access')return new Promise(resolve=>{finishAccess=resolve;});
    assert.equal(url,'/admin/logout');
    return Promise.resolve({ok:true,status:200,json:async()=>({ok:true})});
  });
  const reveal=get('reveal-key').handlers.click();
  await get('logout').handlers.click();
  finishAccess({ok:true,status:200,json:async()=>({api_key:'late-private-key'})});
  await reveal;
  assert.equal(get('api-key').value,'','late access response repopulated logged-out browser');
  assert.equal(get('login-view').hidden,false);
});

test('stale 401 responses cannot revoke a newly logged-in session', async () => {
  for (const path of ['status','logout']) {
    let finishOld;
    const {get,ctx} = logoutFixture(url => {
      if(url==='/admin/session')return new Promise(()=>{});
      if(url==='/admin/'+path)return new Promise(resolve=>{finishOld=resolve;});
      assert.equal(url,'/admin/logout');
      return Promise.resolve({ok:true,status:200,json:async()=>({ok:true})});
    });
    const old=path==='logout'?get('logout').handlers.click():vm.runInContext("jsonAPI('status')",ctx).catch(error=>error);
    if(path!=='logout')await get('logout').handlers.click();
    vm.runInContext("csrf='new-session-csrf';",ctx);
    get('login-view').hidden=true;get('console-view').hidden=false;get('api-key').value='new-session-key';
    finishOld({ok:false,status:401,json:async()=>({error:'old session expired'})});
    await old;
    assert.equal(vm.runInContext('csrf',ctx),'new-session-csrf',path+' 401 revoked new session');
    assert.equal(get('console-view').hidden,false);
    assert.equal(get('login-view').hidden,true);
    assert.equal(get('api-key').value,'new-session-key');
  }
});

test('stale copy responses never unmask a future key or trigger clipboard fallback', async () => {
  for (const failureStage of ['access','clipboard','clipboard-success']) {
    let finishOld;
    const {get,ctx} = logoutFixture(url => {
      if(url==='/admin/session')return new Promise(()=>{});
      if(url==='/admin/access')return new Promise(resolve=>{finishOld=resolve;});
      assert.equal(url,'/admin/logout');
      return Promise.resolve({ok:true,status:200,json:async()=>({ok:true})});
    });
    ctx.navigator={clipboard:{writeText:()=>new Promise((resolve,reject)=>{finishOld=failureStage==='clipboard-success'?resolve:reject;})}};
    let selected=false;get('api-key').select=()=>{selected=true;};
    if(failureStage==='access')get('api-key').value='';
    const copy=get('copy-key').handlers.click();
    await get('logout').handlers.click();
    vm.runInContext("csrf='new-session-csrf';",ctx);
    get('login-view').hidden=true;get('console-view').hidden=false;
    get('api-key').value='future-private-key';get('api-key').type='password';
    if(failureStage==='access')finishOld({ok:true,status:200,json:async()=>({api_key:'old-private-key'})});
    else finishOld(new Error('old clipboard permission denied'));
    await copy;
    assert.equal(get('api-key').type,'password',failureStage+' fallback unmasked future key');
    assert.equal(get('api-key').value,'future-private-key');
    assert.equal(selected,false);
    assert.equal(get('notice').textContent,'');
  }
});

test('current-session clipboard denial still offers manual copy', async () => {
  const {get,ctx}=logoutFixture(()=>new Promise(()=>{}));
  ctx.navigator={clipboard:{writeText:async()=>{throw new Error('permission denied');}}};
  let selected=false;get('api-key').select=()=>{selected=true;};get('api-key').type='password';
  await get('copy-key').handlers.click();
  assert.equal(get('api-key').type,'text');
  assert.equal(selected,true);
  assert.match(get('notice').textContent,/手动复制/);
});

test('a current-session 401 still clears local authentication', async () => {
  const {get,ctx}=logoutFixture(url=>url==='/admin/session'?new Promise(()=>{}):Promise.resolve({ok:false,status:401}));
  await assert.rejects(vm.runInContext("jsonAPI('status')",ctx),/管理会话已过期/);
  assert.equal(vm.runInContext('csrf',ctx),'');
  assert.equal(get('api-key').value,'');
  assert.equal(get('console-view').hidden,true);
  assert.equal(get('login-view').hidden,false);
});

function chatFixture(chunks, status=200, headers={}) {
  const requests=[];
  const fixture=taskFixture(async (url,options)=>{
    if(url==='/admin/session'||url==='/admin/status')return new Promise(()=>{});
    requests.push({url,...options});
    let index=0;
    return {ok:status===200,status,json:async()=>({error:{message:'安全错误'}}),headers:{get:(name)=>headers[String(name).toLowerCase()]??null},body:{getReader:()=>({read:async()=>index<chunks.length?{value:chunks[index++],done:false}:{done:true}})}};
  });
  fixture.get('model').value='global:claude-sonnet-4.6';
  fixture.get('access-model').value='global:claude-sonnet-4.6';
  fixture.get('max-tokens').value='1024';
  fixture.submit=()=>{fixture.get('prompt').value='你好';return fixture.get('chat-form').handlers.submit({preventDefault(){}});};
  return {...fixture,requests};
}
const sse=(...parts)=>new TextEncoder().encode(parts.map(part=>'data: '+(typeof part==='string'?part:JSON.stringify(part))+'\r\n\r\n').join(''));
const textDelta={type:'content_block_delta',delta:{type:'text_delta',text:'你好世界'}};
const stopMessage={type:'message_stop'};

test('protocol buttons synchronize, preserve same-protocol history and generate safe real-model access examples', async()=>{
  const {ctx,get}=taskFixture(()=>new Promise(()=>{}));
  assert.equal(get('base-url').value,'http://console.test/v1');
  assert.equal(get('api-endpoint').value,'http://console.test/v1/chat/completions');
  assert.doesNotMatch(get('protocol-support').textContent,/Beta 测试/);
  assert.equal(get('copy-example').disabled,true);
  assert.equal(get('go-chat').disabled,true);
  vm.runInContext("modelList=[{id:'global:real-model'}];history=[{role:'user',content:'old'}]",ctx);
  get('access-model').value='global:real-model';get('api-key').value='never-copy-this-secret';
  get('access-anthropic').handlers.click();
  assert.equal(vm.runInContext('history.length',ctx),0);
  assert.equal(get('base-url').value,'http://console.test');
  assert.equal(get('api-endpoint').value,'http://console.test/v1/messages');
  assert.match(get('protocol-support').textContent,/Beta 测试/);
  assert.match(get('api-example').textContent,/anthropic-version: 2023-06-01/);
  assert.match(get('api-example').textContent,/global:real-model/);
  assert.doesNotMatch(get('api-example').textContent,/never-copy-this-secret/);
  assert.equal(get('effort').hidden,true);assert.equal(get('max-tokens').hidden,false);
  assert.equal(get('chat-anthropic')['aria-pressed'],'true');
  const conversation=vm.runInContext('conversation',ctx);
  vm.runInContext("history=[{role:'user',content:'keep'}]",ctx);
  get('chat-anthropic').handlers.click();assert.equal(vm.runInContext('history.length',ctx),1);
  assert.equal(vm.runInContext('conversation',ctx),conversation);
  let copied;ctx.navigator.clipboard.writeText=async value=>{copied=value;};
  await get('copy-example').handlers.click();assert.equal(copied,get('api-example').textContent);
  get('go-chat').handlers.click();assert.equal(vm.runInContext('page',ctx),'chat');assert.equal(get('model').value,'global:real-model');
  get('prompt').value='private draft';get('usage').textContent='private stream error';
  vm.runInContext('signedOut()',ctx);assert.equal(vm.runInContext('protocol',ctx),'openai');assert.equal(get('api-key').value,'');assert.equal(get('prompt').value,'');assert.equal(get('usage').textContent,'用量将在上游返回后显示');
  assert.doesNotMatch(get('protocol-support').textContent,/Beta 测试/);
});

test('chat usage line shows the serving account and platform from attribution headers', async()=>{
  const ok=[sse({choices:[{delta:{content:'hi'}}]},{choices:[{delta:{}}],usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}},'[DONE]')];
  const withHeaders=chatFixture(ok,200,{'x-account':'alice','x-account-realm':'global'});
  await withHeaders.submit();
  assert.match(withHeaders.get('usage').textContent,/本次账号：alice（国际版）/);
  const cnHeaders=chatFixture(ok,200,{'x-account':'u-cn','x-account-realm':'cn'});
  await cnHeaders.submit();
  assert.match(cnHeaders.get('usage').textContent,/本次账号：u-cn（国内版）/);
  const none=chatFixture(ok);
  await none.submit();
  assert.doesNotMatch(none.get('usage').textContent,/本次账号/);
});

test('Anthropic fragmented UTF8 stream uses management envelope and preserves real usage and multi-turn text', async()=>{
  const bytes=sse(textDelta,{type:'message_delta',usage:{input_tokens:0,output_tokens:2}},stopMessage);
  const {ctx,get,requests,submit}=chatFixture(Array.from(bytes,byte=>new Uint8Array([byte])));
  vm.runInContext("setProtocol('anthropic')",ctx);get('effort').value='high';get('max-tokens').value='9000';
  await submit();
  assert.equal(requests[0].url,'/admin/messages');
  const body=JSON.parse(requests[0].body);
  assert.ok(body.conversation_id.startsWith('web-'));
  assert.deepEqual(body.request,{model:'global:claude-sonnet-4.6',max_tokens:9000,messages:[{role:'user',content:'你好'}],stream:true});
  assert.equal(vm.runInContext('history[1].content',ctx),'你好世界');assert.match(get('usage').textContent,/输入 0 · 输出 2/);
  await submit();assert.equal(JSON.parse(requests[1].body).request.messages.length,3);
});

test('Anthropic unknown usage, stream error and premature EOF never invent completion or usage', async()=>{
  for(const [parts,success,usageText] of [
    [[textDelta,{type:'message_delta',usage:{input_tokens:null,output_tokens:null}},stopMessage],true,'上游未返回用量'],
    [[textDelta,{type:'message_delta',usage:{input_tokens:null,output_tokens:0}},stopMessage],true,'输入 — · 输出 0'],
    [[textDelta,{type:'error',error:{message:'安全的上游错误'}}],false,'安全的上游错误'],
    [[textDelta],false,'响应提前中断'],
    [[textDelta,'[DONE]'],false,'响应提前中断']]) {
    const {ctx,get,requests,submit}=chatFixture([sse(...parts)]);vm.runInContext("setProtocol('anthropic')",ctx);await submit();
    assert.equal(vm.runInContext('history.length',ctx),success?2:0);assert.ok(get('usage').textContent.includes(usageText));assert.ok(requests[0].signal.aborted);
    assert.equal(get('effort').disabled,true);assert.equal(get('model').disabled,false);
  }
});

test('Anthropic validates only safe positive integers; OpenAI retains reasoning, tools and DONE behavior', async()=>{
  for(const value of ['','0','-1','1.5','9007199254740992']) {
    const {ctx,get,requests,submit}=chatFixture([]);vm.runInContext("setProtocol('anthropic')",ctx);get('max-tokens').value=value;await submit();assert.equal(requests.length,0,value);assert.match(get('notice').textContent,/正整数/);
  }
  const {ctx,get,requests,submit}=chatFixture([sse({choices:[{delta:{content:'answer',reasoning_content:'reason',tool_calls:[{index:0,function:{name:'tool',arguments:'{}'}}]}}]},'[DONE]')]);
  get('max-tokens').value='invalid';get('effort').value='high';await submit();
  assert.equal(requests[0].url,'/admin/chat');assert.equal(JSON.parse(requests[0].body).reasoning_effort,'high');assert.equal(vm.runInContext('history[1].content',ctx),'answer');assert.match(get('messages').children[1].children.at(-1).textContent,/仅展示，不执行/);
});

test('active requests lock both protocols and models, ignore refresh, and stop without success history', async()=>{
  let finishRead,signal;
  const {ctx,get}=taskFixture(async(url,options)=>{
    if(url==='/admin/session'||url==='/admin/status')return new Promise(()=>{});
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[{id:'replacement'}]})};
    signal=options.signal;return {ok:true,status:200,body:{getReader:()=>({read:()=>new Promise(resolve=>{finishRead=resolve;})})}};
  });
  vm.runInContext("modelList=[{id:'old',reasoning_supported_efforts:['high']}];setProtocol('anthropic')",ctx);
  get('model').value='old';get('access-model').value='old';get('max-tokens').value='1024';get('prompt').value='hello';
  const pending=get('chat-form').handlers.submit({preventDefault(){}});await new Promise(resolve=>setImmediate(resolve));
  for(const id of ['model','access-model','clear-chat','chat-openai','access-anthropic'])assert.equal(get(id).disabled,true,id);
  get('chat-openai').handlers.click();assert.equal(vm.runInContext('protocol',ctx),'anthropic');
  await vm.runInContext('refreshModels()',ctx);assert.equal(get('model').value,'old');assert.equal(get('access-model').value,'old');
  get('stop-chat').handlers.click();assert.ok(signal.aborted);finishRead({done:true});await pending;
  assert.equal(vm.runInContext('history.length',ctx),0);assert.equal(get('usage').textContent,'已停止生成');
  get('chat-openai').handlers.click();assert.equal(get('effort').disabled,false);
});

test('Anthropic authentication expiry resets protocol and browser secrets', async()=>{
  const {ctx,get,submit}=chatFixture([],401);vm.runInContext("setProtocol('anthropic')",ctx);get('api-key').value='private';await submit();
  assert.equal(vm.runInContext('protocol',ctx),'openai');assert.equal(vm.runInContext('history.length',ctx),0);assert.equal(get('api-key').value,'');assert.equal(get('console-view').hidden,true);
});

test('an empty Anthropic text response does not fabricate a tool call in history', async()=>{
  const {ctx,submit}=chatFixture([sse(stopMessage)]);vm.runInContext("setProtocol('anthropic')",ctx);await submit();
  assert.equal(vm.runInContext('history[1].content',ctx),'');
});

test('refresh populates both model selects from server data, preserves choices and disables empty access', async()=>{
  let models=[{id:'global:first'},{id:'global:second'}];
  const {ctx,get}=taskFixture(url=>url==='/admin/models'?Promise.resolve({ok:true,status:200,json:async()=>({data:models})}):new Promise(()=>{}));
  vm.runInContext("accounts=[{realm:'global'}]",ctx);
  await vm.runInContext('refreshModels()',ctx);
  assert.deepEqual(get('access-model').children.map(option=>option.value),models.map(model=>model.id));
  get('access-model').value='global:second';get('access-model').handlers.change();
  assert.match(get('api-example').textContent,/global:second/);
  await vm.runInContext('refreshModels()',ctx);assert.equal(get('access-model').value,'global:second');assert.equal(get('model').value,'global:first');
  models=[];await vm.runInContext('refreshModels()',ctx);assert.equal(get('copy-example').disabled,true);assert.equal(get('go-chat').disabled,true);
});

test('access and chat model options carry a platform realm label from the model tag',async()=>{
  let models=[{id:'cn:workbuddy',realm:'cn'},{id:'global:claude',realm:'global'}];
  const {ctx,get}=taskFixture(url=>url==='/admin/models'?Promise.resolve({ok:true,status:200,json:async()=>({data:models})}):new Promise(()=>{}));
  await vm.runInContext('refreshModels()',ctx);
  const chatLabels=get('model').children.map(o=>o.textContent);
  assert.deepEqual(chatLabels,['国内版 · cn:workbuddy','国际版 · global:claude']);
  assert.deepEqual(get('access-model').children.map(o=>o.textContent),chatLabels,'access select labels differ');
  assert.deepEqual(get('access-model').children.map(o=>o.value),['cn:workbuddy','global:claude'],'option value must stay the bare id');
});

test('model options append an account-count suffix only for multi-account models',async()=>{
  let models=[{id:'cn:workbuddy',realm:'cn',accounts:[{uid:'u1',nickname:'alice'},{uid:'u2'}]},{id:'cn:solo',realm:'cn',accounts:[{uid:'u1'}]},{id:'glm-5.3-flash',realm:'glm'}];
  const {ctx,get}=taskFixture(url=>url==='/admin/models'?Promise.resolve({ok:true,status:200,json:async()=>({data:models})}):new Promise(()=>{}));
  await vm.runInContext('refreshModels()',ctx);
  assert.deepEqual(get('model').children.map(o=>o.textContent),['国内版 · cn:workbuddy · 2账号','国内版 · cn:solo','GLM·智谱 · glm-5.3-flash']);
  assert.deepEqual(get('access-model').children.map(o=>o.textContent),get('model').children.map(o=>o.textContent),'access labels differ');
});

test('access model select merges GLM channel models and routes glm go-chat to the zcode tab',async()=>{
  const {ctx,get}=zcodeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'glm-5.3-flash',realm:'glm'}]});
  vm.runInContext("modelList=[{id:'cn:workbuddy',realm:'cn'}]",ctx);
  await vm.runInContext('loadZcode()',ctx);
  assert.deepEqual(get('access-model').children.map(o=>o.value),['cn:workbuddy','glm-5.3-flash'],'glm model missing from access select');
  assert.deepEqual(get('access-model').children.map(o=>o.textContent),['国内版 · cn:workbuddy','GLM·智谱 · glm-5.3-flash']);
  get('access-model').value='glm-5.3-flash';vm.runInContext('renderAccess()',ctx);
  assert.equal(get('copy-example').disabled,false,'glm example stayed disabled');
  assert.match(get('api-example').textContent,/zcode 通道/);
  vm.runInContext("zcodeModels=['glm-5.3-flash']",ctx);
  get('go-chat').handlers.click();
  assert.equal(vm.runInContext('page',ctx),'zcode','glm go-chat did not route to the zcode tab');
});

test('access tab lazily loads zcode status once so glm models appear without visiting the tab',async()=>{
  const urls=[];
  const {ctx,get}=zcodeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'glm-5.3-flash'}]});
  ctx.fetch=async(url,options={})=>{
    urls.push(url);
    if(url==='/admin/zcode')return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:1,models:[{id:'glm-5.3-flash'}]})};
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[{id:'cn:workbuddy',realm:'cn'}]})};
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  assert.ok(!urls.includes('/admin/zcode'),'zcode status fetched eagerly at login');
  vm.runInContext("showPage('access')",ctx);
  await new Promise(r=>setImmediate(r));
  assert.ok(urls.includes('/admin/zcode'),'access tab did not trigger the lazy zcode status load');
  assert.ok(get('access-model').children.map(o=>o.value).includes('glm-5.3-flash'),'glm model absent after lazy load');
});

test('access curl quotes JSON model strings without executing shell metacharacters',()=>{
  const {ctx,get}=taskFixture(()=>new Promise(()=>{}));
  const model='global:quote\'$(not-executed)"';ctx.actualModel=model;
  vm.runInContext('modelList=[{id:actualModel}]',ctx);get('access-model').value=model;vm.runInContext('renderAccess()',ctx);
  const quoted=get('api-example').textContent.split("  -d '")[1].slice(0,-1);
  assert.equal(JSON.parse(quoted.replace(/'\\''/g,"'")).model,model);
  assert.ok(quoted.includes("'\\''"));
});

test('server-provided task and flow ids cannot reshape the admin request path',async()=>{
  const urls=[];
  const {ctx,get}=taskFixture(url=>{urls.push(url);return new Promise(()=>{});});
  const hostile='../../owners/other/tasks';
  vm.runInContext(`taskState.items=[{id:${JSON.stringify(hostile)},enabled:true,hours:[],timezone:'Asia/Shanghai',next_at:null}]`,ctx);
  vm.runInContext('triggerTask(taskState.items[0].id)',ctx);
  await Promise.resolve();
  vm.runInContext(`flowID=${JSON.stringify(hostile)};pollFlow()`,ctx);
  get('region').value='cn';
  get('region-form').handlers.submit({preventDefault(){},submitter:{disabled:false}});
  const encoded=encodeURIComponent(hostile);
  assert.deepEqual(urls.filter(url=>url.startsWith('/admin/tasks/')),[`/admin/tasks/${encoded}/runs`]);
  assert.deepEqual(urls.filter(url=>url.startsWith('/admin/oauth/')),[`/admin/oauth/${encoded}/poll`,`/admin/oauth/${encoded}/region`]);
});

function pinFixture() {
  const posts=[];
  let pinIndex=0;
  const response=(status,body)=>({ok:status<400,status,json:async()=>body});
  const pinsView=[
    {pins:{cn:{uid:'u1',nickname:'alice',realm:'cn',exists:true},global:null}},
    {pins:{cn:{uid:'u2',nickname:'bob',realm:'cn',exists:true},global:null}},
    {pins:{cn:null,global:null}},
  ];
  const accounts=[
    {uid:'u1',nickname:'alice',realm:'cn',in_flight:0,success_count:1,err_total:0,rate_limited_models:[]},
    {uid:'u2',nickname:'bob',realm:'cn',in_flight:0,success_count:0,err_total:0,rate_limited_models:[]},
  ];
  const fixture=taskFixture((url,options={})=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response(200,{total:2,healthy:2,cooling:0,disabled:0,accounts});
    if(url==='/admin/models')return response(200,{data:[{id:'cn:workbuddy',realm:'cn'}]});
    if(url==='/admin/pin'&&options.method!=='POST')return response(200,pinsView[Math.min(pinIndex,pinsView.length-1)]);
    if(url==='/admin/pin'&&options.method==='POST'){posts.push(['pin',JSON.parse(options.body)]);pinIndex=1;return response(200,{realm:'cn',uid:'u2',nickname:'bob',exists:true});}
    if(url==='/admin/unpin'){posts.push(['unpin',JSON.parse(options.body)]);pinIndex=2;return response(200,pinsView[2]);}
    throw new Error('unexpected '+url+' '+(options.method||'GET'));
  });
  fixture.get('realm').querySelector=()=>({disabled:false});
  const opCell=row=>row.children[6];
  const button=td=>td.children.find(c=>c.tagName==='BUTTON');
  const badge=td=>td.children.find(c=>c.tagName==='SPAN');
  const click=async btn=>{btn.closest=()=>btn;await fixture.ctx.document.handlers.click({target:btn});};
  return {...fixture,posts,opCell,button,badge,click,rows:()=>fixture.get('accounts-body').children};
}

test('account pin: locked row shows badge/unlock, pin replaces, unpin clears banner',async()=>{
  const {ctx,get,posts,opCell,button,badge,click,rows}=pinFixture();
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false})",ctx);
  const [row1,row2]=rows();
  assert.equal(badge(opCell(row1)).textContent,'已锁定','locked row missing 已锁定 badge');
  assert.equal(button(opCell(row1)).textContent,'解锁','locked row button not 解锁');
  assert.equal(badge(opCell(row2)),undefined,'unlocked row unexpectedly shows a badge');
  assert.equal(button(opCell(row2)).textContent,'锁定','unlocked row button not 锁定');
  const banner=get('pin-banner');
  assert.equal(banner.hidden,false,'pin banner hidden while a pin exists');
  assert.match(banner.children[0].children[0].textContent,/alice/);
  // 锁定同平台另一个账号 → POST pin {uid}，UI 替换锁定目标
  await click(button(opCell(row2)));
  assert.deepEqual(posts,[['pin',{uid:'u2'}]],'pin POST body mismatch');
  assert.equal(button(opCell(rows()[0])).textContent,'锁定','old locked row did not revert');
  assert.equal(badge(opCell(rows()[1])).textContent,'已锁定','new locked row missing badge');
  assert.match(get('pin-banner').children[0].children[0].textContent,/bob/);
  // 横幅解锁 → POST unpin {realm}，横幅隐藏，所有行恢复锁定按钮
  await click(get('pin-banner').children[0].children[1]);
  assert.deepEqual(posts[1],['unpin',{realm:'cn'}],'unpin POST body mismatch');
  assert.equal(get('pin-banner').hidden,true,'banner stayed visible after unpin');
  assert.equal(button(opCell(rows()[0])).textContent,'锁定');
  assert.equal(button(opCell(rows()[1])).textContent,'锁定');
});

test('account pin UI degrades silently when core lacks pin endpoints',async()=>{
  const response=(status,body)=>({ok:status<400,status,json:async()=>body});
  const {ctx,get}=taskFixture((url)=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response(200,{total:1,healthy:1,cooling:0,disabled:0,accounts:[{uid:'u1',realm:'cn'}]});
    if(url==='/admin/models')return response(200,{data:[]});
    if(url==='/admin/pin')return response(404,{error:'接口不存在'});
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false})",ctx);
  assert.equal(get('pin-banner').hidden,true,'banner shown without pin endpoint');
  const row=get('accounts-body').children[0];
  assert.equal(row.children[6].children.find(c=>c.tagName==='BUTTON').textContent,'锁定');
});

function zcodeFixture(zcodeStatus) {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get,opened}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/zcode')return response(zcodeStatus);
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,opened};
}

test('zcode tab greys out and shows the disabled notice when the channel is off',async()=>{
  const {ctx,get}=zcodeFixture({enabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false})",ctx);
  assert.equal(get('nav-zcode').classList.toggled['nav-muted'],true,'disabled channel left the tab fully lit');
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-disabled').hidden,false,'disabled notice stayed hidden');
  assert.equal(get('zcode-content').hidden,true,'enabled content showed while disabled');
});

test('zcode tab renders status badge, model table and model select when reachable',async()=>{
  const {ctx,get}=zcodeFixture({enabled:true,reachable:true,model_count:2,models:[{id:'glm-5.2'},{id:'glm-4.7'}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  assert.equal(get('nav-zcode').classList.toggled['nav-muted'],false,'enabled channel greyed the tab');
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-disabled').hidden,true);
  assert.equal(get('zcode-content').hidden,false);
  assert.equal(get('zcode-status-badge').textContent,'在线');
  assert.equal(get('zcode-models-body').children.length,2,'model table row count');
  assert.deepEqual(get('zcode-model').children.map(option=>option.value),['glm-5.2','glm-4.7']);
});

test('zcode tab reports an unreachable channel without hiding the panel',async()=>{
  const {ctx,get}=zcodeFixture({enabled:true,reachable:false,model_count:0,models:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-content').hidden,false);
  assert.equal(get('zcode-status-badge').textContent,'不可达');
  assert.equal(get('zcode-status-badge').className,'badge warn');
});

function controlFixture(statuses) {
  const requests=[];
  let index=0;
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get,opened}=zcodeFixture(statuses[0]);
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/zcode'){const status=statuses[Math.min(index,statuses.length-1)];index++;return response(status);}
    if(url==='/admin/zcode/login'||url==='/admin/zcode/config'||url==='/admin/zcode/enable'||url==='/admin/zcode/disable'||url==='/admin/zcode/logout'){requests.push({url,body:options.body});return response(url==='/admin/zcode/login'?{authorize_url:'https://bigmodel.cn/oauth?state=x'}:{ok:true});}
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  let poll;
  ctx.setTimeout=(fn,ms)=>{poll=fn;return 1;};
  return {ctx,get,requests,opened,poll:()=>poll};
}

test('zcode control tab shows the login card and schedules polling while logged out',async()=>{
  const {ctx,get}=controlFixture([{enabled:true,control:true,logged_in:false,proxy_running:false,provider:'bigmodel',reachable:false,model_count:0,models:[]}]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  vm.runInContext("page='zcode'",ctx);
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-view-login').hidden,false,'login tab missing while logged out');
  assert.equal(get('zcode-pane-login').hidden,false,'did not auto-land on the login tab');
  assert.equal(get('zcode-pane-status').hidden,true,'status pane stayed open over login');
  assert.equal(get('zcode-enable').hidden,true,'enable showed before login');
  assert.equal(get('zcode-view-models').hidden,true,'models tab offered before login');
  assert.equal(get('zcode-status-badge').textContent,'未登录');
  assert.ok(vm.runInContext('zcodePollTimer',ctx),'logged-out tab scheduled no status poll');
});

test('zcode login form posts the chosen provider, auto-opens the authorize page, and keeps manual provider choice',async()=>{
  const {ctx,get,requests,opened}=controlFixture([
    {enabled:true,control:true,logged_in:false,proxy_running:false,provider:'bigmodel',reachable:false,model_count:0,models:[]},
    {enabled:true,control:true,logged_in:false,proxy_running:false,provider:'zai',reachable:false,model_count:0,models:[]},
    {enabled:true,control:true,logged_in:false,proxy_running:false,provider:'zai',reachable:false,model_count:0,models:[]},
  ]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  vm.runInContext("page='zcode'",ctx);
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-provider').value,'bigmodel','provider not prefilled from status');
  // The user picks bigmodel explicitly; later polls reporting zai must not override it.
  get('zcode-provider').value='bigmodel';
  get('zcode-provider').handlers.change();
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-provider').value,'bigmodel','poll after user interaction overrode the provider');
  await get('zcode-login-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,1,'login issued no request');
  assert.equal(requests[0].url,'/admin/zcode/login');
  assert.deepEqual(JSON.parse(requests[0].body),{provider:'bigmodel'});
  assert.equal(get('zcode-auth-row').hidden,false,'authorize link stayed hidden after login start');
  assert.match(get('zcode-login-hint').textContent,/自动检查/);
  const authorize=opened.find(w=>w.location.href==='https://bigmodel.cn/oauth?state=x');
  assert.ok(authorize,'login did not auto-open the authorize page');
});

test('zcode enable action posts the command and the running state reveals the panels',async()=>{
  const {ctx,get,requests}=controlFixture([
    {enabled:true,control:true,logged_in:true,proxy_running:false,provider:'bigmodel',reachable:false,model_count:0,models:[]},
    {enabled:true,control:true,logged_in:true,proxy_running:true,provider:'bigmodel',reachable:true,model_count:1,models:[{id:'glm-5.2'}]},
  ]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  vm.runInContext("page='zcode'",ctx);
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-enable').hidden,false,'enable button missing in logged-in idle state');
  assert.equal(get('zcode-view-login').hidden,true,'login tab offered after login');
  assert.equal(vm.runInContext('zcodePollTimer',ctx),undefined,'idle logged-in tab polled status');
  await get('zcode-enable').handlers.click();
  assert.deepEqual(requests.map(item=>item.url),['/admin/zcode/enable']);
  assert.equal(get('zcode-view-models').hidden,false,'models tab missing after enable');
  assert.equal(get('zcode-view-chat').hidden,false,'chat tab missing after enable');
  assert.equal(get('zcode-pane-models').hidden,true,'models pane opened without a click');
  await get('zcode-view-models').handlers.click();
  assert.equal(get('zcode-pane-models').hidden,false,'models pane stayed closed after clicking the tab');
  assert.equal(get('zcode-pane-status').hidden,true,'status pane stayed open over models');
  assert.equal(get('zcode-models-body').children.length,1,'model table not rendered');
  assert.equal(get('zcode-status-badge').textContent,'在线');
  assert.equal(get('zcode-disable').hidden,false,'disable button missing while running');
});

test('zcode plan select hides while running and posts setConfig when stopped',async()=>{
  const {ctx,get,requests}=controlFixture([
    {enabled:true,control:true,logged_in:true,proxy_running:true,provider:'bigmodel',plan:'coding-plan',reachable:true,model_count:1,models:[{id:'glm-5.2'}]},
    {enabled:true,control:true,logged_in:true,proxy_running:false,provider:'bigmodel',plan:'coding-plan',reachable:false,model_count:0,models:[]},
    {enabled:true,control:true,logged_in:true,proxy_running:false,provider:'bigmodel',plan:'start-plan',reachable:false,model_count:0,models:[]},
  ]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  vm.runInContext("page='zcode'",ctx);
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-config-form').hidden,true,'plan switch showed while the proxy runs');
  await vm.runInContext('loadZcode()',ctx);
  assert.equal(get('zcode-config-form').hidden,false,'plan switch hidden while stopped');
  assert.equal(get('zcode-plan').value,'coding-plan','plan not prefilled from status');
  get('zcode-plan').value='start-plan';
  get('zcode-plan').handlers.change();
  await get('zcode-config-form').handlers.submit({preventDefault(){}});
  assert.equal(requests[0].url,'/admin/zcode/config');
  assert.deepEqual(JSON.parse(requests[0].body),{plan:'start-plan'});
  // After the successful switch the next poll reports start-plan and the select follows.
  assert.equal(get('zcode-plan').value,'start-plan','plan select did not follow the new status');
});

test('zcode logout needs a second confirming click',async()=>{
  const {ctx,get,requests}=controlFixture([{enabled:true,control:true,logged_in:true,proxy_running:true,provider:'bigmodel',reachable:true,model_count:1,models:[{id:'glm-5.2'}]}]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  vm.runInContext("page='zcode'",ctx);
  await vm.runInContext('loadZcode()',ctx);
  await get('zcode-logout').handlers.click();
  assert.equal(requests.length,0,'first logout click sent a request');
  assert.equal(get('zcode-logout').textContent,'再次点击确认退出');
  await get('zcode-logout').handlers.click();
  assert.deepEqual(requests.map(item=>item.url),['/admin/zcode/logout']);
});

test('zcode dialog posts a glm model to the admin proxy and streams the answer',async()=>{
  const requests=[];
  const {ctx,get}=zcodeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'glm-5.2'}]});
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/zcode/chat'){
      requests.push({url,...options});
      const chunks=[new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:'GLM'} }]})+'\n\n'),new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:' reply'} }],usage:{prompt_tokens:1,completion_tokens:2,total_tokens:3}})+'\n\n'),new TextEncoder().encode('data: [DONE]\n\n')];
      let index=0;
      return {ok:true,status:200,body:{getReader:()=>({read:async()=>index<chunks.length?{value:chunks[index++],done:false}:{done:true}})}};
    }
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[]})};
    if(url==='/admin/zcode')return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:1,models:[{id:'glm-5.2'}]})};
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  await vm.runInContext('loadZcode()',ctx);
  get('zcode-model').value='glm-5.2';
  get('zcode-prompt').value='你好';
  await get('zcode-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,1,'zcode dialog issued no request');
  const body=JSON.parse(requests[0].body);
  assert.equal(body.model,'glm-5.2');
  assert.deepEqual(body.messages,[{role:'user',content:'你好'}]);
  assert.equal(body.stream,true);
  assert.match(vm.runInContext('zcodeHistory[1].content',ctx),/GLM reply/);
  assert.match(get('zcode-usage').textContent,/输入 1 · 输出 2 · 总计 3/);
  assert.equal(vm.runInContext('activeRequest',ctx),undefined,'zcode request left controls locked');
});

function qoderFixture(qoderStatus) {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get,opened}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/qoder')return response(qoderStatus);
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,opened};
}

test('qoder tab greys out and shows the disabled notice when the channel is off',async()=>{
  const {ctx,get}=qoderFixture({enabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false})",ctx);
  assert.equal(get('nav-qoder').classList.toggled['nav-muted'],true,'disabled channel left the tab fully lit');
  await vm.runInContext('loadQoder()',ctx);
  assert.equal(get('qoder-disabled').hidden,false,'disabled notice stayed hidden');
  assert.equal(get('qoder-content').hidden,true,'enabled content showed while disabled');
});

test('qoder tab renders status badge, model table and model select when reachable',async()=>{
  const {ctx,get}=qoderFixture({enabled:true,reachable:true,model_count:2,models:[{id:'qoder-qwen3.8-max',realm:'qoder'},{id:'qoder-qwen3.8-pro',realm:'qoder'}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:true})",ctx);
  assert.equal(get('nav-qoder').classList.toggled['nav-muted'],false,'enabled channel greyed the tab');
  await vm.runInContext('loadQoder()',ctx);
  assert.equal(get('qoder-disabled').hidden,true);
  assert.equal(get('qoder-content').hidden,false);
  assert.equal(get('qoder-status-badge').textContent,'在线');
  assert.equal(get('qoder-models-body').children.length,2,'model table row count');
  assert.deepEqual(get('qoder-model').children.map(option=>option.value),['qoder-qwen3.8-max','qoder-qwen3.8-pro']);
});

test('qoder tab reports an unreachable channel without hiding the panel',async()=>{
  const {ctx,get}=qoderFixture({enabled:true,reachable:false,model_count:0,models:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:true})",ctx);
  await vm.runInContext('loadQoder()',ctx);
  assert.equal(get('qoder-content').hidden,false);
  assert.equal(get('qoder-status-badge').textContent,'不可达');
  assert.equal(get('qoder-status-badge').className,'badge warn');
});

test('access model select merges qoder models and routes qoder go-chat to the qoder tab',async()=>{
  const {ctx,get}=qoderFixture({enabled:true,reachable:true,model_count:1,models:[{id:'qoder-qwen3.8-max',realm:'qoder'}]});
  vm.runInContext("modelList=[{id:'cn:workbuddy',realm:'cn'}]",ctx);
  await vm.runInContext('loadQoder()',ctx);
  assert.deepEqual(get('access-model').children.map(o=>o.value),['cn:workbuddy','qoder-qwen3.8-max'],'qoder model missing from access select');
  assert.deepEqual(get('access-model').children.map(o=>o.textContent),['国内版 · cn:workbuddy','Qoder · qoder-qwen3.8-max']);
  get('access-model').value='qoder-qwen3.8-max';vm.runInContext('renderAccess()',ctx);
  assert.equal(get('copy-example').disabled,false,'qoder example stayed disabled');
  assert.match(get('api-example').textContent,/qoder 通道/);
  vm.runInContext("qoderModels=['qoder-qwen3.8-max']",ctx);
  get('go-chat').handlers.click();
  assert.equal(vm.runInContext('page',ctx),'qoder','qoder go-chat did not route to the qoder tab');
});

test('qoder dialog posts a qoder model to the admin proxy and streams the answer',async()=>{
  const requests=[];
  const {ctx,get}=qoderFixture({enabled:true,reachable:true,model_count:1,models:[{id:'qoder-qwen3.8-max',realm:'qoder'}]});
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/qoder/chat'){
      requests.push({url,...options});
      const chunks=[new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:'Qoder'} }]})+'\n\n'),new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:' answer'} }],usage:{prompt_tokens:3,completion_tokens:4,total_tokens:7}})+'\n\n'),new TextEncoder().encode('data: [DONE]\n\n')];
      let index=0;
      return {ok:true,status:200,body:{getReader:()=>({read:async()=>index<chunks.length?{value:chunks[index++],done:false}:{done:true}})}};
    }
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[]})};
    if(url==='/admin/qoder')return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:1,models:[{id:'qoder-qwen3.8-max',realm:'qoder'}]})};
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:true})",ctx);
  await vm.runInContext('loadQoder()',ctx);
  get('qoder-model').value='qoder-qwen3.8-max';
  get('qoder-prompt').value='你好';
  await get('qoder-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,1,'qoder dialog issued no request');
  const body=JSON.parse(requests[0].body);
  assert.equal(body.model,'qoder-qwen3.8-max');
  assert.deepEqual(body.messages,[{role:'user',content:'你好'}]);
  assert.equal(body.stream,true);
  assert.match(vm.runInContext('qoderHistory[1].content',ctx),/Qoder answer/);
  assert.match(get('qoder-usage').textContent,/输入 3 · 输出 4 · 总计 7/);
  assert.equal(vm.runInContext('activeRequest',ctx),undefined,'qoder request left controls locked');
});

function qoderControlFixture(statuses,loginPolls){
  const requests=[];let statusIndex=0,pollIndex=0;let poll;
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get,opened}=qoderFixture(statuses[0]);
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/qoder'){const status=statuses[Math.min(statusIndex,statuses.length-1)];statusIndex++;return response(status);}
    if(url==='/admin/qoder/login'){
      if(options.method==='POST'){requests.push({url,method:'POST',body:options.body});return response({state:'waiting',auth_url:'https://qoder.cn/device/selectAccounts?x=1'});}
      requests.push({url,method:'GET'});
      const state=loginPolls[Math.min(pollIndex,loginPolls.length-1)];pollIndex++;return response(state);
    }
    if(url==='/admin/qoder/logout'){requests.push({url,method:'POST',body:options.body});return response({ok:true});}
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url+' '+options.method);
  };
  ctx.setTimeout=fn=>{poll=fn;return 1;};
  return {ctx,get,opened,requests,poll:()=>poll};
}

test('qoder login tab drives device login: auto-lands logged out, starts, authorizes, polls to success',async()=>{
  const loggedOut={enabled:true,control:true,logged_in:false,reachable:false,model_count:0,models:[]};
  const loggedIn={enabled:true,control:true,logged_in:true,reachable:true,model_count:1,models:[{id:'qoder-qwen3.8-max',realm:'qoder'}]};
  const {ctx,get,opened,requests,poll}=qoderControlFixture([loggedOut,loggedIn],[{state:'waiting'},{state:'success'}]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:true})",ctx);
  vm.runInContext("page='qoder'",ctx);
  await vm.runInContext('loadQoder()',ctx);
  assert.equal(get('qoder-view-login').hidden,false,'login tab missing while logged out');
  assert.equal(get('qoder-pane-login').hidden,false,'did not auto-land on the login pane');
  assert.equal(get('qoder-pane-status').hidden,true,'status pane stayed open over login');
  assert.equal(get('qoder-view-models').hidden,true,'models tab offered before login');
  assert.equal(get('qoder-view-chat').hidden,true,'chat tab offered before login');
  assert.equal(get('qoder-status-badge').textContent,'未登录');
  assert.equal(get('qoder-login-badge').textContent,'未登录');
  assert.equal(get('qoder-logout').hidden,true,'logout showed while logged out');
  await get('qoder-login-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.filter(r=>r.url==='/admin/qoder/login'&&r.method==='POST').length,1,'login start not posted');
  assert.equal(opened[opened.length-1].location.href,'https://qoder.cn/device/selectAccounts?x=1','authorize window not redirected to the device page');
  assert.equal(get('qoder-auth-row').hidden,false,'manual authorize link stayed hidden');
  assert.equal(get('qoder-auth-link').href,'https://qoder.cn/device/selectAccounts?x=1');
  assert.match(get('qoder-login-hint').textContent,/若被拦截请用下方链接打开/);
  assert.equal(get('qoder-login-start').disabled,true,'start stayed enabled while login is pending');
  await poll()(); // waiting: reschedules
  await poll()(); // success: triggers loadQoder with the logged-in status
  assert.equal(get('qoder-view-login').hidden,true,'login tab stayed after success');
  assert.equal(get('qoder-view-models').hidden,false,'models tab missing after login');
  assert.equal(get('qoder-view-chat').hidden,false,'chat tab missing after login');
  assert.equal(get('qoder-pane-status').hidden,false,'did not return to the status pane');
  assert.equal(get('qoder-pane-login').hidden,true,'login pane stayed open after success');
  assert.equal(get('qoder-logout').hidden,false,'logout hidden after login');
});

test('qoder logout needs two clicks, posts, and returns the tab to logged-out login pane',async()=>{
  const loggedIn={enabled:true,control:true,logged_in:true,reachable:true,model_count:1,models:[{id:'qoder-qwen3.8-max',realm:'qoder'}]};
  const loggedOut={enabled:true,control:true,logged_in:false,reachable:false,model_count:0,models:[]};
  const {ctx,get,requests}=qoderControlFixture([loggedIn,loggedOut],[]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:true})",ctx);
  vm.runInContext("page='qoder'",ctx);
  await vm.runInContext('loadQoder()',ctx);
  assert.equal(get('qoder-logout').hidden,false);
  await get('qoder-logout').handlers.click();
  assert.equal(get('qoder-logout').textContent,'再次点击确认退出');
  assert.equal(requests.some(r=>r.url==='/admin/qoder/logout'),false,'logout posted on the first arming click');
  await get('qoder-logout').handlers.click();
  await new Promise(r=>setTimeout(r,0)); // 同步 click 处理器内部触发异步 qoderAction，排空微任务
  assert.ok(requests.some(r=>r.url==='/admin/qoder/logout'&&r.method==='POST'),'logout not posted');
  assert.equal(get('qoder-view-login').hidden,false,'login tab missing after logout');
  assert.equal(get('qoder-pane-login').hidden,false,'did not auto-land on the login pane');
  assert.equal(get('qoder-pane-status').hidden,true,'status pane stayed open after logout');
  assert.equal(get('qoder-logout').hidden,true);
  assert.equal(get('qoder-login-start').hidden,false);
});

test('qoder login tab stays hidden without a usable control endpoint',async()=>{
  // PAT-only mode: control field absent, data plane reachable -> status/models/chat, no login tab.
  const patOnly=qoderFixture({enabled:true,reachable:true,model_count:1,models:[{id:'qoder-qwen3.8-max',realm:'qoder'}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:true})",patOnly.ctx);
  await vm.runInContext('loadQoder()',patOnly.ctx);
  assert.equal(patOnly.get('qoder-view-login').hidden,true,'login tab showed in PAT-only mode');
  assert.equal(patOnly.get('qoder-pane-login').hidden,true,'login pane showed in PAT-only mode');
  assert.equal(patOnly.get('qoder-view-models').hidden,false,'models tab missing in PAT-only mode');
  assert.match(patOnly.get('qoder-status-text').textContent,/PAT 模式/);
  // Control configured but unreachable: control:false hides the login tab as well.
  const down=qoderFixture({enabled:true,control:false,reachable:false,model_count:0,models:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:true})",down.ctx);
  await vm.runInContext('loadQoder()',down.ctx);
  assert.equal(down.get('qoder-view-login').hidden,true,'login tab showed while the control endpoint is down');
  assert.match(down.get('qoder-status-text').textContent,/登录控制端不可达/);
});

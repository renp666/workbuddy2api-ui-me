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
  vm.runInContext("csrf='active-csrf';page='workbuddy';workbuddyView='tasks';taskState={items:[{id:'checkin',enabled:true,hours:[9,21],timezone:'Asia/Shanghai',next_at:null}],active_run:null,latest_runs:[]};",ctx);
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
  get('go-chat').handlers.click();assert.equal(vm.runInContext('page',ctx),'workbuddy');assert.equal(vm.runInContext('workbuddyView',ctx),'chat');assert.equal(get('model').value,'global:real-model');
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

function opencodeFixture(opencodeStatus) {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get,opened}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/opencode')return response(opencodeStatus);
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,opened};
}

test('opencode tab greys out and shows the disabled notice when the channel is off',async()=>{
  const {ctx,get}=opencodeFixture({enabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  assert.equal(get('nav-opencode').classList.toggled['nav-muted'],true,'disabled channel left the tab fully lit');
  await vm.runInContext('loadOpenCode()',ctx);
  assert.equal(get('opencode-disabled').hidden,false,'disabled notice stayed hidden');
  assert.equal(get('opencode-content').hidden,true,'enabled content showed while disabled');
});

test('opencode tab renders badge, health table, model table and select when reachable',async()=>{
  const {ctx,get}=opencodeFixture({enabled:true,reachable:true,model_count:2,
    models:[{id:'opencode-OC · Free',realm:'opencode'},{id:'opencode-OC · Big',realm:'opencode'}],
    health:{phase:'ready',version:'1.2.3',opencodeVersion:'1.4.5',endpoint:'https://opencode.ai/zen/v1',
      modelResults:{'OC · Free':{ok:true},'OC · Big':{ok:false}},probe:{running:true,current:'OC · Big'}}});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:true})",ctx);
  assert.equal(get('nav-opencode').classList.toggled['nav-muted'],false,'enabled channel greyed the tab');
  await vm.runInContext('loadOpenCode()',ctx);
  assert.equal(get('opencode-disabled').hidden,true);
  assert.equal(get('opencode-content').hidden,false);
  assert.equal(get('opencode-status-badge').textContent,'在线');
  assert.match(get('opencode-status-text').textContent,/OW Bridge 可达 · 2 个免费模型/);
  assert.equal(get('opencode-health-wrap').hidden,false,'health table hidden while /health returned');
  const healthRows=get('opencode-health-body').children.map(tr=>tr.children[0].textContent);
  assert.deepEqual(healthRows,['phase','version','opencode 版本','endpoint','模型探测','探测进行中']);
  const probeRow=get('opencode-health-body').children[4].children[1].textContent;
  assert.equal(probeRow,'1/2 通过','model probe summary row');
  assert.equal(get('opencode-view-models').hidden,false,'models tab missing when reachable');
  assert.equal(get('opencode-view-chat').hidden,false,'chat tab missing when reachable');
  assert.equal(get('opencode-models-body').children.length,2,'model table row count');
  assert.deepEqual(get('opencode-model').children.map(option=>option.value),['opencode-OC · Free','opencode-OC · Big']);
});

test('opencode tab reports an unreachable channel and hides data tabs without health',async()=>{
  const {ctx,get}=opencodeFixture({enabled:true,reachable:false,model_count:0,models:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:true})",ctx);
  await vm.runInContext('loadOpenCode()',ctx);
  assert.equal(get('opencode-content').hidden,false);
  assert.equal(get('opencode-status-badge').textContent,'不可达');
  assert.equal(get('opencode-status-badge').className,'badge warn');
  assert.match(get('opencode-status-text').textContent,/OW Bridge 当前不可达/);
  assert.equal(get('opencode-health-wrap').hidden,true,'health table showed without a health payload');
  assert.equal(get('opencode-view-models').hidden,true,'models tab offered while unreachable');
  assert.equal(get('opencode-view-chat').hidden,true,'chat tab offered while unreachable');
});

test('opencode tab falls back to the status view when the channel drops mid-session',async()=>{
  const {ctx,get}=opencodeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'opencode-OC · Free',realm:'opencode'}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:true})",ctx);
  await vm.runInContext('loadOpenCode()',ctx);
  get('opencode-view-chat').handlers.click();
  assert.equal(get('opencode-pane-chat').hidden,false,'chat pane did not open');
  ctx.fetch=async()=>({ok:true,status:200,json:async()=>({enabled:true,reachable:false,model_count:0,models:[]})});
  await vm.runInContext('loadOpenCode()',ctx);
  assert.equal(vm.runInContext('opencodeView',ctx),'status','view stayed on chat after the channel dropped');
  assert.equal(get('opencode-pane-chat').hidden,true,'chat pane stayed open after the channel dropped');
});

test('opencode model table keeps probed-but-unavailable models with capability, probe detail and failure reason',async()=>{
  // /v1/models only lists the 2 published models; /health.modelResults probes 3, so the failed
  // one must still show up with its reason instead of vanishing from the console.
  const {ctx,get}=opencodeFixture({enabled:true,reachable:true,model_count:2,
    models:[{id:'opencode-OC · Big Pickle',realm:'opencode'},{id:'opencode-OC · LongCat 2.5 Preview Free',realm:'opencode'}],
    health:{phase:'ready',
      models:[{name:'Big Pickle',context:200000,images:false,toolcall:true,reasoning:true},
        {name:'LongCat 2.5 Preview Free',context:1000000,images:true,toolcall:true,reasoning:true,variants:{low:{},medium:{},high:{}}},
        {name:'Muse Spark 1.3 Free',context:1048576,images:true,toolcall:true,reasoning:true}],
      modelResults:{'OC · Big Pickle':{ok:true,category:'available',durationMs:7945,source:'probe'},
        'OC · LongCat 2.5 Preview Free':{ok:true,chatOnly:true,code:'chat_only',error:'探测时只返回文本、未产生动作；已按仅对话发布'},
        'OC · Muse Spark 1.3 Free':{ok:false,category:'access',status:403,durationMs:120,source:'probe',error:'This model is not available in your country.'}}}});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:true})",ctx);
  await vm.runInContext('loadOpenCode()',ctx);
  const body=get('opencode-models-body');
  assert.equal(body.children.length,3,'probed-but-unavailable model dropped from the table');
  const [available,chatOnly,blocked]=body.children;
  // Row 1: a healthy model -> capability badges, context limit, duration and probe source.
  assert.equal(available.children[0].textContent,'opencode-OC · Big Pickle');
  assert.deepEqual(available.children[1].children[0].children.map(tag=>tag.textContent),['工具','推理'],'capability badges');
  assert.equal(available.children[1].children[1].textContent,'上下文 200000');
  assert.equal(available.children[2].children[0].textContent,'可用');
  assert.equal(available.children[2].children[0].className,'badge');
  assert.equal(available.children[3].textContent,'7.9 秒');
  assert.equal(available.children[3].children[0].textContent,'探测');
  assert.equal(available.children[5].textContent,'—');
  // Row 2: chat-only publish downgrade is not the same as unavailable, and variants surface.
  assert.equal(chatOnly.children[2].children[0].textContent,'仅对话');
  assert.equal(chatOnly.children[2].children[0].className,'badge warn');
  assert.match(chatOnly.children[1].children[1].textContent,/档位 low \/ medium \/ high/);
  assert.match(chatOnly.children[5].textContent,/仅对话发布/);
  // Row 3: a 403 region block keeps its own label, duration and upstream reason.
  assert.equal(blocked.children[0].textContent,'opencode-OC · Muse Spark 1.3 Free');
  assert.equal(blocked.children[2].children[0].textContent,'地区限制');
  assert.equal(blocked.children[3].children[0].textContent,'探测');
  assert.equal(blocked.children[5].textContent,'This model is not available in your country.');
  assert.equal(get('opencode-models-empty').hidden,true);
});

test('opencode health table reports model sync, availability and the latest request',async()=>{
  const {ctx,get}=opencodeFixture({enabled:true,reachable:true,model_count:1,
    models:[{id:'opencode-OC · Free',realm:'opencode'}],
    health:{phase:'ready',version:'1.2.3',opencodeVersion:'1.4.5',endpoint:'https://opencode.ai/zen/v1',
      modelResults:{'OC · Free':{ok:true}},probe:{running:false},
      availableModels:['opencode/Free'],sync:{skipped:true,count:5},lastRequest:{model:'opencode/Free',ok:false,category:'error'},
      updatedAt:'2026-09-30T14:38:34.482Z'}});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:true})",ctx);
  await vm.runInContext('loadOpenCode()',ctx);
  const rows=get('opencode-health-body').children;
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['phase','version','opencode 版本','endpoint','模型探测','可用模型','模型同步','最近请求','更新时间']);
  assert.equal(rows[5].children[1].textContent,'1 个');
  assert.equal(rows[6].children[1].textContent,'本次跳过');
  assert.match(rows[7].children[1].textContent,/opencode\/Free · 失败（调用失败）/);
  assert.match(rows[8].children[1].textContent,/2026/);
});

test('channels without probe data fall back to an explicit 未探测 state instead of hiding the column',async()=>{
  const {ctx,get}=zcodeFixture({enabled:true,reachable:true,model_count:2,models:[{id:'glm-5.2'},{id:'glm-4.7'}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true})",ctx);
  await vm.runInContext('loadZcode()',ctx);
  const rows=get('zcode-models-body').children;
  assert.equal(rows.length,2);
  assert.equal(rows[0].children[0].textContent,'glm-5.2');
  assert.equal(rows[0].children[2].children[0].textContent,'未探测');
  assert.equal(rows[0].children[2].children[0].className,'badge warn');
  assert.equal(rows[0].children[3].textContent,'—');
});

test('core model table renders the catalog with the shared layout and an explicit 未探测 state',async()=>{
  const models=[{id:'cn:workbuddy'},{id:'global:claude',reasoning_supported_efforts:['low','high']}];
  const {ctx,get}=taskFixture(url=>url==='/admin/models'?Promise.resolve({ok:true,status:200,json:async()=>({data:models})}):new Promise(()=>{}));
  await vm.runInContext('refreshModels()',ctx);
  const rows=get('core-models-body').children;
  assert.equal(rows.length,2,'core model rows missing');
  assert.equal(rows[0].children[0].textContent,'cn:workbuddy');
  assert.equal(rows[0].children[2].children[0].textContent,'未探测');
  assert.equal(rows[0].children[2].children[0].className,'badge warn');
  assert.equal(rows[1].children[1].children[0].children[0].textContent,'推理','reasoning effort hint lost');
  assert.equal(get('core-models-empty').hidden,true);
});

test('access model select merges opencode models and routes opencode go-chat to the opencode tab',async()=>{
  const {ctx,get}=opencodeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'opencode-OC · Free',realm:'opencode'}]});
  vm.runInContext("modelList=[{id:'cn:workbuddy',realm:'cn'}]",ctx);
  await vm.runInContext('loadOpenCode()',ctx);
  assert.deepEqual(get('access-model').children.map(o=>o.value),['cn:workbuddy','opencode-OC · Free'],'opencode model missing from access select');
  assert.deepEqual(get('access-model').children.map(o=>o.textContent),['国内版 · cn:workbuddy','OpenCode · opencode-OC · Free']);
  get('access-model').value='opencode-OC · Free';vm.runInContext('renderAccess()',ctx);
  assert.equal(get('copy-example').disabled,false,'opencode example stayed disabled');
  assert.match(get('api-example').textContent,/OW Bridge sidecar/);
  vm.runInContext("protocol='anthropic'",ctx);
  vm.runInContext('renderAccess()',ctx);
  assert.match(get('api-example').textContent,/仅支持 OpenAI 协议/,'anthropic opencode example not rejected');
  vm.runInContext("protocol='openai'",ctx);
  get('go-chat').handlers.click();
  assert.equal(vm.runInContext('page',ctx),'opencode','opencode go-chat did not route to the opencode tab');
  assert.equal(get('opencode-model').value,'opencode-OC · Free','go-chat did not preselect the model');
});

test('opencode dialog posts an opencode model to the admin proxy and streams the answer',async()=>{
  const requests=[];
  const {ctx,get}=opencodeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'opencode-OC · Free',realm:'opencode'}]});
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/opencode/chat'){
      requests.push({url,...options});
      const chunks=[new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:'Free'} }]})+'\n\n'),new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:' model'} }],usage:{prompt_tokens:5,completion_tokens:6,total_tokens:11}})+'\n\n'),new TextEncoder().encode('data: [DONE]\n\n')];
      let index=0;
      return {ok:true,status:200,body:{getReader:()=>({read:async()=>index<chunks.length?{value:chunks[index++],done:false}:{done:true}})}};
    }
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[]})};
    if(url==='/admin/opencode')return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:1,models:[{id:'opencode-OC · Free',realm:'opencode'}]})};
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:true})",ctx);
  await vm.runInContext('loadOpenCode()',ctx);
  get('opencode-model').value='opencode-OC · Free';
  get('opencode-prompt').value='你好';
  await get('opencode-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,1,'opencode dialog issued no request');
  const body=JSON.parse(requests[0].body);
  assert.equal(body.model,'opencode-OC · Free');
  assert.deepEqual(body.messages,[{role:'user',content:'你好'}]);
  assert.equal(body.stream,true);
  assert.match(vm.runInContext('opencodeHistory[1].content',ctx),/Free model/);
  assert.match(get('opencode-usage').textContent,/输入 5 · 输出 6 · 总计 11/);
  assert.match(get('opencode-usage').textContent,/本次通道：OpenCode（免费模型）/);
  assert.equal(vm.runInContext('activeRequest',ctx),undefined,'opencode request left controls locked');
});

test('model table sorts by probe outcome first and ascending latency second',async()=>{
  // The catalog order is deliberately misleading: the healthy model is the slowest one, and the
  // fastest probe fails. Availability has to win over latency, latency only breaks ties inside a band.
  const {ctx,get}=opencodeFixture({enabled:true,reachable:true,model_count:3,
    models:[{id:'opencode-OC · Alpha',realm:'opencode'},{id:'opencode-OC · Bravo',realm:'opencode'},{id:'opencode-OC · Charlie',realm:'opencode'}],
    health:{phase:'ready',
      modelResults:{'OC · Alpha':{ok:true,category:'available',durationMs:9000,source:'probe'},
        'OC · Bravo':{ok:false,category:'timeout',durationMs:120,source:'probe'},
        'OC · Charlie':{ok:true,chatOnly:true,durationMs:5000,source:'probe'}}}});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:true})",ctx);
  await vm.runInContext('loadOpenCode()',ctx);
  const rows=get('opencode-models-body').children;
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['opencode-OC · Alpha','opencode-OC · Charlie','opencode-OC · Bravo'],'rows are not ordered 可用 → 仅对话 → 不可用');
  assert.deepEqual(rows.map(tr=>tr.children[2].children[0].textContent),['可用','仅对话','探测超时']);
  assert.equal(rows[0].children[3].textContent,'9.0 秒');
  assert.equal(rows[2].children[3].textContent,'0.1 秒','a faster failing probe must not outrank a healthy one');
});

test('the three most called models get red, orange and yellow stars on every channel, the rest show —',async()=>{
  // The catalog ids carry realm prefixes (cn:/global:) but the ledger stores the bare model name;
  // heat must still line up, otherwise every core row falls back to —.
  const models=[{id:'cn:workbuddy'},{id:'global:claude'},{id:'cn:other'}];
  const {ctx,get}=taskFixture(url=>url==='/admin/models'?Promise.resolve({ok:true,status:200,json:async()=>({data:models})}):new Promise(()=>{}));
  await vm.runInContext('refreshModels()',ctx);
  // Ledger of the current month: workbuddy 2 calls, claude 1, opencode Big Pickle 1 (tie broken by name).
  await vm.runInContext("applyModelHeat([{model:'workbuddy'},{model:'workbuddy'},{model:'claude'},{model:'opencode-OC · Big Pickle'}])",ctx);
  const heatOf=td=>td.children.length?[td.children[0].className,td.children[0].textContent,td.children[1].textContent]:[null,'—',''];
  const rows=get('core-models-body').children;
  assert.deepEqual(rows.map(tr=>heatOf(tr.children[6])),[['heat-stars heat-1','★','2 次'],['heat-stars heat-3','★','1 次'],[null,'—','']],'core heat cells');
  await vm.runInContext("renderModelTable('opencode',[{id:'opencode-OC · Big Pickle'}],{modelResults:{'OC · Big Pickle':{ok:true,category:'available',durationMs:800,source:'probe'}}})",ctx);
  assert.deepEqual(heatOf(get('opencode-models-body').children[0].children[6]),['heat-stars heat-2','★','1 次'],'heat ranking is not shared across channels');
});

test('credit column shows the multiplier, defaults the switch from it, and sorts paid models first',async()=>{
  // Catalog order is deliberately misleading: the free model sits in the middle. creditSort has to
  // put the paid one on top, keep free above unknown, and leave same-band rows in catalog order.
  const models=[{id:'cn:paid',credits:'x3.47'},{id:'cn:free',credits:'x0.00'},{id:'cn:unknown'}];
  const posts=[];
  const {ctx,get}=taskFixture((url,options={})=>{
    if(url==='/admin/models')return Promise.resolve({ok:true,status:200,json:async()=>({data:models})});
    if(url==='/admin/model-switch'){posts.push(JSON.parse(options.body));return Promise.resolve({ok:true,status:200,json:async()=>({enabled:true,overrides:{'cn:paid':true,'cn:free':false}})});}
    return new Promise(()=>{});
  });
  await vm.runInContext("renderModelTable('core',[{id:'cn:paid',credits:'x3.47'},{id:'cn:free',credits:'x0.00'},{id:'cn:unknown'}])",ctx);
  const rows=get('core-models-body').children;
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['cn:paid','cn:free','cn:unknown'],'credit order must be 倍率倒序 with unknown last');
  assert.deepEqual(rows.map(tr=>tr.children[8].textContent),['×3.47','×0','未知'],'credit cell must not fake an unknown multiplier as zero');
  // 默认态：倍率>0 与未知默认停用，倍率=0 默认启用。
  assert.deepEqual(rows.map(tr=>tr.children[9].children[0].textContent),['已停用','已启用','已停用'],'switch badge must follow the credit rule');
  assert.deepEqual(rows.map(tr=>tr.children[9].children[2].dataset.action),['enable-model','disable-model','enable-model'],'switch button must offer the opposite action');
  assert.deepEqual(rows.map(tr=>tr.children[9].children[2].dataset.model),['cn:paid','cn:free','cn:unknown'],'switch button must carry the public model name');
  // 免费通道不加积分列与开关列，但全球热度列照常存在（共 8 列）。
  await vm.runInContext("renderModelTable('opencode',[{id:'opencode-OC · Free'}],{modelResults:{}})",ctx);
  assert.equal(get('opencode-models-body').children[0].children.length,8,'opencode must stay exempt from the credit columns');
  // 点击委托：停用中的模型点「启用」应 POST 一次开关（closest 按选择器区分 pin 与开关两条委托）。
  const enableButton=rows[0].children[9].children[2];
  await ctx.document.handlers.click({target:{closest:sel=>sel.includes('enable-model')?enableButton:null}});
  assert.deepEqual(posts,[{model:'cn:paid',enabled:true}],'switch click did not reach the admin endpoint');
  // 人工覆盖优先于倍率默认态，并重渲染。
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['cn:paid','cn:free','cn:unknown']);
  assert.deepEqual(get('core-models-body').children.map(tr=>tr.children[9].children[0].textContent),['已启用','已停用','已停用'],'manual override must beat the credit default');
});

test('global heat column maps OpenRouter ranks by model key and never invents one',async()=>{
  const ranks=[
    {id:'stealth/space-bunny-alpha',canonical_slug:'stealth/space-bunny-alpha'},
    {id:'deepseek/deepseek-v4.1-flash',canonical_slug:'deepseek/deepseek-v4.1-flash-20260910'},
    {id:'tencent/hy4-preview',canonical_slug:'tencent/hy4-preview'},
    {id:'anthropic/claude-sonnet-4.5:batch',canonical_slug:'anthropic/claude-sonnet-4.5-20250910'}];
  const {ctx,get}=taskFixture(url=>url==='/admin/heat'?Promise.resolve({ok:true,status:200,json:async()=>({available:true,ranks})}):new Promise(()=>{}));
  await vm.runInContext('loadGlobalHeat()',ctx);
  // 对齐键口径：vendor 前缀与 :variant 后缀剥离，日期后缀不强行归一。
  assert.equal(vm.runInContext("orModelKey('tencent/hy4-preview')",ctx),'hy4preview');
  assert.equal(vm.runInContext("orModelKey('anthropic/claude-sonnet-4.5:batch')",ctx),'claudesonnet45');
  await vm.runInContext("renderModelTable('core',[{id:'cn:hy4-preview'},{id:'cn:deepseek-v4.1-flash-20260910'},{id:'cn:glm-5.3'}])",ctx);
  const cells=get('core-models-body').children.map(tr=>tr.children[7].textContent);
  // hy4 经 id 对齐到 #3；deepseek 带日期后缀的本地名经 canonical_slug 对齐到 #2；glm-5.3 不在源里留「—」。
  assert.deepEqual(cells,['#3','#2','—'],'rank must follow the OpenRouter order; unmatched models stay 无 rather than a guessed place');
});

test('global heat degrades to dashes when the source is unavailable',async()=>{
  const {ctx,get}=taskFixture(url=>url==='/admin/heat'?Promise.resolve({ok:true,status:200,json:async()=>({available:false,ranks:[]})}):new Promise(()=>{}));
  await vm.runInContext("globalHeat=new Map([['hy4preview',3]]);renderModelTable('core',[{id:'cn:hy4-preview'}])",ctx);
  assert.equal(get('core-models-body').children[0].children[7].textContent,'#3','a warm cache must render before reload');
  await vm.runInContext('loadGlobalHeat()',ctx);
  assert.equal(get('core-models-body').children[0].children[7].textContent,'—','unavailable source must clear the map, not keep stale ranks');
});

test('workbuddy models table lives in its own tab, not inside the chat pane',()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  assert.match(html,/id="workbuddy-view-models"[^>]*>[^<]*模型列表/,'models sub-tab button missing');
  assert.match(html,/id="workbuddy-pane-models"[\s\S]*?core-models-body[\s\S]*?<\/div>\s*<\/div>/,'core model table must sit in the models pane');
  const chat=html.match(/id="workbuddy-pane-chat"[\s\S]*?<\/section>/)[0];
  assert.ok(!chat.includes('core-models-body'),'core model table must not stay inside the chat pane');
});

test('usage statistics is a standalone sidebar page and left the workbuddy tabs',()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  assert.match(html,/data-view="usage"[\s\S]*?调用统计/,'usage entry missing from the sidebar nav');
  assert.match(html,/data-page="usage"[\s\S]*?id="usage-channel"[\s\S]*?id="usage-by-channel-body"[\s\S]*?id="usage-body"/,'standalone usage page missing channel filter, distribution and detail tables');
  assert.ok(!/id="workbuddy-view-usage"/.test(html),'workbuddy still exposes a usage sub-tab');
  assert.ok(!/id="workbuddy-pane-usage"/.test(html),'workbuddy still carries a usage pane');
});

test('usage page aggregates by channel, filters the detail list and keeps missing distinct from zero',async()=>{
  const items=[
    {ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'stream',prompt_tokens:100,completion_tokens:200,credit:1.5},
    {ts:1001,uid:'zcode',account:'GLM 通道',model:'glm-5.3-flash',mode:'sync',prompt_tokens:10,completion_tokens:20,credit:null},
    {ts:1002,uid:'qoder',account:'Qoder 通道',model:'qoder-x',mode:'stream',prompt_tokens:-1,completion_tokens:5,credit:0},
    {ts:1003,uid:'opencode',account:'OpenCode 通道',model:'opencode-y',mode:'sync',prompt_tokens:3,completion_tokens:4,credit:2},
  ];
  const {ctx,get}=taskFixture(async(url)=>({ok:true,status:200,json:async()=>({range:'today',items,summary:{}})}));
  await vm.runInContext('loadUsage()',ctx);
  assert.equal(get('usage-calls').textContent,'4');
  assert.equal(get('usage-tokens').textContent,'342');
  assert.equal(get('usage-credit').textContent,'3.5');
  assert.match(get('usage-note').textContent,/1 条记录未回报 token 用量/,'unknown token not surfaced');
  assert.match(get('usage-note').textContent,/1 条记录未回报积分扣费/,'missing credit not surfaced');
  const dist=get('usage-by-channel-body').children;
  assert.deepEqual(dist.map(tr=>tr.children[0].textContent),['WorkBuddy','GLM（Zcode）','Qoder','OpenCode'],'distribution rows missing or misordered');
  assert.deepEqual(dist[1].children.map(td=>td.textContent),['GLM（Zcode）','1','10','20','—'],'zcode missing credit must show dash, not 0');
  assert.deepEqual(dist[2].children.map(td=>td.textContent),['Qoder','1','0','5','0'],'qoder unknown token counted as 0 observed, real 0 credit kept');
  // Filtering narrows only the detail list; the distribution keeps the full picture.
  ctx.USAGE_ITEMS=items;
  vm.runInContext("usageChannel='zcode';renderUsage({items:USAGE_ITEMS})",ctx);
  assert.equal(get('usage-calls').textContent,'1');
  const detail=get('usage-body').children;
  assert.equal(detail.length,1,'detail list not filtered to the selected channel');
  assert.equal(detail[0].children[1].textContent,'GLM（Zcode）','detail channel column wrong');
  assert.equal(get('usage-by-channel-body').children.length,4,'distribution must stay full while detail is filtered');
});

test('credit rule parsing matches the server and never invents a zero',()=>{
  const {ctx}=logoutFixture(()=>new Promise(()=>{}));
  assert.equal(vm.runInContext("parseCreditRule('x0.79 credits')",ctx),0.79);
  assert.equal(vm.runInContext("parseCreditRule('x0.00')",ctx),0);
  assert.equal(vm.runInContext("parseCreditRule('X1.5')",ctx),1.5);
  assert.equal(vm.runInContext("parseCreditRule('')",ctx),null);
  assert.equal(vm.runInContext("parseCreditRule('free')",ctx),null);
  assert.equal(vm.runInContext("parseCreditRule('x-1')",ctx),null);
  assert.equal(vm.runInContext("parseCreditRule(undefined)",ctx),null);
});

function routeFixture(routeState) {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const posts=[];
  const {ctx,get}=taskFixture((url,options={})=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/route')return response(routeState);
    if(url==='/admin/route/save'){posts.push(JSON.parse(options.body));return response(routeState);}
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,posts};
}

test('Agent 接入 tab hides the editor and shows the disabled notice when routing is off',async()=>{
  const {ctx,get}=routeFixture({enabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  assert.equal(get('routes-disabled').hidden,false,'disabled notice stayed hidden');
  assert.equal(get('routes-content').hidden,true,'editor showed while routing is off');
});

test('Agent 接入 tab renders auto preview, alias table, channel sources and a client snippet',async()=>{
  const {ctx,get}=routeFixture({enabled:true,auto_model:'gateway-auto',auto_fallback:'',default_auto_fallback:'cn:auto',
    auto_preview:{routed_model:'opencode-OC · Free',channel:'opencode',reason:'探测可用 · 0.8 秒',fallback:false},
    aliases:[{alias:'my-fast',channel:'opencode',model:'OC · Free',public_model:'opencode-OC · Free',enabled:true,note:'日常'}],
    channels:[{channel:'core',enabled:true,reachable:true,models:['cn:workbuddy']},
      {channel:'opencode',enabled:true,reachable:true,models:['opencode-OC · Free']},
      {channel:'qoder',enabled:false,reachable:false,models:[]}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:true})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  assert.equal(get('routes-content').hidden,false,'editor hidden while routing is on');
  assert.equal(get('routes-auto-badge').textContent,'自动优选');
  assert.equal(get('routes-auto-badge').className,'badge');
  assert.match(get('routes-auto-text').textContent,/现在自动挑中的是 opencode-OC · Free（OpenCode 通道）/);
  assert.equal(get('routes-default-fallback').textContent,'cn:auto');
  const rows=get('routes-alias-body').children;
  assert.equal(rows.length,1,'alias row missing');
  assert.deepEqual(rows[0].children.slice(0,6).map(td=>td.textContent),['my-fast','OpenCode 通道','OC · Free','opencode-OC · Free','','日常']);
  assert.equal(rows[0].children[4].children[0].textContent,'启用','alias state badge');
  assert.equal(rows[0].children[4].children[0].className,'badge');
  // Only enabled channels are offered; the disabled qoder channel must not be selectable.
  assert.deepEqual(get('routes-alias-channel').children.map(option=>option.value),['core','opencode']);
  const channelTitles=get('routes-channels').children.map(article=>article.children[0].textContent);
  assert.deepEqual(channelTitles,['核心账号池','OpenCode 通道','Qoder 通道']);
  assert.match(get('routes-example').textContent,/"model": "my-fast"/);
});

test('Agent 接入 tab posts the whole alias list when adding an alias',async()=>{
  const {ctx,get,posts}=routeFixture({enabled:true,auto_model:'gateway-auto',auto_fallback:'',default_auto_fallback:'cn:auto',
    auto_preview:{routed_model:'cn:auto',channel:'core',reason:'无探测结论，回退核心通道',fallback:true},
    aliases:[],
    channels:[{channel:'core',enabled:true,reachable:true,models:['cn:workbuddy']}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  assert.equal(get('routes-auto-badge').textContent,'用兜底模型');
  assert.equal(get('routes-auto-badge').className,'badge warn');
  get('routes-alias-name').value='daily';
  get('routes-alias-channel').value='core';
  get('routes-alias-model').value='cn:workbuddy';
  get('routes-alias-note').value='主力';
  await get('routes-alias-form').handlers.submit({preventDefault(){}});
  assert.equal(posts.length,1,'alias save issued no request');
  assert.deepEqual(posts[0].models,[{alias:'daily',channel:'core',model:'cn:workbuddy',enabled:true,note:'主力'}]);
  assert.equal(posts[0].auto_fallback,'');
});

test('speed column only renders a real tokensPerSec and never derives one from latency',()=>{
  const {ctx}=logoutFixture(()=>new Promise(()=>{}));
  assert.equal(vm.runInContext('speedText({tokensPerSec:40})',ctx),'40.0 tok/s');
  assert.equal(vm.runInContext('speedText({tokensPerSec:12.34})',ctx),'12.3 tok/s');
  assert.equal(vm.runInContext('speedText({durationMs:100,outputTokens:5})',ctx),'—','latency alone must not fake a rate');
  assert.equal(vm.runInContext('speedText({tokensPerSec:0})',ctx),'—');
  assert.equal(vm.runInContext('speedText(null)',ctx),'—');
});

function probeFixture() {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const posts=[];
  const states=[
    {running:true,done:0,total:2,channels:{}},
    {running:true,done:1,total:2,channels:{}},
    {running:false,done:2,total:2,channels:{core:{
      'cn:fast':{ok:true,source:'probe',durationMs:1200,ttftMs:300,outputTokens:48,tokensPerSec:40},
      'cn:slow':{ok:false,category:'timeout',error:'探测超时',durationMs:30000}}}},
  ];
  let started=false,index=0;
  const {ctx,get}=taskFixture((url,options={})=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:1,healthy:1,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[{id:'cn:fast'},{id:'cn:slow'}]});
    if(url==='/admin/probe'&&options.method==='POST'){started=true;index=0;posts.push(JSON.parse(options.body));return response(states[0]);}
    if(url==='/admin/probe')return response(started?states[Math.min(++index,states.length-1)]:{running:false,done:0,total:0,channels:{}});
    if(url.startsWith('/admin/usage'))return response({range:'month',items:[]});
    if(url==='/admin/heat')return response({available:false,ranks:[]});
    if(url==='/admin/model-switch')return response({enabled:false,overrides:{}});
    throw new Error('unexpected '+url);
  });
  let poll;
  ctx.setTimeout=(fn)=>{poll=fn;return 1;};
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,posts,poll:()=>poll};
}

test('probe button triggers one channel, polls progress and feeds the speed column',async()=>{
  const {ctx,get,posts,poll}=probeFixture();
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await new Promise(r=>setImmediate(r));
  get('probe-core').handlers.click();
  await new Promise(r=>setImmediate(r));
  assert.deepEqual(posts,[{channels:['core']}],'probe click did not scope the request to one channel');
  assert.equal(get('probe-core').disabled,true,'probe button stayed clickable while running');
  assert.equal(get('probe-zcode').disabled,true,'sibling probe button stayed enabled');
  assert.equal(get('probe-core-progress').hidden,false);
  assert.match(get('probe-core-progress').textContent,/正在探测 0\/2/);
  await poll()();
  assert.match(get('probe-core-progress').textContent,/正在探测 1\/2/);
  await poll()();
  await new Promise(r=>setImmediate(r));
  assert.match(get('probe-core-progress').textContent,/探测完成/);
  assert.equal(get('probe-core').disabled,false,'buttons not restored after the probe finished');
  const rows=get('core-models-body').children;
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['cn:fast','cn:slow']);
  assert.equal(rows[0].children[2].children[0].textContent,'可用');
  assert.equal(rows[0].children[3].textContent,'1.2 秒');
  assert.equal(rows[0].children[4].textContent,'40.0 tok/s','speed column missing the probed rate');
  assert.equal(rows[1].children[2].children[0].textContent,'探测超时');
  assert.equal(rows[1].children[4].textContent,'—','failed probe must not show a rate');
});

test('probe start rejects while a run is in flight and keeps the old conclusions',async()=>{
  const {ctx,get}=probeFixture();
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await new Promise(r=>setImmediate(r));
  vm.runInContext("probeSnapshot={running:true,done:1,total:2,channels:{core:{'cn:fast':{ok:true,source:'probe',tokensPerSec:40}}}}",ctx);
  await vm.runInContext("renderModelTable('core',[{id:'cn:fast'}],{modelResults:{'cn:fast':{ok:true,source:'probe',tokensPerSec:40}}})",ctx);
  await get('probe-core').handlers.click();
  assert.equal(vm.runInContext('probeBusy',ctx),false,'second start was not rejected before any request');
  assert.equal(get('core-models-body').children[0].children[4].textContent,'40.0 tok/s','rejected start wiped cached conclusions');
});

function overviewFixture(items,zcodeStatus) {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get}=taskFixture((url)=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:1,healthy:1,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[{id:'cn:workbuddy',realm:'cn'}]});
    if(url.startsWith('/admin/usage'))return response({range:'today',items});
    if(url==='/admin/heat')return response({available:false,ranks:[]});
    if(url==='/admin/model-switch')return response({enabled:false,overrides:{}});
    if(url==='/admin/probe')return response({running:false,done:0,total:0,channels:{}});
    if(url==='/admin/zcode')return response(zcodeStatus);
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get};
}

test('overview aggregates every enabled channel and filters the metric cards by channel',async()=>{
  const items=[
    {ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'stream',prompt_tokens:100,completion_tokens:200,credit:1.5},
    {ts:1001,uid:'zcode',account:'GLM 通道',model:'glm-5.3-flash',mode:'sync',prompt_tokens:10,completion_tokens:20,credit:null},
    {ts:1002,uid:'qoder',account:'Qoder 通道',model:'qoder-x',mode:'stream',prompt_tokens:-1,completion_tokens:5,credit:0},
    {ts:1003,uid:'opencode',account:'OpenCode 通道',model:'opencode-y',mode:'sync',prompt_tokens:3,completion_tokens:4,credit:2},
  ];
  const {ctx,get}=overviewFixture(items,{enabled:true,reachable:true,model_count:3});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true,qoder_enabled:true,opencode_enabled:true})",ctx);
  // 概览的旁路状态只读页签缓存：先访问一次 Zcode 页签，再刷新概览，验证「在线/3 模型」口径。
  await vm.runInContext('loadZcode()',ctx);
  await vm.runInContext('loadOverview()',ctx);
  await new Promise(r=>setImmediate(r));
  const rows=get('overview-channels-body').children;
  const text=td=>td.children.length?td.children[0].textContent:td.textContent;
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['WorkBuddy','GLM（Zcode）','Qoder','OpenCode'],'channel overview rows missing or misordered');
  assert.deepEqual(rows[0].children.map(text),['WorkBuddy','运行中','1','1','100','200','1.5']);
  // 旁路状态来自页签缓存：未访问过的通道如实标「未加载」，不伪造在线。
  assert.deepEqual(rows[1].children.map(text),['GLM（Zcode）','在线','3','1','10','20','—'],'zcode missing credit must show dash, not 0');
  assert.deepEqual(rows[2].children.map(text),['Qoder','未加载','—','1','0','5','0']);
  assert.match(text(rows[3].children[1]),/不可达|未加载/);
  assert.equal(get('overview-usage-calls').textContent,'4');
  assert.equal(get('overview-usage-tokens').textContent,'342');
  assert.equal(get('overview-usage-credit').textContent,'3.5');
  assert.match(get('overview-usage-note').textContent,/1 条记录未回报 token 用量/);
  assert.match(get('overview-usage-note').textContent,/1 条记录未回报积分扣费/);
  // 单通道筛选只作用于指标卡，通道总览表保持全量。
  get('overview-usage-channel').handlers.change({target:{value:'zcode'}});
  assert.equal(get('overview-usage-calls').textContent,'1');
  assert.equal(get('overview-usage-tokens').textContent,'30');
  assert.equal(get('overview-usage-credit').textContent,'—','all-missing credit must not read as zero');
  assert.equal(get('overview-channels-body').children.length,4,'channel table must stay full while metrics are filtered');
});

test('overview hides channels the deployment never enabled',async()=>{
  const {ctx,get}=overviewFixture([{ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'sync',prompt_tokens:1,completion_tokens:2,credit:0}],{enabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadZcode()',ctx);
  await vm.runInContext('loadOverview()',ctx);
  await new Promise(r=>setImmediate(r));
  const rows=get('overview-channels-body').children;
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['WorkBuddy','GLM（Zcode）']);
  assert.equal(rows[1].children[1].children[0].textContent,'未启用','disabled channel must not claim reachability');
});

test('agent snippet falls back to gateway-auto, never the ambiguous bare auto',()=>{
  const {ctx,get}=logoutFixture(()=>new Promise(()=>{}));
  vm.runInContext('renderRouteExample({aliases:[]})',ctx);
  assert.match(get('routes-example').textContent,/"model": "gateway-auto"/);
});

test('overview page, probe buttons and the speed column exist in the page shell',()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  assert.match(html,/id="overview-channels-body"/,'channel overview table missing');
  assert.match(html,/id="overview-usage-channel"[\s\S]*id="overview-usage-range"/,'overview usage filters missing');
  for(const id of ['probe-core','probe-zcode','probe-qoder'])assert.ok(html.includes('id="'+id+'"'),'probe button '+id+' missing');
  assert.ok(!html.includes('id="probe-opencode"'),'opencode must not get a manual probe button');
  assert.equal((html.match(/<th>速度<\/th>/g)||[]).length,4,'every model table must carry the speed column');
  assert.match(html,/gateway-auto/,'routes page still advertises the bare auto name');
});


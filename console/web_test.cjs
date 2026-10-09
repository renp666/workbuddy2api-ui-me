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
    replaceChildren(...children){this.children=children;}, select(){}, querySelector(){return null;}});
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
      replaceChildren(...children){this.children=children;},setAttribute(name,value){this[name]=value;},insertBefore(child){this.children.unshift(child);},querySelector(){return null;},scrollIntoView(){},select(){},focus(){this.focused=true;}};
  }
  const get=id=>{if(!elements.has(id))elements.set(id,element());return elements.get(id);};
  const protocolButtons=['access-openai','access-anthropic','chat-openai','chat-anthropic'].map(id=>{const button=get(id);button.dataset.protocol=id.split('-')[1];return button;});
  const railButtons=[['refresh','overview'],['zcode-refresh','zcode'],['qoder-refresh','qoder'],['opencode-refresh','opencode'],['ccr-refresh','ccr'],['autoclaw-refresh','autoclaw'],['trae-refresh','trae'],['raccoon-refresh','raccoon'],['usage-refresh','usage'],['routes-refresh','routes']].map(([id,view])=>{const button=get(id);button.dataset.refreshFor=view;return button;});
  const opened=[];
  const ctx=vm.createContext({document:{getElementById:get,querySelectorAll:selector=>selector==='[data-protocol]'?protocolButtons:selector==='[data-refresh-for]'?railButtons:[],createElement:element,hidden:false,handlers:{},addEventListener(name,fn){this.handlers[name]=fn;}},
    location:{origin:'http://console.test'},AbortController,TextDecoder,TextEncoder,Option:function(text,value){return {textContent:text,value};},
    setInterval(){},clearTimeout(){},setTimeout(){},fetch,crypto:cryptoImpl,navigator:{clipboard:{writeText:async()=>{}}},window:{scrollTo(){},open(url){const w={closed:false,location:{href:url||''},close(){this.closed=true;}};opened.push(w);return w;}}});
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
  assert.equal(get('zcode-status-badge').className,'badge bad');
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
  assert.equal(get('qoder-status-badge').className,'badge bad');
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
  assert.equal(get('opencode-status-badge').className,'badge bad');
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
  assert.equal(available.children[2].children[0].className,'badge ok');
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
  assert.equal(rows[0].children[2].children[0].className,'badge unknown');
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
  assert.equal(rows[0].children[2].children[0].className,'badge unknown');
  assert.equal(rows[1].children[1].children[0].children[0].textContent,'推理','reasoning effort hint lost');
  assert.equal(get('core-models-empty').hidden,true);
});

test('core model table groups rows by owning account when accounts are present',async()=>{
  const models=[
    {id:'cn:alpha',accounts:[{uid:'u1',nickname:'一号'}]},
    {id:'cn:beta',accounts:[{uid:'u1',nickname:'一号'},{uid:'u2',nickname:'二号'}]},
    {id:'global:gamma',accounts:[{uid:'u2',nickname:'二号'}]}
  ];
  const {ctx,get}=taskFixture(url=>url==='/admin/models'?Promise.resolve({ok:true,status:200,json:async()=>({data:models})}):new Promise(()=>{}));
  await vm.runInContext('refreshModels()',ctx);
  let rows=get('core-models-body').children;
  // 组头 + 数据行：u1 组（alpha、beta）+ u2 组（beta、gamma）= 2 头 + 4 行。
  assert.equal(rows.length,6,'grouped rows missing');
  assert.equal(rows[0].className,'model-group-head');
  assert.match(rows[0].children[0].textContent,/一号 · 2 个模型/);
  assert.equal(rows[0].children[0].colSpan,10);
  assert.equal(rows[1].children[0].textContent,'cn:alpha');
  assert.equal(rows[2].children[0].textContent,'cn:beta');
  assert.match(rows[3].children[0].textContent,/二号 · 2 个模型/);
  assert.equal(rows[4].children[0].textContent,'cn:beta','multi-account model missing from second group');
  assert.equal(rows[5].children[0].textContent,'global:gamma');
  // 切换为平铺：无组头，每模型仅一行。
  get('models-group-toggle').handlers.click();
  rows=get('core-models-body').children;
  assert.equal(rows.length,3,'flat rows missing');
  assert.equal(rows[0].children[0].textContent,'cn:alpha');
  assert.equal(get('models-group-toggle').textContent,'▤ 平铺列表');
  assert.equal(get('models-group-toggle')['aria-pressed'],'false');
  // 切回分层。
  get('models-group-toggle').handlers.click();
  rows=get('core-models-body').children;
  assert.equal(rows.length,6,'re-grouped rows missing');
  assert.equal(get('models-group-toggle').textContent,'▤ 按账号分层');
});

test('core model table stays flat when no account ownership is reported',async()=>{
  const models=[{id:'cn:workbuddy'},{id:'global:claude'}];
  const {ctx,get}=taskFixture(url=>url==='/admin/models'?Promise.resolve({ok:true,status:200,json:async()=>({data:models})}):new Promise(()=>{}));
  await vm.runInContext('refreshModels()',ctx);
  const rows=get('core-models-body').children;
  assert.equal(rows.length,2);
  assert.ok(rows.every(tr=>tr.className!=='model-group-head'),'group head rendered without account data');
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

function autoclawFixture(autoclawStatus) {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get,opened}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/autoclaw')return response(autoclawStatus);
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,opened};
}

test('autoclaw tab greys out and shows the disabled notice when the channel is off',async()=>{
  const {ctx,get}=autoclawFixture({enabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false})",ctx);
  assert.equal(get('nav-autoclaw').classList.toggled['nav-muted'],true,'disabled channel left the tab fully lit');
  await vm.runInContext('loadAutoClaw()',ctx);
  assert.equal(get('autoclaw-disabled').hidden,false,'disabled notice stayed hidden');
  assert.equal(get('autoclaw-content').hidden,true,'enabled content showed while disabled');
});

test('autoclaw tab renders badge, health rows, accounts, models and select when reachable',async()=>{
  const {ctx,get}=autoclawFixture({enabled:true,reachable:true,model_count:2,mode:'global',
    models:[{id:'autoclaw-AC · Fast',realm:'autoclaw'},{id:'autoclaw-AC · Slow',realm:'autoclaw'}],
    accounts:[{uid:'ac-1',nickname:'一号',region:'global',points:120,success_count:3,err_count:1},
      {uid:'ac-2',region:'cn',cooling:true,cool_remaining_sec:3600,reason:'签到冷却'},{uid:'ac-3',region:'cn',disabled:true}],
    health:{healthy:1,total:3}});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:true})",ctx);
  assert.equal(get('nav-autoclaw').classList.toggled['nav-muted'],false,'enabled channel greyed the tab');
  await vm.runInContext('loadAutoClaw()',ctx);
  assert.equal(get('autoclaw-disabled').hidden,true);
  assert.equal(get('autoclaw-content').hidden,false);
  assert.equal(get('autoclaw-status-badge').textContent,'在线');
  assert.equal(get('autoclaw-status-badge').className,'badge ok');
  assert.match(get('autoclaw-status-text').textContent,/autoclaw2api 可达 · 2 个模型 · 健康账号 1 \/ 3/);
  assert.deepEqual(get('autoclaw-health-body').children.map(tr=>tr.children[0].textContent),['健康账号','账号模式','模型数']);
  assert.equal(get('autoclaw-health-body').children[1].children[1].textContent,'国际版');
  for(const id of ['autoclaw-view-status','autoclaw-view-login','autoclaw-view-accounts','autoclaw-view-models','autoclaw-view-chat'])
    assert.equal(get(id).hidden,false,'reachable container must keep tab '+id+' offered');
  assert.deepEqual(['autoclaw-stat-total','autoclaw-stat-usable','autoclaw-stat-cooling','autoclaw-stat-disabled'].map(id=>get(id).textContent),['3','1','1','1']);
  const rows=get('autoclaw-accounts-body').children;
  assert.equal(rows.length,3,'account table row count');
  assert.match(rows[0].children[0].textContent,/一号/);
  assert.equal(rows[0].children[1].textContent,'国际版');
  assert.equal(rows[0].children[2].children[0].textContent,'可用');
  assert.equal(rows[0].children[3].textContent,'120');
  assert.equal(rows[0].children[4].textContent,'3 / 1');
  assert.equal(rows[0].children[6].children[0].textContent,'停用');
  assert.equal(rows[0].children[6].children[2].textContent,'签到');
  assert.equal(rows[0].children[6].children[4].textContent,'删除');
  assert.equal(rows[2].children[2].children[0].textContent,'已停用');
  assert.equal(rows[2].children[6].children[0].textContent,'启用','disabled account must offer enable instead of disable');
  assert.equal(get('autoclaw-models-body').children.length,2,'model table row count');
  assert.deepEqual(get('autoclaw-model').children.map(option=>option.value),['autoclaw-AC · Fast','autoclaw-AC · Slow']);
});

test('autoclaw model table groups by owning account, badges agent tier and notes the global prefix',async()=>{
  const {ctx,get}=autoclawFixture({enabled:true,reachable:true,model_count:3,
    models:[{id:'autoclaw-cn:alpha',realm:'autoclaw',accounts:[{uid:'ac-1',nickname:'一号'}],tier:'agent'},
      {id:'autoclaw-cn:beta',realm:'autoclaw',accounts:[{uid:'ac-1',nickname:'一号'},{uid:'ac-2',nickname:'二号'}]},
      {id:'autoclaw-global:zai_auto',realm:'autoclaw',accounts:[{uid:'ac-1',nickname:'一号'}]}],
    accounts:[{uid:'ac-1',nickname:'一号',region:'cn',points:10,success_count:1,err_count:0}],
    health:{healthy:1,total:1}});
  await vm.runInContext('loadAutoClaw()',ctx);
  let rows=get('autoclaw-models-body').children;
  assert.equal(rows.length,6,'autoclaw grouped rows missing');
  assert.equal(rows[0].className,'model-group-head');
  assert.match(rows[0].children[0].textContent,/一号 · 3 个模型/);
  assert.equal(rows[0].children[0].colSpan,8);
  assert.equal(rows[0].children[0].children[0].textContent,'含 global: 前缀的模型实际由国内版账号承接');
  assert.equal(rows[1].children[0].textContent,'autoclaw-cn:alpha');
  assert.equal(rows[2].children[0].textContent,'autoclaw-cn:beta');
  assert.equal(rows[3].children[0].textContent,'autoclaw-global:zai_auto');
  assert.match(rows[4].children[0].textContent,/二号 · 1 个模型/);
  assert.equal(rows[4].children[0].children[0].textContent,'含 global: 前缀的模型实际由国内版账号承接');
  assert.equal(rows[5].children[0].textContent,'autoclaw-cn:beta','multi-account model missing from second group');
  const badges=tr=>tr.children[1].children.length?tr.children[1].children[0].children.map(tag=>tag.textContent):[];
  assert.deepEqual(badges(rows[1]),['Agent'],'agent tier lost its badge');
  assert.deepEqual(badges(rows[2]),[],'row without tier gained an Agent badge');
  assert.deepEqual(badges(rows[3]),[],'global row without tier gained an Agent badge');
  assert.equal(rows[2].children[1].textContent,'—');
  get('models-group-toggle').handlers.click();
  rows=get('autoclaw-models-body').children;
  assert.equal(rows.length,3,'autoclaw flat rows missing');
  assert.ok(rows.every(tr=>tr.className!=='model-group-head'),'group head survived the flat toggle');
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['autoclaw-cn:alpha','autoclaw-cn:beta','autoclaw-global:zai_auto']);
  assert.deepEqual(badges(rows[0]),['Agent'],'agent badge lost in flat mode');
  get('models-group-toggle').handlers.click();
  assert.equal(get('autoclaw-models-body').children.length,6,'autoclaw table did not regroup');
});

test('autoclaw tab reports an unreachable container and keeps only the login entry',async()=>{
  const {ctx,get}=autoclawFixture({enabled:true,reachable:false,model_count:0,models:[],accounts:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:true})",ctx);
  await vm.runInContext('loadAutoClaw()',ctx);
  assert.equal(get('autoclaw-content').hidden,false);
  assert.equal(get('autoclaw-status-badge').textContent,'不可达');
  assert.equal(get('autoclaw-status-badge').className,'badge bad');
  assert.match(get('autoclaw-status-text').textContent,/autoclaw2api 容器不可达/);
  assert.equal(get('autoclaw-health-wrap').hidden,true,'health rows showed without a reachable container');
  assert.equal(get('autoclaw-view-accounts').hidden,true,'accounts tab offered while unreachable');
  assert.equal(get('autoclaw-view-models').hidden,true,'models tab offered while unreachable');
  assert.equal(get('autoclaw-view-chat').hidden,true,'chat tab offered while unreachable');
  assert.equal(get('autoclaw-view-login').hidden,false,'login tab is the troubleshooting entry and must stay');
});

test('access model select merges autoclaw models and routes autoclaw go-chat to the autoclaw tab',async()=>{
  const {ctx,get}=autoclawFixture({enabled:true,reachable:true,model_count:1,models:[{id:'autoclaw-AC · Free',realm:'autoclaw'}],accounts:[],health:{healthy:1,total:1}});
  vm.runInContext("modelList=[{id:'cn:workbuddy',realm:'cn'}]",ctx);
  await vm.runInContext('loadAutoClaw()',ctx);
  assert.deepEqual(get('access-model').children.map(o=>o.value),['cn:workbuddy','autoclaw-AC · Free'],'autoclaw model missing from access select');
  assert.deepEqual(get('access-model').children.map(o=>o.textContent),['国内版 · cn:workbuddy','AutoClaw · autoclaw-AC · Free']);
  get('access-model').value='autoclaw-AC · Free';vm.runInContext('renderAccess()',ctx);
  assert.equal(get('copy-example').disabled,false,'autoclaw example stayed disabled');
  assert.match(get('api-example').textContent,/autoclaw2api 桥接转发/);
  vm.runInContext("protocol='anthropic'",ctx);
  vm.runInContext('renderAccess()',ctx);
  assert.match(get('api-example').textContent,/AutoClaw 通道仅支持 OpenAI 协议/,'anthropic autoclaw example not rejected');
  vm.runInContext("protocol='openai'",ctx);
  get('go-chat').handlers.click();
  assert.equal(vm.runInContext('page',ctx),'autoclaw','autoclaw go-chat did not route to the autoclaw tab');
  assert.equal(get('autoclaw-model').value,'autoclaw-AC · Free','go-chat did not preselect the model');
});

test('autoclaw dialog posts an autoclaw model to the admin proxy and streams the answer',async()=>{
  const requests=[];
  const {ctx,get}=autoclawFixture({enabled:true,reachable:true,model_count:1,models:[{id:'autoclaw-AC · Free',realm:'autoclaw'}],accounts:[],health:{healthy:1,total:1}});
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/autoclaw/chat'){
      requests.push({url,...options});
      const chunks=[new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:'Auto'} }]})+'\n\n'),new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:' reply'} }],usage:{prompt_tokens:2,completion_tokens:3,total_tokens:5}})+'\n\n'),new TextEncoder().encode('data: [DONE]\n\n')];
      let index=0;
      return {ok:true,status:200,body:{getReader:()=>({read:async()=>index<chunks.length?{value:chunks[index++],done:false}:{done:true}})}};
    }
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[]})};
    if(url==='/admin/autoclaw')return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:1,models:[{id:'autoclaw-AC · Free',realm:'autoclaw'}],accounts:[],health:{healthy:1,total:1}})};
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:true})",ctx);
  await vm.runInContext('loadAutoClaw()',ctx);
  get('autoclaw-model').value='autoclaw-AC · Free';
  get('autoclaw-prompt').value='你好';
  await get('autoclaw-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,1,'autoclaw dialog issued no request');
  const body=JSON.parse(requests[0].body);
  assert.equal(body.model,'autoclaw-AC · Free');
  assert.deepEqual(body.messages,[{role:'user',content:'你好'}]);
  assert.equal(body.stream,true);
  assert.match(vm.runInContext('autoclawHistory[1].content',ctx),/Auto reply/);
  assert.match(get('autoclaw-usage').textContent,/输入 2 · 输出 3 · 总计 5/);
  assert.match(get('autoclaw-usage').textContent,/本次通道：AutoClaw/);
  assert.equal(vm.runInContext('activeRequest',ctx),undefined,'autoclaw request left controls locked');
});

test('autoclaw bridge business errors surface verbatim through the 200 envelope',async()=>{
  const status={enabled:true,reachable:true,model_count:1,models:[{id:'autoclaw-AC · Free',realm:'autoclaw'}],accounts:[],health:{healthy:1,total:1}};
  const response=body=>({ok:true,status:200,json:async()=>body});
  const {ctx,get}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/autoclaw')return response(status);
    if(url==='/admin/autoclaw/maintain')return response({error:'维护接口暂不可用'});
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:true})",ctx);
  await vm.runInContext('loadAutoClaw()',ctx);
  await get('autoclaw-maintain').handlers.click();
  assert.equal(get('notice').textContent,'维护接口暂不可用','bridge error was not surfaced verbatim');
  assert.equal(get('notice').hidden,false,'error notice stayed hidden');
  assert.equal(vm.runInContext('autoclawBusy',ctx),false,'failed maintain left the channel busy');
});

test('autoclaw sendcode validates the phone number and starts the resend countdown',async()=>{
  const status={enabled:true,reachable:true,model_count:0,models:[],accounts:[],health:{healthy:0,total:0}};
  const response=body=>({ok:true,status:200,json:async()=>body});
  const posts=[];
  const {ctx,get}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/autoclaw')return response(status);
    if(url==='/admin/autoclaw/login/cn/sendcode'){posts.push(url);return response({msg:'验证码已发送（测试）'});}
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:true})",ctx);
  get('autoclaw-phone').value='12345';
  await get('autoclaw-sendcode-form').handlers.submit({preventDefault(){}});
  assert.equal(posts.length,0,'invalid phone number reached the bridge');
  assert.equal(get('autoclaw-login-hint').textContent,'请输入 11 位国内手机号。');
  get('autoclaw-phone').value='13800138000';
  await get('autoclaw-sendcode-form').handlers.submit({preventDefault(){}});
  assert.deepEqual(posts,['/admin/autoclaw/login/cn/sendcode']);
  assert.equal(get('autoclaw-login-hint').textContent,'验证码已发送（测试）');
  // setTimeout 在 fixture 里是 no-op：tick 立即执行一次就停在第一秒文案，按钮保持禁用。
  assert.equal(get('autoclaw-sendcode').textContent,'重新发送（60s）');
  assert.equal(get('autoclaw-sendcode').disabled,true,'countdown left the button enabled');
});

test('autoclaw account checkin reports the tally, chains maintenance and reloads',async()=>{
  const status={enabled:true,reachable:true,model_count:0,models:[],mode:'cn',
    accounts:[{uid:'ac-1',region:'cn',points:10}],health:{healthy:1,total:1}};
  const response=body=>({ok:true,status:200,json:async()=>body});
  const posts=[];let statusGets=0;
  const {ctx,get}=taskFixture((url,options={})=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/autoclaw'){statusGets++;return response(status);}
    if(url==='/admin/autoclaw/checkin'){posts.push({url,body:options.body});return response({results:[{ok:true,uid:'ac-1'},{already_completed:true,uid:'ac-2'}]});}
    if(url==='/admin/autoclaw/maintain'){posts.push({url,body:options.body});return response({});}
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:true})",ctx);
  await vm.runInContext('loadAutoClaw()',ctx);
  const rows=get('autoclaw-accounts-body').children;
  const checkin=rows[0].children[6].children[2];
  assert.equal(checkin.textContent,'签到','checkin button missing from the account row');
  // 账号操作走事件委托，mock 元素没有 closest，这里直接把目标按钮喂给处理器。
  await get('autoclaw-accounts-body').handlers.click({target:{closest:()=>checkin}});
  await new Promise(resolve=>setImmediate(resolve));
  assert.deepEqual(posts.map(item=>item.url),['/admin/autoclaw/checkin','/admin/autoclaw/maintain'],'checkin did not chain the maintenance refresh');
  assert.deepEqual(JSON.parse(posts[0].body),{region:'cn',uid:'ac-1'});
  assert.equal(get('notice').textContent,'签到完成：1 成功 / 1 已签');
  assert.equal(statusGets,2,'checkin did not reload the account table');
  assert.equal(vm.runInContext('autoclawBusy',ctx),false,'checkin left the channel busy');
});

function traeFixture(traeStatus){
  const response=body=>({ok:true,status:200,json:async()=>body});
  let statusGets=0;
  const {ctx,get,opened}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/trae'){statusGets++;return response(traeStatus);}
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,opened,statusGets:()=>statusGets};
}

test('trae tab greys out and shows the disabled notice when the channel is off',async()=>{
  const {ctx,get}=traeFixture({enabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false})",ctx);
  assert.equal(get('nav-trae').classList.toggled['nav-muted'],true,'disabled channel left the trae tab fully lit');
  await vm.runInContext('loadTrae()',ctx);
  assert.equal(get('trae-disabled').hidden,false,'disabled notice stayed hidden');
  assert.equal(get('trae-content').hidden,true,'enabled content showed while disabled');
});

test('trae tab renders badge, health rows, accounts, models and select when reachable',async()=>{
  const {ctx,get}=traeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'trae-GLM · X',realm:'trae'}],
    accounts:[{uid:'t1',nickname:'一号',credits:120,cooling:false,disabled:false,enabled:true,err_count:1,expires_at:1799999999,expired_soon:false,has_auth:true},
      {uid:'t2',nickname:'二号',credits:0,cooling:true,reason:'签到冷却',until:'2026-10-07T12:00:00Z',disabled:false,enabled:true,err_count:0},
      {uid:'t3',nickname:'三号',credits:0,disabled:true,enabled:false,err_count:0}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:true})",ctx);
  assert.equal(get('nav-trae').classList.toggled['nav-muted'],false,'enabled channel greyed the trae tab');
  await vm.runInContext('loadTrae()',ctx);
  assert.equal(get('trae-disabled').hidden,true);
  assert.equal(get('trae-content').hidden,false);
  assert.equal(get('trae-status-badge').textContent,'在线');
  assert.equal(get('trae-status-badge').className,'badge ok');
  assert.match(get('trae-status-text').textContent,/trae2api 可达 · 1 个模型 · 3 个账号/);
  assert.deepEqual(get('trae-health-body').children.map(tr=>tr.children[0].textContent),['模型数','账号数']);
  assert.equal(get('trae-health-body').children[1].children[1].textContent,'3');
  for(const id of ['trae-view-status','trae-view-login','trae-view-accounts','trae-view-schedule','trae-view-models','trae-view-chat','trae-view-usage'])
    assert.equal(get(id).hidden,false,'reachable container must keep tab '+id+' offered');
  assert.deepEqual(['trae-stat-total','trae-stat-usable','trae-stat-cooling','trae-stat-disabled'].map(id=>get(id).textContent),['3','1','1','1']);
  const rows=get('trae-accounts-body').children;
  assert.equal(rows.length,3,'account table row count');
  assert.match(rows[0].children[0].textContent,/一号/);
  assert.equal(rows[0].children[1].children[0].textContent,'可用');
  assert.equal(rows[0].children[2].textContent,'120');
  assert.equal(rows[0].children[3].textContent,'— / 1');
  assert.match(rows[0].children[4].textContent,/20\d\d/,'有效期列应显示凭据到期时间');
  assert.equal(rows[0].children[6].children[0].textContent,'停用');
  assert.equal(rows[1].children[1].children[0].textContent,'冷却中');
  assert.match(rows[1].children[5].textContent,/签到冷却/);
  assert.equal(rows[2].children[1].children[0].textContent,'已停用');
  assert.equal(rows[2].children[6].children[0].textContent,'启用','disabled account must offer enable instead of disable');
  assert.equal(get('trae-models-body').children.length,1,'model table row count');
  assert.deepEqual(get('trae-model').children.map(option=>option.value),['trae-GLM · X']);
});

test('trae tab reports an unreachable container and keeps only the login entry',async()=>{
  const {ctx,get}=traeFixture({enabled:true,reachable:false,model_count:0,models:[],accounts:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:true})",ctx);
  await vm.runInContext('loadTrae()',ctx);
  assert.equal(get('trae-content').hidden,false);
  assert.equal(get('trae-status-badge').textContent,'不可达');
  assert.equal(get('trae-status-badge').className,'badge bad');
  assert.match(get('trae-status-text').textContent,/trae2api 容器不可达/);
  assert.equal(get('trae-health-wrap').hidden,true,'health rows showed without a reachable container');
  for(const id of ['trae-view-accounts','trae-view-schedule','trae-view-models','trae-view-chat','trae-view-usage'])
    assert.equal(get(id).hidden,true,id+' tab offered while unreachable');
  assert.equal(get('trae-view-login').hidden,false,'login tab is the troubleshooting entry and must stay');
});

test('access model select merges trae models and routes trae go-chat to the trae tab',async()=>{
  const {ctx,get}=traeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'trae-GLM · X',realm:'trae'}],accounts:[]});
  vm.runInContext("modelList=[{id:'cn:workbuddy',realm:'cn'}]",ctx);
  await vm.runInContext('loadTrae()',ctx);
  assert.deepEqual(get('access-model').children.map(o=>o.value),['cn:workbuddy','trae-GLM · X'],'trae model missing from access select');
  assert.deepEqual(get('access-model').children.map(o=>o.textContent),['国内版 · cn:workbuddy','Trae · trae-GLM · X']);
  get('access-model').value='trae-GLM · X';vm.runInContext('renderAccess()',ctx);
  assert.equal(get('copy-example').disabled,false,'trae example stayed disabled');
  assert.match(get('api-example').textContent,/trae2api 桥接转发/);
  vm.runInContext("protocol='anthropic'",ctx);
  vm.runInContext('renderAccess()',ctx);
  assert.match(get('api-example').textContent,/Trae 通道仅支持 OpenAI 协议/,'anthropic trae example not rejected');
  vm.runInContext("protocol='openai'",ctx);
  get('go-chat').handlers.click();
  assert.equal(vm.runInContext('page',ctx),'trae','trae go-chat did not route to the trae tab');
  assert.equal(get('trae-model').value,'trae-GLM · X','go-chat did not preselect the model');
});

test('trae dialog posts a trae model to the admin proxy and streams the answer',async()=>{
  const requests=[];
  const {ctx,get}=traeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'trae-GLM · X',realm:'trae'}],accounts:[]});
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/trae/chat'){
      requests.push({url,...options});
      const chunks=[new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:'Trae'} }]})+'\n\n'),new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:' reply'} }],usage:{prompt_tokens:2,completion_tokens:3,total_tokens:5}})+'\n\n'),new TextEncoder().encode('data: [DONE]\n\n')];
      let index=0;
      return {ok:true,status:200,body:{getReader:()=>({read:async()=>index<chunks.length?{value:chunks[index++],done:false}:{done:true}})}};
    }
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[]})};
    if(url==='/admin/trae')return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:1,models:[{id:'trae-GLM · X',realm:'trae'}],accounts:[]})};
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:true})",ctx);
  await vm.runInContext('loadTrae()',ctx);
  get('trae-model').value='trae-GLM · X';
  get('trae-prompt').value='你好';
  await get('trae-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,1,'trae dialog issued no request');
  const body=JSON.parse(requests[0].body);
  assert.equal(body.model,'trae-GLM · X');
  assert.deepEqual(body.messages,[{role:'user',content:'你好'}]);
  assert.equal(body.stream,true);
  assert.match(vm.runInContext('traeHistory[1].content',ctx),/Trae reply/);
  assert.match(get('trae-usage').textContent,/输入 2 · 输出 3 · 总计 5/);
  assert.match(get('trae-usage').textContent,/本次通道：Trae/);
  assert.equal(vm.runInContext('activeRequest',ctx),undefined,'trae request left controls locked');
});

test('trae login start opens the authorize page and the poll loop lands on the account tab',async()=>{
  const {ctx,get,opened}=traeFixture({enabled:true,reachable:true,model_count:1,models:[{id:'trae-GLM · X',realm:'trae'}],accounts:[]});
  let statusGets=0,starts=0,polls=0;
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/trae/login'&&options.method==='POST'){starts++;return {ok:true,status:200,json:async()=>({login_url:'https://auth.test/authorize?x=1',pending_id:'p1',callback_url:'http://console.test/admin/trae/authorize'})};}
    if(url==='/admin/trae/login?pending_id=p1'){polls++;return {ok:true,status:200,json:async()=>({state:'success',uid:'t1',nickname:'一号'})};}
    if(url==='/admin/trae'){statusGets++;return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:1,models:[{id:'trae-GLM · X',realm:'trae'}],accounts:[{uid:'t1',nickname:'一号',credits:0,cooling:false,disabled:false,enabled:true,err_count:0}]})};}
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[]})};
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:true})",ctx);
  await get('trae-login-form').handlers.submit({preventDefault(){}});
  assert.equal(starts,1,'login start issued no request');
  assert.equal(opened.length,1,'the authorize page was not opened');
  assert.equal(opened[0].location.href,'https://auth.test/authorize?x=1');
  assert.equal(get('trae-auth-row').hidden,false,'the authorize link stayed hidden');
  // fixture 的 setTimeout 不会回调，成功路径没有定时器参与，排空微任务即可走完轮询。
  await new Promise(resolve=>setImmediate(resolve));
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(polls,1,'the login poll loop did not query the pending id');
  assert.equal(get('trae-login-badge').textContent,'已登录');
  assert.equal(get('trae-login-badge').className,'badge ok');
  assert.equal(get('trae-auth-row').hidden,true,'the authorize link stayed visible after login');
  assert.match(get('trae-login-hint').textContent,/一号/);
  assert.equal(vm.runInContext('traeView',ctx),'accounts','a successful login did not switch to the account tab');
  assert.equal(statusGets,1,'a successful login did not reload the channel status');
  assert.equal(vm.runInContext('traeLoginBusy',ctx),false,'the login button stayed locked');
});

// 小浣熊通道：与 trae 同为标准 HTTP 状态码控制链路，但没有网页登录（桌面登录态依赖宿主
// DPAPI），登录环节等价于凭证导入；sidecar 账号积分字段是 points（非 trae 的 credits），
// 且只回报失败次数，成功列恒为「—」。
function raccoonFixture(raccoonStatus){
  const response=body=>({ok:true,status:200,json:async()=>body});
  let statusGets=0;
  const {ctx,get}=taskFixture(url=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/raccoon'){statusGets++;return response(raccoonStatus);}
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,statusGets:()=>statusGets};
}

test('raccoon tab greys out and shows the disabled notice when the channel is off',async()=>{
  const {ctx,get}=raccoonFixture({enabled:false});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:false})",ctx);
  assert.equal(get('nav-raccoon').classList.toggled['nav-muted'],true,'disabled channel left the raccoon tab fully lit');
  await vm.runInContext('loadRaccoon()',ctx);
  assert.equal(get('raccoon-disabled').hidden,false,'disabled notice stayed hidden');
  assert.equal(get('raccoon-content').hidden,true,'enabled content showed while disabled');
});

test('raccoon tab renders badge, health rows, accounts, models and select when reachable',async()=>{
  const {ctx,get}=raccoonFixture({enabled:true,reachable:true,model_count:1,models:[{id:'raccoon-GLM · X',realm:'raccoon'}],
    accounts:[{uid:'r1',nickname:'一号',points:120,cooling:false,disabled:false,enabled:true,err_count:1,expires_at:1799999999,expired_soon:false,has_auth:true},
      {uid:'r2',nickname:'二号',points:0,cooling:true,reason:'触发频控',until:'2026-10-08T12:00:00Z',disabled:false,enabled:true,err_count:0},
      {uid:'r3',nickname:'三号',points:0,disabled:true,enabled:false,err_count:0}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:true})",ctx);
  assert.equal(get('nav-raccoon').classList.toggled['nav-muted'],false,'enabled channel greyed the raccoon tab');
  await vm.runInContext('loadRaccoon()',ctx);
  assert.equal(get('raccoon-disabled').hidden,true);
  assert.equal(get('raccoon-content').hidden,false);
  assert.equal(get('raccoon-status-badge').textContent,'在线');
  assert.equal(get('raccoon-status-badge').className,'badge ok');
  assert.match(get('raccoon-status-text').textContent,/raccoon2api 可达 · 1 个模型 · 3 个账号/);
  assert.deepEqual(get('raccoon-health-body').children.map(tr=>tr.children[0].textContent),['模型数','账号数']);
  assert.equal(get('raccoon-health-body').children[1].children[1].textContent,'3');
  for(const id of ['raccoon-view-status','raccoon-view-import','raccoon-view-models','raccoon-view-chat'])
    assert.equal(get(id).hidden,false,'reachable container must keep tab '+id+' offered');
  assert.deepEqual(['raccoon-stat-total','raccoon-stat-usable','raccoon-stat-cooling','raccoon-stat-disabled'].map(id=>get(id).textContent),['3','1','1','1']);
  const rows=get('raccoon-accounts-body').children;
  assert.equal(rows.length,3,'account table row count');
  assert.match(rows[0].children[0].textContent,/一号/);
  assert.equal(rows[0].children[1].children[0].textContent,'可用');
  assert.equal(rows[0].children[2].textContent,'120','points column must read the sidecar points field');
  assert.equal(rows[0].children[3].textContent,'— / 1','sidecar has no success counter, success column stays dashed');
  assert.match(rows[0].children[4].textContent,/20\d\d/,'有效期列应显示凭据到期时间');
  assert.equal(rows[0].children[6].children[0].textContent,'停用');
  assert.equal(rows[1].children[1].children[0].textContent,'冷却中');
  assert.match(rows[1].children[5].textContent,/触发频控/);
  // mock 的 textContent 不聚合子节点，small 里的恢复提示要单独看孩子。
  assert.match(rows[1].children[5].children[0].textContent,/恢复/,'冷却行缺恢复时间提示');
  assert.equal(rows[2].children[1].children[0].textContent,'已停用');
  assert.equal(rows[2].children[6].children[0].textContent,'启用','disabled account must offer enable instead of disable');
  assert.equal(get('raccoon-models-body').children.length,1,'model table row count');
  assert.deepEqual(get('raccoon-model').children.map(option=>option.value),['raccoon-GLM · X']);
});

test('raccoon soft-disabled account shows as stopped and offers enable',async()=>{
  // 软停用（enabled=false、disabled 缺省）此前被当成「可用」，且只给「停用」按钮，
  // 用户因此点不到「启用」。这条守住软停用与硬禁用同一显示口径。
  const {ctx,get}=raccoonFixture({enabled:true,reachable:true,model_count:0,models:[],
    accounts:[{uid:'r1',nickname:'软停',points:5,cooling:false,disabled:false,enabled:false,err_count:0}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:true})",ctx);
  await vm.runInContext('loadRaccoon()',ctx);
  assert.deepEqual(['raccoon-stat-usable','raccoon-stat-cooling','raccoon-stat-disabled'].map(id=>get(id).textContent),['0','0','1']);
  const rows=get('raccoon-accounts-body').children;
  assert.equal(rows[0].children[1].children[0].textContent,'已停用');
  assert.equal(rows[0].children[6].children[0].textContent,'启用','soft-disabled account must offer enable');
});

test('raccoon models show the upstream display name while the select value stays the public id',async()=>{
  // sidecar 目录条目带 name（上游真实展示名）。此前模型表与下拉都只读 id，页面显示
  // raccoon-8c4485 这类代码名。展示名只改可读标签，选路键仍是公共 id。
  const {ctx,get}=raccoonFixture({enabled:true,reachable:true,model_count:2,
    models:[{id:'raccoon-8c4485',name:'Raccoon-Work',realm:'raccoon'},
      {id:'raccoon-chat-ml-5-5',name:'Raccoon Chat ML 5.5',realm:'raccoon'}],
    accounts:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:true})",ctx);
  await vm.runInContext('loadRaccoon()',ctx);
  const rows=get('raccoon-models-body').children;
  assert.equal(rows.length,2,'model table row count');
  assert.equal(rows[0].children[0].textContent,'Raccoon-Work','model table must show the display name');
  assert.equal(rows[1].children[0].textContent,'Raccoon Chat ML 5.5','model table must show the display name');
  assert.deepEqual(get('raccoon-model').children.map(option=>option.textContent),['Raccoon-Work','Raccoon Chat ML 5.5'],'select labels use the display name');
  assert.deepEqual(get('raccoon-model').children.map(option=>option.value),['raccoon-8c4485','raccoon-chat-ml-5-5'],'select values stay the public id');
});

test('raccoon models without a name fall back to the id',async()=>{
  const {ctx,get}=raccoonFixture({enabled:true,reachable:true,model_count:1,
    models:[{id:'raccoon-8c4485',realm:'raccoon'}],accounts:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:true})",ctx);
  await vm.runInContext('loadRaccoon()',ctx);
  assert.equal(get('raccoon-models-body').children[0].children[0].textContent,'raccoon-8c4485','missing name must fall back to the id');
  assert.deepEqual(get('raccoon-model').children.map(option=>option.value),['raccoon-8c4485']);
});

test('raccoon probe results align with rows by id, not by display name',async()=>{
  // 探测结论键是公共 id 名（fetchChannelModels 不带 name）。展示名不能影响身份对齐，
  // 否则带 name 的目录行会与探测结论各成一行、出现重复且状态永远「未探测」。
  const {ctx,get}=raccoonFixture({enabled:true,reachable:true,model_count:1,
    models:[{id:'raccoon-8c4485',name:'Raccoon-Work',realm:'raccoon'}],
    health:{modelResults:{'raccoon-8c4485':{ok:true,durationMs:1200,source:'probe'}}},
    accounts:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:true})",ctx);
  await vm.runInContext('loadRaccoon()',ctx);
  const rows=get('raccoon-models-body').children;
  assert.equal(rows.length,1,'probe result must merge into the catalog row instead of adding a second one');
  assert.equal(rows[0].children[0].textContent,'Raccoon-Work','merged row keeps the display name');
  assert.equal(rows[0].children[2].children[0].textContent,'可用','merged row must carry the probe state');
});

test('raccoon tab reports an unreachable container and keeps the import entry',async()=>{
  const {ctx,get}=raccoonFixture({enabled:true,reachable:false,model_count:0,models:[],accounts:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:true})",ctx);
  await vm.runInContext('loadRaccoon()',ctx);
  assert.equal(get('raccoon-content').hidden,false);
  assert.equal(get('raccoon-status-badge').textContent,'不可达');
  assert.equal(get('raccoon-status-badge').className,'badge bad');
  assert.match(get('raccoon-status-text').textContent,/raccoon2api 容器不可达/);
  assert.equal(get('raccoon-health-wrap').hidden,true,'health rows showed without a reachable container');
  for(const id of ['raccoon-view-models','raccoon-view-chat'])
    assert.equal(get(id).hidden,true,id+' tab offered while unreachable');
  assert.equal(get('raccoon-view-import').hidden,false,'import tab is the troubleshooting entry and must stay');
});

test('raccoon import form posts parsed credentials and explains a 409 skip',async()=>{
  const requests=[];
  const {ctx,get}=raccoonFixture({enabled:true,reachable:true,model_count:0,models:[],accounts:[]});
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/raccoon/accounts/import'&&options.method==='POST'){
      requests.push({url,body:options.body});
      if(requests.length===1)return {ok:false,status:409,json:async()=>({action:'skipped_existing_newer'})};
      return {ok:true,status:200,json:async()=>({uid:'r1',nickname:'一号',action:'created',needs_check:true})};
    }
    if(url==='/admin/raccoon')return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:0,models:[],accounts:[]})};
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[]})};
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:true})",ctx);
  // 直接粘贴文件路径是最常见的误用：拦在前端，不发请求，并指向「选择文件」按钮。
  get('raccoon-import-json').value='C:\\Users\\Administrator\\.box-agent\\config\\auth.json';
  await get('raccoon-import-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,0,'a pasted file path must not reach the bridge');
  assert.match(get('raccoon-import-hint').textContent,/文件路径/);
  assert.match(get('raccoon-import-hint').textContent,/选择 auth\.json 文件/);
  // 非法 JSON 在前端拦截，不发请求。
  get('raccoon-import-json').value='{not json';
  await get('raccoon-import-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,0,'invalid JSON must not reach the bridge');
  assert.match(get('raccoon-import-hint').textContent,/不是合法的 JSON/);
  // 合法 JSON 原样作为请求体：sidecar 的 auth.Parse 吃的是 auth.json 原样内容，不做包装。
  get('raccoon-import-json').value='{"token":"abc"}';
  await get('raccoon-import-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,1,'import issued no request');
  assert.equal(requests[0].body,JSON.stringify({token:'abc'}),'import body must be the raw parsed credentials');
  assert.match(get('raccoon-import-hint').textContent,/已有更新的凭证，未覆盖（409）/,'409 must surface the friendly skip message');
  // 第二次提交成功，提示建议刷新核对。
  await get('raccoon-import-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,2,'retry after 409 issued no request');
  assert.match(get('raccoon-import-hint').textContent,/已导入 一号/);
  assert.match(get('raccoon-import-hint').textContent,/建议.*刷新/,'needs_check hint missing');
  assert.equal(vm.runInContext('raccoonView',ctx),'status','a successful import did not switch to the status tab');
  assert.equal(vm.runInContext('raccoonBusy',ctx),false,'the import button stayed locked');
  // 网页不能直接打开本机文件夹，只提供「复制 Windows 路径」：按钮把 %USERPROFILE% 形式写进剪贴板。
  let copied;
  ctx.navigator.clipboard.writeText=async value=>{copied=value;};
  await get('raccoon-copy-path').handlers.click();
  assert.equal(copied,'%USERPROFILE%\\.box-agent\\config','copy-path button must copy the Windows path');
  // 「选择 auth.json 文件」由浏览器本地读取填入输入框，不经过网络。
  let readText=null;
  ctx.FileReader=class{readAsText(file){readText=file.text;this.result=file.text;this.onload();}};
  get('raccoon-file').files=[{name:'auth.json',text:'{"access_token":"t"}'}];
  await get('raccoon-file').handlers.change();
  assert.equal(readText,'{"access_token":"t"}','picked file must be read locally');
  assert.equal(get('raccoon-import-json').value,'{"access_token":"t"}','picked file must fill the JSON box');
  assert.match(get('raccoon-import-hint').textContent,/已读取 auth\.json/);
  assert.equal(requests.length,2,'picking a file must not itself issue an import request');
  // 读入成功后把下一步「导入」指出来：高亮并聚焦，用户不用回头找按钮。
  assert.equal(get('raccoon-import').classList.toggled.attn,true,'picking a file must highlight the import button');
  assert.equal(get('raccoon-import').focused,true,'picking a file must focus the import button');
  // 点「导入」进入请求后高亮即撤下，避免和提交态抢注意力。
  await get('raccoon-import-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,3,'submitting the picked file must issue the import request');
  assert.ok(!get('raccoon-import').classList.toggled.attn,'submitting must clear the attention highlight');
});

test('access model select merges raccoon models and routes raccoon go-chat to the raccoon tab',async()=>{
  const {ctx,get}=raccoonFixture({enabled:true,reachable:true,model_count:1,models:[{id:'raccoon-GLM · X',realm:'raccoon'}],accounts:[]});
  vm.runInContext("modelList=[{id:'cn:workbuddy',realm:'cn'}]",ctx);
  await vm.runInContext('loadRaccoon()',ctx);
  assert.deepEqual(get('access-model').children.map(o=>o.value),['cn:workbuddy','raccoon-GLM · X'],'raccoon model missing from access select');
  assert.deepEqual(get('access-model').children.map(o=>o.textContent),['国内版 · cn:workbuddy','小浣熊 · raccoon-GLM · X']);
  get('access-model').value='raccoon-GLM · X';vm.runInContext('renderAccess()',ctx);
  assert.equal(get('copy-example').disabled,false,'raccoon example stayed disabled');
  assert.match(get('api-example').textContent,/raccoon2api 桥接转发/);
  vm.runInContext("protocol='anthropic'",ctx);
  vm.runInContext('renderAccess()',ctx);
  assert.match(get('api-example').textContent,/小浣熊 通道仅支持 OpenAI 协议/,'anthropic raccoon example not rejected');
  vm.runInContext("protocol='openai'",ctx);
  get('go-chat').handlers.click();
  assert.equal(vm.runInContext('page',ctx),'raccoon','raccoon go-chat did not route to the raccoon tab');
  assert.equal(get('raccoon-model').value,'raccoon-GLM · X','go-chat did not preselect the model');
});

test('raccoon dialog posts a raccoon model to the admin proxy and streams the answer',async()=>{
  const requests=[];
  const {ctx,get}=raccoonFixture({enabled:true,reachable:true,model_count:1,models:[{id:'raccoon-GLM · X',realm:'raccoon'}],accounts:[]});
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/raccoon/chat'){
      requests.push({url,...options});
      const chunks=[new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:'Raccoon'} }]})+'\n\n'),new TextEncoder().encode('data: '+JSON.stringify({choices:[{delta:{content:' reply'} }],usage:{prompt_tokens:2,completion_tokens:3,total_tokens:5}})+'\n\n'),new TextEncoder().encode('data: [DONE]\n\n')];
      let index=0;
      return {ok:true,status:200,body:{getReader:()=>({read:async()=>index<chunks.length?{value:chunks[index++],done:false}:{done:true}})}};
    }
    if(url==='/admin/status')return {ok:true,status:200,json:async()=>({total:0,healthy:0,cooling:0,disabled:0,accounts:[]})};
    if(url==='/admin/models')return {ok:true,status:200,json:async()=>({data:[]})};
    if(url==='/admin/raccoon')return {ok:true,status:200,json:async()=>({enabled:true,reachable:true,model_count:1,models:[{id:'raccoon-GLM · X',realm:'raccoon'}],accounts:[]})};
    if(url==='/admin/session')return new Promise(()=>{});
    throw new Error('unexpected '+url);
  };
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,autoclaw_enabled:false,trae_enabled:false,raccoon_enabled:true})",ctx);
  await vm.runInContext('loadRaccoon()',ctx);
  get('raccoon-model').value='raccoon-GLM · X';
  get('raccoon-prompt').value='你好';
  await get('raccoon-form').handlers.submit({preventDefault(){}});
  assert.equal(requests.length,1,'raccoon dialog issued no request');
  const body=JSON.parse(requests[0].body);
  assert.equal(body.model,'raccoon-GLM · X');
  assert.deepEqual(body.messages,[{role:'user',content:'你好'}]);
  assert.equal(body.stream,true);
  assert.match(vm.runInContext('raccoonHistory[1].content',ctx),/Raccoon reply/);
  assert.match(get('raccoon-usage').textContent,/输入 2 · 输出 3 · 总计 5/);
  assert.match(get('raccoon-usage').textContent,/本次通道：小浣熊/);
  assert.equal(vm.runInContext('activeRequest',ctx),undefined,'raccoon request left controls locked');
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
  // 只有异常行抬底色：可用行留在白底上。整表染色等于没染色，失败行反而被淹掉。
  assert.deepEqual(rows.map(tr=>tr.className),['','row-warn','row-bad'],'异常行没抬底色，或把可用行也一起染了');
  assert.equal(rows[0].children[3].className,'num','耗时是数值列，单元格要和表头同侧对齐');
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

test('credit column shows the multiplier, defaults the switch from it, and sorts cheap models first',async()=>{
  // Catalog order is deliberately misleading: the free model sits in the middle. creditSort has to
  // put the free one on top, keep unknown last, and leave same-band rows in catalog order.
  const models=[{id:'cn:paid',credits:'x3.47'},{id:'cn:free',credits:'x0.00'},{id:'cn:unknown'}];
  const posts=[];
  const {ctx,get}=taskFixture((url,options={})=>{
    if(url==='/admin/models')return Promise.resolve({ok:true,status:200,json:async()=>({data:models})});
    if(url==='/admin/model-switch'){posts.push(JSON.parse(options.body));return Promise.resolve({ok:true,status:200,json:async()=>({enabled:true,overrides:{'cn:paid':true,'cn:free':false}})});}
    return new Promise(()=>{});
  });
  await vm.runInContext("renderModelTable('core',[{id:'cn:paid',credits:'x3.47'},{id:'cn:free',credits:'x0.00'},{id:'cn:unknown'}])",ctx);
  const rows=get('core-models-body').children;
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['cn:free','cn:paid','cn:unknown'],'credit order must be 倍率升序 with unknown last');
  assert.deepEqual(rows.map(tr=>tr.children[8].textContent),['×0','×3.47','未知'],'credit cell must not fake an unknown multiplier as zero');
  // 默认态：倍率>0 与未知默认停用，倍率=0 默认启用。
  assert.deepEqual(rows.map(tr=>tr.children[9].children[0].textContent),['已启用','已停用','已停用'],'switch badge must follow the credit rule');
  assert.deepEqual(rows.map(tr=>tr.children[9].children[2].dataset.action),['disable-model','enable-model','enable-model'],'switch button must offer the opposite action');
  assert.deepEqual(rows.map(tr=>tr.children[9].children[2].dataset.model),['cn:free','cn:paid','cn:unknown'],'switch button must carry the public model name');
  // 免费通道不加积分列与开关列，但全球热度列照常存在（共 8 列）。
  await vm.runInContext("renderModelTable('opencode',[{id:'opencode-OC · Free'}],{modelResults:{}})",ctx);
  assert.equal(get('opencode-models-body').children[0].children.length,8,'opencode must stay exempt from the credit columns');
  // 点击委托：停用中的模型点「启用」应 POST 一次开关（closest 按选择器区分 pin 与开关两条委托）。
  const enableButton=rows[1].children[9].children[2];
  await ctx.document.handlers.click({target:{closest:sel=>sel.includes('enable-model')?enableButton:null}});
  assert.deepEqual(posts,[{model:'cn:paid',enabled:true}],'switch click did not reach the admin endpoint');
  // 人工覆盖优先于倍率默认态，并重渲染。
  assert.deepEqual(get('core-models-body').children.map(tr=>tr.children[0].textContent),['cn:free','cn:paid','cn:unknown']);
  assert.deepEqual(get('core-models-body').children.map(tr=>tr.children[9].children[0].textContent),['已停用','已启用','已停用'],'manual override must beat the credit default');
});

test('cn: and global: variants of the same name stay separate rows with their own credits and accounts',async()=>{
  // 真实目录里同名模型在两个 realm 各有一条（倍率与可用账号都不同）；行身份必须是
  // 完整公共名。按对齐键合并会把 CN 倍率冲成「未知」（如 cn:deepseek-v4.1-flash 的
  // x0.11）、把 cn: 行挂到国际账号组下。
  const models=[
   {id:'cn:glm-5.2',credits:'x0.79',accounts:[{uid:'u1',nickname:'国内一号'}]},
   {id:'global:glm-5.2',credits:'x0.79',accounts:[{uid:'u2',nickname:'国际一号'}]},
   {id:'cn:deepseek-v4.1-flash',credits:'x0.11',accounts:[{uid:'u1',nickname:'国内一号'}]},
   {id:'global:deepseek-v4.1-flash',accounts:[{uid:'u2',nickname:'国际一号'}]},
   {id:'cn:hy4-preview',credits:'x0.29',accounts:[{uid:'u1',nickname:'国内一号'}]},
   {id:'global:hy4-preview',credits:'x0.00',accounts:[{uid:'u2',nickname:'国际一号'}]}
  ];
  const {ctx,get}=taskFixture(url=>url==='/admin/models'?Promise.resolve({ok:true,status:200,json:async()=>({data:models})}):new Promise(()=>{}));
  await vm.runInContext('refreshModels()',ctx);
  const rows=get('core-models-body').children;
  const heads=rows.filter(tr=>tr.className==='model-group-head').map(tr=>tr.children[0].textContent);
  assert.deepEqual(heads,['国内一号 · 3 个模型','国际一号 · 3 个模型'],'each realm keeps its own account group');
  const labels=rows.filter(tr=>tr.className!=='model-group-head').map(tr=>tr.children[0].textContent);
  assert.deepEqual(labels,['cn:deepseek-v4.1-flash','cn:hy4-preview','cn:glm-5.2','global:hy4-preview','global:glm-5.2','global:deepseek-v4.1-flash'],'six rows must survive, sorted by credit inside each group');
  const creditOf=id=>rows.find(tr=>tr.children[0].textContent===id).children[8].textContent;
  assert.equal(creditOf('cn:deepseek-v4.1-flash'),'×0.11','CN multiplier must not be overwritten by the global entry');
  assert.equal(creditOf('cn:hy4-preview'),'×0.29');
  assert.equal(creditOf('global:hy4-preview'),'×0');
  assert.equal(creditOf('global:deepseek-v4.1-flash'),'未知','missing global credits stay 未知, never faked as zero');
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
  assert.match(get('usage-note').textContent,/1 条记录未回报输入 token/,'unknown input token not surfaced');
  assert.doesNotMatch(get('usage-note').textContent,/未回报输出 token/,'输出都回报了却声称缺失：'+get('usage-note').textContent);
  assert.match(get('usage-note').textContent,/1 条记录未回报积分扣费/,'missing credit not surfaced');
  const dist=get('usage-by-channel-body').children;
  assert.deepEqual(dist.map(tr=>tr.children[0].textContent),['WorkBuddy','GLM（Zcode）','Qoder','OpenCode'],'distribution rows missing or misordered');
  assert.deepEqual(dist[1].children.map(td=>td.textContent),['GLM（Zcode）','1','10','20','—'],'zcode missing credit must show dash, not 0');
  assert.deepEqual(dist[2].children.map(td=>td.textContent),['Qoder','1','—','5','0'],'qoder 整组输入缺失要显示「—」，真实 0 积分保留');
  // Filtering narrows only the detail list; the distribution keeps the full picture.
  ctx.USAGE_ITEMS=items;
  vm.runInContext("usageChannel='zcode';renderUsage({items:USAGE_ITEMS})",ctx);
  assert.equal(get('usage-calls').textContent,'1');
  const detail=get('usage-body').children;
  assert.equal(detail.length,1,'detail list not filtered to the selected channel');
  assert.equal(detail[0].children[1].textContent,'GLM（Zcode）','detail channel column wrong');
  assert.equal(get('usage-by-channel-body').children.length,4,'distribution must stay full while detail is filtered');
});

test('调用明细按页展示：默认 50 行，翻页只换表格，汇总口径不随页变', async()=>{
  const items=Array.from({length:120},(_,i)=>({ts:1000+i,uid:'u1',account:'alice',model:'m'+i,mode:'sync',prompt_tokens:1,completion_tokens:1,credit:1}));
  const {ctx,get}=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items,summary:{}})}));
  await vm.runInContext('loadUsage()',ctx);
  assert.equal(vm.runInContext('usagePageSize',ctx),50,'默认每页不是 50 行');
  assert.equal(get('usage-body').children.length,50,'首屏仍把全部行建进 DOM：'+get('usage-body').children.length);
  assert.equal(get('usage-calls').textContent,'120','指标条必须按整个时间范围算，不能只算当前页');
  assert.equal(get('usage-pager-info').textContent,'共 120 条 · 第 1/3 页');
  assert.equal(get('usage-pager-bar').hidden,false,'有明细却不给分页条');
  const pager=get('usage-pager').children;
  assert.equal(pager[0].textContent,'‹ 上一页');assert.equal(pager[0].disabled,true,'首页还能点上一页');
  assert.equal(pager[pager.length-1].disabled,false,'末页不该禁用下一页');
  assert.equal(pager[1]['aria-current'],'page','当前页没有 aria-current');
  pager[pager.length-1].handlers.click();
  assert.equal(get('usage-pager-info').textContent,'共 120 条 · 第 2/3 页');
  assert.equal(get('usage-body').children.length,50);
  assert.equal(get('usage-calls').textContent,'120','翻页后汇总口径变了');
  assert.equal(get('usage-by-channel-body').children.length,1,'通道分布跟着翻页被重切了');
  vm.runInContext('usagePage=3;renderUsageDetail()',ctx);
  assert.equal(get('usage-body').children.length,20,'末页应剩 20 行');
  get('usage-page-size').handlers.change({target:{value:'30'}});
  assert.equal(vm.runInContext('usagePageSize',ctx),30);
  assert.equal(vm.runInContext('usagePage',ctx),1,'换每页条数后没回到第 1 页');
  assert.equal(get('usage-body').children.length,30);
  assert.equal(get('usage-pager-info').textContent,'共 120 条 · 第 1/4 页');
  vm.runInContext('renderUsage({items:[]})',ctx);
  assert.equal(get('usage-pager-bar').hidden,true,'空表还挂着分页条');
  // 跨 realm 的数组直接 deepEqual 会比原型，一律 join 成字符串再判。
  assert.equal(vm.runInContext('usagePageWindow(10,20).join("|")',ctx),'1|…|9|10|11|…|20','页码窗口错');
  assert.equal(vm.runInContext('usagePageWindow(1,3).join("|")',ctx),'1|2|3');
  assert.equal(vm.runInContext('usagePageWindow(4,7).join("|")',ctx),'1|2|3|4|5|6|7');
});

test('token 缺失按输入与输出分别计数：只回报输入时输出不得当 0 带过',async()=>{
  const items=[
    {ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'sync',prompt_tokens:7,completion_tokens:-1,credit:1},
    {ts:1001,uid:'zcode',account:'GLM 通道',model:'glm-5',mode:'stream',prompt_tokens:-1,completion_tokens:-1,credit:null},
  ];
  const {ctx,get}=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items,summary:{}})}));
  await vm.runInContext('loadUsage()',ctx);
  assert.equal(get('usage-calls').textContent,'2');
  assert.equal(get('usage-tokens').textContent,'7','合计只能包含已观测的值');
  const note=get('usage-note').textContent;
  assert.match(note,/1 条记录未回报输入 token/,'输入缺失计数错：'+note);
  assert.match(note,/2 条记录未回报输出 token/,'输出缺失必须单独报，否则合计少报无人知道：'+note);
  assert.match(note,/1 条记录未回报积分扣费/,'积分缺失口径错：'+note);
  assert.doesNotMatch(note,/未回报 token 用量/,'仍在使用输入输出合并的旧口径：'+note);
  // 通道分布表同样按字段留空：输出缺失的通道显示「—」而不是 0。
  const dist=get('usage-by-channel-body').children;
  assert.deepEqual(dist[0].children.map(td=>td.textContent),['WorkBuddy','1','7','—','1'],'输出缺失被写成 0：'+dist[0].children.map(td=>td.textContent));
});

test('全部未观测时两张卡的积分与 Tokens 都读「—」，概览与调用统计口径一致',async()=>{
  const items=[
    {ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'stream',prompt_tokens:-1,completion_tokens:-1,credit:null},
    {ts:1001,uid:'zcode',account:'GLM 通道',model:'glm-5',mode:'sync',prompt_tokens:-1,completion_tokens:-1,credit:null},
  ];
  const usage=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items,summary:{}})}));
  await vm.runInContext('loadUsage()',usage.ctx);
  assert.equal(usage.get('usage-calls').textContent,'2','调用次数照常统计');
  assert.equal(usage.get('usage-tokens').textContent,'—','一条都没观测到 token 却显示 0：'+usage.get('usage-tokens').textContent);
  assert.equal(usage.get('usage-credit').textContent,'—','一条都没观测到积分却显示 0：'+usage.get('usage-credit').textContent);
  const overview=overviewFixture(items,{enabled:true,reachable:true,model_count:3});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true,qoder_enabled:false,opencode_enabled:false})",overview.ctx);
  await vm.runInContext('loadOverview()',overview.ctx);
  await new Promise(r=>setImmediate(r));
  assert.equal(overview.get('overview-usage-tokens').textContent,'—','概览与调用统计必须同口径');
  assert.equal(overview.get('overview-usage-credit').textContent,'—','概览与调用统计必须同口径');
});

test('调用明细带出协议入口、来源网段与客户端标识，缺失一律破折号',async()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  const before=html.split('<tbody id="usage-body">')[0];
  const head=before.slice(before.lastIndexOf('<thead>'));
  assert.ok(head.includes('<th>入口</th>'),'明细表头没有协议入口列：'+head);
  assert.ok(head.includes('<th>来源</th>')&&head.includes('<th>客户端</th>'),'明细表头没有来源列：'+head);
  assert.match(html,/调用明细[\s\S]{0,200}网段/,'明细区没交代来源只记到网段（隐私口径要写在页面上）');
  const items=[
    {ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'stream',prompt_tokens:1,completion_tokens:1,credit:1,src:'203.0.113.0/24',client:'claude-cli/1.2.3',api:'messages'},
    {ts:1001,uid:'zcode',account:'GLM 通道',model:'glm-5',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:1},
  ];
  const {ctx,get}=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items,summary:{}})}));
  await vm.runInContext('loadUsage()',ctx);
  const detail=get('usage-body').children;
  assert.equal(detail.length,2);
  // 明细按时间倒序：ts1001（无来源观测）在前，ts1000 在后。
  const blank=detail[0].children.map(td=>td.textContent);
  const known=detail[1].children.map(td=>td.textContent);
  assert.deepEqual([blank[4],blank[9],blank[10]],['—','—','—'],'缺失的来源观测不得被补成任何值：'+blank);
  assert.equal(known[4],'Messages','协议入口列没读出 messages');
  assert.equal(known[9],'203.0.113.0/24','来源网段列错了');
  assert.equal(known[10],'claude-cli/1.2.3','客户端标识列错了');
});

test('鉴权失败留痕在调用统计页成一条来源分布，零失败不打扰、缺失不编造',async()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  const usage=html.match(/data-page="usage"[\s\S]*?<\/section>/)[0];
  assert.ok(/id="usage-denied"[^>]*hidden/.test(usage),'调用统计页缺少默认隐藏的鉴权失败行');
  const denied={calls:3,sources:[{src:'203.0.113.0/24',calls:2},{src:'',calls:1}]};
  const {ctx,get}=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items:[],summary:{},denied})}));
  await vm.runInContext('loadUsage()',ctx);
  assert.equal(get('usage-denied').hidden,false,'有鉴权失败时必须显示');
  const text=get('usage-denied').textContent;
  assert.match(text,/鉴权失败 3 次/,'没有给出失败次数：'+text);
  assert.match(text,/203\.0\.113\.0\/24/,'没有给出来源网段：'+text);
  assert.match(text,/未知来源/,'空网段必须写成未知来源，不能留空也不能补值：'+text);
  // 没有失败记录（或后端未上报该字段）时保持隐藏，不显示「鉴权失败 0 次」。
  const quiet=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items:[],summary:{},denied:{calls:0,sources:[]}})}));
  await vm.runInContext('loadUsage()',quiet.ctx);
  assert.equal(quiet.get('usage-denied').hidden,true,'零失败不得占位');
  const absent=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items:[],summary:{}})}));
  await vm.runInContext('loadUsage()',absent.ctx);
  assert.equal(absent.get('usage-denied').hidden,true,'后端未带该字段时不得报错或显示零');
});

// —— 调用统计·日度汇总视图（指标条 / 热力图 / 趋势 / 环形图）——
// 期望日期用 Intl 独立算一次上海日历日，而不是复制 app.js 的 dayShift：
// 实现把窗口算错一天时，跟着复制的测试会一起错。
const shanghaiDay=new Intl.DateTimeFormat('en-CA',{timeZone:'Asia/Shanghai'});
function dayAgoISO(n){const p=shanghaiDay.format(new Date()).split('-').map(Number);return new Date(Date.UTC(p[0],p[1]-1,p[2])-n*864e5).toISOString().slice(0,10);}
function dailyFixture({days=[],lifetime=null,withSvg=true,fail=false}={}) {
  const urls=[];
  const usage=taskFixture(async(url)=>{
    // app.js 装载时会自取一次 /admin/session；夹具回一个带 csrf 的会话，
    // 免得异步的 signedIn 把稍后刷新要用的 csrf 清掉。这条请求不计入断言。
    if(url==='/admin/session')return {ok:true,status:200,json:async()=>({csrf:'active-csrf'})};
    urls.push(url);
    if(fail)throw new Error('核心服务没有响应');
    if(url.startsWith('/admin/usage-daily'))return {ok:true,status:200,json:async()=>({window:'365d',days,lifetime})};
    return {ok:true,status:200,json:async()=>({range:'today',items:[],summary:{}})};
  });
  if(withSvg)usage.ctx.document.createElementNS=(ns,tag)=>({tag,children:[],attrs:{},setAttribute(k,v){this.attrs[k]=v;},append(...c){this.children.push(...c);},textContent:''});
  return {ctx:usage.ctx,get:usage.get,urls,load:()=>vm.runInContext('loadUsageDaily()',usage.ctx)};
}

test('日度指标条只读 usage-daily 端点一次，累计/峰值/连续天数各自守住未观测不等于零',async()=>{
  const lifetime={calls:1234567,prompt_tokens:200,completion_tokens:100,tokens:1234567,tokens_missing:false,credit:3.5,days:42,first_date:'2026-01-05',peak_date:'2026-05-05',peak_tokens:88888,peak_tokens_missing:false,current_streak:3,longest_streak:9};
  const fx=dailyFixture({lifetime});
  await fx.load();
  assert.deepEqual(fx.urls,['/admin/usage-daily?window=365d'],'日度视图必须一次拉满近一年，窗口切片留给客户端：'+fx.urls);
  assert.equal(fx.get('usage-life-tokens').textContent,'1,234,567');
  assert.equal(fx.get('usage-life-peak').textContent,'88,888');
  assert.equal(fx.get('usage-life-peak-note').textContent,'Token 最多的一天：2026-05-05');
  assert.equal(fx.get('usage-life-streak').textContent,'3');
  assert.equal(fx.get('usage-life-longest').textContent,'9');
  assert.match(fx.get('usage-life-line').textContent,/全历史 42 个自然日有调用/);
  assert.match(fx.get('usage-life-line').textContent,/自 2026-01-05/);
  const dark=dailyFixture({lifetime:{...lifetime,tokens_missing:true,peak_tokens_missing:true,peak_tokens:0,tokens:0}});
  await dark.load();
  assert.equal(dark.get('usage-life-tokens').textContent,'—','一个 token 都没观测到却显示数字：'+dark.get('usage-life-tokens').textContent);
  assert.equal(dark.get('usage-life-peak').textContent,'—');
  assert.equal(dark.get('usage-life-peak-note').textContent,'Token 最多的一天：2026-05-05','峰值读数缺失不等于峰值日期也不知道');
  const none=dailyFixture();
  await none.load();
  assert.equal(none.get('usage-life-line').textContent,'正在读取日度汇总…');
  // 夹具里的格子没有 HTML 的初始「—」，所以这里断言的是「一个格子都没被写入」：
  // 浏览器里这四格会原样保留破折号，而不是被补成 0。
  for(const id of ['usage-life-tokens','usage-life-peak','usage-life-streak','usage-life-longest'])
    assert.equal(none.get(id).textContent,'','后端没带 lifetime 时不该往 '+id+' 写值');
  const broken=dailyFixture({fail:true});
  await broken.load();
  assert.match(broken.get('usage-life-line').textContent,/日度汇总读取失败：核心服务没有响应/);
});

test('Token 热力图固定铺满近一年：分档只按已观测 Token 分布，未观测日不并成空格',async()=>{
  const num=n=>({calls:1,prompt_tokens:n,completion_tokens:0,prompt_missing:0,completion_missing:0,denied:0,models:[]});
  const darkDay={date:dayAgoISO(4),calls:2,prompt_tokens:0,completion_tokens:0,prompt_missing:2,completion_missing:2,denied:3,models:[]};
  const fx=dailyFixture({days:[Object.assign({date:dayAgoISO(3)},num(100)),Object.assign({date:dayAgoISO(2)},num(200)),Object.assign({date:dayAgoISO(1)},num(300)),Object.assign({date:dayAgoISO(0)},num(400)),darkDay,{date:dayAgoISO(400),calls:9,prompt_tokens:99999,completion_tokens:0,prompt_missing:0,completion_missing:0,denied:0,models:[]}]});
  await fx.load();
  const [scroller,legend]=fx.get('usage-heat').children;
  const svg=scroller.children[0];
  assert.equal(svg.attrs.class,'heat-svg');
  assert.equal(fx.get('usage-heat-note').hidden,true);
  const cells=svg.children.filter(c=>c.tag==='rect'&&c.attrs['data-date']);
  assert.equal(cells.length,365,'热力图必须按近一年固定铺格，与统计窗口无关：'+cells.length);
  const cell=date=>cells.find(c=>c.attrs['data-date']===date);
  // 已观测值只有 100/200/300/400 四个，四分位取 200/300/300；未观测那天不参与分档。
  assert.equal(cell(dayAgoISO(0)).attrs['data-level'],'4');
  assert.equal(cell(dayAgoISO(0)).attrs.class,'heat-cell hm4');
  assert.equal(cell(dayAgoISO(1)).attrs['data-level'],'2','未观测的零被当成了分档样本');
  assert.equal(cell(dayAgoISO(2)).attrs['data-level'],'1');
  assert.equal(cell(dayAgoISO(3)).attrs['data-level'],'1');
  assert.equal(cell(dayAgoISO(4)).attrs['data-level'],'dark','有调用但没回报 Token 的一天被画成了空格');
  assert.equal(cell(dayAgoISO(4)).attrs.class,'heat-cell heat-dark');
  assert.equal(cell(dayAgoISO(5)).attrs['data-level'],'0');
  assert.match(cell(dayAgoISO(5)).children[0].textContent,/无记录/);
  assert.match(cell(dayAgoISO(4)).children[0].textContent,/Token 未观测/);
  assert.match(cell(dayAgoISO(4)).children[0].textContent,/鉴权失败 3 次/);
  assert.match(cell(dayAgoISO(0)).children[0].textContent,/400 tokens/);
  assert.equal(cell(dayAgoISO(365)),undefined,'近一年之外还多铺了一格');
  assert.equal(cell(dayAgoISO(400)),undefined,'超出近一年的一天不该出现在热力图里');
  const texts=svg.children.filter(c=>c.tag==='text'&&c.attrs.class==='heat-month');
  assert.ok(texts.filter(t=>/月$/.test(t.textContent)).length>=12,'月份刻度不足：'+texts.map(t=>t.textContent).join());
  assert.deepEqual(texts.filter(t=>['一','三','五'].includes(t.textContent)).map(t=>t.textContent),['一','三','五'],'星期刻度错');
  assert.equal(legend.children.filter(c=>String(c.className).includes('heat-sw-dark')).length,1);
  assert.equal(legend.children[legend.children.length-1].textContent,'Token 未观测（1 天）');
  assert.match(legend.children[0].className,/hm0/);
});

test('热力图没有日度汇总时给一句人话，不铺一张全空的网格',async()=>{
  const fx=dailyFixture({days:[]});
  await fx.load();
  assert.equal(fx.get('usage-heat').children.length,0);
  assert.equal(fx.get('usage-heat-note').hidden,false);
  assert.match(fx.get('usage-heat-note').textContent,/近一年没有任何日度汇总/);
});

test('每日趋势与环形图：前五名之外并入「其他模型」，无 Token 的模型与天数各自有文字交代',async()=>{
  const m=(model,tokens,calls)=>({model,prompt_tokens:tokens,completion_tokens:0,calls});
  const darkDay={date:dayAgoISO(1),calls:2,prompt_tokens:0,completion_tokens:0,prompt_missing:2,completion_missing:2,denied:0,models:[m('m9',0,2)]};
  const today={date:dayAgoISO(0),calls:10,prompt_tokens:2800,completion_tokens:0,prompt_missing:0,completion_missing:0,denied:0,
    models:[m('m1',700,1),m('m2',600,1),m('m3',500,1),m('m4',400,1),m('m5',300,1),m('m6',200,1),m('m7',100,1),m('m8',0,3)]};
  const fx=dailyFixture({days:[darkDay,today]});
  await fx.load();
  const rows=fx.get('usage-model-legend').children;
  assert.deepEqual(rows.map(r=>r['data-model']),['m1','m2','m3','m4','m5','其他模型'],'环形排名该是前五名加一档「其他模型」');
  assert.equal(rows[0].children[1].textContent,'1. m1');
  assert.equal(rows[0].children[2].textContent,'700');
  assert.equal(rows[0].children[3].textContent,'25.0%');
  assert.equal(rows[0].children[4].textContent,'1 次');
  assert.equal(rows[5].children[2].textContent,'300','其他模型没并入第六、七名');
  assert.equal(rows[5].children[3].textContent,'10.7%');
  assert.equal(rows[5].children[4].textContent,'2 次');
  const [scroller,legend]=fx.get('usage-daily-trend').children;
  const svg=scroller.children[0];
  assert.equal(svg.attrs.class,'trend-svg');
  const segs=date=>svg.children.filter(c=>c.tag==='rect'&&c.attrs['data-date']===date);
  assert.deepEqual(segs(dayAgoISO(0)).map(c=>c.attrs['data-model']),['m1','m2','m3','m4','m5','其他模型'],'趋势柱里「其他模型」永远画不出来');
  assert.equal(segs(dayAgoISO(1)).length,0,'整段没有 Token 却被画出了柱高');
  assert.equal(segs(dayAgoISO(0))[0].attrs.fill,'#6366f1');
  assert.equal(segs(dayAgoISO(0))[5].attrs.fill,'#94a3b8','其他模型的分段颜色与图例不同源');
  assert.ok(svg.children.filter(c=>c.attrs&&c.attrs.class==='trend-tick').map(c=>c.textContent).includes('2,800'),'纵轴没有柱顶读数');
  assert.equal(legend.children.length,6);
  assert.equal(legend.children[5].children[0].className,'trend-swatch sw-other');
  // 该柱合计就是纵轴最大值，所以分段高度之和必须正好铺满绘图区，堆叠不能留缝。
  assert.ok(Math.abs(segs(dayAgoISO(0)).reduce((s,c)=>s+c.attrs.height,0)-190)<1e-6,'分段高度之和没有铺满纵轴');
  assert.equal(segs(dayAgoISO(0))[5].attrs.y,14);
  assert.equal(fx.get('usage-daily-note').hidden,false);
  assert.match(fx.get('usage-daily-note').textContent,/1 天有调用但未回报 Token，未计入柱高/);
  assert.match(fx.get('usage-daily-note').textContent,/Token 合计为 0 的模型：m8（3 次）、m9（2 次）/);
  const donut=fx.get('usage-model-donut').children[0];
  assert.equal(donut.attrs['aria-label'],'模型 Token 用量占比环形图，合计 2,800 tokens');
  assert.equal(donut.children.filter(c=>c.tag==='circle'&&c.attrs['data-model']).length,6);
  assert.deepEqual(donut.children.filter(c=>c.tag==='circle'&&c.attrs['data-model']).map(c=>c.attrs.stroke),
    ['#6366f1','#06b6d4','#8b5cf6','#ec4899','#d97706','#94a3b8'],'环形分段颜色与图例不同源');
  assert.equal(donut.children.find(c=>c.attrs&&c.attrs.class==='donut-total').textContent,'2,800');
  const none=dailyFixture({days:[darkDay]});
  await none.load();
  assert.equal(none.get('usage-daily-trend').children.length,0);
  assert.match(none.get('usage-daily-note').textContent,/上游都没有回报 Token/);
  assert.equal(none.get('usage-model-donut').children[0].className,'empty');
  assert.equal(none.get('usage-model-legend').children.length,0,'没有份额可算却仍列出排名');
});

test('统计窗口只在客户端切片：切换窗口不再请求，超过 60 天才按周合并柱子',async()=>{
  const mk=(date,tokens)=>({date,calls:1,prompt_tokens:tokens,completion_tokens:0,prompt_missing:0,completion_missing:0,denied:0,models:[{model:'mx',prompt_tokens:tokens,completion_tokens:0,calls:1}]});
  const days=[mk(dayAgoISO(200),50)];
  for(let i=60;i>=0;i--)days.push(mk(dayAgoISO(i),100));
  const fx=dailyFixture({days});
  await fx.load();
  assert.equal(fx.get('usage-model-legend').children[0].children[4].textContent,'30 次','默认近 30 天窗口切片错了');
  assert.equal(fx.get('usage-daily-note').hidden,true);
  const svgOf=()=>fx.get('usage-daily-trend').children[0].children[0];
  assert.ok(svgOf().children.filter(c=>c.tag==='rect').every(c=>!String(c.attrs['data-date']).includes(' ~ ')),'30 个点不该按周合并');
  const far=fx.get('usage-heat').children[0].children[0].children.filter(c=>c.tag==='rect'&&c.attrs['data-date']).find(c=>c.attrs['data-date']===dayAgoISO(200));
  assert.ok(far&&far.attrs['data-level']!=='0','热力图跟着统计窗口缩了短');
  fx.get('usage-window').handlers.change({target:{value:'365d'}});
  assert.equal(fx.urls.length,1,'切换统计窗口又发了一次请求：'+fx.urls);
  assert.equal(fx.get('usage-model-legend').children[0].children[4].textContent,'62 次','窗口切到近一年没生效');
  assert.match(svgOf().children.filter(c=>c.tag==='rect')[0].attrs['data-date'],/ ~ /,'超过 60 个点应按周合并时间段');
  fx.get('usage-window').handlers.change({target:{value:'7d'}});
  assert.equal(fx.get('usage-model-legend').children[0].children[4].textContent,'30 次','非法档位没退回默认窗口');
  assert.equal(fx.urls.length,1);
});

test('趋势 x 轴标签只写起始 MM-DD，末位落在最后一根柱上且相邻标签不压字',async()=>{
  const mk=(date,tokens)=>({date,calls:1,prompt_tokens:tokens,completion_tokens:0,prompt_missing:0,completion_missing:0,denied:0,models:[{model:'mx',prompt_tokens:tokens,completion_tokens:0,calls:1}]});
  const svgOf=fx=>fx.get('usage-daily-trend').children[0].children[0];
  const ticksOf=svg=>svg.children.filter(c=>c.tag==='text'&&c.attrs['text-anchor']==='middle');
  const centersOf=svg=>svg.children.filter(c=>c.tag==='rect').map(c=>Number(c.attrs.x)+Number(c.attrs.width)/2);
  // 26 根柱（近 30 天里跳过 4 个周日）正好踩中真实数据里的形状：step=4，
  // 最后一个点距上一个候选标签只差一档，并排追加就会压字。30 根连续日柱测不出来。
  const daily=[];for(let i=29;i>=0;i--)if(i%7!==3)daily.push(mk(dayAgoISO(i),100+i));
  const a=dailyFixture({days:daily});
  await a.load();
  const svgA=svgOf(a),ta=ticksOf(svgA);
  assert.ok(ta.length>=2,'26 根日柱一个 x 轴标签都没有');
  for(const t of ta)assert.match(t.textContent,/^\d{2}-\d{2}$/,'日柱标签不是 MM-DD：'+t.textContent);
  const centersA=centersOf(svgA);
  assert.ok(Math.abs(Number(ta[ta.length-1].attrs.x)-centersA[centersA.length-1])<0.01,'最后一根柱没有 x 轴标签');
  assert.equal(ta[ta.length-1].textContent,dayAgoISO(0).slice(5),'末位标签写的不是最后一个数据点');
  for(let i=1;i<ta.length;i++)assert.ok(Number(ta[i].attrs.x)-Number(ta[i-1].attrs.x)>=30,'相邻 x 轴标签间距不足一个标签宽度，会互相压字');
  const weekly=[];for(let i=364;i>=0;i--)weekly.push(mk(dayAgoISO(i),100));
  const b=dailyFixture({days:weekly});
  await b.load();
  b.get('usage-window').handlers.change({target:{value:'365d'}});
  const svgB=svgOf(b),tb=ticksOf(svgB);
  assert.ok(svgB.children.filter(c=>c.tag==='rect')[0].attrs['data-date'].includes(' ~ '),'这份夹具没触发按周合并，测不到区间标签');
  for(const t of tb){
    assert.match(t.textContent,/^\d{2}-\d{2}$/,'周合并后轴标签漏出了年份或半截区间：'+t.textContent);
    assert.match(t.children[0].textContent,/ ~ /,'轴标签只留 MM-DD，完整时间段要能用 title 读到');
  }
  const xb=tb.map(t=>Number(t.attrs.x));
  for(let i=1;i<xb.length;i++)assert.ok(xb[i]-xb[i-1]>=30,'周合并视图的 x 轴标签互相压字');
});

test('窄视口下热力图与趋势图按原始宽度出图，由容器横向滚动而不是等比压小轴标签',async()=>{
  const css=readFileSync(__dirname+'/web/style.css','utf8');
  const ruleOf=selector=>{
    const at=css.indexOf(selector);
    assert.notEqual(at,-1,'style.css 里找不到规则 '+selector);
    const open=css.indexOf('{',at);
    return css.slice(open+1,css.indexOf('}',open));
  };
  const mk=(date,tokens)=>({date,calls:1,prompt_tokens:tokens,completion_tokens:0,prompt_missing:0,completion_missing:0,denied:0,models:[{model:'mx',prompt_tokens:tokens,completion_tokens:0,calls:1}]});
  const days=[];for(let i=40;i>=0;i--)days.push(mk(dayAgoISO(i),100));
  const fx=dailyFixture({days});
  await fx.load();
  assert.match(ruleOf('.chart-scroll'),/overflow-x:auto/,'缺少 .chart-scroll 滚动容器：窄屏只能把 SVG 等比缩小');
  for(const [id,selector,cls] of [['usage-heat','#usage-heat .heat-svg','.heat-svg'],['usage-daily-trend','#usage-daily-trend .trend-svg','.trend-svg']]){
    const wrap=fx.get(id),scroller=wrap.children[0];
    assert.equal(scroller.className,'chart-scroll',id+' 的 SVG 没有包进滚动容器');
    const svg=scroller.children[0];
    assert.equal(svg.tag,'svg');
    const min=ruleOf(selector).match(/min-width:(\d+)px/);
    assert.ok(min,cls+' 缺少与视口无关的 min-width：手机上 10px 的轴标签会被压到读不清');
    assert.equal(Number(min[1]),Number(svg.attrs.viewBox.split(' ')[2]),cls+' 的 min-width 与 viewBox 宽度脱钩，缩放比例不再是 1:1');
  }
  // 图例留在滚动容器之外：横向滑动图形时图例不该跟着一起划出可视区。
  assert.equal(fx.get('usage-heat').children[1].className,'heat-legend','热力图图例被卷进了滚动容器');
  assert.ok(fx.get('usage-daily-trend').children.some(c=>c.className==='trend-legend'),'趋势图图例被卷进了滚动容器');
});

test('y 轴最大值标签按自身宽度留位，亿级读数不会被 viewBox 裁掉',async()=>{
  // 无头 Edge 在 1440×900 下 getBBox 实测："164,749,553" 在 10px 字号下宽 60.46px，
  // 而写死的 padL=48 只把标签右端留到 x=42，左边 18.46px 落在 viewBox 外被裁掉。
  // 字体度量不在这里复算（那归活体截图），这里钉两条：估宽要盖住实测值，
  // 且渲染确实按估宽留了位；小数字不许跟着把绘图区挤窄。
  const mk=(date,tokens)=>({date,calls:1,prompt_tokens:tokens,completion_tokens:0,prompt_missing:0,completion_missing:0,denied:0,
    models:[{model:'mx',prompt_tokens:tokens,completion_tokens:0,calls:1}]});
  const topLabel=fx=>fx.get('usage-daily-trend').children[0].children[0].children
    .find(c=>c.tag==='text'&&c.attrs['text-anchor']==='end'&&c.textContent!=='0');
  const big=dailyFixture({days:[mk(dayAgoISO(1),1200),mk(dayAgoISO(0),164749553)]});
  await big.load();
  const tick=topLabel(big);
  assert.equal(tick.textContent,'164,749,553');
  const est=vm.runInContext("axisLabelWidth('164,749,553')",big.ctx);
  assert.ok(est>60.46&&est<90,'估宽要盖住实测的 60.46px 又不出格：'+est);
  assert.ok(Number(tick.attrs.x)-est>=0,'y 轴标签左边超出 viewBox：x='+tick.attrs.x+' 估宽='+est);
  const small=dailyFixture({days:[mk(dayAgoISO(1),300),mk(dayAgoISO(0),900)]});
  await small.load();
  assert.equal(Number(topLabel(small).attrs.x),42,'四位数以内不该把绘图区跟着挤窄');
  // 概览页折线用的是无千分位的 String(yMax)，同一处留位逻辑要一起守住。
  // 峰值那天要有 5 位数次调用，另一天只要 3 次，凑出 days.length>=2 才会画线。
  const ov=dailyFixture({days:[mk(dayAgoISO(1),1),mk(dayAgoISO(0),1)]});
  await ov.load();
  // 在上下文里造数据并调用：顶层 function 是否挂在 sandbox 对象上不作依赖。
  vm.runInContext("(function(){const a=[];for(let i=0;i<12345;i++)a.push({ts:1700000000,account:'a',model:'mx'});"
    +"for(let i=0;i<3;i++)a.push({ts:1800000000,account:'a',model:'mx'});renderTrend(a);})()",ov.ctx);
  const ovTick=ov.get('overview-trend').children.find(c=>c.tag==='svg').children
    .find(c=>c.tag==='text'&&c.attrs['text-anchor']==='end'&&c.textContent!=='0');
  assert.equal(ovTick.textContent,'12345');
  const ovEst=vm.runInContext("axisLabelWidth('12345')",ov.ctx);
  assert.ok(Number(ovTick.attrs.x)-ovEst>=0,'概览页 y 轴标签被裁：x='+ovTick.attrs.x+' 估宽='+ovEst);
});

test('没有 SVG 的渲染环境里三张图各自降级成文字，指标条与排名照常可读',async()=>{
  const lifetime={calls:2,days:1,first_date:dayAgoISO(0),tokens:700,tokens_missing:false,peak_date:dayAgoISO(0),peak_tokens:700,peak_tokens_missing:false,current_streak:1,longest_streak:1};
  const days=[{date:dayAgoISO(0),calls:2,prompt_tokens:700,completion_tokens:0,prompt_missing:0,completion_missing:0,denied:0,models:[{model:'m1',prompt_tokens:700,completion_tokens:0,calls:2}]}];
  const fx=dailyFixture({withSvg:false,days,lifetime});
  await fx.load();
  assert.equal(fx.get('usage-life-tokens').textContent,'700');
  assert.equal(fx.get('usage-heat').children.length,0);
  assert.match(fx.get('usage-heat-note').textContent,/需要浏览器绘制 SVG/);
  assert.equal(fx.get('usage-daily-trend').children.length,0);
  assert.match(fx.get('usage-daily-note').textContent,/需要浏览器绘制 SVG/);
  assert.equal(fx.get('usage-model-donut').children.length,0,'不支持 SVG 却仍试图画环');
  assert.equal(fx.get('usage-model-legend').children.length,1,'没有环形也要给出模型排名：份额、Token 与次数都在排名行里');
});

test('刷新同时重取明细与日度，新版容器、窗口档位与不伪造的口径都落在页面上',async()=>{
  const fx=dailyFixture({days:[]});
  await fx.load();
  assert.equal(fx.urls.length,1);
  fx.get('usage-refresh').handlers.click();
  await new Promise(r=>setImmediate(r));
  assert.equal(fx.urls.filter(u=>u.includes('usage-daily')).length,2,'刷新没有重取日度汇总：'+fx.urls);
  assert.equal(fx.urls.filter(u=>!u.includes('usage-daily')).length,1,'刷新应当同时重取逐次明细');
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  const page=html.match(/data-page="usage"[\s\S]*?<\/section>/)[0];
  for(const id of ['usage-life-line','usage-life-tokens','usage-life-peak','usage-life-peak-note','usage-life-streak','usage-life-longest','usage-window','usage-heat','usage-heat-note','usage-daily-trend','usage-daily-note','usage-model-donut','usage-model-legend'])
    assert.ok(page.includes('id="'+id+'"'),'新版调用统计缺少容器 '+id);
  assert.deepEqual((page.match(/<option value="\d+d"/g)||[]).map(s=>s.match(/\d+/)[0]),['30','90','365'],'页面档位与 core 的 usageDailyWindows 不再一致');
  assert.ok(!/style="/.test(page),"新版区块出现内联 style，CSP 的 style-src 'self' 会把它静默丢掉");
  assert.ok(!/聊天时长|会话时长/.test(html),'账本只有逐次时间戳，观测不到聊天时长，页面上不得出现该项');
  assert.match(page,/明细筛选/);
  assert.match(page,/只作用于下方两张表/,'明细筛选没说明它不再作用于上方图表');
  assert.match(page,/永久保留/,'日度汇总永久保留这条口径没写在页面上');
});

test('热力图分档与模型图例配色在 CSS 与 app.js 里必须同源',()=>{
  const css=readFileSync(__dirname+'/web/style.css','utf8');
  const {ctx}=taskFixture(async()=>({ok:true,status:200,json:async()=>({})}));
  const colors=JSON.parse(vm.runInContext('JSON.stringify(modelColors)',ctx));
  const other=vm.runInContext('otherModelColor',ctx);
  const cuts=JSON.parse(vm.runInContext('JSON.stringify(heatCut)',ctx));
  assert.equal(colors.length,5);
  assert.deepEqual(cuts,['hm0','hm1','hm2','hm3','hm4']);
  const background=sel=>(css.match(new RegExp('\\.'+sel+'\\{background:(#[0-9a-f]{6})\\}'))||[])[1];
  for(let i=0;i<colors.length;i++)assert.equal(background('sw-r'+i),colors[i],'图例第 '+(i+1)+' 档配色与 app.js 不同源');
  assert.equal(background('sw-other'),other,'「其他模型」配色与 app.js 不同源');
  for(const cls of cuts)assert.ok(new RegExp('\\.'+cls+'\\{fill:').test(css),'热力图分档类 '+cls+' 在 CSS 里没有填充色');
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

function routeFixture(routeState, capState={available:false, rows:[]}, switches={}) {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const posts=[];
  const {ctx,get}=taskFixture((url,options={})=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    if(url==='/admin/route')return response(routeState);
    if(url==='/admin/route/save'){posts.push(JSON.parse(options.body));return response(routeState);}
    if(url==='/admin/model-switch')return response({enabled:true,overrides:switches});
    if(url==='/admin/capabilities')return response(capState);
    throw new Error('unexpected '+url);
  });
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,posts};
}
// 榜单和开关表都是 loadRoutes/signedIn 里 void 出去的后台加载，断言前先把微任务排空，
// 否则读到的是上一帧的渲染结果。
async function flushMicrotasks(count=10) {
  for (let index=0; index<count; index++) await Promise.resolve();
}
// capabilityRows 是 2026-10-05 真实快照里出现过的形态：heat 由服务端按 most-popular
// 顺序给出、AA 指数可为 null、arena 条目带 models/agents 两种 arena 与不同类目。
const capabilityRows=[
  {heat:1,id:'vendor/strong',slug:'vendor/strong',name:'Strong',context:200000,free:false,intelligence:50,coding:60,agentic:null,
    arena:[{arena:'models',category:'website',elo:1300,win_rate:60,rank:2},{arena:'models',category:'gamedev',elo:1200,win_rate:55,rank:9}],
    tools:true,structured:true,reasoning:true,video_in:false,image_in:true},
  {heat:2,id:'vendor/free-one',slug:'vendor/free-one',name:'Free One',context:131072,free:true,intelligence:30,coding:40,agentic:null,
    arena:[],tools:true,structured:false,reasoning:false,video_in:false,image_in:false},
  {heat:3,id:'vendor/unknown-cost',slug:'vendor/unknown-cost',name:'Unknown Cost',context:1000000,free:false,intelligence:70,coding:80,agentic:null,
    arena:[{arena:'agents',category:'agenticslides',elo:1250,win_rate:52,rank:1}],tools:false,structured:false,reasoning:false,video_in:true,image_in:false},
  {heat:4,id:'vendor/outside',slug:'vendor/outside',name:'Outside Pool',context:64000,free:false,intelligence:90,coding:99,agentic:88,
    arena:[],tools:true,structured:true,reasoning:true,video_in:false,image_in:false},
];
function capabilityFixture(capState) {
  return routeFixture({enabled:true,auto_model:'gateway-auto',auto_fallback:'',default_auto_fallback:'cn:auto',
    auto_preview:{},aliases:[],
    channels:[{channel:'core',enabled:true,reachable:true,models:['cn:strong','cn:free-one','cn:unknown-cost']},
      {channel:'qoder',enabled:false,reachable:false,models:[]}],
    model_costs:{'cn:strong':'x0.79 credits','cn:free-one':'x0.00'}}, capState);
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
  assert.equal(get('routes-auto-badge').className,'badge ok');
  assert.match(get('routes-auto-text').textContent,/现在自动挑中的是 opencode-OC · Free（OpenCode 通道）/);
  assert.equal(get('routes-default-fallback').textContent,'cn:auto');
  const rows=get('routes-alias-body').children;
  assert.equal(rows.length,1,'alias row missing');
  assert.deepEqual(rows[0].children.slice(0,6).map(td=>td.textContent),['my-fast','OpenCode 通道','OC · Free','opencode-OC · Free','','日常']);
  assert.equal(rows[0].children[4].children[0].textContent,'启用','alias state badge');
  assert.equal(rows[0].children[4].children[0].className,'badge ok');
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

test('模型能力 TOP ranks only pool models and tiers them by real local cost',async()=>{
  const {ctx,get}=capabilityFixture({available:true,snapshot_at:'2026-10-05T02:00:00Z',note:'源站不标注各项评测的发布日期',rows:capabilityRows});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  await flushMicrotasks();
  assert.match(get('cap-stamp').textContent,/4 个模型/,'loadRoutes 没有把榜单拉起来');
  assert.equal(get('cap-note').textContent,'源站不标注各项评测的发布日期','来源说明没露出');
  assert.equal(get('cap-note').hidden,false);
  // 默认维度是编程：池外模型不进表，池内三个按 coding 从高到低。
  assert.deepEqual(get('cap-body').children.map(tr=>tr.children[0].textContent),['Unknown Cost','Strong','Free One']);
  const costs=get('cap-body').children.map(tr=>tr.children[5].children[0].textContent);
  assert.deepEqual(costs,['未公布','×0.79','×0.00'],'倍率列丢失或把未公布当成了零');
  assert.deepEqual(get('cap-body').children.map(tr=>tr.children[6].children[0].textContent),['未放行','未放行','已放行']);
  assert.equal(get('cap-body').children[0].children[6].children[0].className,'badge warn','未放行应当走告警配色而不是等同可用');
  // 三档：免费兜底与最省都只能落在明示 0 积分的模型上，上限不看花费。
  const tiers=get('cap-tiers').children;
  assert.equal(tiers.length,3);
  assert.deepEqual(tiers.map(article=>article.children[1].textContent),['Free One','Free One','Unknown Cost']);
  assert.match(tiers[2].children[2].textContent,/未公布/,'上限档必须如实交代花费未知');
  assert.match(tiers[2].children[2].textContent,/当前未放行/);
  // 切到全量视图：池外模型只读，不生成任何可点元素。
  vm.runInContext("capDim='all';renderCapabilities()",ctx);
  const all=get('cap-body').children;
  assert.deepEqual(all.map(tr=>tr.children[1].textContent),['Strong','Free One','Unknown Cost','Outside Pool'],'全量榜单没按热度顺序给全');
  assert.deepEqual(all.map(tr=>tr.children[2].children[0].textContent),['核心账号池','核心账号池','核心账号池','池外']);
  assert.equal(all[3].children[8].children.length,0,'池外模型生成了可点元素');
  assert.equal(all[0].children[8].children[0].textContent,'填入表单');
  all[0].children[8].children[0].handlers.click();
  assert.equal(get('routes-alias-channel').value,'core');
  assert.equal(get('routes-alias-model').value,'cn:strong','填入表单必须写通道里的真实模型名，不是榜单里的外部 id');
});

test('模型能力 TOP 面板用静态文字解释「填入表单」做什么',()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  const panel=html.match(/<h2>模型能力 TOP<\/h2>[\s\S]*?<\/article>/)[0];
  assert.match(panel,/「填入表单」[^<]*灌进[^<]*别名/,'TOP 面板没在文字里说明「填入表单」把什么灌进上方别名表单');
  assert.match(panel,/点「保存别名」才生效/,'没有交代点完按钮之后还要保存');
});

test('两处「填入表单」共用同一句提示和同一条悬停说明',async()=>{
  const {ctx,get}=capabilityFixture({available:true,snapshot_at:'2026-10-05T02:00:00Z',note:'n',rows:capabilityRows});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  await flushMicrotasks();
  const tableButton=get('cap-body').children[0].children[7].children[0];
  const tierButton=get('cap-tiers').children[0].children[3];
  assert.equal(tierButton.textContent,'填入表单','三档卡片的按钮和表格里不叫同一个名字');
  assert.equal(tableButton.title,'填进上方别名表单，仍需起名并保存');
  assert.equal(tierButton.title,'填进上方别名表单，仍需起名并保存');
  tableButton.handlers.click();
  const tableNotice=get('notice').textContent;
  tierButton.handlers.click();
  const tierNotice=get('notice').textContent;
  assert.match(tableNotice,/^已填入 \S+，起个名字后点「保存别名」才生效$/,'表格那侧的提示没交代保存这一步');
  assert.equal(tierNotice.replace(/已填入 \S+/,''),tableNotice.replace(/已填入 \S+/,''),'两处提示的措辞不一致');
});

test('模型能力 TOP renders a bounded window of the full board and says so',async()=>{
  const rows=Array.from({length:105},(_,i)=>({heat:i+1,id:'vendor/model-'+i,name:'Model '+i,context:0,free:false,arena:[],tools:false,structured:false,reasoning:false,videoIn:false,imageIn:false}));
  const {ctx,get}=capabilityFixture({available:true,snapshot_at:'2026-10-05T02:00:00Z',note:'源站说明。',rows});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  await flushMicrotasks();
  vm.runInContext("capDim='all';renderCapabilities()",ctx);
  assert.equal(get('cap-body').children.length,100,'全量视图按整份快照塞进了 DOM');
  assert.match(get('cap-note').textContent,/只渲染前 100 行/,'截断没有向用户交代');
  assert.match(get('cap-note').textContent,/命中 105/);
  // 筛选作用于全表而不是渲染窗口：命中数落回窗口内就不再声明截断。
  vm.runInContext("capFilter='model 100';renderCapabilities()",ctx);
  assert.deepEqual(get('cap-body').children.map(tr=>tr.children[1].textContent),['Model 100']);
  assert.equal(get('cap-note').textContent,'源站说明。','命中不足一个窗口时仍挂着截断提示');
});

test('模型能力 TOP never pads a missing index and says so instead of an empty board',async()=>{
  const {ctx,get}=capabilityFixture({available:true,snapshot_at:'2026-10-05T02:00:00Z',note:'n',rows:capabilityRows});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  await flushMicrotasks();
  // 智能体：池内三个模型上游都是 null，只有池外模型有分——不能拿 0 分凑一张表。
  vm.runInContext("capDim='agentic';renderCapabilities()",ctx);
  assert.equal(get('cap-body').children.length,0);
  assert.equal(get('cap-empty').hidden,false,'缺数据时没有交代原因');
  assert.match(get('cap-empty').textContent,/对齐/);
  assert.deepEqual(get('cap-tiers').children.map(article=>article.children[1].textContent),['暂无','暂无','暂无']);
  assert.match(get('cap-tiers').children[2].children[2].textContent,/都没有外部数据/);
  // 设计维度按类目对齐：名次只在同类目内可比。
  vm.runInContext("capDim='design';renderCapabilities()",ctx);
  assert.equal(get('cap-category-wrap').hidden,false,'设计维度没给出类目选择器');
  assert.deepEqual(get('cap-category').children.map(option=>option.value),['agenticslides','gamedev','website']);
  assert.equal(get('cap-category').value,'agenticslides','默认类目应落在池内真有数据的类目上');
  assert.deepEqual(get('cap-body').children.map(tr=>tr.children[2].textContent),['#1']);
  get('cap-category').value='website';
  await get('cap-category').handlers.change();
  assert.deepEqual(get('cap-body').children.map(tr=>[tr.children[0].textContent,tr.children[2].textContent]),[['Strong','#2']]);
  // 筛选只缩表，不动档位：档位是同一份候选的三种取法，跟着筛选走会自证式变矮。
  vm.runInContext("capDim='coding';renderCapabilities()",ctx);
  get('cap-filter').value='free';
  await get('cap-filter').handlers.input();
  assert.deepEqual(get('cap-body').children.map(tr=>tr.children[0].textContent),['Free One']);
  assert.deepEqual(get('cap-tiers').children.map(article=>article.children[1].textContent),['Free One','Free One','Unknown Cost']);
});

test('模型能力 TOP shows an honest unavailable state instead of a blank board',async()=>{
  const {ctx,get}=capabilityFixture({available:false,rows:[]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  await flushMicrotasks();
  assert.equal(get('cap-stamp').textContent,'外部榜单未取到');
  assert.equal(get('cap-note').hidden,true,'没取到数据时不该展示来源说明');
  assert.equal(get('cap-body').children.length,0);
  assert.equal(get('cap-tiers').children.length,0,'拿不到数据还硬摆三档');
  assert.match(get('cap-empty').textContent,/拿不到/);
  assert.match(get('cap-empty').textContent,/不受影响/);
});

test('模型能力 TOP exists in the page shell with a labelled dimension switch',()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  assert.match(html,/模型能力 TOP/);
  for(const id of ['cap-stamp','cap-dims','cap-category','cap-filter','cap-tiers','cap-head','cap-body','cap-note'])assert.ok(html.includes('id="'+id+'"'),'capability module id '+id+' missing');
  assert.match(html,/id="cap-dims"[^>]*role="group"[^>]*aria-label="能力维度"/,'维度开关缺组标签');
  assert.match(html,/id="cap-filter"[^>]*type="search"/,'筛选框应是 search 类型');
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

function overviewFixture(items,zcodeStatus,extraStatuses) {
  const response=body=>({ok:true,status:200,json:async()=>body});
  const extras=extraStatuses||{};
  const {ctx,get}=taskFixture((url)=>{
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:1,healthy:1,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[{id:'cn:workbuddy',realm:'cn'}]});
    if(url.startsWith('/admin/usage'))return response({range:'today',items});
    if(url==='/admin/heat')return response({available:false,ranks:[]});
    if(url==='/admin/model-switch')return response({enabled:false,overrides:{}});
    if(url==='/admin/probe')return response({running:false,done:0,total:0,channels:{}});
    if(url==='/admin/zcode')return response(zcodeStatus);
    if(url==='/admin/autoclaw'&&extras.autoclaw)return response(extras.autoclaw);
    if(url==='/admin/trae'&&extras.trae)return response(extras.trae);
    if(url==='/admin/raccoon'&&extras.raccoon)return response(extras.raccoon);
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
    {ts:1004,uid:'autoclaw',account:'AutoClaw 通道',model:'autoclaw-a',mode:'sync',prompt_tokens:6,completion_tokens:7,credit:0},
    {ts:1005,uid:'trae',account:'Trae 通道',model:'trae-b',mode:'stream',prompt_tokens:8,completion_tokens:9,credit:0},
    {ts:1006,uid:'raccoon',account:'Raccoon 通道',model:'raccoon-c',mode:'sync',prompt_tokens:2,completion_tokens:3,credit:0},
  ];
  const {ctx,get}=overviewFixture(items,{enabled:true,reachable:true,model_count:3},{
    autoclaw:{enabled:true,reachable:true,model_count:2,health:{healthy:1,total:2},models:[{id:'autoclaw-a'},{id:'autoclaw-c'}]},
    trae:{enabled:true,reachable:true,model_count:2,accounts:[{uid:'t1',enabled:true},{uid:'t2',enabled:false}],models:[{id:'trae-b'},{id:'trae-d'}]},
    raccoon:{enabled:true,reachable:true,model_count:1,accounts:[{uid:'r1',enabled:true,points:50}],models:[{id:'raccoon-c'}]},
  });
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true,qoder_enabled:true,opencode_enabled:true,autoclaw_enabled:true,trae_enabled:true,raccoon_enabled:true})",ctx);
  // 概览的旁路状态只读页签缓存：先访问一次各旁路页签，再刷新概览，验证「在线/N 模型」口径。
  await vm.runInContext('loadZcode()',ctx);
  await vm.runInContext('loadAutoClaw()',ctx);
  await vm.runInContext('loadTrae()',ctx);
  await vm.runInContext('loadRaccoon()',ctx);
  await vm.runInContext('loadOverview()',ctx);
  await new Promise(r=>setImmediate(r));
  const rows=get('overview-channels-body').children;
  const text=td=>td.children.length?td.children[0].textContent:td.textContent;
  assert.deepEqual(rows.map(tr=>tr.children[0].textContent),['WorkBuddy','GLM（Zcode）','Qoder','OpenCode','AutoClaw','Trae','小浣熊'],'channel overview rows missing or misordered');
  assert.deepEqual(rows[0].children.map(text),['WorkBuddy','运行中','1','1','100','200','1.5']);
  // 旁路状态来自页签缓存：未访问过的通道如实标「未加载」，不伪造在线。
  assert.deepEqual(rows[1].children.map(text),['GLM（Zcode）','在线','3','1','10','20','—'],'zcode missing credit must show dash, not 0');
  assert.deepEqual(rows[2].children.map(text),['Qoder','未加载','—','1','—','5','0'],'qoder 输入整组缺失显示「—」，输出与真实 0 积分保留');
  assert.match(text(rows[3].children[1]),/不可达|未加载/);
  assert.deepEqual(rows[4].children.map(text),['AutoClaw','在线','2','1','6','7','0'],'autoclaw 通道行口径不符');
  assert.deepEqual(rows[5].children.map(text),['Trae','在线','2','1','8','9','0'],'trae 通道行口径不符');
  assert.deepEqual(rows[6].children.map(text),['小浣熊','在线','1','1','2','3','0'],'raccoon 通道行口径不符');
  assert.equal(get('overview-usage-calls').textContent,'7');
  assert.equal(get('overview-usage-tokens').textContent,'377');
  assert.equal(get('overview-usage-credit').textContent,'3.5');
  assert.match(get('overview-usage-note').textContent,/1 条记录未回报输入 token/);
  assert.match(get('overview-usage-note').textContent,/1 条记录未回报积分扣费/);
  // 单通道筛选只作用于指标卡，通道总览表保持全量。
  get('overview-usage-channel').handlers.change({target:{value:'zcode'}});
  assert.equal(get('overview-usage-calls').textContent,'1');
  assert.equal(get('overview-usage-tokens').textContent,'30');
  assert.equal(get('overview-usage-credit').textContent,'—','all-missing credit must not read as zero');
  assert.equal(get('overview-channels-body').children.length,7,'channel table must stay full while metrics are filtered');
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

test('overview channel cards summarize each enabled channel and jump to its tab',async()=>{
  const items=[{ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'sync',prompt_tokens:1,completion_tokens:2,credit:0}];
  const {ctx,get}=overviewFixture(items,{enabled:true,reachable:true,model_count:3});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadZcode()',ctx);
  await vm.runInContext('loadOverview()',ctx);
  await new Promise(r=>setImmediate(r));
  const cards=get('overview-cards').children;
  // 机架上的每个仓位顶着自己的通道色：卡片必须挂 ch-<通道>，否则 --ch-lit 在名称上解析不出来。
  assert.deepEqual(cards.map(c=>c.className),['channel-card ch-workbuddy','channel-card ch-zcode'],'通道卡片没有挂通道类');
  assert.deepEqual(cards.map(c=>c.children[0].children[1].className),['badge rail-state ok','badge rail-state ok'],'机架上的状态徽章没走深底那一套');
  assert.deepEqual(cards.map(c=>c.children[2].children[0].className),['rail-btn','rail-btn'],'机架上的入口按钮不该是白底 secondary');
  assert.deepEqual(cards.map(c=>c.children[0].children[0].textContent),['WorkBuddy','GLM（Zcode）'],'cards must follow enabled channels');
  const stats=card=>card.children[1].children.map(d=>d.children[0].textContent+':'+d.children[1].textContent);
  assert.deepEqual(stats(cards[0]),['账号:1','可用:1','冷却 / 禁用:0 / 0','在途:0','模型:1'],'workbuddy card must mirror the account pool snapshot');
  assert.deepEqual(stats(cards[1]),['模型:3','套餐:—'],'zcode card without plan must show dash');
  assert.equal(cards[1].children[0].children[1].textContent,'在线');
  cards[1].children[2].children[0].handlers.click();
  assert.equal(vm.runInContext('page',ctx),'zcode','card shortcut did not navigate');
});

test('overview trend draws one polyline per channel and degrades to a note below two days',async()=>{
  const d1=1759968000,d2=d1+86400;
  const items=[
    {ts:d1,uid:'u1',account:'a',model:'cn:m',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0},
    {ts:d1+60,uid:'u1',account:'a',model:'cn:m',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0},
    {ts:d2,uid:'u1',account:'a',model:'cn:m',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0},
    {ts:d2,uid:'zcode',account:'GLM 通道',model:'glm-x',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:null},
  ];
  const {ctx,get}=overviewFixture(items,{enabled:true,reachable:true,model_count:1});
  ctx.document.createElementNS=(ns,tag)=>({tag,children:[],attrs:{},setAttribute(k,v){this.attrs[k]=v;},append(...c){this.children.push(...c);},textContent:''});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadZcode()',ctx);
  await vm.runInContext('loadOverview()',ctx);
  await new Promise(r=>setImmediate(r));
  const [svg,legend]=get('overview-trend').children;
  assert.equal(svg.tag,'svg');
  assert.equal(svg.children.filter(c=>c.tag==='polyline').length,2,'one line per reporting channel');
  assert.equal(legend.children.length,2,'legend missing a channel');
  assert.equal(get('overview-trend-note').hidden,true);
  // 线、折点、图例必须认同一束光：先前线用一组临时亮色、图例用通道色，同一个通道在一张图里
  // 长出了两种蓝。三处都只能挂 class，色值归 --ch-* 那一处管。
  assert.deepEqual(svg.children.filter(c=>c.tag==='polyline').map(c=>c.attrs.class),
    ['trend-line stroke-workbuddy','trend-line stroke-zcode'],'折线没有挂通道 class');
  assert.deepEqual([...new Set(svg.children.filter(c=>c.tag==='circle').map(c=>c.attrs.class))],
    ['fill-workbuddy','fill-zcode'],'折点没有挂通道 class');
  assert.deepEqual(legend.children.map(i=>i.children[0].className),
    ['trend-swatch sw-workbuddy','trend-swatch sw-zcode'],'图例色块没有挂通道 class');
  // 单通道筛选后只剩 workbuddy 一条线。
  get('overview-usage-channel').handlers.change({target:{value:'workbuddy'}});
  const [svg2]=get('overview-trend').children;
  assert.equal(svg2.children.filter(c=>c.tag==='polyline').length,1,'filtered trend must drop other channels');
  // 不足两天时不画折线，给文字提示。
  await vm.runInContext("overviewUsageItems=[{ts:1000,uid:'u1',account:'a',model:'cn:m',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0}];renderOverview()",ctx);
  assert.equal(get('overview-trend').children.length,0);
  assert.equal(get('overview-trend-note').hidden,false);
  assert.match(get('overview-trend-note').textContent,/至少 2 天/);
});

test('agent snippet falls back to gateway-auto, never the ambiguous bare auto',()=>{
  const {ctx,get}=logoutFixture(()=>new Promise(()=>{}));
  vm.runInContext('renderRouteExample({aliases:[]})',ctx);
  assert.match(get('routes-example').textContent,/"model": "gateway-auto"/);
});

test('overview page, probe buttons and the speed column exist in the page shell',()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  assert.match(html,/id="overview-channels-body"/,'channel overview table missing');
  assert.match(html,/id="overview-cards"/,'channel cards container missing');
  assert.match(html,/id="overview-trend"[\s\S]*id="overview-trend-note"/,'daily trend chart missing');
  assert.match(html,/id="models-group-toggle"/,'model grouping toggle missing');
  assert.match(html,/id="overview-usage-channel"[\s\S]*id="overview-usage-range"/,'overview usage filters missing');
  // 探测按钮的名单从 app.js 的 probeChannelNames 现读：先前这里写死四颗，
  // Trae 接入时页面补了按钮、脚本补了监听，这条测试一直没去看第三处。
  const js=readFileSync(__dirname+'/web/app.js','utf8');
  const probeNames=objectKeysFrom(js,'probeChannelNames');
  for(const name of probeNames){
    assert.ok(html.includes('id="probe-'+name+'"'),'探测按钮 probe-'+name+' 不存在，脚本会给 undefined 绑事件');
    assert.ok(html.includes('id="probe-'+name+'-progress"'),'探测进度行 probe-'+name+'-progress 不存在，renderProbeProgress 会读到 null');
  }
  assert.ok(!html.includes('id="probe-opencode"'),'opencode must not get a manual probe button');
  // 允许表头带 class（数值列要挂 class="num" 才能和单元格同侧对齐），只认列名本身。
  assert.equal((html.match(/<th[^>]*>速度<\/th>/g)||[]).length,7,'every model table must carry the speed column');
  assert.match(html,/gateway-auto/,'routes page still advertises the bare auto name');
  // 改造后的界面契约：常驻无障碍播报区存在，逐节的营销小标题不再回来。
  assert.match(html,/id="announce"[^>]*aria-live="polite"/,'persistent live region missing');
  assert.ok(!html.includes('class="eyebrow"'),'per-section eyebrow kickers are back');
  assert.ok(!html.match(/尽在这里|开始你的下一次对话|接入你喜欢的客户端|一切，从连接开始|给模型起个名字/),'marketing page copy is back');
});

test('alias delete asks for a second click before it writes the route file',async()=>{
  const {ctx,get,posts}=routeFixture({enabled:true,auto_model:'gateway-auto',auto_fallback:'',default_auto_fallback:'cn:auto',
    auto_preview:{routed_model:'cn:workbuddy',channel:'core',reason:'探测可用',fallback:false},
    aliases:[{alias:'my-fast',channel:'core',model:'cn:workbuddy',public_model:'cn:workbuddy',enabled:true,note:'日常'}],
    channels:[{channel:'core',enabled:true,reachable:true,models:['cn:workbuddy']}]});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadRoutes()',ctx);
  const remove=get('routes-alias-body').children[0].children[6].children[2];
  assert.equal(remove.textContent,'删除');
  await remove.handlers.click({});
  assert.equal(posts.length,0,'the first click must not write the route file');
  assert.match(remove.textContent,/再次点击确认删除/,'delete is not armed visibly');
  await remove.handlers.click({});
  await new Promise(resolve=>setTimeout(resolve,0));
  assert.equal(posts.length,1,'the second click must save');
  assert.deepEqual(posts[0].models,[],'deleting the only alias must post an empty list');
});


test('clearing a transcript restores the pane invitation instead of a blank box', () => {
  const {ctx,get}=taskFixture(async()=>({ok:true,status:200,json:async()=>({}),headers:{get:()=>null}}));
  const box=get('messages');
  const invitation={className:'chat-empty',textContent:'你好，想先问些什么？'};
  invitation.remove=function(){const i=box.children.indexOf(this);if(i>=0)box.children.splice(i,1);};
  box.children=[invitation];
  box.querySelector=function(sel){
    return sel==='.chat-empty'?(this.children.find(c=>c.className==='chat-empty')||null):null;
  };
  vm.runInContext("clearChat()",ctx);
  assert.equal(box.children.length,1,'clearing must leave exactly the invitation node');
  assert.equal(box.children[0].className,'chat-empty');
  vm.runInContext("message('user','私密内容')",ctx);
  assert.equal(box.children.some(c=>c.className==='chat-empty'),false,'a live reply must retire the invitation');
  vm.runInContext("clearChat()",ctx);
  assert.equal(box.children.length,1,'the remembered invitation must come back');
  assert.equal(box.children[0].className,'chat-empty');
});

test('changing page starts the new view at its own top', () => {
  const {ctx}=taskFixture(async()=>({ok:true,status:200,json:async()=>({}),headers:{get:()=>null}}));
  const scrolls=vm.runInContext(
    "let n=0;window.scrollTo=()=>{n++};showPage('usage');n",ctx);
  assert.equal(scrolls,1,'a deliberate page change must reset the scroll once');
});

test('页面级刷新按钮吸顶，落在网关状态左侧且只有当前页那一个可见', () => {
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  const rail=html.match(/<header class="topbar">[\s\S]*?<\/header>/)[0];
  const ids=['refresh','zcode-refresh','qoder-refresh','opencode-refresh','ccr-refresh','autoclaw-refresh','trae-refresh','raccoon-refresh','usage-refresh','routes-refresh'];
  const stateAt=rail.indexOf('id="service-state"');
  assert.ok(stateAt>0,'顶栏里找不到网关状态，无从判断左右');
  for(const id of ids){
    assert.equal(html.split(`id="${id}"`).length-1,1,`#${id} 在页面里出现了不止一次，搬顶栏时没删掉原处那个`);
    assert.ok(rail.includes(`id="${id}"`),`#${id} 没搬进顶栏`);
    assert.ok(rail.indexOf(`id="${id}"`)<stateAt,`#${id} 排到了网关状态的右侧`);
  }
  // WorkBuddy 的「刷新任务」只管自动任务子页，抬到顶栏会让人以为它能刷账号，必须留在原位。
  assert.ok(!rail.includes('id="task-refresh"'),'刷新任务被误抬进顶栏');

  const {ctx,get}=taskFixture(async()=>({ok:true,status:200,json:async()=>({}),headers:{get:()=>null}}));
  const visible=()=>ids.filter(id=>get(id).hidden===false);
  for(const [view,id] of [['overview','refresh'],['zcode','zcode-refresh'],['qoder','qoder-refresh'],['opencode','opencode-refresh'],['ccr','ccr-refresh'],['autoclaw','autoclaw-refresh'],['trae','trae-refresh'],['raccoon','raccoon-refresh'],['usage','usage-refresh'],['routes','routes-refresh']]){
    vm.runInContext(`showPage('${view}')`,ctx);
    assert.deepEqual(visible(),[id],`切到 ${view} 页时顶栏可见的刷新按钮不是唯一那一个`);
  }
  vm.runInContext("showPage('workbuddy')",ctx);
  assert.deepEqual(visible(),[],'WorkBuddy 与 API 接入页没有页面级刷新，顶栏不该留一个可点的空按钮');
});

test('状态徽章分五档：可用、打折、不可达、未启用、未探测各占一档且互不相同', () => {
  const css=readFileSync(__dirname+'/web/style.css','utf8');
  // 档位名写错不会报错，只会静默退回 .badge 的浅紫胶囊，所以先钉样式真的定义了这五档。
  for(const tone of ['ok','warn','bad','idle','unknown'])
    assert.ok(css.includes(`.badge.${tone}{`),`style.css 缺 .badge.${tone}，该档位会画成默认紫`);

  const {ctx,get}=taskFixture(async()=>({ok:true,status:200,json:async()=>({}),headers:{get:()=>null}}));
  const pair=expr=>vm.runInContext(`(()=>{const s=${expr};return [s.label,s.tone].join('|')})()`,ctx);

  assert.equal(pair('modelProbeState(null)'),'未探测|unknown','没有探测数据不能画成告警，也不能画成可用');
  assert.equal(pair('modelProbeState({ok:true})'),'可用|ok');
  assert.equal(pair('modelProbeState({ok:true,chatOnly:true})'),'仅对话|warn');
  assert.equal(pair('modelProbeState({ok:false,category:"access"})'),'地区限制|bad');
  assert.equal(pair('modelProbeState({ok:false})'),'不可用|bad');
  // order 是排序档位（可用→仅对话→未探测→不可用），与配色各自独立。
  assert.equal(vm.runInContext('[null,{ok:true},{ok:true,chatOnly:true},{ok:false}].map(r=>modelProbeState(r).order).join("-")',ctx),'2-0-1-3');

  // 三个不同的负面事实必须落在三个不同的档位上，肉眼扫色就能分开。
  assert.equal(pair("overviewChannelStatus('zcode')"),'未加载|unknown');
  vm.runInContext("zcodeStatus={enabled:false}",ctx);
  assert.equal(pair("overviewChannelStatus('zcode')"),'未启用|idle');
  vm.runInContext("zcodeStatus={enabled:true,reachable:false}",ctx);
  assert.equal(pair("overviewChannelStatus('zcode')"),'不可达|bad');
  vm.runInContext("zcodeStatus={enabled:true,reachable:true}",ctx);
  assert.equal(pair("overviewChannelStatus('zcode')"),'在线|ok');

  vm.runInContext("lastStatus=null",ctx);
  assert.equal(pair("overviewChannelStatus('workbuddy')"),'未知|unknown');
  vm.runInContext("lastStatus={total:0}",ctx);
  assert.equal(pair("overviewChannelStatus('workbuddy')"),'等待账号|idle');
  vm.runInContext("lastStatus={total:2}",ctx);
  assert.equal(pair("overviewChannelStatus('workbuddy')"),'运行中|ok');

  // 概览卡片说清旁路卡在哪一步：没凭据是要人去办的（告警），没启动是状态本身（中性）。
  vm.runInContext("zcodeStatus={enabled:true,reachable:true,control:true,logged_in:false,proxy_running:false}",ctx);
  assert.equal(pair("cardStatus('zcode')"),'未登录|warn');
  vm.runInContext("zcodeStatus={enabled:true,reachable:true,control:true,logged_in:true,proxy_running:false}",ctx);
  assert.equal(pair("cardStatus('zcode')"),'未启用|idle');

  vm.runInContext("renderAccounts({total:3,healthy:1,cooling:1,disabled:1,accounts:["
    +"{uid:'u1',nickname:'甲',credits:0,credits_known:true},"
    +"{uid:'u2',nickname:'乙',cooling:true,credits:0,credits_known:true},"
    +"{uid:'u3',nickname:'丙',disabled:true,credits:0,credits_known:true}]})",ctx);
  assert.deepEqual(get('accounts-body').children.map(tr=>[tr.children[1].children[0].textContent,tr.children[1].children[0].className].join('|')),
    ['可用|badge ok','冷却中|badge warn','已禁用|badge idle'],'账号池三态没各占一档');
});

// 视觉契约只能按字面核对：选择器或类名写错不会报错，只会静默退回默认色/默认对齐。
function cssRuleBody(css,selector){
  const at=css.indexOf(selector+'{');
  assert.notEqual(at,-1,'style.css 里找不到规则 '+selector);
  const open=at+selector.length+1;
  return css.slice(open,css.indexOf('}',open));
}
function numHeaderCells(html,tbodyId){
  const head=html.match(new RegExp('<thead><tr>(.*?)</tr></thead><tbody id="'+tbodyId+'">'));
  assert.ok(head,`找不到 ${tbodyId} 的表头`);
  return (head[1].match(/<th class="num">/g)||[]).length;
}

// 通道色这一族每条通道有十处落点：:root 的四行令牌（本色/ink/wash/lit）、两条谱线、
// 以及 .ch- .stroke- .fill- .sw- 四条规则。少写任何一处都不会报错——折线会整条不画
// （SVG stroke 默认 none）、折点变成黑点、图例色块和色点变成透明。
// 通道名单一律从 app.js 的 usageChannelOrder 现读：新增通道时测试名单会自己变长，
// 若在 style.css 漏了一处就立刻红。先前这里写死四条，autoclaw、trae 先后接入时
// 这条测试一直是绿的，缺陷一直留在页面上。
function channelList(js){
  const m=js.match(/const usageChannelOrder\s*=\s*\[([^\]]*)\]/);
  assert.ok(m,'app.js 里找不到 usageChannelOrder，通道名单没法现读（锚点写错了？）');
  const list=m[1].split(',').map(s=>s.trim().replace(/^['"]|['"]$/g,'')).filter(Boolean);
  assert.ok(list.includes('workbuddy'),'usageChannelOrder 解析结果异常：'+JSON.stringify(list));
  return list;
}
// 对象字面量的键也现读：页面上有哪几颗探测按钮，真源是 app.js 的 probeChannelNames。
function objectKeysFrom(js,name){
  const m=js.match(new RegExp('const '+name+'\\s*=\\s*\\{([^}]*)\\}'));
  assert.ok(m,'app.js 里找不到 const '+name+'，名单没法现读（锚点写错了？）');
  const keys=m[1].split(',').map(pair=>pair.split(':')[0].trim().replace(/^['"]|['"]$/g,'')).filter(Boolean);
  assert.ok(keys.length>0,'const '+name+' 解析出空名单');
  return keys;
}
// 对比度现算，不再抄注释：WCAG 相对亮度与比值。
function wcagLuminance(hex){
  const [r,g,b]=[1,3,5].map(i=>parseInt(hex.slice(i,i+2),16)/255)
    .map(v=>v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4));
  return 0.2126*r+0.7152*g+0.0722*b;
}
function contrastRatio(a,b){
  const [hi,lo]=[wcagLuminance(a),wcagLuminance(b)].sort((x,y)=>y-x);
  return (hi+0.05)/(lo+0.05);
}
function cssToken(css,name){
  const m=css.match(new RegExp('--'+name+':(#[0-9a-f]{6})'));
  assert.ok(m,'style.css 里读不到 --'+name+'（必须是六位小写十六进制）');
  return m[1];
}

test('通道色只有一处真源：app.js 不再自带调色板，每条通道的十处落点全部指向同一条 --ch-*',()=>{
  const js=readFileSync(__dirname+'/web/app.js','utf8');
  const css=readFileSync(__dirname+'/web/style.css','utf8');
  const channels=channelList(js);
  // 先自证算尺：纯黑白必须正好 21，两个接近的浅灰必须够不到 4.5，否则下面的门禁是假绿。
  assert.equal(Math.round(contrastRatio('#000000','#ffffff')*100)/100,21,'对比度算错了：纯黑白应当是 21');
  assert.ok(contrastRatio('#eeeeee','#ffffff')<1.3,'对比度算错了：两个浅灰不该接近');
  // 旧的临时亮色一旦按通道名写回脚本，改令牌就不会改到图上的线，图例和折线会分家。
  // （modelColors 那五条是「模型名次」的调色板，与通道无关，允许留在脚本里。）
  assert.ok(!new RegExp('('+channels.join('|')+')\\s*:\\s*[\'"]#[0-9a-f]{3,8}[\'"]','i').test(js),'app.js 里还留着「通道名 → 十六进制」的私有调色板');
  assert.ok(!js.includes('trendColors'),'trendColors 又回来了：折线色必须走 stroke-* 类');
  const sidebar=cssToken(css,'sidebar');
  for(const prop of ['--spectrum','--spectrum-v']){
    const decl=css.match(new RegExp('\\s+'+prop+':([^;]+);'));
    assert.ok(decl,'style.css 里读不到 '+prop);
    for(const channel of channels)assert.ok(decl[1].includes('var(--ch-'+channel+')'),prop+' 里少了 '+channel+' 这一束光，机架顶边会缺一截');
  }
  for(const channel of channels){
    for(const suffix of ['','-ink','-wash','-lit']){
      const defs=css.match(new RegExp('--ch-'+channel+suffix+':#[0-9a-f]{6}','g'))||[];
      assert.equal(defs.length,1,`--ch-${channel}${suffix} 在 :root 里定义了 ${defs.length} 次，改色要改的地方不止一处`);
    }
    for(const selector of ['.ch-'+channel,'.stroke-'+channel,'.fill-'+channel,'.sw-'+channel]){
      const body=cssRuleBody(css,selector);
      assert.ok(body.includes('var(--ch-'+channel+')'),selector+' 没有从 --ch-'+channel+' 取色');
    }
    // 文字版和图形版是两条令牌：本色只做描边和色点，够不够正文对比度都不许拿它当文字色。
    assert.ok(contrastRatio(cssToken(css,'ch-'+channel+'-ink'),cssToken(css,'ch-'+channel+'-wash'))>=4.5,
      `${channel} 的通道名（ink 落在自己的 wash 上）对比度不到 4.5:1`);
    assert.ok(contrastRatio(cssToken(css,'ch-'+channel+'-lit'),sidebar)>=4.5,
      `${channel} 的机架卡名（lit 落在 --sidebar 上）对比度不到 4.5:1`);
  }
});

test('通道归类：usageChannelOrder 里每条通道都能从账本 uid 认出自己，也都有译名',()=>{
  const channels=channelList(readFileSync(__dirname+'/web/app.js','utf8'));
  const {ctx}=taskFixture(async()=>({ok:true,status:200,json:async()=>({}),headers:{get:()=>null}}));
  for(const channel of channels){
    // 归类漏一条不会报错，那一路用量会静默塌进核心账号池：总数看着对，通道列里却找不到它。
    assert.equal(vm.runInContext(`usageChannelOf({uid:${JSON.stringify(channel)}})`,ctx),channel,
      `usageChannelOf 不认 uid=${channel}，它的用量会被算进核心账号池`);
    assert.ok(vm.runInContext(`typeof usageChannelNames[${JSON.stringify(channel)}]==='string'&&usageChannelNames[${JSON.stringify(channel)}].length>0`,ctx),
      `usageChannelNames 里缺 ${channel}，机架卡和表格的通道名会显示 undefined`);
  }
  // 客户端列的通道标签与通道名单同序同集合：漏一条，那一类旁路的客户端就永远不加前缀。
  assert.deepEqual(objectKeysFrom(readFileSync(__dirname+'/web/app.js','utf8'),'usageChannelTags'),channels,
    'usageChannelTags 的键与 usageChannelOrder 不同序（客户端列会漏前缀或留下孤儿标签）');
});

test('调用明细的客户端列：旁路通道前置通道标签认是谁报的，核心原样，空值不加前缀',async()=>{
  const items=[
    {ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0,client:'pi/0.78.2'},
    {ts:1001,uid:'trae',account:'Trae 通道',model:'trae-doubao',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0,client:'Hermes 0.18.0'},
    {ts:1002,uid:'zcode',account:'GLM 通道',model:'glm-5.2',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0,client:'WorkBuddy/2.8.0'},
    {ts:1003,uid:'qoder',account:'Qoder 通道',model:'qoder-x',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0},
  ];
  const usage=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items,summary:{}})}));
  await vm.runInContext('loadUsage()',usage.ctx);
  const rows=usage.get('usage-body').children;
  const clientOf=tr=>tr.children[10].textContent;
  // 明细最新在上：qoder（无 client）→ zcode → trae → 核心。
  assert.deepEqual(rows.map(clientOf),['—','GLM · WorkBuddy/2.8.0','Trae · Hermes 0.18.0','pi/0.78.2'],
    '客户端列没按通道加标签：旁路报的串看不出是谁，核心的 UA 反而被改了写');
  // 色点/类名不受影响：仍然只挂通道名那一格。
  assert.equal(rows[2].children[1].className,'ch-name ch-trae','客户端列的改动串到了通道列');
});

test('两条通道筛选下拉：选项正好等于 usageChannelOrder，新通道不许只加在脚本里',()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  const channels=channelList(readFileSync(__dirname+'/web/app.js','utf8'));
  for(const id of ['overview-usage-channel','usage-channel']){
    const block=html.match(new RegExp('<select id="'+id+'">([\\s\\S]*?)</select>'));
    assert.ok(block,'index.html 里找不到通道筛选下拉 '+id);
    const values=(block[1].match(/<option value="([^"]+)"[^>]*>/g)||[]).map(s=>s.replace(/^<option value="|"[^>]*>$/g,''));
    // 下拉是静态 HTML、名单在脚本里：新通道漏这一处，页面筛选就永远切不到它，且不会报错。
    assert.deepEqual(values,['all',...channels],'下拉 '+id+' 的选项与通道名单不一致');
  }
});

test('通道名单元格：文字是唯一的子节点，色点由 ::before 画；routes 的 core/glm 也认同一束光',()=>{
  const {ctx}=taskFixture(async()=>({ok:true,status:200,json:async()=>({}),headers:{get:()=>null}}));
  const shape=expr=>vm.runInContext(`(()=>{const td=${expr};return [td.className,td.textContent,String(td.children.length)].join('|')})()`,ctx);
  assert.equal(shape("channelCell('GLM（Zcode）','zcode')"),'ch-name ch-zcode|GLM（Zcode）|0');
  assert.equal(shape("channelCell('核心账号池','core')"),'ch-name ch-workbuddy|核心账号池|0','core 没翻译成 workbuddy 那束光');
  assert.equal(shape("channelCell('GLM 通道','glm')"),'ch-name ch-zcode|GLM 通道|0','glm 没翻译成 zcode 那束光');
  const css=readFileSync(__dirname+'/web/style.css','utf8');
  assert.match(cssRuleBody(css,'.ch-name'),/color:var\(--ch-ink\)/,'通道名没有走浅色底专用的 ink 版');
  assert.match(cssRuleBody(css,'.ch-name::before'),/background:var\(--ch\)/,'色点没有从 --ch 取色');
});

test('数值列：表头挂 num 的列数与单元格挂 num 的列数必须一致，右对齐才不会错开一列',async()=>{
  const html=readFileSync(__dirname+'/web/index.html','utf8');
  const items=[
    {ts:1000,uid:'u1',account:'alice',model:'cn:m',mode:'sync',prompt_tokens:100,completion_tokens:200,credit:1.5},
    {ts:1001,uid:'zcode',account:'GLM 通道',model:'glm-5.3',mode:'stream',prompt_tokens:10,completion_tokens:20,credit:null},
  ];
  const {ctx,get}=overviewFixture(items,{enabled:true,reachable:true,model_count:2});
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:true,qoder_enabled:false,opencode_enabled:false})",ctx);
  await vm.runInContext('loadZcode()',ctx);
  await vm.runInContext('loadOverview()',ctx);
  await new Promise(r=>setImmediate(r));
  const numOf=tr=>tr.children.filter(td=>String(td.className).split(' ').includes('num')).length;
  assert.ok(numHeaderCells(html,'overview-channels-body')>0,'通道总览表没有任何数值列表头');
  assert.equal(numOf(get('overview-channels-body').children[0]),numHeaderCells(html,'overview-channels-body'),'通道总览的数值单元格与表头不对齐');
  // 明细与分布是另一张页面、另一套列：翻页和筛选都不该改变对齐。
  const usage=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'today',items,summary:{}})}));
  await vm.runInContext('loadUsage()',usage.ctx);
  const detailRow=usage.get('usage-body').children[0];
  assert.equal(numOf(detailRow),numHeaderCells(html,'usage-body'),'调用明细的数值单元格与表头不对齐');
  // 明细最新在上（items 进来先 .slice().reverse()），第一行是 ts 更大的那条 zcode。
  assert.equal(detailRow.children[1].className,'ch-name ch-zcode','明细的通道列没认色');
  assert.equal(numOf(usage.get('usage-by-channel-body').children[0]),numHeaderCells(html,'usage-by-channel-body'),'通道分布的数值单元格与表头不对齐');
});

test('通道分布的占比条：分母是整个范围，10% 一档取整，最小一格不许画成空条',async()=>{
  const items=[];
  for(let i=0;i<8;i++)items.push({ts:1000+i,uid:'u'+i,account:'a',model:'cn:m',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0});
  for(let i=0;i<2;i++)items.push({ts:2000+i,uid:'zcode',account:'GLM 通道',model:'glm-5',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0});
  items.push({ts:3000,uid:'opencode',account:'OpenCode 通道',model:'oc-y',mode:'sync',prompt_tokens:1,completion_tokens:1,credit:0});
  const {ctx,get}=taskFixture(async()=>({ok:true,status:200,json:async()=>({range:'month',items,summary:{}})}));
  await vm.runInContext('loadUsage()',ctx);
  const rows=get('usage-by-channel-body').children;
  assert.deepEqual(rows.map(tr=>tr.className),['ch-workbuddy ch-band','ch-zcode ch-band','ch-opencode ch-band'],'分布行没有按通道认色');
  const barOf=tr=>tr.children[1].children[0].children[0].className;
  // 8/11 → 72.7% → 第七档；2/11 → 18.2% → 第二档；1/11 → 9.1% → 四舍五入到第一档而不是空条。
  assert.deepEqual(rows.map(barOf),['w7','w2','w1'],'占比条档位算错');
  assert.equal(rows[1].children[1].children[0].className,'share','占比条没挂进 .share 容器');
  // 通道筛选只切明细，分布仍是整个范围的全景：分母跟着变会让占比条失去意义。
  ctx.USAGE_ITEMS=items;
  vm.runInContext("usageChannel='zcode';renderUsage({items:USAGE_ITEMS})",ctx);
  assert.deepEqual(get('usage-by-channel-body').children.map(tr=>tr.children[1].children[0].children[0].className),['w7','w2','w1'],'筛选通道后占比条被改分了母');
  // 条子在右对齐的 .num 单元格里，块级元素不吃 text-align，得自己靠右，否则和读数错开一截。
  assert.match(cssRuleBody(readFileSync(__dirname+'/web/style.css','utf8'),'.share'),/margin:[^;}]*auto/,'占比条没有靠右，会落在数值格的左边缘');
});

test('机架面（暗底）用深底亮字那一套：五档徽章与通道名都有 lit 版，浅色 wash 不许出现在机架上',()=>{
  const css=readFileSync(__dirname+'/web/style.css','utf8');
  assert.match(cssRuleBody(css,'.channel-cards'),/background:var\(--sidebar\)/,'概览顶部没有落成机架暗底');
  assert.match(cssRuleBody(css,'.channel-cards::before'),/background:var\(--spectrum\)/,'机架顶沿缺谱线');
  assert.match(cssRuleBody(css,'.topbar::after'),/background:var\(--spectrum\)/,'顶栏下沿缺谱线');
  assert.match(cssRuleBody(css,'.sidebar::after'),/background:var\(--spectrum-v\)/,'侧栏右沿缺竖向谱线');
  assert.match(cssRuleBody(css,'.channel-card-name'),/color:var\(--ch-lit\)/,'机架上的通道名没用暗底专用的亮版');
  assert.match(cssRuleBody(css,'.channel-card-stats strong'),/color:#fff/,'机架读数不是白字，暗底上会读不清');
  for(const tone of ['ok','warn','bad','idle','unknown'])
    assert.ok(css.includes(`.badge.rail-state.${tone}{`),`缺 .badge.rail-state.${tone}，机架上的该档位会退回浅紫胶囊`);
});

function ccrFixture(statuses,linkBody={ui_url:'http://127.0.0.1:3458/?ccr_web_token=zzz'}) {
  const requests=[];let index=0;
  const response=(body,ok=true)=>({ok,status:ok?200:502,json:async()=>body});
  const {ctx,get,opened}=taskFixture(()=>{throw new Error('unused');});
  ctx.fetch=async(url,options={})=>{
    if(url==='/admin/ccr'){const body=statuses[Math.min(index,statuses.length-1)];index++;requests.push({url,method:'GET'});return response(body);}
    if(url==='/admin/ccr/gateway'){requests.push({url,method:'POST',body:options.body});return response(statuses[Math.min(index,statuses.length-1)]);}
    if(url==='/admin/ccr/link'){requests.push({url,method:'GET'});return response(linkBody);}
    if(url==='/admin/session')return new Promise(()=>{});
    if(url==='/admin/status')return response({total:0,healthy:0,cooling:0,disabled:0,accounts:[]});
    if(url==='/admin/models')return response({data:[]});
    throw new Error('unexpected '+url+' '+options.method);
  };
  get('realm').querySelector=()=>({disabled:false});
  return {ctx,get,opened,requests};
}

const ccrRunning={enabled:true,auth_ready:true,reachable:true,state:'running',endpoint:'http://127.0.0.1:3456',pid:455672,last_started_at:'2026-10-07T01:22:25.643Z'};
const ccrStopped={enabled:true,auth_ready:true,reachable:true,state:'stopped',endpoint:'http://127.0.0.1:3456'};
const ccrDown={enabled:true,auth_ready:true,reachable:false};
const ccrNoToken={enabled:true,auth_ready:false,reachable:false};
const ccrStaleToken={enabled:true,auth_ready:true,reachable:false,auth_mismatch:true};

test('CCR tab greys out and shows the disabled notice when the channel is off',async()=>{
  const {ctx,get}=ccrFixture([{enabled:false}]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,ccr_enabled:false})",ctx);
  assert.equal(get('nav-ccr').classList.toggled['nav-muted'],true,'disabled channel left the tab fully lit');
  await vm.runInContext('loadCCR()',ctx);
  assert.equal(get('ccr-disabled').hidden,false,'disabled notice stayed hidden');
  assert.equal(get('ccr-content').hidden,true,'enabled content showed while disabled');
});

test('CCR running state lights the badge and offers only the stop action',async()=>{
  const {ctx,get}=ccrFixture([ccrRunning]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,ccr_enabled:true})",ctx);
  assert.equal(get('nav-ccr').classList.toggled['nav-muted'],false,'enabled channel greyed the tab');
  await vm.runInContext('loadCCR()',ctx);
  assert.equal(get('ccr-content').hidden,false);
  assert.equal(get('ccr-status-badge').textContent,'运行中');
  assert.equal(get('ccr-status-badge').className,'badge ok');
  assert.match(get('ccr-status-text').textContent,/127\.0\.0\.1:3456/);
  assert.equal(get('ccr-start').disabled,true,'start offered while the gateway already runs');
  assert.equal(get('ccr-stop').disabled,false,'stop disabled while the gateway runs');
  assert.deepEqual(get('ccr-detail-body').children.map(tr=>tr.children[0].textContent),['网关地址','进程号','上次启动']);
});

test('CCR stopped state offers the start action and marks the badge warn',async()=>{
  const {ctx,get}=ccrFixture([ccrStopped]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,ccr_enabled:true})",ctx);
  await vm.runInContext('loadCCR()',ctx);
  assert.equal(get('ccr-status-badge').textContent,'已停止');
  assert.equal(get('ccr-status-badge').className,'badge warn');
  assert.equal(get('ccr-start').disabled,false,'start disabled while the gateway is stopped');
  assert.equal(get('ccr-stop').disabled,true,'stop offered while the gateway is stopped');
});

test('CCR admin service down locks both actions and names the host command',async()=>{
  const {ctx,get}=ccrFixture([ccrDown]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,ccr_enabled:true})",ctx);
  await vm.runInContext('loadCCR()',ctx);
  assert.equal(get('ccr-status-badge').textContent,'不可达');
  assert.equal(get('ccr-status-badge').className,'badge bad');
  assert.match(get('ccr-status-text').textContent,/ccr ui/,'hint must name what the admin has to run');
  assert.equal(get('ccr-start').disabled,true);
  assert.equal(get('ccr-stop').disabled,true);
});

test('CCR without a mounted token file asks for the mount instead of probing',async()=>{
  const {ctx,get}=ccrFixture([ccrNoToken]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,ccr_enabled:true})",ctx);
  await vm.runInContext('loadCCR()',ctx);
  assert.match(get('ccr-status-text').textContent,/WB2A_CCR_TOKEN_FILE/);
  assert.equal(get('ccr-start').disabled,true);
  assert.equal(get('ccr-stop').disabled,true);
});

test('CCR start posts once, refreshes the badge, and keeps buttons usable after failure',async()=>{
  const {ctx,get,requests}=ccrFixture([ccrStopped,ccrRunning]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,ccr_enabled:true})",ctx);
  await vm.runInContext('loadCCR()',ctx);
  get('ccr-start').handlers.click();
  get('ccr-start').handlers.click();
  await new Promise(r=>setTimeout(r,0));
  const posts=requests.filter(r=>r.url==='/admin/ccr/gateway');
  assert.equal(posts.length,1,'double click posted the start action twice');
  assert.equal(JSON.parse(posts[0].body).action,'start');
  assert.equal(get('ccr-status-badge').textContent,'运行中','badge did not follow the refreshed state');
  assert.equal(get('ccr-start').disabled,true);
  // 后端报错时按钮要回到可用，否则一次失败就把页面钉死。
  ctx.fetch=async(url,options={})=>({ok:false,status:502,json:async()=>({error:'CCR 模型网关关停失败'})});
  await get('ccr-stop').handlers.click();
  await new Promise(r=>setTimeout(r,0));
  assert.equal(get('notice').textContent,'CCR 模型网关关停失败');
  assert.equal(get('ccr-stop').disabled,false,'action stayed locked after a failure');
});

test('CCR management link is fetched only on click and opened in a new tab',async()=>{
  const {ctx,get,opened,requests}=ccrFixture([ccrRunning]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,ccr_enabled:true})",ctx);
  await vm.runInContext('loadCCR()',ctx);
  assert.equal(requests.some(r=>r.url==='/admin/ccr/link'),false,'management token reached the browser before the click');
  await get('ccr-link').handlers.click();
  assert.equal(requests.filter(r=>r.url==='/admin/ccr/link').length,1);
  assert.equal(opened[opened.length-1].location.href,'http://127.0.0.1:3458/?ccr_web_token=zzz');
});

test('CCR token mismatch is its own state, not a missing service',async()=>{
  const {ctx,get}=ccrFixture([ccrStaleToken]);
  await vm.runInContext("signedIn({csrf:'c',global_enabled:true,zcode_enabled:false,qoder_enabled:false,opencode_enabled:false,ccr_enabled:true})",ctx);
  await vm.runInContext('loadCCR()',ctx);
  assert.equal(get('ccr-status-badge').textContent,'Token 不匹配');
  assert.match(get('ccr-status-text').textContent,/force-recreate/,'hint must name the recreate command');
  assert.doesNotMatch(get('ccr-status-text').textContent,/ccr ui/,'stale token must not send the admin to ccr ui');
  assert.equal(get('ccr-start').disabled,true);
  assert.equal(get('ccr-stop').disabled,true);
});

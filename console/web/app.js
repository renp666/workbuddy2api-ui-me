const $ = (id) => document.getElementById(id);
let csrf = '', modelList = [], accounts = [], history = [], conversation = newConversation(), activeRequest, flowID, flowTimer;
// pinState：{cn:{uid,nickname,exists}|null, global:null|...}，null=该平台未锁定。
let pinState = {cn:null, global:null}, lastStatus = null;
let page = 'overview', sessionGeneration = 0;
let protocol = 'openai';
let taskState = {items:[],active_run:null,latest_runs:[]}, taskHistory = [], taskBefore = null, taskStarting = false, taskPollTimer, taskRenderKey, detailController, detailGeneration = 0;
let usageRange = 'today', usageGeneration = 0;
let zcodeModels = [], zcodeHistory = [], zcodeStatus = null, zcodePollTimer, zcodeBusy = false, zcodeAuthURL = '', zcodeLogoutArmed = false, zcodeProviderTouched = false, zcodePlanTouched = false, zcodeView = 'status', zcodeViewTouched = false, zcodeEnabled = false;
let qoderModels = [], qoderHistory = [], qoderStatus = null, qoderPollTimer, qoderBusy = false, qoderAuthURL = '', qoderLogoutArmed = false, qoderControl = false, qoderLoggedIn = false, qoderLoginTimer = null, qoderView = 'status', qoderViewTouched = false, qoderEnabled = false;
let selectedTaskRun, historyGeneration = 0, historyLoading = false, taskHistoryKey;
const taskIntents = new Map(), taskReads = new Set();
function newConversation() { return `web-${Date.now()}-${Math.random().toString(36).slice(2)}`; }
function notice(text = '') { $('notice').textContent = text; $('notice').hidden = !text; }
async function api(path, data, signal) {
 const generation = sessionGeneration;
 const response = await fetch('/admin/' + path, {method:data === undefined ? 'GET' : 'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:data === undefined ? undefined : JSON.stringify(data),signal});
 if (path !== 'logout' && generation !== sessionGeneration) throw new Error('管理会话已退出，请重新登录');
 if (response.status === 401 && path !== 'login' && path !== 'logout') { signedOut(); throw new Error('管理会话已过期，请重新登录'); }
 return response;
}
async function jsonAPI(path, data, signal) {
 const generation = sessionGeneration;
 const response = await api(path, data, signal); let result;
 try { result = await response.json(); } catch { throw new Error(`服务返回了非 JSON 响应（${response.status}）`); }
 if (path !== 'logout' && generation !== sessionGeneration) throw new Error('管理会话已退出，请重新登录');
 if (!response.ok) { const error=new Error(typeof result.error === 'string' ? result.error : result.error?.message || `请求失败（${response.status}）`);error.status=response.status;error.runID=result.run_id;throw error; }
 return result;
}
function signedOut() {
 sessionGeneration++;
 csrf = ''; activeRequest?.abort(); clearTimeout(flowTimer); flowID = undefined; history = []; conversation = newConversation(); stopTaskReads(); taskIntents.clear(); taskState={items:[],active_run:null,latest_runs:[]};taskHistory=[];taskBefore=null;taskStarting=false;taskRenderKey=undefined;
 $('messages').replaceChildren();$('task-list').replaceChildren();$('task-history-body').replaceChildren();$('task-detail').hidden=true;$('task-accounts').textContent='';$('task-log').textContent=''; $('api-key').value = ''; $('api-key').type = 'password'; $('admin-key').value = ''; $('console-view').hidden = true; $('login-view').hidden = false;
 protocol='openai';$('prompt').value='';$('max-tokens').value='1024';$('usage').textContent='用量将在上游返回后显示';usageGeneration++;renderAccess();
 zcodeModels=[];zcodeHistory=[];zcodeStatus=null;zcodeAuthURL='';zcodeLogoutArmed=false;zcodeBusy=false;zcodeProviderTouched=false;zcodePlanTouched=false;zcodeView='status';zcodeViewTouched=false;zcodeEnabled=false;clearTimeout(zcodePollTimer);zcodePollTimer=undefined;$('zcode-model').replaceChildren();$('zcode-messages').replaceChildren();$('zcode-prompt').value='';$('zcode-usage').textContent='用量将在上游返回后显示';$('zcode-disabled').hidden=true;$('zcode-content').hidden=true;$('nav-zcode').classList.remove('nav-muted');
 qoderModels=[];qoderHistory=[];qoderStatus=null;qoderEnabled=false;qoderControl=false;qoderLoggedIn=false;qoderAuthURL='';qoderBusy=false;qoderLogoutArmed=false;qoderView='status';qoderViewTouched=false;clearTimeout(qoderPollTimer);qoderPollTimer=undefined;clearTimeout(qoderLoginTimer);qoderLoginTimer=null;$('qoder-model').replaceChildren();$('qoder-messages').replaceChildren();$('qoder-prompt').value='';$('qoder-usage').textContent='用量将在上游返回后显示';$('qoder-disabled').hidden=true;$('qoder-content').hidden=true;$('nav-qoder').classList.remove('nav-muted');$('qoder-auth-row').hidden=true;$('qoder-pane-login').hidden=true;$('qoder-login-hint').textContent='';
 pinState={cn:null,global:null};lastStatus=null;renderPinBanner();
}
async function signedIn(session) {
 csrf = session.csrf; $('admin-key').value = ''; $('login-view').hidden = true; $('console-view').hidden = false;
 $('realm').querySelector('[value="global"]').disabled = !session.global_enabled;
 $('nav-zcode').classList.toggle('nav-muted', !session.zcode_enabled);
 zcodeEnabled = !!session.zcode_enabled;
 $('nav-qoder').classList.toggle('nav-muted', !session.qoder_enabled);
 qoderEnabled = !!session.qoder_enabled;
 qoderControl = !!session.qoder_control;
 await refreshStatus(); await refreshModels(); await loadPins();
}
function showPage(value) {
 if (page === 'tasks' && value !== 'tasks') stopTaskReads(true);
 if (page === 'zcode' && value !== 'zcode') stopZcodePoll();
 page = value;
 document.querySelectorAll('[data-page]').forEach(el => el.hidden = el.dataset.page !== page);
 document.querySelectorAll('.nav').forEach(el => el.classList.toggle('active', el.dataset.view === page));
 $('breadcrumb-name').textContent = {overview:'运行概览',accounts:'账号管理',tasks:'自动任务',usage:'调用统计',chat:'对话测试',zcode:'Zcode 通道',qoder:'Qoder 通道',access:'API 接入'}[page];
 if (page !== 'access') { $('api-key').value = ''; $('api-key').type = 'password'; }
 if (page === 'tasks') loadTaskPage();
 if (page === 'usage') loadUsage();
 if (page === 'zcode') loadZcode();
 if (page === 'qoder') loadQoder();
 // The access tab lists GLM and Qoder channel models too; fetch statuses once lazily
 // so its select is complete even when the operator never opens those tabs.
 if (page === 'access' && zcodeEnabled && !zcodeStatus) loadZcode();
 if (page === 'access' && qoderEnabled && !qoderStatus) loadQoder();
}
document.querySelectorAll('[data-view]').forEach(button => button.addEventListener('click', () => showPage(button.dataset.view)));
$('login-form').addEventListener('submit', async event => {
 event.preventDefault(); const button = event.submitter; button.disabled = true; $('login-error').textContent = '';
 try { const session = await jsonAPI('login', {key:$('admin-key').value}); await signedIn(session); } catch(error) { $('login-error').textContent = error.message; } finally { button.disabled = false; }
});
$('logout').addEventListener('click', async () => {$('login-error').textContent='';const pending=jsonAPI('logout', {});signedOut();try {await pending;} catch(e){$('login-error').textContent=e.message;} });
function cell(text, small) { const td = document.createElement('td'); td.textContent = text; if (small) { const s=document.createElement('small');s.textContent=small;td.append(s); } return td; }
function renderAccounts(data) {
 lastStatus = data;
 accounts = data.accounts || [];
 $('count-total').textContent = data.total; $('count-healthy').textContent = data.healthy; $('count-limited').textContent = `${data.cooling} / ${data.disabled}`;
 $('count-flight').textContent = accounts.reduce((n,a) => n + a.in_flight, 0);
 $('service-state').textContent = data.total ? '网关运行中' : '运行中 · 等待添加账号';
 $('welcome-title').textContent = data.total ? '你的网关已连接账号' : '添加第一个账号';
 $('welcome-text').textContent = data.total ? '检查账号状态，或发出一个问题来验证模型当前的响应。' : '在浏览器中完成授权，网关会自动保存并加载账号。';
 $('accounts-body').replaceChildren(); $('accounts-empty').hidden = accounts.length > 0;
 for (const a of accounts) {
  const tr=document.createElement('tr');const state=a.disabled?'已禁用':a.cooling?'冷却中':'可用';
  const status=cell('');const badge=document.createElement('span');badge.className='badge'+(state==='可用'?'':' warn');badge.textContent=state;status.append(badge);
  const limits=(a.rate_limited_models||[]).map(m => `${m.model} 至 ${new Date(m.until).toLocaleString()}`).join('；');
  tr.append(cell(a.nickname||a.uid,`${a.realm === 'global'?'国际版':'国内版'} · ${a.uid}`),status,cell(a.credits_known ? String(a.credits) : a.credits>0 ? `${a.credits}（历史）` : '待确认'),cell(`${a.success_count||0} / ${a.err_total||0}`),cell(String(a.in_flight)),cell(limits||a.disabled_reason||a.reason||'—', a.cool_remaining_sec ? `约 ${Math.ceil(a.cool_remaining_sec/60)} 分钟后恢复` : ''), pinOpCell(a));
  $('accounts-body').append(tr);
 }
 updateModelHint();
}
// pinOpCell 构造账号行的锁定操作单元格：同平台锁定号显示「已锁定 + 解锁」，
// 其余账号显示「锁定」。realm 以账号实际归属为准（防 pinState 与行错配）。
function pinOpCell(a) {
 const td = document.createElement('td');
 const realm = a.realm === 'global' ? 'global' : 'cn';
 const pinned = pinState[realm];
 const btn = document.createElement('button');
 btn.className = 'secondary pin-btn';
 if (pinned && pinned.uid === a.uid) {
  const tag = document.createElement('span');
  tag.className = 'badge pin-tag';
  tag.textContent = '已锁定';
  btn.textContent = '解锁';
  btn.dataset.action = 'unpin';
  btn.dataset.realm = realm;
  td.append(tag, ' ', btn);
 } else {
  btn.textContent = '锁定';
  btn.dataset.action = 'pin';
  btn.dataset.uid = a.uid;
  btn.dataset.realm = realm;
  // 同平台已锁定其他账号时，按钮仍可用（锁定此号即替换），title 说明语义。
  if (pinned) btn.title = `将替换当前锁定的账号（${pinned.nickname || pinned.uid}）`;
  td.append(btn);
 }
 return td;
}
async function loadPins() {
 try { pinState = (await jsonAPI('pin')).pins || {cn:null, global:null}; }
 catch { pinState = {cn:null, global:null}; } // 旧核心无端点时降级为无锁定 UI
 renderPinBanner();
 if (lastStatus) renderAccounts(lastStatus);
}
function renderPinBanner() {
 const banner = $('pin-banner');
 banner.replaceChildren();
 const realms = ['cn','global'].filter(r => pinState[r]);
 banner.hidden = realms.length === 0;
 for (const realm of realms) {
  const p = pinState[realm];
  const row = document.createElement('div');
  row.className = 'pin-row';
  const label = document.createElement('span');
  label.textContent = `🔒 ${realmLabel(realm)}消费已锁定到「${p.nickname || p.uid}」——该平台对话请求只会使用此账号，账号不可用或请求失败时直接报错，不会切换其他账号。` + (p.exists ? '' : ' 注意：该账号已不在账号池，请解锁或改锁其他账号。');
  const btn = document.createElement('button');
  btn.className = 'secondary pin-btn';
  btn.textContent = '解锁';
  btn.dataset.action = 'unpin';
  btn.dataset.realm = realm;
  row.append(label, btn);
  banner.append(row);
 }
}
async function pinAction(btn) {
 const action = btn.dataset.action;
 btn.disabled = true;
 try {
  if (action === 'pin') {
   const r = await jsonAPI('pin', {uid: btn.dataset.uid});
   await loadPins();
   notice(`${realmLabel(r.realm)}消费已锁定到该账号`);
  } else {
   await jsonAPI('unpin', {realm: btn.dataset.realm});
   await loadPins();
   notice(`已解除${realmLabel(btn.dataset.realm)}账号锁定，恢复自动选号`);
  }
 } catch(e) {
  notice(e.message);
  btn.disabled = false;
 }
}
document.addEventListener?.('click', e => {
 const btn = e.target.closest?.('button[data-action="pin"], button[data-action="unpin"]');
 if (btn) return pinAction(btn);
});
async function refreshStatus() { try { renderAccounts(await jsonAPI('status')); } catch(e) { notice(e.message); } }
function realmLabel(realm) { return realm === 'global' ? '国际版' : realm === 'cn' ? '国内版' : realm === 'glm' ? 'GLM·智谱' : realm === 'qoder' ? 'Qoder' : ''; }
function modelRealm(model) { return model.realm || (model.id.startsWith('global:') ? 'global' : model.id.startsWith('glm-') ? 'glm' : model.id.startsWith('qoder-') ? 'qoder' : 'cn'); }
function modelOption(model) { const label = realmLabel(modelRealm(model)); const n = Array.isArray(model.accounts) ? model.accounts.length : 0; return `${label ? `${label} · ` : ''}${model.id}${n > 1 ? ` · ${n}账号` : ''}`; }
// accessModelOptions feeds the API-access select: core models plus the GLM
// channel models (the public /v1/models merges them, but /admin/models only
// covers core), so the access tab shows every model a client can actually call.
function accessModelOptions() {
 const items = modelList.map(m => ({...m}));
 for (const id of zcodeModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'glm'});
 for (const id of qoderModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'qoder'});
 return items;
}
function renderAccessModelSelect() {
 const previous = $('access-model').value;
 const items = accessModelOptions();
 $('access-model').replaceChildren();
 for (const model of items) $('access-model').append(new Option(modelOption(model), model.id));
 $('access-model').value = items.some(m => m.id === previous) ? previous : $('model').value;
}
async function refreshModels() {
 try {
  const models=(await jsonAPI('models')).data||[];if(activeRequest)return;
  const previous=$('model').value;modelList=models;$('model').replaceChildren();
  for (const model of modelList) { $('model').append(new Option(modelOption(model),model.id)); }
  if (modelList.some(m=>m.id===previous)) $('model').value=previous;
  else {const available=modelList.find(m=>accounts.some(a=>m.id.startsWith(`${a.realm||'cn'}:`)));if(available)$('model').value=available.id;}
  renderAccessModelSelect();
  updateEfforts();renderAccess();
 } catch(e) { notice(e.message); }
}
function updateModelHint() { const realm=$('model').value.startsWith('global:')?'global':'cn'; $('model-hint').textContent=accounts.some(a=>(a.realm||'cn')===realm&&!a.disabled)?'模型列表可能包含静态候选；是否可调用以实际回答为准。':'当前没有此版本的已启用账号，请先添加对应账号。'; }
function updateEfforts() { const model=modelList.find(m=>m.id===$('model').value),previous=$('effort').value;$('effort').replaceChildren(new Option('默认',''));for(const e of model?.reasoning_supported_efforts||[])$('effort').append(new Option(e,e));$('effort').value=model?.reasoning_supported_efforts?.includes(previous)?previous:'';$('effort').disabled=!!activeRequest||protocol==='anthropic'||!model?.reasoning_supported_efforts?.length;updateModelHint(); }
$('model').addEventListener('change',updateEfforts);
$('refresh').addEventListener('click',async()=>{await refreshStatus();await refreshModels();});
setInterval(()=>{if(csrf&&!document.hidden){refreshStatus();void loadPins();}},15000);

const taskNames={checkin:'签到',travel:'猫猫旅行',activity:'活跃上报',keepalive:'Token 保活',school:'开学季',cat:'夜猫子'};
const runStatuses={running:'运行中',success:'成功',partial_failure:'部分失败',failed:'失败',skipped:'已跳过',interrupted:'已中断（结果未确认）',unknown:'结果未确认'};
function formatTaskReward(value){return value===null||value===undefined?'未确认':String(value);}
function formatTaskAvailability(task){return task.enabled===false?'已禁用':task.next_at?'已启用':'暂不可用 / 未知';}
function formatRunStatus(status){return runStatuses[status]||'结果未确认';}
function formatTaskTime(value){if(!value)return '未知';const date=new Date(value);return Number.isNaN(date.getTime())?'未知':date.toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false});}
function createTaskRequestID(){
 if(typeof crypto.randomUUID==='function')return crypto.randomUUID();
 const bytes=new Uint8Array(16);crypto.getRandomValues(bytes);return Array.from(bytes,byte=>byte.toString(16).padStart(2,'0')).join('');
}
function cancelTaskDetail(hide=false){detailGeneration++;detailController?.abort();detailController=undefined;if(hide){selectedTaskRun=undefined;$('task-detail').hidden=true;}}
function stopTaskReads(hideDetail=false){clearTimeout(taskPollTimer);taskPollTimer=undefined;historyGeneration++;historyLoading=false;for(const controller of taskReads)controller.abort();taskReads.clear();cancelTaskDetail(hideDetail);}
async function taskJSON(path){
 const controller=new AbortController();taskReads.add(controller);
 try{return await jsonAPI(path,undefined,controller.signal);}finally{taskReads.delete(controller);}
}
function taskPageVisible(){return page==='tasks'&&csrf&&!document.hidden;}
function scheduleTaskPoll(){
 clearTimeout(taskPollTimer);taskPollTimer=undefined;
 if(taskPageVisible())taskPollTimer=setTimeout(pollTaskRun,2000);
}
function scheduleTaskRetry(){clearTimeout(taskPollTimer);taskPollTimer=taskPageVisible()?setTimeout(loadTaskState,2000):undefined;}
async function loadTaskState(schedule=true){
 if(!taskPageVisible())return;
 try{taskState=await taskJSON('tasks');renderTasks();$('task-live').textContent=taskState.active_run?'后台任务正在运行，页面将自动刷新。':'任务状态已刷新。';if(schedule)scheduleTaskPoll();return true;}
 catch(error){if(error.name!=='AbortError'){$('task-live').textContent=error.message;notice(error.message);if(schedule)scheduleTaskRetry();}}
}
function taskRunKey(){return JSON.stringify([taskState.active_run,taskState.latest_runs]);}
async function loadTaskHistory(reset=false,refresh=false){
 if(!taskPageVisible())return;
 if(reset){historyGeneration++;historyLoading=false;}
 if(historyLoading||(!reset&&!refresh&&!taskBefore))return;
 const generation=historyGeneration,key=taskRunKey();historyLoading=true;$('task-more').disabled=true;
 const path='task-runs?limit=20'+(!reset&&!refresh&&taskBefore?'&before='+encodeURIComponent(taskBefore):'');
 try{
  const result=await taskJSON(path);if(generation!==historyGeneration||!taskPageVisible())return;
  if(refresh){const ids=new Set(result.items.map(run=>run.id));if(!taskHistory.length)taskBefore=result.next_before;taskHistory=[...result.items,...taskHistory.filter(run=>!ids.has(run.id))];}
  else{taskHistory=reset?result.items:[...taskHistory,...result.items];taskBefore=result.next_before;}
  if(reset||refresh)taskHistoryKey=key;renderTaskHistory();
 }catch(error){if(generation===historyGeneration&&error.name!=='AbortError')notice(error.message);}
 finally{if(generation===historyGeneration){historyLoading=false;$('task-more').disabled=false;}}
}
async function loadTaskPage(){
 stopTaskReads();taskHistory=[];taskBefore=null;renderTaskHistory();await Promise.all([loadTaskState(),loadTaskHistory(true)]);
}
function renderTasks(){
 const key=JSON.stringify([taskState,taskStarting,[...taskIntents.keys()]]);if(key===taskRenderKey)return;taskRenderKey=key;
 const list=$('task-list');list.replaceChildren();const active=taskState.active_run;
 for(const task of taskState.items||[]){
  const card=document.createElement('article');card.className='panel task-card';
  const heading=document.createElement('div');heading.className='task-card-heading';const title=document.createElement('h3');title.textContent=taskNames[task.id]||task.id;const badge=document.createElement('span');badge.className='badge'+(task.enabled===false?' warn':'');badge.textContent=formatTaskAvailability(task);heading.append(title,badge);
  const hours=document.createElement('p');hours.className='muted small';hours.textContent=`配置时间：${(task.hours||[]).map(hour=>String(hour).padStart(2,'0')+':00').join('、')||'未知'} · ${task.timezone==='Asia/Shanghai'?'北京时间':task.timezone||'时区未知'}`;
  const next=document.createElement('p');next.className='small';next.textContent=task.enabled===false?'下次执行：已禁用':`下次执行：${task.next_at?formatTaskTime(task.next_at):'暂不可用 / 未知'}`;
  const latest=(taskState.latest_runs||[]).find(run=>run.task_id===task.id);const summary=document.createElement('p');summary.className='muted small';summary.textContent=active?.task_id===task.id?`当前：${formatRunStatus(active.status)}`:latest?`最近：${formatRunStatus(latest.status)} · ${formatTaskTime(latest.started_at)}`:'最近：暂无记录';
  const actions=document.createElement('div');actions.className='task-actions';const run=document.createElement('button');run.className='primary';run.textContent=active?.task_id===task.id?'运行中':taskIntents.has(task.id)?'重试同一操作':'立即执行';run.disabled=task.enabled===false||taskStarting||!!active;run.addEventListener('click',()=>triggerTask(task.id));actions.append(run);
  if(latest){const detail=document.createElement('button');detail.className='secondary';detail.textContent='查看记录';detail.addEventListener('click',()=>loadTaskDetail(latest.id));actions.append(detail);}
  card.append(heading,hours,next,summary,actions);list.append(card);
 }
}
async function triggerTask(taskID){
 const task=(taskState.items||[]).find(item=>item.id===taskID);if(taskStarting||taskState.active_run||!task||task.enabled===false)return;
 const generation=sessionGeneration;taskStarting=true;const requestID=taskIntents.get(taskID)||createTaskRequestID();taskIntents.set(taskID,requestID);renderTasks();notice('');
 try{
  const run=await jsonAPI('tasks/'+encodeURIComponent(taskID)+'/runs',{request_id:requestID});if(generation!==sessionGeneration)return;taskIntents.delete(taskID);taskState.active_run=run;renderTaskDetail(run);await Promise.all([loadTaskState(),loadTaskHistory(true)]);
 }catch(error){if(generation!==sessionGeneration)return;if(error.status===409&&error.runID){taskIntents.delete(taskID);await loadTaskDetail(error.runID);await loadTaskState();}else notice(error.message+'；再次点击会沿用同一请求标识。');}
 finally{if(generation===sessionGeneration){taskStarting=false;renderTasks();}}
}
async function pollTaskRun(){
 if(!taskPageVisible())return;
 clearTimeout(taskPollTimer);taskPollTimer=undefined;
 try{
  if(!await loadTaskState(false))return;
  if(selectedTaskRun&&!$('task-detail').hidden)await loadTaskDetail(selectedTaskRun,false);
  if(taskHistoryKey!==taskRunKey())await loadTaskHistory(false,true);
 }finally{scheduleTaskPoll();}
}
function appendHistoryCell(row,text){const td=document.createElement('td');td.textContent=text;row.append(td);}
function renderTaskHistory(){
 const body=$('task-history-body');body.replaceChildren();$('task-history-empty').hidden=taskHistory.length>0;$('task-more').hidden=!taskBefore;
 for(const run of taskHistory){const row=document.createElement('tr');appendHistoryCell(row,taskNames[run.task_id]||run.task_id);appendHistoryCell(row,formatRunStatus(run.status));appendHistoryCell(row,formatTaskTime(run.started_at));appendHistoryCell(row,run.source==='scheduled'?'计划':'手动');const action=document.createElement('td');const button=document.createElement('button');button.className='quiet';button.textContent='查看详情';button.addEventListener('click',()=>loadTaskDetail(run.id));action.append(button);row.append(action);body.append(row);}
}
function formatBalance(balance){return balance?`余额 ${balance.value}（观测于 ${formatTaskTime(balance.observed_at)}）`:'余额未观测';}
function renderTaskDetail(run,scroll=true){
 selectedTaskRun=run.id;
 $('task-detail').hidden=false;$('task-detail-title').textContent=`${taskNames[run.task_id]||run.task_id} · ${formatRunStatus(run.status)}`;
 const finish=run.status==='interrupted'?`观察时间：${formatTaskTime(run.finished_at)}（不代表真实业务结束）`:`结束时间：${formatTaskTime(run.finished_at)}`;
 $('task-detail-meta').textContent=`开始时间：${formatTaskTime(run.started_at)} · ${finish} · 耗时：${run.duration_ms===null||run.duration_ms===undefined?'未知':run.duration_ms+' ms'}`;
 $('task-accounts').textContent=(run.accounts||[]).map(account=>`${account.uid} · ${formatRunStatus(account.status)} · ${account.detail||'无补充说明'}\n${formatBalance(account.before)} → ${formatBalance(account.after)} · 已确认奖励：${formatTaskReward(account.reward)}`).join('\n\n')||'没有账号结果。';
 $('task-log').textContent=run.log||'没有日志摘要。';$('task-log-truncated').hidden=!run.log_truncated;if(scroll)$('task-detail').scrollIntoView({block:'nearest'});
}
async function loadTaskDetail(id,scroll=true){
 if(!scroll&&(detailController||selectedTaskRun!==id||$('task-detail').hidden))return;
 selectedTaskRun=id;
 cancelTaskDetail();const generation=detailGeneration;const controller=new AbortController();detailController=controller;taskReads.add(controller);
 try{const run=await jsonAPI('task-runs/'+encodeURIComponent(id),undefined,controller.signal);if(generation===detailGeneration&&selectedTaskRun===id&&taskPageVisible())renderTaskDetail(run,scroll);}
 catch(error){if(generation===detailGeneration&&error.name!=='AbortError')notice(error.message);}
 finally{taskReads.delete(controller);if(detailController===controller)detailController=undefined;}
}
$('task-refresh').addEventListener('click',loadTaskPage);
$('task-more').addEventListener('click',()=>loadTaskHistory(false));
$('task-detail-close').addEventListener('click',()=>cancelTaskDetail(true));
document.addEventListener?.('visibilitychange',()=>{if(document.hidden)stopTaskReads();else if(page==='tasks')pollTaskRun();else if(page==='zcode'&&zcodeStatus&&zcodeStatus.control&&!zcodeStatus.logged_in)loadZcode();else if(page==='qoder'&&qoderStatus&&qoderStatus.control&&!qoderStatus.logged_in)loadQoder();});

const usageModeNames={stream:'流式',sync:'同步'};
function formatUsageTokens(value){return value<0?'—':String(value);}
function formatUsageCredit(value){return value==null||value===undefined?'—':String(value);}
function renderUsage(data){
 const items=(data.items||[]).slice().reverse(),summary=data.summary||{calls:0,prompt_tokens:0,completion_tokens:0,credit:0,credit_missing:0,usage_missing:0};
 $('usage-calls').textContent=String(summary.calls);
 $('usage-tokens').textContent=String(summary.prompt_tokens+summary.completion_tokens);
 $('usage-credit').textContent=String(summary.credit);
 const notes=[];
 if(summary.usage_missing)notes.push(`${summary.usage_missing} 条记录未回报 token 用量，明细按「—」展示`);
 if(summary.credit_missing)notes.push(`${summary.credit_missing} 条记录未回报积分扣费，未计入合计`);
 $('usage-note').textContent=notes.join('；');$('usage-note').hidden=!notes.length;
 const body=$('usage-body');body.replaceChildren();$('usage-empty').hidden=items.length>0;
 for(const e of items){
  const tr=document.createElement('tr');
  tr.append(cell(formatTaskTime(e.ts*1000)),cell(e.account||e.uid||'—'),cell(e.model||'—'),cell(usageModeNames[e.mode]||e.mode||'—'),cell(formatUsageTokens(e.prompt_tokens)),cell(formatUsageTokens(e.completion_tokens)),cell(formatUsageCredit(e.credit)));
  body.append(tr);
 }
}
async function loadUsage(){
 if(!csrf)return;
 const generation=usageGeneration;
 try{const data=await jsonAPI('usage?range='+encodeURIComponent(usageRange));if(generation===usageGeneration)renderUsage(data);}
 catch(error){if(generation===usageGeneration)notice(error.message);}
}
$('usage-range').addEventListener('change',event=>{usageRange=event.target.value;usageGeneration++;loadUsage();});
$('usage-refresh').addEventListener('click',()=>{usageGeneration++;loadUsage();});

async function loadZcode(){
 if(!csrf)return;
 try{renderZcode(await jsonAPI('zcode'));}
 catch(error){notice(error.message);}
}
function zcodePageVisible(){return page==='zcode'&&csrf&&!document.hidden;}
function scheduleZcodePoll(){
 clearTimeout(zcodePollTimer);zcodePollTimer=undefined;
 if(zcodePageVisible()&&zcodeStatus&&zcodeStatus.control&&!zcodeStatus.logged_in)zcodePollTimer=setTimeout(loadZcode,3000);
}
function stopZcodePoll(){clearTimeout(zcodePollTimer);zcodePollTimer=undefined;}
function renderZcode(status){
 zcodeStatus=status;
 const enabled=!!status.enabled;
 $('zcode-disabled').hidden=enabled;
 $('zcode-content').hidden=!enabled;
 if(!enabled){stopZcodePoll();return;}
 const control=!!status.control,loggedIn=!!status.logged_in,running=!!status.proxy_running;
 if(control&&loggedIn)zcodeAuthURL='';
 const badge=$('zcode-status-badge');
 if(!control){
  badge.textContent=status.reachable?'在线':'不可达';
  badge.className='badge'+(status.reachable?'':' warn');
  $('zcode-status-text').textContent=status.reachable?`zcode-proxy 可达 · ${status.model_count} 个 GLM 模型`:'zcode-proxy 当前不可达，请确认容器已启动。';
 } else if(!loggedIn){
  badge.textContent='未登录';badge.className='badge warn';
  $('zcode-status-text').textContent='尚未保存 GLM 凭据，切换到「GLM 登录」完成登录后即可启用通道。';
 } else if(running){
  badge.textContent=status.reachable?'在线':'运行中 · 探测失败';
  badge.className='badge'+(status.reachable?'':' warn');
  $('zcode-status-text').textContent=status.reachable?`zcode-proxy 可达 · ${status.model_count} 个 GLM 模型`:'代理已启动，但模型探测失败，可能是上游凭据或额度异常。';
 } else {
  badge.textContent='已登录 · 未启用';badge.className='badge warn';
  $('zcode-status-text').textContent='凭据已保存，点击「启用通道」开始服务。';
 }
 $('zcode-login-badge').textContent='未登录';
 $('zcode-enable').hidden=!control||!loggedIn||running;
 $('zcode-disable').hidden=!control||!running;
 $('zcode-logout').hidden=!control||!loggedIn;
 $('zcode-logout').textContent=zcodeLogoutArmed?'再次点击确认退出':'退出登录';
 $('zcode-auth-row').hidden=!zcodeAuthURL;
 if(zcodeAuthURL){$('zcode-auth-link').href=zcodeAuthURL;$('zcode-auth-link').textContent=zcodeAuthURL;}
 $('zcode-login-hint').textContent=!control||loggedIn?'':zcodeAuthURL?'已在新标签页打开授权页面，完成授权后本页每 3 秒自动检查登录结果；若被拦截请用下方链接打开。':'选择服务商后点击「开始登录」，授权在智谱页面完成，凭据只保存在 GLM 容器内。';
 if(!zcodeProviderTouched&&(status.provider==='zai'||status.provider==='bigmodel'))$('zcode-provider').value=status.provider;
 const plan=(status.plan==='coding-plan'||status.plan==='start-plan')?status.plan:'';
 if(!zcodePlanTouched&&plan)$('zcode-plan').value=plan;
 $('zcode-config-form').hidden=!control||running;
 const loginTab=control&&!loggedIn;
 const dataTab=control?running:status.reachable;
 if(loginTab&&!zcodeViewTouched)zcodeView='login';
 if(zcodeView==='login'&&!loginTab)zcodeView='status';
 if((zcodeView==='models'||zcodeView==='chat')&&!dataTab)zcodeView='status';
 $('zcode-view-status').hidden=false;
 $('zcode-view-login').hidden=!loginTab;
 $('zcode-view-models').hidden=!dataTab;
 $('zcode-view-chat').hidden=!dataTab;
 for(const view of ['status','login','models','chat']){
  $('zcode-view-'+view).classList.toggle('active',view===zcodeView);
  $('zcode-pane-'+view).hidden=view!==zcodeView;
 }
 zcodeModels=(status.models||[]).map(item=>item.id).filter(Boolean);
 // Keep the API-access select in sync: it lists GLM channel models too.
 renderAccessModelSelect();renderAccess();
 const previous=$('zcode-model').value;
 $('zcode-model').replaceChildren();
 for(const id of zcodeModels)$('zcode-model').append(new Option(id,id));
 if(zcodeModels.includes(previous))$('zcode-model').value=previous;
 $('zcode-models-body').replaceChildren();
 for(const id of zcodeModels){const tr=document.createElement('tr');tr.append(cell(id));$('zcode-models-body').append(tr);}
 $('zcode-models-empty').hidden=zcodeModels.length>0;
 $('zcode-models-note').hidden=plan!=='start-plan';
 $('zcode-model').disabled=!zcodeModels.length||!!activeRequest;
 $('zcode-send').disabled=!zcodeModels.length||!!activeRequest;
 zcodeActionControls();
 scheduleZcodePoll();
}
function zcodeControls(){
 for(const id of ['zcode-model','zcode-prompt','zcode-send'])$(id).disabled=!!activeRequest;
 $('zcode-stop').hidden=!activeRequest;
}
function zcodeActionControls(){
 for(const id of ['zcode-login-start','zcode-enable','zcode-disable','zcode-logout','zcode-refresh','zcode-config-apply'])$(id).disabled=zcodeBusy;
}
async function zcodeAction(path){
 if(zcodeBusy)return;
 const generation=sessionGeneration;zcodeBusy=true;zcodeActionControls();notice('');
 try{
  await jsonAPI(path,{});
  if(generation!==sessionGeneration)return;
  zcodeAuthURL='';zcodeLogoutArmed=false;
  await loadZcode();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){zcodeBusy=false;zcodeActionControls();}}
}
$('zcode-refresh').addEventListener('click',loadZcode);
for(const view of ['status','login','models','chat'])$('zcode-view-'+view).addEventListener('click',()=>{zcodeViewTouched=true;setZcodeView(view);});
function setZcodeView(view){zcodeView=view;if(zcodeStatus)renderZcode(zcodeStatus);}
$('zcode-provider').addEventListener('change',()=>{zcodeProviderTouched=true;});
$('zcode-login-form').addEventListener('submit',async event=>{
 event.preventDefault();if(zcodeBusy)return;
 const generation=sessionGeneration;zcodeBusy=true;zcodeActionControls();notice('');
 zcodeProviderTouched=true;
 let authWindow=null;
 try{authWindow=window.open('about:blank','_blank');if(authWindow)authWindow.opener=null;}catch(_){authWindow=null;}
 try{
  const result=await jsonAPI('zcode/login',{provider:$('zcode-provider').value});
  if(generation!==sessionGeneration){try{authWindow?.close();}catch(_){}return;}
  zcodeAuthURL=result.authorize_url||'';
  if(zcodeAuthURL){
   if(authWindow&&!authWindow.closed)authWindow.location.href=zcodeAuthURL;
   else try{window.open(zcodeAuthURL,'_blank','noopener');}catch(_){}
  }
  renderZcode(zcodeStatus);
 }catch(error){
  try{authWindow?.close();}catch(_){}
  if(generation===sessionGeneration)notice(error.message);
 }
 finally{if(generation===sessionGeneration){zcodeBusy=false;zcodeActionControls();}}
});
$('zcode-enable').addEventListener('click',()=>zcodeAction('zcode/enable'));
$('zcode-plan').addEventListener('change',()=>{zcodePlanTouched=true;});
$('zcode-config-form').addEventListener('submit',async event=>{
 event.preventDefault();if(zcodeBusy)return;
 const generation=sessionGeneration;zcodeBusy=true;zcodeActionControls();notice('');
 try{
  await jsonAPI('zcode/config',{plan:$('zcode-plan').value});
  if(generation!==sessionGeneration)return;
  zcodePlanTouched=false;
  await loadZcode();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){zcodeBusy=false;zcodeActionControls();}}
});
$('zcode-disable').addEventListener('click',()=>zcodeAction('zcode/disable'));
$('zcode-logout').addEventListener('click',()=>{
 if(!zcodeLogoutArmed){zcodeLogoutArmed=true;$('zcode-logout').textContent='再次点击确认退出';return;}
 zcodeLogoutArmed=false;zcodeAction('zcode/logout');
});
$('zcode-stop').addEventListener('click',()=>activeRequest?.abort());
$('zcode-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('zcode-prompt').value.trim();if(!text||!$('zcode-model').value)return;
 const generation=sessionGeneration;
 notice('');message('user',text,$('zcode-messages'));$('zcode-prompt').value='';const answer=message('assistant','正在等待模型…',$('zcode-messages'));
 const controller=new AbortController();activeRequest=controller;zcodeControls();
 let content='',usage,finished=false;const outgoing=[...zcodeHistory,{role:'user',content:text}];
 try{
  const response=await api('zcode/chat',{model:$('zcode-model').value,messages:outgoing,stream:true},controller.signal);
  if(!response.ok){const err=await response.json();throw new Error(err.error?.message||err.error||`HTTP ${response.status}`);}
  const reader=response.body.getReader(),decoder=new TextDecoder();let buffer='';
  const processFrame=frame=>{
   const payload=frame.split('\n').filter(l=>l.startsWith('data:')).map(l=>l.slice(5).trimStart()).join('\n');if(!payload)return;
   if(payload==='[DONE]'){finished=true;return;}
   const part=JSON.parse(payload);if(part.error)throw new Error(part.error.message||'上游流式响应发生错误');
   content+=part.choices?.[0]?.delta?.content||'';answer.content.textContent=content||'模型正在思考…';
   if(part.usage)usage=part.usage;
  };
  while(true){const{value,done}=await reader.read();if(controller.signal.aborted){const error=new Error('已停止生成');error.name='AbortError';throw error;}buffer+=done?decoder.decode():decoder.decode(value,{stream:true});buffer=buffer.replace(/\r\n/g,'\n');let split;while((split=buffer.indexOf('\n\n'))>=0){processFrame(buffer.slice(0,split));buffer=buffer.slice(split+2);}if(done){if(buffer.trim())processFrame(buffer);break;}}
  if(!finished)throw new Error('响应提前中断，可重新发送问题');
  if(!content)answer.content.textContent='模型未返回文本内容。';
  zcodeHistory=[...outgoing,{role:'assistant',content}];
  $('zcode-usage').textContent=(usage?`输入 ${usage.prompt_tokens??'—'} · 输出 ${usage.completion_tokens??'—'} · 总计 ${usage.total_tokens??'—'} tokens`:'上游未返回用量')+' · 本次通道：GLM（智谱）';
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=(content?content+'\n\n':'')+msg;$('zcode-usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;zcodeControls();}
});

async function loadQoder(){
 if(!csrf)return;
 try{renderQoder(await jsonAPI('qoder'));}
 catch(error){notice(error.message);}
}
function qoderPageVisible(){return page==='qoder'&&csrf&&!document.hidden;}
function scheduleQoderPoll(){
 clearTimeout(qoderPollTimer);qoderPollTimer=undefined;
 if(qoderPageVisible()&&qoderStatus&&qoderStatus.control&&!qoderStatus.logged_in)qoderPollTimer=setTimeout(loadQoder,3000);
}
function stopQoderPoll(){clearTimeout(qoderPollTimer);qoderPollTimer=undefined;}
function renderQoder(status){
 qoderStatus=status;
 const enabled=!!status.enabled;
 $('qoder-disabled').hidden=enabled;
 $('qoder-content').hidden=!enabled;
 if(!enabled){stopQoderPoll();return;}
 const control=status.control===true;
 // status.control===false 表示配置了控制端但不可达；字段缺失是纯 PAT 模式。
 qoderControl=control;
 const loggedIn=!!status.logged_in;
 qoderLoggedIn=loggedIn;
 if(control&&loggedIn)qoderAuthURL='';
 const badge=$('qoder-status-badge');
 let statusText='';
 if(control&&!loggedIn){
  badge.textContent='未登录';badge.className='badge warn';
  statusText='尚未保存 Qoder 凭据，切换到「Qoder 登录」完成设备授权后即可使用通道。';
 }else{
  badge.textContent=status.reachable?'在线':'不可达';
  badge.className='badge'+(status.reachable?'':' warn');
  let extra='';
  if(status.control===false)extra=' · 登录控制端不可达';
  else if(control)extra=' · Qoder 账号已登录';
  else if(status.reachable)extra=' · PAT 模式';
  statusText=status.reachable?`qoder-proxy 可达 · ${status.model_count} 个模型${extra}`:`qoder-proxy 当前不可达，请确认容器已启动。${extra}`;
 }
 $('qoder-status-text').textContent=statusText;
 $('qoder-login-badge').textContent='未登录';
 $('qoder-logout').hidden=!control||!loggedIn;
 $('qoder-logout').textContent=qoderLogoutArmed?'再次点击确认退出':'退出登录';
 $('qoder-auth-row').hidden=!qoderAuthURL;
 if(qoderAuthURL)$('qoder-auth-link').href=qoderAuthURL;
 $('qoder-login-hint').textContent=!control||loggedIn?'':qoderAuthURL?'已在新标签页打开授权页面，完成授权后本页每 2 秒自动检查登录结果；若被拦截请用下方链接打开（5 分钟内有效）。':'点击「开始登录」，在打开的 Qoder 页面完成授权；凭据只保存在 qoder-proxy 容器内。';
 qoderActionControls();
 // 与 zcode 同一视图模型：未登录只给登录页签并自动落地；模型/对话页签在数据面可用后出现。
 const loginTab=control&&!loggedIn;
 const dataTab=!loginTab&&!!status.reachable;
 if(loginTab&&!qoderViewTouched)qoderView='login';
 if(qoderView==='login'&&!loginTab)qoderView='status';
 if((qoderView==='models'||qoderView==='chat')&&!dataTab)qoderView='status';
 $('qoder-view-status').hidden=false;
 $('qoder-view-login').hidden=!loginTab;
 $('qoder-view-models').hidden=!dataTab;
 $('qoder-view-chat').hidden=!dataTab;
 for(const view of ['status','login','models','chat']){
  $('qoder-view-'+view).classList.toggle('active',view===qoderView);
  $('qoder-pane-'+view).hidden=view!==qoderView;
 }
 qoderModels=(status.models||[]).map(item=>item.id).filter(Boolean);
 renderAccessModelSelect();renderAccess();
 const previous=$('qoder-model').value;
 $('qoder-model').replaceChildren();
 for(const id of qoderModels)$('qoder-model').append(new Option(id,id));
 if(qoderModels.includes(previous))$('qoder-model').value=previous;
 $('qoder-models-body').replaceChildren();
 for(const id of qoderModels){const tr=document.createElement('tr');tr.append(cell(id),cell('Qoder'));$('qoder-models-body').append(tr);}
 $('qoder-models-empty').hidden=qoderModels.length>0;
 $('qoder-model').disabled=!qoderModels.length||!!activeRequest;
 $('qoder-send').disabled=!qoderModels.length||!!activeRequest;
 qoderControls();
 scheduleQoderPoll();
}
function qoderControls(){
 for(const id of ['qoder-model','qoder-prompt','qoder-send'])$(id).disabled=!!activeRequest;
 $('qoder-stop').hidden=!activeRequest;
}
function qoderActionControls(){
 for(const id of ['qoder-login-start','qoder-logout','qoder-refresh'])$(id).disabled=qoderBusy;
}
async function qoderAction(path){
 if(qoderBusy)return;
 const generation=sessionGeneration;qoderBusy=true;qoderActionControls();notice('');
 try{
  await jsonAPI(path,{});
  if(generation!==sessionGeneration)return;
  qoderAuthURL='';qoderLogoutArmed=false;
  await loadQoder();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){qoderBusy=false;qoderActionControls();}}
}
// 设备码登录自身有状态机（waiting→success/error），登录期间 2 秒轮询控制进程；
// 未登录期间另有与 zcode 同构的 3 秒状态轮询，二者并存互不影响。
function qoderLoginPollLoop(generation){
 clearTimeout(qoderLoginTimer);
 const tick=async()=>{
  if(generation!==sessionGeneration){return;}
  try{
   const state=await jsonAPI('qoder/login');
   if(generation!==sessionGeneration)return;
   if(state.state==='success'){
    qoderBusy=false;qoderAuthURL='';
    await loadQoder();
    return;
   }
   if(state.state==='error'){
    qoderBusy=false;qoderAuthURL='';
    $('qoder-login-hint').textContent='登录失败：'+(state.detail||'未知错误')+'，可重新发起。';
    if(qoderStatus)renderQoder(qoderStatus);
    return;
   }
   // waiting / idle（链接生成中）：继续轮询
   qoderLoginTimer=setTimeout(tick,2000);
  }catch(e){
   if(generation===sessionGeneration){qoderBusy=false;notice(e.message);if(qoderStatus)renderQoder(qoderStatus);}
  }
 };
 qoderLoginTimer=setTimeout(tick,1000);
}
$('qoder-refresh').addEventListener('click',loadQoder);
for(const view of ['status','login','models','chat'])$('qoder-view-'+view).addEventListener('click',()=>{qoderViewTouched=true;setQoderView(view);});
function setQoderView(view){qoderView=view;if(qoderStatus)renderQoder(qoderStatus);}
$('qoder-login-form').addEventListener('submit',async event=>{
 event.preventDefault();
 if(qoderBusy||qoderLoggedIn)return;
 const generation=sessionGeneration;
 qoderBusy=true;notice('');
 qoderAuthURL='';
 let authWindow=null;
 try{authWindow=window.open('about:blank','_blank');if(authWindow)authWindow.opener=null;}catch(_){authWindow=null;}
 try{
  const result=await jsonAPI('qoder/login',{});
  if(generation!==sessionGeneration){try{authWindow?.close();}catch(_){}return;}
  qoderAuthURL=result.auth_url||'';
  if(qoderAuthURL){
   if(authWindow&&!authWindow.closed)authWindow.location.href=qoderAuthURL;
   else{try{window.open(qoderAuthURL,'_blank','noopener');}catch(_){}}
  }
  if(qoderStatus)renderQoder(qoderStatus);
  qoderLoginPollLoop(generation);
 }catch(e){
  qoderBusy=false;
  try{authWindow?.close();}catch(_){}
  notice(e.message);
  if(qoderStatus)renderQoder(qoderStatus);
 } finally{
  if(generation===sessionGeneration)qoderActionControls();
 }
});
$('qoder-logout').addEventListener('click',()=>{
 if(qoderBusy)return;
 if(!qoderLogoutArmed){qoderLogoutArmed=true;$('qoder-logout').textContent='再次点击确认退出';return;}
 qoderLogoutArmed=false;qoderAction('qoder/logout');
});
$('qoder-stop').addEventListener('click',()=>activeRequest?.abort());
$('qoder-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('qoder-prompt').value.trim();if(!text||!$('qoder-model').value)return;
 const generation=sessionGeneration;
 notice('');message('user',text,$('qoder-messages'));$('qoder-prompt').value='';const answer=message('assistant','正在等待模型…',$('qoder-messages'));
 const controller=new AbortController();activeRequest=controller;qoderControls();
 let content='',usage,finished=false;const outgoing=[...qoderHistory,{role:'user',content:text}];
 try{
  const response=await api('qoder/chat',{model:$('qoder-model').value,messages:outgoing,stream:true},controller.signal);
  if(!response.ok){const err=await response.json();throw new Error(err.error?.message||err.error||`HTTP ${response.status}`);}
  const reader=response.body.getReader(),decoder=new TextDecoder();let buffer='';
  const processFrame=frame=>{
   const payload=frame.split('\n').filter(l=>l.startsWith('data:')).map(l=>l.slice(5).trimStart()).join('\n');if(!payload)return;
   if(payload==='[DONE]'){finished=true;return;}
   const part=JSON.parse(payload);if(part.error)throw new Error(part.error.message||'上游流式响应发生错误');
   content+=part.choices?.[0]?.delta?.content||'';answer.content.textContent=content||'模型正在思考…';
   if(part.usage)usage=part.usage;
  };
  while(true){const{value,done}=await reader.read();if(controller.signal.aborted){const error=new Error('已停止生成');error.name='AbortError';throw error;}buffer+=done?decoder.decode():decoder.decode(value,{stream:true});buffer=buffer.replace(/\r\n/g,'\n');let split;while((split=buffer.indexOf('\n\n'))>=0){processFrame(buffer.slice(0,split));buffer=buffer.slice(split+2);}if(done){if(buffer.trim())processFrame(buffer);break;}}
  if(!finished)throw new Error('响应提前中断，可重新发送问题');
  if(!content)answer.content.textContent='模型未返回文本内容。';
  qoderHistory=[...outgoing,{role:'assistant',content}];
  $('qoder-usage').textContent=(usage?`输入 ${usage.prompt_tokens??'—'} · 输出 ${usage.completion_tokens??'—'} · 总计 ${usage.total_tokens??'—'} tokens`:'上游未返回用量')+' · 本次通道：Qoder';
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=(content?content+'\n\n':'')+msg;$('qoder-usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;qoderControls();}
});

$('add-account').addEventListener('submit',async event=>{
 event.preventDefault();clearTimeout(flowTimer);const button=event.submitter;button.disabled=true;notice('');
 const popup=window.open('about:blank','_blank');if(popup)popup.opener=null;
 try {
  const flow=await jsonAPI('oauth',{realm:$('realm').value});flowID=flow.id;renderFlow(flow);
  if(popup)popup.location=flow.auth_url;pollFlow();
 } catch(e){if(popup)popup.close();notice(e.message);}finally{button.disabled=false;}
});
function renderFlow(flow){
 $('flow').hidden=false;$('flow-label').textContent={waiting:'等待授权',retry:'等待重试',needs_region:'需要补充地区',complete:'授权完成',failed:'授权失败'}[flow.status]||flow.status;
 $('flow-message').textContent=flow.message;$('auth-link').href=flow.auth_url;$('auth-link').hidden=flow.status==='complete';
 $('region-form').hidden=flow.status!=='needs_region';$('flow-retry').hidden=flow.status!=='retry';
 if(flow.countries){$('region').replaceChildren(new Option('请选择你的注册地区',''));for(const c of flow.countries)$('region').append(new Option(c.Name||c.EnName,c.IOS2));}
}
async function pollFlow(){
 const id=flowID;if(!id||!csrf)return;
 try{const flow=await jsonAPI(`oauth/${encodeURIComponent(id)}/poll`,{});if(id!==flowID)return;renderFlow(flow);
  if(flow.status==='complete'){await refreshStatus();await refreshModels();return;}
  if(flow.status==='failed'||flow.status==='needs_region')return;
  flowTimer=setTimeout(pollFlow,flow.status==='retry'?10000:2500);
 }catch(e){$('flow-message').textContent=e.message;$('flow-retry').hidden=false;}
}
$('flow-retry').addEventListener('click',()=>{clearTimeout(flowTimer);pollFlow();});
$('region-form').addEventListener('submit',async event=>{
 event.preventDefault();if(!$('region').value)return;const button=event.submitter;button.disabled=true;
 try{const flow=await jsonAPI(`oauth/${encodeURIComponent(flowID)}/region`,{region:$('region').value});renderFlow(flow);if(flow.status==='complete'){await refreshStatus();await refreshModels();}else if(flow.status==='retry'){flowTimer=setTimeout(pollFlow,10000);}}catch(e){notice(e.message);}finally{button.disabled=false;}
});

function message(role,text,container){
 container=container||$('messages');container.querySelector('.chat-empty')?.remove();const el=document.createElement('div');el.className='message '+role;
 const label=document.createElement('span');label.className='role';label.textContent=role==='user'?'你':'ASSISTANT';const content=document.createElement('div');content.textContent=text;
 el.append(label,content);container.append(el);el.scrollIntoView({block:'nearest'});return{el,content};
}
function clearChat(){history=[];conversation=newConversation();$('messages').replaceChildren();$('usage').textContent='用量将在上游返回后显示';}
function setProtocol(value){
 if(activeRequest||!['openai','anthropic'].includes(value)||value===protocol)return;
 protocol=value;clearChat();updateEfforts();renderAccess();
}
function renderAccess(){
 const anthropic=protocol==='anthropic',model=$('access-model').value;
 document.querySelectorAll('[data-protocol]').forEach(button=>{button.setAttribute('aria-pressed',String(button.dataset.protocol===protocol));button.disabled=!!activeRequest;});
 $('effort').hidden=$('effort-label').hidden=anthropic;
 $('max-tokens').hidden=$('max-tokens-label').hidden=!anthropic;
 $('base-url').value=location.origin+(anthropic?'':'/v1');
 $('api-endpoint').value=location.origin+(anthropic?'/v1/messages':'/v1/chat/completions');
 $('api-auth').textContent=anthropic?'x-api-key · anthropic-version: 2023-06-01':'Authorization: Bearer <API Key>';
 $('protocol-support').textContent=anthropic?'Anthropic · Beta 测试 · 文本兼容':'OpenAI · Chat Completions';
 $('protocol-note').textContent=anthropic?'Base URL 使用站点根地址，SDK 会追加 /v1/messages。支持文本、多轮和流式回答；暂不支持图片、工具、thinking，也不承诺 Claude Code 兼容。':'Base URL 包含 /v1，使用兼容 Chat Completions 的客户端连接。';
 const available=accessModelOptions().some(item=>item.id===model);
 $('copy-example').disabled=!available||!!activeRequest;$('go-chat').disabled=!available||!!activeRequest;
 if(!available){$('api-example').textContent='暂无可选模型，请先刷新模型列表。';return;}
 const isGlm=model.startsWith('glm-');
 const isQoder=model.startsWith('qoder-');
 if(anthropic&&(isGlm||isQoder)){$('copy-example').disabled=true;$('api-example').textContent=(isGlm?'GLM':'Qoder')+' 通道仅支持 OpenAI 协议（/v1/chat/completions），请切换协议后复制示例。';return;}
 const body={model,...(anthropic?{max_tokens:1024}:{}),messages:[{role:'user',content:'你好'}],stream:true};
 const auth=anthropic?'  -H "x-api-key: <你的 API Key>" \\\n  -H "anthropic-version: 2023-06-01"':'  -H "Authorization: Bearer <你的 API Key>"';
 const json=JSON.stringify(body,null,2).replace(/'/g,"'\\''");
 let prefix='';
 if(isGlm) prefix='# GLM 模型经 zcode 通道转发（按 glm- 前缀分流），账号为容器登录态\ncurl ';
 else if(isQoder) prefix='# Qoder 模型经 qoder 通道转发（按 qoder- 前缀分流）\ncurl ';
 else if(!anthropic) prefix='# 成功响应头 X-Account / X-Account-Realm 标明本次服务的账号与平台\ncurl ';
 else prefix='curl ';
 $('api-example').textContent=prefix+$('api-endpoint').value+` \\\n${auth} \\\n  -H "Content-Type: application/json" \\\n  -d '${json}'`;
}
document.querySelectorAll('[data-protocol]').forEach(button=>button.addEventListener('click',()=>setProtocol(button.dataset.protocol)));
$('access-model').addEventListener('change',renderAccess);
$('copy-example').addEventListener('click',async()=>{if($('copy-example').disabled)return;try{await navigator.clipboard.writeText($('api-example').textContent);notice('调用示例已复制，请替换 API Key 占位符');}catch{notice('浏览器不允许自动复制，请手动复制下方示例');}});
$('go-chat').addEventListener('click',()=>{if(activeRequest||$('go-chat').disabled)return;const model=$('access-model').value;if(model.startsWith('glm-')){if(zcodeModels.includes(model))$('zcode-model').value=model;showPage('zcode');return;}if(model.startsWith('qoder-')){if(qoderModels.includes(model))$('qoder-model').value=model;showPage('qoder');return;}$('model').value=model;updateEfforts();showPage('chat');});
function chatControls(){
 for(const id of ['model','access-model','clear-chat','send-chat','max-tokens'])$(id).disabled=!!activeRequest;
 $('stop-chat').hidden=!activeRequest;
 renderAccess();
}
$('clear-chat').addEventListener('click',()=>{if(activeRequest)return;clearChat();});
$('stop-chat').addEventListener('click',()=>activeRequest?.abort());
// accountNote reads the attribution headers core attaches to successful chat
// responses (X-Account / X-Account-Realm) so the operator can see which pooled
// account and which platform served the request. Headers are absent on error
// paths and non-core upstreams, so it degrades to an empty string.
function accountNote(response) {
 const account = response.headers?.get?.('X-Account') || '';
 const realm = response.headers?.get?.('X-Account-Realm') || '';
 if (!account && !realm) return '';
 const platform = realm === 'global' ? '国际版' : realm === 'cn' ? '国内版' : realm;
 return ` · 本次账号：${account || '—'}${platform ? `（${platform}）` : ''}`;
}
for(const [inputId,formId] of [['prompt','chat-form'],['zcode-prompt','zcode-form'],['qoder-prompt','qoder-form']])$(inputId).addEventListener('keydown',event=>{
 if(event.key!=='Enter'||event.shiftKey||event.isComposing)return;
 event.preventDefault();$(formId).requestSubmit();
});
$('chat-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('prompt').value.trim();if(!text)return;
 const requestProtocol=protocol,generation=sessionGeneration;
 if(requestProtocol==='anthropic'&&(!Number.isSafeInteger(Number($('max-tokens').value))||Number($('max-tokens').value)<=0)){notice('最大输出 tokens 必须是安全的正整数');return;}
 notice('');message('user',text);$('prompt').value='';const answer=message('assistant','正在等待模型…');
 const controller=new AbortController();activeRequest=controller;chatControls();$('effort').disabled=true;
 let content='',reasoning='',details,reasonText,usage,finished=false;const tools=new Map();const outgoing=[...history,{role:'user',content:text}];
 try{
  const body={model:$('model').value,messages:outgoing,stream:true,conversationId:conversation};if($('effort').value)body.reasoning_effort=$('effort').value;
  const anthropicBody={model:$('model').value,max_tokens:Number($('max-tokens').value),messages:outgoing,stream:true};
  const response=requestProtocol==='anthropic'?await api('messages',{conversation_id:conversation,request:anthropicBody},controller.signal):await api('chat',body,controller.signal);
  if(!response.ok){const err=await response.json();throw new Error(err.error?.message||err.error||`HTTP ${response.status}`);}
  const attribution=accountNote(response);
  const reader=response.body.getReader(),decoder=new TextDecoder();let buffer='';
  const processFrame=frame=>{
   const payload=frame.split('\n').filter(l=>l.startsWith('data:')).map(l=>l.slice(5).trimStart()).join('\n');if(!payload)return;
   if(payload==='[DONE]'){if(requestProtocol==='openai')finished=true;return;}
   const part=JSON.parse(payload);if(part.error)throw new Error(part.error.message||'上游流式响应发生错误');
   if(requestProtocol==='anthropic'){
    if(part.type==='error')throw new Error('上游流式响应发生错误');
    if(part.type==='content_block_delta'&&part.delta?.type==='text_delta'){content+=part.delta.text||'';answer.content.textContent=content;}
    if(part.type==='message_delta'&&part.usage)usage=part.usage;
    if(part.type==='message_stop')finished=true;
    $('messages').scrollTop=$('messages').scrollHeight;return;
   }
   const delta=part.choices?.[0]?.delta||{};content+=delta.content||'';
   if(delta.reasoning_content){reasoning+=delta.reasoning_content;if(!details){details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='查看推理内容';reasonText=document.createElement('pre');details.append(summary,reasonText);answer.el.insertBefore(details,answer.content);}reasonText.textContent=reasoning;}
   for(const call of delta.tool_calls||[]){const prev=tools.get(call.index)||{name:'',arguments:''};prev.name+=call.function?.name||'';prev.arguments+=call.function?.arguments||'';tools.set(call.index,prev);}
   const toolText=tools.size?'\n\n工具调用（仅展示，不执行）：\n'+[...tools.values()].map(t=>`${t.name}\n${t.arguments}`).join('\n'):'';
   answer.content.textContent=content+toolText||'模型正在思考…';
   if(part.usage)usage=part.usage;
   $('messages').scrollTop=$('messages').scrollHeight;
  };
  while(true){const{value,done}=await reader.read();if(controller.signal.aborted){const error=new Error('已停止生成');error.name='AbortError';throw error;}buffer+=done?decoder.decode():decoder.decode(value,{stream:true});buffer=buffer.replace(/\r\n/g,'\n');let split;while((split=buffer.indexOf('\n\n'))>=0){processFrame(buffer.slice(0,split));buffer=buffer.slice(split+2);}if(done){if(buffer.trim())processFrame(buffer);break;}}
  if(!finished)throw new Error('响应提前中断，可重新发送问题');
  if(!content&&!tools.size)answer.content.textContent='模型未返回文本内容。';
  history=[...outgoing,{role:'assistant',content:content||(requestProtocol==='openai'?'[本轮返回了工具调用，控制台未执行工具]':'')}];
  $('usage').textContent=(requestProtocol==='anthropic'?(usage?.input_tokens!=null||usage?.output_tokens!=null?`输入 ${usage.input_tokens??'—'} · 输出 ${usage.output_tokens??'—'} tokens`:'上游未返回用量'):(usage?`输入 ${usage.prompt_tokens??'—'} · 输出 ${usage.completion_tokens??'—'} · 总计 ${usage.total_tokens??'—'} tokens`:'上游未返回用量'))+attribution;
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=(content?content+'\n\n':'')+msg;$('usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;chatControls();updateEfforts();if(generation===sessionGeneration)refreshStatus();}
});
renderAccess();
$('reveal-key').addEventListener('click',async()=>{try{$('api-key').value=(await jsonAPI('access',{})).api_key;$('api-key').type='text';}catch(e){notice(e.message);}});
$('copy-key').addEventListener('click',async()=>{const generation=sessionGeneration;try{if(!$('api-key').value)$('api-key').value=(await jsonAPI('access',{})).api_key;await navigator.clipboard.writeText($('api-key').value);if(generation===sessionGeneration)notice('API Key 已复制');}catch{if(generation!==sessionGeneration)return;$('api-key').type='text';$('api-key').select();notice('浏览器不允许自动复制，请手动复制选中的密钥');}});
jsonAPI('session').then(signedIn).catch(()=>{});

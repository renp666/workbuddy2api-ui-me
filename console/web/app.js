const $ = (id) => document.getElementById(id);
let csrf = '', modelList = [], accounts = [], history = [], conversation = newConversation(), activeRequest, flowID, flowTimer;
// pinState：{cn:{uid,nickname,exists}|null, global:null|...}，null=该平台未锁定。
let pinState = {cn:null, global:null}, lastStatus = null;
let page = 'overview', workbuddyView = 'accounts', sessionGeneration = 0;
let protocol = 'openai';
let taskState = {items:[],active_run:null,latest_runs:[]}, taskHistory = [], taskBefore = null, taskStarting = false, taskPollTimer, taskRenderKey, detailController, detailGeneration = 0;
let usageRange = 'today', usageChannel = 'all', usageGeneration = 0;
let zcodeModels = [], zcodeHistory = [], zcodeStatus = null, zcodePollTimer, zcodeBusy = false, zcodeAuthURL = '', zcodeLogoutArmed = false, zcodeProviderTouched = false, zcodePlanTouched = false, zcodeView = 'status', zcodeViewTouched = false, zcodeEnabled = false;
let qoderModels = [], qoderHistory = [], qoderStatus = null, qoderPollTimer, qoderBusy = false, qoderAuthURL = '', qoderLogoutArmed = false, qoderControl = false, qoderLoggedIn = false, qoderLoginTimer = null, qoderView = 'status', qoderViewTouched = false, qoderEnabled = false;
let opencodeModels = [], opencodeHistory = [], opencodeStatus = null, opencodeBusy = false, opencodeView = 'status', opencodeEnabled = false;
let routesState = null, routesBusy = false, routesEditing = null;
let probeSnapshot = {running:false,done:0,total:0,channels:{}}, probeTarget = '', probeTimer, probeBusy = false;
// modelsGroupByAccount：core 模型表是否按可用账号分层展示（默认开）。
let modelsGroupByAccount = true;
let overviewUsageRange = 'today', overviewUsageChannel = 'all', overviewUsageItems = [], overviewGeneration = 0;
let selectedTaskRun, historyGeneration = 0, historyLoading = false, taskHistoryKey;
const taskIntents = new Map(), taskReads = new Set();
function newConversation() { return `web-${Date.now()}-${Math.random().toString(36).slice(2)}`; }
function notice(text = '') { $('notice').textContent = text; $('notice').hidden = !text; $('announce').textContent = text; }
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
 clearMessages($('messages'));$('task-list').replaceChildren();$('task-history-body').replaceChildren();$('task-detail').hidden=true;$('task-accounts').textContent='';$('task-log').textContent=''; $('api-key').value = ''; $('api-key').type = 'password'; $('admin-key').value = ''; $('console-view').hidden = true; $('login-view').hidden = false;
 protocol='openai';$('prompt').value='';$('max-tokens').value='1024';$('usage').textContent='用量将在上游返回后显示';usageGeneration++;workbuddyView='accounts';renderAccess();
 zcodeModels=[];zcodeHistory=[];zcodeStatus=null;zcodeAuthURL='';zcodeLogoutArmed=false;zcodeBusy=false;zcodeProviderTouched=false;zcodePlanTouched=false;zcodeView='status';zcodeViewTouched=false;zcodeEnabled=false;clearTimeout(zcodePollTimer);zcodePollTimer=undefined;$('zcode-model').replaceChildren();clearMessages($('zcode-messages'));$('zcode-prompt').value='';$('zcode-usage').textContent='用量将在上游返回后显示';$('zcode-disabled').hidden=true;$('zcode-content').hidden=true;$('nav-zcode').classList.remove('nav-muted');
 qoderModels=[];qoderHistory=[];qoderStatus=null;qoderEnabled=false;qoderControl=false;qoderLoggedIn=false;qoderAuthURL='';qoderBusy=false;qoderLogoutArmed=false;qoderView='status';qoderViewTouched=false;clearTimeout(qoderPollTimer);qoderPollTimer=undefined;clearTimeout(qoderLoginTimer);qoderLoginTimer=null;$('qoder-model').replaceChildren();clearMessages($('qoder-messages'));$('qoder-prompt').value='';$('qoder-usage').textContent='用量将在上游返回后显示';$('qoder-disabled').hidden=true;$('qoder-content').hidden=true;$('nav-qoder').classList.remove('nav-muted');$('qoder-auth-row').hidden=true;$('qoder-pane-login').hidden=true;$('qoder-login-hint').textContent='';
 opencodeModels=[];opencodeHistory=[];opencodeStatus=null;opencodeEnabled=false;opencodeBusy=false;opencodeView='status';$('opencode-model').replaceChildren();clearMessages($('opencode-messages'));$('opencode-prompt').value='';$('opencode-usage').textContent='用量将在上游返回后显示';$('opencode-disabled').hidden=true;$('opencode-content').hidden=true;$('nav-opencode').classList.remove('nav-muted');$('opencode-health-wrap').hidden=true;$('opencode-health-body').replaceChildren();
 routesState=null;routesBusy=false;routesEditing=null;modelSwitchOverrides=new Map();$('routes-disabled').hidden=true;$('routes-content').hidden=true;$('routes-alias-body').replaceChildren();$('routes-channels').replaceChildren();$('routes-model-options').replaceChildren();$('routes-example').textContent='';$('routes-alias-name').value='';$('routes-alias-model').value='';$('routes-alias-note').value='';$('routes-auto-fallback').value='';
 probeSnapshot={running:false,done:0,total:0,channels:{}};probeTarget='';probeBusy=false;clearTimeout(probeTimer);probeTimer=undefined;for(const n of ['core','zcode','qoder']){$('probe-'+n+'-progress').hidden=true;$('probe-'+n+'-progress').textContent='';}
 overviewUsageItems=[];overviewGeneration++;
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
 $('nav-opencode').classList.toggle('nav-muted', !session.opencode_enabled);
 opencodeEnabled = !!session.opencode_enabled;
 await refreshStatus(); await refreshModels(); await loadPins();
 void loadModelHeat(); void loadModelSwitch(); void loadGlobalHeat(); void loadOverview(true); void restoreProbe();
}
const pageNames = {overview:'运行概览',workbuddy:'WorkBuddy 通道',zcode:'Zcode 通道',qoder:'Qoder 通道',opencode:'OpenCode 通道',usage:'调用统计',routes:'Agent 接入',access:'API 接入'};
const workbuddyNames = {accounts:'账号管理',tasks:'自动任务',models:'模型列表',chat:'对话测试'};
function setWorkBuddyView(view) {
 workbuddyView = view;
 for (const name of ['accounts','tasks','models','chat']) {
  $('workbuddy-view-'+name).classList.toggle('active', name === view);
  $('workbuddy-pane-'+name).hidden = name !== view;
 }
 renderBreadcrumb();
}
function renderBreadcrumb() {
 const base = pageNames[page] || page;
 $('breadcrumb-name').textContent = page === 'workbuddy' ? `${base} / ${workbuddyNames[workbuddyView] || ''}` : base;
}
function showPage(value) {
 if (page === 'workbuddy' && value !== 'workbuddy') stopTaskReads(true);
 if (page === 'zcode' && value !== 'zcode') stopZcodePoll();
 page = value;
 document.querySelectorAll('[data-page]').forEach(el => el.hidden = el.dataset.page !== page);
 document.querySelectorAll('.nav').forEach(el => el.classList.toggle('active', el.dataset.view === page));
 // Every caller is a deliberate navigation (nav button, channel card, 前往测试).
 // Without this the new page opens at the previous page's scroll depth, so a
 // short page like API 接入 can land mid-form with its heading above the fold.
 window.scrollTo(0, 0);
 renderBreadcrumb();
 if (page !== 'access') { $('api-key').value = ''; $('api-key').type = 'password'; }
 if (page === 'workbuddy') setWorkBuddyView(workbuddyView);
 if (page === 'workbuddy' && workbuddyView === 'tasks') loadTaskPage();
 if (page === 'overview') loadOverview();
 if (page === 'usage') loadUsage();
 if (page === 'zcode') loadZcode();
 if (page === 'qoder') loadQoder();
 if (page === 'opencode') loadOpenCode();
 if (page === 'routes') loadRoutes();
 // The access tab lists GLM and Qoder channel models too; fetch statuses once lazily
 // so its select is complete even when the operator never opens those tabs.
 if (page === 'access' && zcodeEnabled && !zcodeStatus) loadZcode();
 if (page === 'access' && qoderEnabled && !qoderStatus) loadQoder();
 if (page === 'access' && opencodeEnabled && !opencodeStatus) loadOpenCode();
}
document.querySelectorAll('[data-view]').forEach(button => button.addEventListener('click', () => {
 showPage(button.dataset.view);
}));
for (const name of ['accounts','tasks','models','chat']) $('workbuddy-view-'+name).addEventListener('click', () => {
 setWorkBuddyView(name);
 if (name === 'tasks') loadTaskPage();
});
$('login-form').addEventListener('submit', async event => {
 event.preventDefault(); const button = event.submitter; button.disabled = true; $('login-error').textContent = '';
 try { const session = await jsonAPI('login', {key:$('admin-key').value}); await signedIn(session); } catch(error) { $('login-error').textContent = error.message; } finally { button.disabled = false; }
});
$('logout').addEventListener('click', async () => {$('login-error').textContent='';const pending=jsonAPI('logout', {});signedOut();try {await pending;} catch(e){$('login-error').textContent=e.message;} });
function cell(text, small) { const td = document.createElement('td'); td.textContent = text; if (small) { const s=document.createElement('small');s.textContent=small;td.append(s); } return td; }
// zcode / qoder / opencode / core 共用的模型表渲染。能力与探测结论都取自上游探测结果；
// 没有探测数据的通道显式显示「未探测」而不是隐藏列，后续上游补上同构数据即可直接复用。
// modelHeat 是调用统计账本按 modelKey 聚合的调用次数，供「热度」列排名。
let modelHeat=new Map(),modelHeatRank=new Map();
// globalHeat 是 OpenRouter 公开目录（sort=most-popular）的全球热度名次：modelKey → 名次（从 1 起）。
// 名次来自服务端按 popularity 顺序下发的 ranks，先到先得即最高名次；匹配不到留「—」，不伪造。
let globalHeat=new Map();
// orModelKey 把 OpenRouter 的 id / canonical_slug 收敛成与本地 modelKey 同一口径的对齐键：
// 去掉 vendor 前缀（第一个 / 之前）与 :variant 后缀，再走 modelKey，使 tencent/hy4-preview
// 与本地 cn:hy4-preview 落到同一个键。日期后缀等差异不做强行归一，匹配不到就如实留空。
function orModelKey(raw){
 const last=String(raw||'').split('/').pop().split(':')[0];
 return modelKey(last);
}
// 模型积分开关：overrides 是服务端人工覆盖表（公共模型名 → 是否启用）。
// 不在表里的模型按积分消耗规则取默认态：倍率已知且为 0 默认开启，倍率>0 或未知默认关闭。
let modelSwitchOverrides=new Map();
// parseCreditRule 解析上游 credits 原始串（实测形态 "x0.79 credits"/"x0.00"/"x3.47"），
// 解析失败返回 null 按「未知」处理，不得伪造零——与服务端 parseCreditRule 同一口径。
function parseCreditRule(raw){
 if(typeof raw!=='string')return null;
 let s=raw.trim().toLowerCase();
 if(!s)return null;
 if(s.endsWith('credits'))s=s.slice(0,-7).trim();
 if(s.startsWith('x'))s=s.slice(1).trim();
 if(!s)return null;
 const value=Number(s);
 return Number.isFinite(value)&&value>=0?value:null;
}
// modelSwitchEffective 报告行的当前生效状态：人工覆盖优先，其次按倍率取默认态。
// opencode 通道免费，不参与开关（row.switchable=false 时恒放行）。
function modelSwitchEffective(row){
 if(!row.switchable)return true;
 if(modelSwitchOverrides.has(row.id))return modelSwitchOverrides.get(row.id);
 return row.creditValue===0;
}
const modelChannels={zcode:{body:'zcode-models-body',empty:'zcode-models-empty',namePrefix:'GLM · '},
 qoder:{body:'qoder-models-body',empty:'qoder-models-empty',namePrefix:'Qoder · '},
 opencode:{body:'opencode-models-body',empty:'opencode-models-empty',namePrefix:'opencode-OC · '},
 core:{body:'core-models-body',empty:'core-models-empty',namePrefix:''}};
const modelFailureLabels={timeout:'探测超时',access:'地区限制',error:'调用失败',model_error:'模型错误',auth:'未授权',rate_limit:'限流',empty:'空返回'};
const modelProbeSources={probe:'探测',request:'真实请求'};
// 公共模型名带通道前缀（opencode-OC · X / glm-X / opencode/x）与 realm 前缀（cn:/global:），去掉后作为跨源对齐键。
function modelKey(id){return String(id||'').replace(/^opencode-OC · /,'').replace(/^OC · /,'').replace(/^opencode\//,'').replace(/^(?:cn|global):/,'').replace(/^glm-/,'').replace(/^qoder-/,'').toLowerCase().replace(/[^a-z0-9\u4e00-\u9fa5]+/g,'');}
function badgeCell(text,warn){const td=document.createElement('td');const tag=document.createElement('span');tag.className='badge'+(warn?' warn':'');tag.textContent=text;td.append(tag);return td;}
function chipsCell(labels,small){const td=document.createElement('td');if(labels.length){const row=document.createElement('span');row.className='badge-row';for(const label of labels){const tag=document.createElement('span');tag.className='badge';tag.textContent=label;row.append(tag);}td.append(row);}else td.textContent='—';if(small){const note=document.createElement('small');note.textContent=small;td.append(note);}return td;}
// order 是排序档位：可用最优，其次仅对话，再次未探测（没有探测数据），最后才是真不可用。
function modelProbeState(result){
 if(!result)return{label:'未探测',warn:true,order:2};
 if(result.ok===true)return result.chatOnly?{label:'仅对话',warn:true,order:1}:{label:'可用',warn:false,order:0};
 const category=typeof result.category==='string'?result.category:'';
 return{label:modelFailureLabels[category]||category||'不可用',warn:true,order:3};
}
function formatProbeDuration(ms){const value=Number(ms);return Number.isFinite(value)&&value>=0?`${(value/1000).toFixed(1)} 秒`:'—';}
// speedText 输出 tokens/秒：只认探测结论里真实回传的 tokensPerSec（上游 usage 的 completion_tokens 除以生成耗时），
// 没有数据留「—」，不得用耗时反推伪造速度。
function speedText(result){const v=result?Number(result.tokensPerSec):NaN;return Number.isFinite(v)&&v>0?`${v.toFixed(1)} tok/s`:'—';}
function formatProbeTime(value){const date=new Date(value);return Number.isNaN(date.getTime())?'':date.toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false});}
function modelCapabilityBadges(cap,fallback){
 if(cap&&(cap.images!==undefined||cap.toolcall!==undefined||cap.reasoning!==undefined))return[cap.images?'图片':'',cap.toolcall?'工具':'',cap.reasoning?'推理':''].filter(Boolean);
 return fallback||[];
}
function modelLimitText(cap){
 if(!cap)return '';
 const parts=[];
 if(cap.context)parts.push(`上下文 ${cap.context}`);
 if(cap.input)parts.push(`输入上限 ${cap.input}`);
 if(cap.output)parts.push(`输出上限 ${cap.output}`);
 return parts.join(' · ');
}
function modelVariantNames(source){const variants=source&&typeof source.variants==='object'?source.variants:null;return variants?Object.keys(variants):[];}
// 热度来自调用统计账本：按 modelKey 聚合调用次数，前三名分获红/橙/黄星。
// 排名是跨通道的公共口径，同一模型在四张表里拿到同一颗星；账本无记录时如实留「—」。
const modelHeatSources={};
function modelHeatCell(key){
 const td=document.createElement('td');
 const rank=modelHeatRank.get(key);
 if(!rank){td.textContent='—';return td;}
 const star=document.createElement('span');
 star.className='heat-stars heat-'+rank;
 star.textContent='★';
 const note=document.createElement('small');
 note.textContent=(modelHeat.get(key)||0)+' 次';
 td.append(star,note);
 return td;
}
// globalHeatCell 渲染「全球热度」列：OpenRouter most-popular 名次显示为 #N；
// 对齐不到或源不可用时如实留「—」，不得伪造名次。
function globalHeatCell(key){
 const td=document.createElement('td');
 const rank=globalHeat.get(key);
 td.textContent=rank?'#'+rank:'—';
 return td;
}
function applyModelHeat(items){
 const counts=new Map();
 for(const entry of(Array.isArray(items)?items:[])){
  const key=modelKey(entry&&entry.model);
  if(key)counts.set(key,(counts.get(key)||0)+1);
 }
 modelHeat=counts;
 const top=[...counts.entries()].sort((a,b)=>b[1]-a[1]||(a[0]<b[0]?-1:1)).slice(0,3);
 modelHeatRank=new Map(top.map(([key],index)=>[key,index+1]));
 for(const channel of Object.keys(modelHeatSources)){const source=modelHeatSources[channel];renderModelTable(channel,source.list,source.health);}
}
// 静默加载：账本不可用时保持「—」，不让热度缺失打扰模型表。
async function loadModelHeat(){
 try{applyModelHeat((await jsonAPI('usage?range=month')).items);}catch(error){}
}
// loadGlobalHeat 拉取 OpenRouter 名次表并按 modelKey 建索引（先到先得=最高名次），
// 再重渲染各通道。源不可用或返回空时静默降级：globalHeat 清空，所有单元格留「—」。
async function loadGlobalHeat(){
 try{
  const state=await jsonAPI('heat');
  const map=new Map();
  if(state&&state.available&&Array.isArray(state.ranks)){
   state.ranks.forEach((entry,index)=>{
    for(const raw of[entry&&entry.id,entry&&entry.canonical_slug]){
     const key=orModelKey(raw);
     if(key&&!map.has(key))map.set(key,index+1);
    }
   });
  }
  globalHeat=map;
 }catch(error){globalHeat=new Map();}
 for(const channel of Object.keys(modelHeatSources)){const source=modelHeatSources[channel];renderModelTable(channel,source.list,source.health);}
}
// loadModelSwitch 拉取人工覆盖表并重渲染各通道。开关功能未启用（无覆盖数据）时
// 静默降级：所有模型按倍率默认态展示，不影响渲染。
async function loadModelSwitch(){
 try{
  const state=await jsonAPI('model-switch');
  modelSwitchOverrides=new Map(Object.entries(state.overrides||{}));
 }catch(error){modelSwitchOverrides=new Map();}
 for(const channel of Object.keys(modelHeatSources)){const source=modelHeatSources[channel];renderModelTable(channel,source.list,source.health);}
}
// modelSwitchAction 切换单个模型并落盘，成功后刷新覆盖表重渲染。
async function modelSwitchAction(btn){
 const enable=btn.dataset.action==='enable-model';
 const model=btn.dataset.model;
 btn.disabled=true;
 try{
  const state=await jsonAPI('model-switch',{model,enabled:enable});
  modelSwitchOverrides=new Map(Object.entries(state.overrides||{}));
  for(const channel of Object.keys(modelHeatSources)){const source=modelHeatSources[channel];renderModelTable(channel,source.list,source.health);}
  notice(`${model} 已${enable?'启用':'停用'}`);
 }catch(e){
  notice(e.message);
  btn.disabled=false;
 }
}
// channelModelRows 把「模型清单」与「探测结果」并成同一批行。/v1/models 只返回可用项，
// 探测过但不可用的模型仍按条保留并带失败原因，不让它们从界面上整体消失。
// 行身份分两套：core 清单里 cn: 与 global: 是两个不同模型（各有倍率与可用账号），
// 去重必须用完整公共名，否则同名模型被合并、账号归属与倍率互相覆盖；其余通道
// 沿用跨源对齐键。row.key 始终是对齐键，供热度与能力表跨源匹配。
function channelModelRows(channel,list,health){
 const prefix=modelChannels[channel].namePrefix;
 const results=health&&typeof health.modelResults==='object'&&health.modelResults?health.modelResults:{};
 const caps=new Map();
 if(health&&Array.isArray(health.models))for(const item of health.models){if(item&&item.name)caps.set(modelKey(item.name),item);}
 const rows=new Map();
 const switchable=channel!=='opencode';
 const ensure=label=>{const identity=channel==='core'?String(label||''):modelKey(label);if(!identity)return null;const key=modelKey(label);if(!rows.has(identity))rows.set(identity,{key,label:String(label),id:String(label),cap:null,result:null,efforts:null,credits:null,creditValue:null,accounts:null});return rows.get(identity);};
 for(const item of(Array.isArray(list)?list:[])){
  if(!item||!item.id)continue;
  const row=ensure(item.id);if(!row)continue;
  row.efforts=Array.isArray(item.reasoning_supported_efforts)?item.reasoning_supported_efforts:null;
  row.cap=caps.get(row.key)||row.cap;
  row.credits=typeof item.credits==='string'?item.credits:null;
  row.creditValue=parseCreditRule(row.credits);
  row.accounts=Array.isArray(item.accounts)?item.accounts:null;
 }
 for(const[id,result]of Object.entries(results)){
  const cap=caps.get(modelKey(id))||null;
  const row=ensure(cap&&cap.name?`${prefix}${cap.name}`:id);if(!row)continue;
  row.cap=cap||row.cap;
  row.result=result&&typeof result==='object'?result:null;
 }
 return[...rows.values()].map(row=>{
  const result=row.result;
  const variants=[...new Set([...modelVariantNames(result),...modelVariantNames(row.cap)])];
  const ms=result?Number(result.durationMs):NaN;
  return{key:row.key,label:row.label,id:row.id,switchable,credits:row.credits,creditValue:row.creditValue,accounts:row.accounts,state:modelProbeState(result),
   caps:modelCapabilityBadges(row.cap,row.efforts&&row.efforts.length?['推理']:null),
   limit:modelLimitText(row.cap),
   variants:variants.length?`档位 ${variants.join(' / ')}`:'',
   duration:result?formatProbeDuration(result.durationMs):'—',
   durationMs:Number.isFinite(ms)&&ms>=0?ms:null,
   speed:speedText(result),
   source:result&&modelProbeSources[result.source]?modelProbeSources[result.source]:'',
   note:result&&typeof result.error==='string'&&result.error?result.error:'—'};
 }).sort(switchable?creditSort:probeSort);
}
// creditSort 是积分开关通道的排序：按积分消耗升序（倍率低在前），未知倍率排最后，
// 已知且为 0（免费）排最前。倍率相同（含同为未知）保持上游目录的插入顺序，
// Array.prototype.sort 在 V8 中稳定，不额外按名字打乱同档模型。
function creditSort(a,b){
 const av=a.creditValue,bv=b.creditValue;
 if(av==null&&bv==null)return 0;
 if(av==null)return 1;
 if(bv==null)return -1;
 return av-bv;
}
// probeSort 是 opencode 通道的原排序：免费通道不采集倍率，保持「探测状态 → 耗时」口径。
function probeSort(a,b){
 return a.state.order-b.state.order||((a.durationMs??Infinity)-(b.durationMs??Infinity));
}
function renderModelTable(channel,list,health){
 const config=modelChannels[channel];
 modelHeatSources[channel]={list,health};
 const body=$(config.body);body.replaceChildren();
 const rows=channelModelRows(channel,list,health);
 const cols=channel==='opencode'?8:10;
 const appendRow=row=>{
  const tr=document.createElement('tr');
  tr.append(cell(row.label),chipsCell(row.caps,[row.limit,row.variants].filter(Boolean).join(' · ')),badgeCell(row.state.label,row.state.warn),cell(row.duration,row.source),cell(row.speed),cell(row.note),modelHeatCell(row.key),globalHeatCell(row.key));
  // opencode 免费通道不采集倍率、不加开关列；其余通道追加「积分消耗」与「操作」两列。
  if(row.switchable){ tr.append(creditCell(row),switchOpCell(row)); }
  body.append(tr);
 };
 if(channel==='core'&&modelsGroupByAccount&&rows.some(row=>Array.isArray(row.accounts)&&row.accounts.length)){
  // 按账号分层：每个模型挂到它的每个可用账号下（多账号模型在各组重复出现，
  // 与「哪些账号能跑这个模型」的归属口径一致）；无归属信息的行归入「未归属」。
  const groups=new Map();
  for(const row of rows){
   const owners=(Array.isArray(row.accounts)&&row.accounts.length)?row.accounts:[null];
   for(const acc of owners){
    const key=acc?acc.uid:'__none__';
    if(!groups.has(key))groups.set(key,{acc,items:[]});
    groups.get(key).items.push(row);
   }
  }
  const ordered=[...groups.values()].sort((a,b)=>a.acc?(b.acc?String(a.acc.uid).localeCompare(String(b.acc.uid)):1):-1);
  for(const g of ordered){
   const head=document.createElement('tr');head.className='model-group-head';
   const th=document.createElement('td');th.colSpan=cols;
   th.textContent=g.acc?`${g.acc.nickname||g.acc.uid} · ${g.items.length} 个模型`:`未归属账号 · ${g.items.length} 个模型`;
   head.append(th);body.append(head);
   for(const row of g.items)appendRow(row);
  }
 }else{
  for(const row of rows)appendRow(row);
 }
 $(config.empty).hidden=rows.length>0;
 return rows;
}
// creditCell 渲染积分消耗倍率：已知显示「×N」，未知显示「未知」。
function creditCell(row){
 if(row.creditValue===null||row.creditValue===undefined)return cell('未知');
 return cell(`×${row.creditValue}`);
}
// switchOpCell 构造模型开关单元格：启用中的模型显示「停用」按钮，
// 停用中的显示「启用」按钮，并带一个状态徽标说明当前生效态与默认来源。
function switchOpCell(row){
 const td=document.createElement('td');
 const enabled=modelSwitchEffective(row);
 const badge=document.createElement('span');
 badge.className='badge'+(enabled?'':' warn');
 badge.textContent=enabled?'已启用':'已停用';
 const btn=document.createElement('button');
 btn.className='secondary model-switch-btn';
 btn.textContent=enabled?'停用':'启用';
 btn.dataset.action=enabled?'disable-model':'enable-model';
 btn.dataset.model=row.id;
 td.append(badge,' ',btn);
 return td;
}
// ── 手工测速探测 ──
// 探测仅由页面按钮触发（不点不耗额度）；结论缓存在服务端内存，重启即清空。
// 通道键：core→"core"、zcode→"glm"、qoder→"qoder"（与后端 routeChannel 同一口径）。
const probeChannelNames={core:'core',zcode:'glm',qoder:'qoder'};
function setProbeButtonsDisabled(disabled){for(const n of ['core','zcode','qoder'])$('probe-'+n).disabled=disabled;}
function renderProbeProgress(){
 const running=!!probeSnapshot.running,total=Number(probeSnapshot.total)||0,done=Number(probeSnapshot.done)||0;
 for(const name of ['core','zcode','qoder']){
  const el=$('probe-'+name+'-progress');
  if(running&&name===probeTarget){el.hidden=false;el.textContent=`正在探测 ${done}/${total}…`;}
  else if(!running&&name===probeTarget&&total>0){el.hidden=false;el.textContent=`探测完成：${total} 个模型，结论已接入 gateway-auto 选路。`;}
  else{el.hidden=true;el.textContent='';}
 }
}
function applyProbeResults(){
 const ch=probeSnapshot.channels||{};
 if(ch.core)renderModelTable('core',modelList,{modelResults:ch.core});
 if(ch.glm&&zcodeEnabled)void loadZcode();
 if(ch.qoder&&qoderEnabled)void loadQoder();
 if(page==='overview')void loadOverview();
}
async function pollProbe(){
 if(!csrf){probeBusy=false;return;}
 try{probeSnapshot=await jsonAPI('probe');}
 catch(error){if(error.name!=='AbortError'){probeBusy=false;setProbeButtonsDisabled(false);notice(error.message);probeTarget='';renderProbeProgress();}return;}
 renderProbeProgress();
 if(probeSnapshot.running){probeTimer=setTimeout(pollProbe,2000);return;}
 // 先渲染「探测完成」提示（依赖 probeTarget），再放开按钮；提示保留到下一次探测或退出登录。
 probeBusy=false;setProbeButtonsDisabled(false);renderProbeProgress();probeTarget='';applyProbeResults();
}
async function startProbe(name){
 if(probeBusy||probeSnapshot.running)return;
 probeBusy=true;probeTarget=name;setProbeButtonsDisabled(true);notice('');renderProbeProgress();
 try{probeSnapshot=await jsonAPI('probe',{channels:[probeChannelNames[name]]});renderProbeProgress();probeTimer=setTimeout(pollProbe,2000);}
 catch(error){probeBusy=false;probeTarget='';setProbeButtonsDisabled(false);renderProbeProgress();notice(error.message);}
}
for(const name of ['core','zcode','qoder'])$('probe-'+name).addEventListener('click',()=>startProbe(name));
$('models-group-toggle').addEventListener('click',()=>{
 modelsGroupByAccount=!modelsGroupByAccount;
 const btn=$('models-group-toggle');
 btn.setAttribute('aria-pressed',String(modelsGroupByAccount));
 btn.textContent=modelsGroupByAccount?'▤ 按账号分层':'▤ 平铺列表';
 const source=modelHeatSources.core;
 renderModelTable('core',source?source.list:modelList,source?source.health:null);
});
async function restoreProbe(){
 try{
  const snap=await jsonAPI('probe');
  probeSnapshot=snap;
  if(snap.running){probeBusy=true;probeTarget='';setProbeButtonsDisabled(true);probeTimer=setTimeout(pollProbe,2000);}
  else applyProbeResults();
 }catch{/* 旧核心无端点时静默降级 */}
}
// ── 运行概览：通道总览 + 全/单通道用量 ──
// 概览只在登录与进入页面时拉一次用量账本（core 本地、廉价）；旁路通道的状态与模型数
// 直接复用各页签已加载的缓存（zcodeStatus 等），未访问过的通道如实标「未加载」，
// 不在登录时额外打旁路状态接口（保持登录轻量与既有懒加载约定）。
function overviewChannelStatus(channel){
 if(channel==='workbuddy'){
  if(!lastStatus)return{label:'未知',warn:true};
  return lastStatus.total?{label:'运行中',warn:false}:{label:'等待账号',warn:true};
 }
 const st=channel==='zcode'?zcodeStatus:channel==='qoder'?qoderStatus:opencodeStatus;
 if(!st)return{label:'未加载',warn:true};
 if(!st.enabled)return{label:'未启用',warn:true};
 return st.reachable?{label:'在线',warn:false}:{label:'不可达',warn:true};
}
function overviewChannelModelCount(channel){
 if(channel==='workbuddy')return modelList.length;
 const st=channel==='zcode'?zcodeStatus:channel==='qoder'?qoderStatus:opencodeStatus;
 if(!st||!st.enabled)return null;
 return Number(st.model_count)||0;
}
function renderOverview(){
 const body=$('overview-channels-body');body.replaceChildren();
 const byChannel=new Map();
 for(const e of overviewUsageItems){const c=usageChannelOf(e);if(!byChannel.has(c))byChannel.set(c,[]);byChannel.get(c).push(e);}
 for(const c of usageChannelOrder){
  const enabled=c==='workbuddy'||(c==='zcode'&&zcodeEnabled)||(c==='qoder'&&qoderEnabled)||(c==='opencode'&&opencodeEnabled);
  if(!enabled)continue;
  const s=summarizeUsage(byChannel.get(c)||[]);
  const status=overviewChannelStatus(c);
  const count=overviewChannelModelCount(c);
  const credit=s.calls?s.creditMissing===s.calls?'—':String(s.credit):'—';
  const tr=document.createElement('tr');
  const td=document.createElement('td');const tag=document.createElement('span');tag.className='badge'+(status.warn?' warn':'');tag.textContent=status.label;td.append(tag);
  tr.append(cell(usageChannelNames[c]),td,cell(count===null?'—':String(count)),cell(String(s.calls)),cell(String(s.prompt)),cell(String(s.completion)),cell(credit));
  body.append(tr);
 }
 const items=overviewUsageChannel==='all'?overviewUsageItems:overviewUsageItems.filter(e=>usageChannelOf(e)===overviewUsageChannel);
 const s=summarizeUsage(items);
 $('overview-usage-calls').textContent=String(s.calls);
 $('overview-usage-tokens').textContent=String(s.prompt+s.completion);
 $('overview-usage-credit').textContent=s.calls&&s.creditMissing===s.calls?'—':String(s.credit);
 const notes=[];
 if(s.usageMissing)notes.push(`${s.usageMissing} 条记录未回报 token 用量，未计入合计`);
 if(s.creditMissing)notes.push(`${s.creditMissing} 条记录未回报积分扣费，未计入合计`);
 $('overview-usage-note').textContent=notes.join('；');$('overview-usage-note').hidden=!notes.length;
 renderTrend(items);
 renderCards();
}
// cardStats/cardActions 描述每张通道卡片的统计行与快捷入口。WorkBuddy 卡片直接读账号池
// 快照（lastStatus/accounts），旁路卡片复用各页签已加载的状态缓存，缺失时如实显示「—」，
// 不伪造在线或凭据态；未启用的通道不渲染卡片（与通道总览表同一口径）。
function cardStats(c){
 if(c==='workbuddy'){
  const flight=accounts.reduce((n,a)=>n+(a.in_flight||0),0);
  return [['账号',String(lastStatus?lastStatus.total:0)],['可用',lastStatus?String(lastStatus.healthy):'—'],['冷却 / 禁用',lastStatus?`${lastStatus.cooling} / ${lastStatus.disabled}`:'—'],['在途',String(flight)],['模型',String(modelList.length)]];
 }
 const count=overviewChannelModelCount(c);
 const out=[['模型',count===null?'—':String(count)]];
 const st=c==='zcode'?zcodeStatus:c==='qoder'?qoderStatus:opencodeStatus;
 if(c==='zcode'&&st&&st.enabled)out.push(['套餐',st.plan==='coding-plan'?'Coding':st.plan==='start-plan'?'Start':'—']);
 if(c==='qoder'&&st&&st.enabled)out.push(['凭据',st.control?(st.logged_in?'已登录':'未登录'):'PAT']);
 if(c==='opencode'&&st&&st.enabled)out.push(['阶段',st.health&&st.health.phase?st.health.phase:'—']);
 return out;
}
function cardActions(c){
 if(c==='workbuddy')return [{label:'◎ 账号管理',view:'workbuddy',sub:'accounts'},{label:'↗ 对话测试',view:'workbuddy',sub:'chat'}];
 return [{label:'进入通道',view:c}];
}
// cardStatus 在通道总览口径之上细化旁路卡片徽标：控制端在线但凭据缺失时显示「未登录」，
// 已登录但代理未启动时显示「未启用」，让概览页就能看出旁路通道卡在哪一步。
function cardStatus(c){
 const base=overviewChannelStatus(c);
 if(c==='workbuddy')return base;
 const st=c==='zcode'?zcodeStatus:c==='qoder'?qoderStatus:opencodeStatus;
 if(!st||!st.enabled||!st.control)return base;
 if(!st.logged_in)return{label:'未登录',warn:true};
 if(c==='zcode'&&!st.proxy_running)return{label:'未启用',warn:true};
 return base;
}
function renderCards(){
 const wrap=$('overview-cards');if(!wrap)return;wrap.replaceChildren();
 for(const c of usageChannelOrder){
  const enabled=c==='workbuddy'||(c==='zcode'&&zcodeEnabled)||(c==='qoder'&&qoderEnabled)||(c==='opencode'&&opencodeEnabled);
  if(!enabled)continue;
  const status=cardStatus(c);
  const card=document.createElement('article');card.className='channel-card'+(status.warn?' warn':'');
  const head=document.createElement('div');head.className='channel-card-head';
  const name=document.createElement('span');name.className='channel-card-name';name.textContent=usageChannelNames[c];
  const badge=document.createElement('span');badge.className='badge'+(status.warn?' warn':'');badge.textContent=status.label;
  head.append(name,badge);card.append(head);
  const stats=document.createElement('div');stats.className='channel-card-stats';
  for(const [k,v] of cardStats(c)){const d=document.createElement('div');const s=document.createElement('span');s.textContent=k;const strong=document.createElement('strong');strong.textContent=v;d.append(s,strong);stats.append(d);}
  card.append(stats);
  const actions=document.createElement('div');actions.className='channel-card-actions';
  for(const a of cardActions(c)){const btn=document.createElement('button');btn.className='secondary';btn.textContent=a.label;btn.addEventListener('click',()=>{if(a.sub&&a.view==='workbuddy')setWorkBuddyView(a.sub);showPage(a.view);});actions.append(btn);}
  card.append(actions);
  wrap.append(card);
 }
}
// renderTrend 用内联 SVG 画「每日调用次数」折线图：全部通道时每个已启用通道一条线（配色
// 与卡片一致），筛选单通道时只画该通道。按 Asia/Shanghai 本地日分桶，缺失观测天然不计入。
// 范围不足两天时不画折线，改用文字提示，避免用单点误导趋势。
const trendColors={workbuddy:'#6366f1',zcode:'#06b6d4',qoder:'#8b5cf6',opencode:'#ec4899'};
function dayKey(ts){const date=new Date(ts*1000);return Number.isNaN(date.getTime())?'':date.toLocaleDateString('en-CA',{timeZone:'Asia/Shanghai'});}
function renderTrend(items){
 const wrap=$('overview-trend');const noteEl=$('overview-trend-note');
 wrap.replaceChildren();
 const series=new Map();
 for(const e of items){
  const c=usageChannelOf(e);const d=dayKey(Number(e.ts)||0);if(!d)continue;
  if(!series.has(c))series.set(c,new Map());
  const m=series.get(c);m.set(d,(m.get(d)||0)+1);
 }
 const days=[...new Set([...series.values()].flatMap(m=>[...m.keys()]))].sort();
 const canDraw=typeof document.createElementNS==='function';
 if(!series.size){noteEl.textContent='所选范围内没有可统计的调用记录。';noteEl.hidden=false;return;}
 if(days.length<2||!canDraw){noteEl.textContent=`所选范围只覆盖 ${days.length} 天，趋势折线需至少 2 天数据。`;noteEl.hidden=false;return;}
 noteEl.hidden=true;
 const NS='http://www.w3.org/2000/svg';
 const mk=(tag,attrs)=>{const el=document.createElementNS(NS,tag);for(const k in attrs)el.setAttribute(k,attrs[k]);return el;};
 const W=620,H=210,padL=36,padR=14,padT=14,padB=30,plotW=W-padL-padR,plotH=H-padT-padB;
 let yMax=1;for(const m of series.values())for(const v of m.values())if(v>yMax)yMax=v;
 const x=i=>padL+(days.length===1?plotW/2:i*plotW/(days.length-1));
 const y=v=>padT+plotH-(v/yMax)*plotH;
 const svg=mk('svg',{viewBox:`0 0 ${W} ${H}`,class:'trend-svg',role:'img','aria-label':'每日调用次数折线图'});
 svg.append(mk('line',{x1:padL,y1:y(0),x2:padL+plotW,y2:y(0),class:'trend-axis'}));
 svg.append(mk('line',{x1:padL,y1:y(yMax),x2:padL+plotW,y2:y(yMax),class:'trend-grid'}));
 const maxLabel=mk('text',{x:padL-6,y:y(yMax)+4,class:'trend-tick','text-anchor':'end'});maxLabel.textContent=String(yMax);svg.append(maxLabel);
 const zeroLabel=mk('text',{x:padL-6,y:y(0)+4,class:'trend-tick','text-anchor':'end'});zeroLabel.textContent='0';svg.append(zeroLabel);
 const step=Math.max(1,Math.ceil(days.length/7));
 days.forEach((d,i)=>{if(i%step&&i!==days.length-1)return;const t=mk('text',{x:x(i),y:padT+plotH+16,class:'trend-tick','text-anchor':'middle'});t.textContent=d.slice(5);svg.append(t);});
 for(const [c,m] of series){
  const pts=days.map((d,i)=>`${x(i)},${y(m.get(d)||0)}`).join(' ');
  svg.append(mk('polyline',{points:pts,fill:'none',stroke:trendColors[c]||'#6366f1','stroke-width':2,'stroke-linejoin':'round','stroke-linecap':'round'}));
  days.forEach((d,i)=>{const v=m.get(d)||0;svg.append(mk('circle',{cx:x(i),cy:y(v),r:2.6,fill:trendColors[c]||'#6366f1'}));});
 }
 wrap.append(svg);
 const legend=document.createElement('div');legend.className='trend-legend';
 for(const c of series.keys()){const item=document.createElement('span');item.className='trend-legend-item';const sw=document.createElement('i');sw.className='trend-swatch sw-'+c;const label=document.createElement('span');label.textContent=usageChannelNames[c];item.append(sw,label);legend.append(item);}
 wrap.append(legend);
}
async function loadOverview(quiet){
 if(!csrf)return;
 const generation=overviewGeneration;
 try{
  const data=await jsonAPI('usage?range='+encodeURIComponent(overviewUsageRange));
  if(generation!==overviewGeneration)return;
  overviewUsageItems=data.items||[];
  renderOverview();
 }catch(error){if(!quiet&&generation===overviewGeneration)notice(error.message);}
}
$('overview-usage-channel').addEventListener('change',event=>{overviewUsageChannel=event.target.value;renderOverview();});
$('overview-usage-range').addEventListener('change',event=>{overviewUsageRange=event.target.value;overviewGeneration++;loadOverview();});
function renderAccounts(data) {
 lastStatus = data;
 accounts = data.accounts || [];
 $('service-state').textContent = data.total ? '网关运行中' : '运行中 · 等待添加账号';
 $('accounts-body').replaceChildren(); $('accounts-empty').hidden = accounts.length > 0;
 for (const a of accounts) {
  const tr=document.createElement('tr');const state=a.disabled?'已禁用':a.cooling?'冷却中':'可用';
  const status=cell('');const badge=document.createElement('span');badge.className='badge'+(state==='可用'?'':' warn');badge.textContent=state;status.append(badge);
  const limits=(a.rate_limited_models||[]).map(m => `${m.model} 至 ${new Date(m.until).toLocaleString()}`).join('；');
  tr.append(cell(a.nickname||a.uid,`${a.realm === 'global'?'国际版':'国内版'} · ${a.uid}`),status,cell(a.credits_known ? String(a.credits) : a.credits>0 ? `${a.credits}（历史）` : '待确认'),cell(`${a.success_count||0} / ${a.err_total||0}`),cell(String(a.in_flight)),cell(limits||a.disabled_reason||a.reason||'—', a.cool_remaining_sec ? `约 ${Math.ceil(a.cool_remaining_sec/60)} 分钟后恢复` : ''), pinOpCell(a));
  $('accounts-body').append(tr);
 }
 updateModelHint();
 // 账号池每 15 秒轮询刷新，WorkBuddy 卡片的账号统计需同步重绘。
 renderCards();
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
 const switchBtn = e.target.closest?.('button[data-action="enable-model"], button[data-action="disable-model"]');
 if (switchBtn) return modelSwitchAction(switchBtn);
});
async function refreshStatus() { try { renderAccounts(await jsonAPI('status')); } catch(e) { notice(e.message); } }
function realmLabel(realm) { return realm === 'global' ? '国际版' : realm === 'cn' ? '国内版' : realm === 'glm' ? 'GLM·智谱' : realm === 'qoder' ? 'Qoder' : realm === 'opencode' ? 'OpenCode' : ''; }
function modelRealm(model) { return model.realm || (model.id.startsWith('global:') ? 'global' : model.id.startsWith('glm-') ? 'glm' : model.id.startsWith('qoder-') ? 'qoder' : model.id.startsWith('opencode-') ? 'opencode' : 'cn'); }
function modelOption(model) { const label = realmLabel(modelRealm(model)); const n = Array.isArray(model.accounts) ? model.accounts.length : 0; return `${label ? `${label} · ` : ''}${model.id}${n > 1 ? ` · ${n}账号` : ''}`; }
// accessModelOptions feeds the API-access select: core models plus the GLM
// channel models (the public /v1/models merges them, but /admin/models only
// covers core), so the access tab shows every model a client can actually call.
function accessModelOptions() {
 const items = modelList.map(m => ({...m}));
 for (const id of zcodeModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'glm'});
 for (const id of qoderModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'qoder'});
 for (const id of opencodeModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'opencode'});
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
  // 刷新目录时保留本会话已到手的手工探测结论，避免刚测完就被空健康数据覆盖回「未探测」。
  renderModelTable('core',modelList,probeSnapshot.channels&&probeSnapshot.channels.core?{modelResults:probeSnapshot.channels.core}:null);
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
function taskPageVisible(){return page==='workbuddy'&&workbuddyView==='tasks'&&csrf&&!document.hidden;}
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
document.addEventListener?.('visibilitychange',()=>{if(document.hidden)stopTaskReads();else if(page==='workbuddy'&&workbuddyView==='tasks')pollTaskRun();else if(page==='zcode'&&zcodeStatus&&zcodeStatus.control&&!zcodeStatus.logged_in)loadZcode();else if(page==='qoder'&&qoderStatus&&qoderStatus.control&&!qoderStatus.logged_in)loadQoder();});

const usageModeNames={stream:'流式',sync:'同步'};
// 通道归类：旁路通道用量由 console 以固定占位 uid 上报 core 账本（zcode/qoder/opencode），
// 其余 uid 都是核心账号池的真实账号。分布表按此口径聚合，明细按当前筛选过滤。
const usageChannelNames={workbuddy:'WorkBuddy',zcode:'GLM（Zcode）',qoder:'Qoder',opencode:'OpenCode'};
const usageChannelOrder=['workbuddy','zcode','qoder','opencode'];
function usageChannelOf(entry){const uid=entry.uid||'';return uid==='zcode'||uid==='qoder'||uid==='opencode'?uid:'workbuddy';}
// summarizeUsage 复刻 core usagelog.Summarize 口径：token 未知（<0）不累加且计入 usage_missing，
// credit 缺失（null）不累加且计入 credit_missing，缺失≠零。
function summarizeUsage(items){const s={calls:items.length,prompt:0,completion:0,credit:0,creditMissing:0,usageMissing:0};for(const e of items){if(e.prompt_tokens>=0)s.prompt+=e.prompt_tokens;else s.usageMissing++;if(e.completion_tokens>=0)s.completion+=e.completion_tokens;if(e.credit!=null)s.credit+=e.credit;else s.creditMissing++;}return s;}
function formatUsageTokens(value){return value<0?'—':String(value);}
function formatUsageCredit(value){return value==null||value===undefined?'—':String(value);}
function renderUsage(data){
 const all=(data.items||[]);
 const items=all.filter(e=>usageChannel==='all'||usageChannelOf(e)===usageChannel).slice().reverse();
 const summary=summarizeUsage(items);
 $('usage-calls').textContent=String(summary.calls);
 $('usage-tokens').textContent=String(summary.prompt+summary.completion);
 $('usage-credit').textContent=String(summary.credit);
 const notes=[];
 if(summary.usageMissing)notes.push(`${summary.usageMissing} 条记录未回报 token 用量，明细按「—」展示`);
 if(summary.creditMissing)notes.push(`${summary.creditMissing} 条记录未回报积分扣费，未计入合计`);
 $('usage-note').textContent=notes.join('；');$('usage-note').hidden=!notes.length;
 const byChannel=new Map();
 for(const e of all){const c=usageChannelOf(e);if(!byChannel.has(c))byChannel.set(c,[]);byChannel.get(c).push(e);}
 const distBody=$('usage-by-channel-body');distBody.replaceChildren();
 const distRows=usageChannelOrder.filter(c=>byChannel.has(c));
 $('usage-by-channel-empty').hidden=distRows.length>0;
	for(const c of distRows){const s=summarizeUsage(byChannel.get(c));const credit=s.creditMissing===s.calls?'—':String(s.credit);const tr=document.createElement('tr');tr.append(cell(usageChannelNames[c]),cell(String(s.calls)),cell(String(s.prompt)),cell(String(s.completion)),cell(credit));distBody.append(tr);}
 const body=$('usage-body');body.replaceChildren();$('usage-empty').hidden=items.length>0;
 for(const e of items){
  const tr=document.createElement('tr');
  tr.append(cell(formatTaskTime(e.ts*1000)),cell(usageChannelNames[usageChannelOf(e)]),cell(e.account||e.uid||'—'),cell(e.model||'—'),cell(usageModeNames[e.mode]||e.mode||'—'),cell(formatUsageTokens(e.prompt_tokens)),cell(formatUsageTokens(e.completion_tokens)),cell(formatUsageCredit(e.credit)));
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
$('usage-channel').addEventListener('change',event=>{usageChannel=event.target.value;usageGeneration++;loadUsage();});
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
  $('zcode-status-text').textContent=status.reachable?`zcode-proxy 可达 · ${status.model_count} 个 GLM 模型`:'zcode-proxy 当前不可达，请确认容器已启动；若容器在运行，通常是 console 刚被重启、旁路网络失联，重启旁路容器即可恢复。';
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
 renderModelTable('zcode',status.models,status.health);
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
  statusText=status.reachable?`qoder-proxy 可达 · ${status.model_count} 个模型${extra}`:`qoder-proxy 当前不可达，请确认容器已启动；若容器在运行，通常是 console 刚被重启、旁路网络失联，重启旁路容器即可恢复。${extra}`;
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
 renderModelTable('qoder',status.models,status.health);
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

async function loadOpenCode(){
 if(!csrf)return;
 try{renderOpenCode(await jsonAPI('opencode'));}
 catch(error){notice(error.message);}
}
function renderOpenCode(status){
 opencodeStatus=status;
 const enabled=!!status.enabled;
 $('opencode-disabled').hidden=enabled;
 $('opencode-content').hidden=!enabled;
 if(!enabled)return;
 const badge=$('opencode-status-badge');
 badge.textContent=status.reachable?'在线':'不可达';
 badge.className='badge'+(status.reachable?'':' warn');
 let statusText=status.reachable?`OW Bridge 可达 · ${status.model_count} 个免费模型`:'OW Bridge 当前不可达，请确认容器已启动（首次启动需下载 opencode 运行时，可能耗时数分钟）；若容器在运行，通常是 console 刚被重启、旁路网络失联，重启旁路容器即可恢复。';
 const health=status.health;
 if(health){
  const message=typeof health.message==='string'?health.message:'';
  if(message)statusText+=` · ${message}`;
  const body=$('opencode-health-body');body.replaceChildren();
  const rows=[['phase',health.phase],['version',health.version],['opencode 版本',health.opencodeVersion],['endpoint',health.endpoint]];
  const results=health.modelResults&&typeof health.modelResults==='object'?Object.entries(health.modelResults):[];
  if(results.length){
   const ok=results.filter(([,r])=>r&&r.ok===true).length;
   rows.push(['模型探测',`${ok}/${results.length} 通过`]);
  }
  if(health.probe&&health.probe.running)rows.push(['探测进行中',String(health.probe.current||'')]);
  if(Array.isArray(health.availableModels))rows.push(['可用模型',`${health.availableModels.length} 个`]);
  const sync=health.sync&&typeof health.sync==='object'?health.sync:null;
  if(sync)rows.push(['模型同步',sync.skipped?'本次跳过':`已同步${Number.isFinite(Number(sync.count))?` ${Number(sync.count)} 个`:''}`]);
  const last=health.lastRequest&&typeof health.lastRequest==='object'?health.lastRequest:null;
  if(last&&last.model)rows.push(['最近请求',`${last.model} · ${last.ok===true?'成功':'失败'}${last.category?`（${modelFailureLabels[last.category]||last.category}）`:''}`]);
  if(health.updatedAt)rows.push(['更新时间',formatProbeTime(health.updatedAt)]);
  for(const [k,v] of rows){
   if(v===undefined||v===null||v==='')continue;
   const tr=document.createElement('tr');
   tr.append(cell(String(k)),cell(String(v)));
   body.append(tr);
  }
 }
 $('opencode-health-wrap').hidden=!health;
 $('opencode-status-text').textContent=statusText;
 const dataTab=!!status.reachable;
 if(opencodeView!=='status'&&!dataTab)opencodeView='status';
 $('opencode-view-models').hidden=!dataTab;
 $('opencode-view-chat').hidden=!dataTab;
 for(const view of ['status','models','chat']){
  $('opencode-view-'+view).classList.toggle('active',view===opencodeView);
  $('opencode-pane-'+view).hidden=view!==opencodeView;
 }
 opencodeModels=(status.models||[]).map(item=>item.id).filter(Boolean);
 renderAccessModelSelect();renderAccess();
 const previous=$('opencode-model').value;
 $('opencode-model').replaceChildren();
 for(const id of opencodeModels)$('opencode-model').append(new Option(id,id));
 if(opencodeModels.includes(previous))$('opencode-model').value=previous;
 renderModelTable('opencode',status.models,health);
 $('opencode-model').disabled=!opencodeModels.length||!!activeRequest;
 $('opencode-send').disabled=!opencodeModels.length||!!activeRequest;
 opencodeControls();
 opencodeActionControls();
}
function opencodeControls(){
 for(const id of ['opencode-model','opencode-prompt','opencode-send'])$(id).disabled=!!activeRequest;
 $('opencode-stop').hidden=!activeRequest;
}
function opencodeActionControls(){
 $('opencode-refresh').disabled=opencodeBusy;
}
$('opencode-refresh').addEventListener('click',async()=>{
 if(opencodeBusy)return;
 const generation=sessionGeneration;opencodeBusy=true;opencodeActionControls();
 try{await loadOpenCode();}finally{if(generation===sessionGeneration){opencodeBusy=false;opencodeActionControls();}}
});
for(const view of ['status','models','chat'])$('opencode-view-'+view).addEventListener('click',()=>{opencodeView=view;if(opencodeStatus)renderOpenCode(opencodeStatus);});
$('opencode-stop').addEventListener('click',()=>activeRequest?.abort());
$('opencode-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('opencode-prompt').value.trim();if(!text||!$('opencode-model').value)return;
 const generation=sessionGeneration;
 notice('');message('user',text,$('opencode-messages'));$('opencode-prompt').value='';const answer=message('assistant','正在等待模型…',$('opencode-messages'));
 const controller=new AbortController();activeRequest=controller;opencodeControls();
 let content='',usage,finished=false;const outgoing=[...opencodeHistory,{role:'user',content:text}];
 try{
  const response=await api('opencode/chat',{model:$('opencode-model').value,messages:outgoing,stream:true},controller.signal);
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
  opencodeHistory=[...outgoing,{role:'assistant',content}];
  $('opencode-usage').textContent=(usage?`输入 ${usage.prompt_tokens??'—'} · 输出 ${usage.completion_tokens??'—'} · 总计 ${usage.total_tokens??'—'} tokens`:'上游未返回用量')+' · 本次通道：OpenCode（免费模型）';
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=(content?content+'\n\n':'')+msg;$('opencode-usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;opencodeControls();}
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

// The first-run copy of a transcript lives in the HTML as .chat-empty, so its
// wording has one source. Clearing must put that node back rather than leave a
// blank pane: an empty messages box reads as a broken panel, not as an invitation.
const chatPlaceholders=new WeakMap();
function clearMessages(container){
 let ph=container.querySelector('.chat-empty');
 if(!ph)ph=chatPlaceholders.get(container);
 if(!ph){container.replaceChildren();return;}
 chatPlaceholders.set(container,ph);
 container.replaceChildren(ph);
}
function message(role,text,container){
 container=container||$('messages');container.querySelector('.chat-empty')?.remove();const el=document.createElement('div');el.className='message '+role;
 const label=document.createElement('span');label.className='role';label.textContent=role==='user'?'你':'ASSISTANT';const content=document.createElement('div');content.textContent=text;
 el.append(label,content);container.append(el);el.scrollIntoView({block:'nearest'});return{el,content};
}
function clearChat(){history=[];conversation=newConversation();clearMessages($('messages'));$('usage').textContent='用量将在上游返回后显示';}
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
 const isOpenCode=model.startsWith('opencode-');
 if(anthropic&&(isGlm||isQoder||isOpenCode)){$('copy-example').disabled=true;$('api-example').textContent=(isGlm?'GLM':isQoder?'Qoder':'OpenCode')+' 通道仅支持 OpenAI 协议（/v1/chat/completions），请切换协议后复制示例。';return;}
 const body={model,...(anthropic?{max_tokens:1024}:{}),messages:[{role:'user',content:'你好'}],stream:true};
 const auth=anthropic?'  -H "x-api-key: <你的 API Key>" \\\n  -H "anthropic-version: 2023-06-01"':'  -H "Authorization: Bearer <你的 API Key>"';
 const json=JSON.stringify(body,null,2).replace(/'/g,"'\\''");
 let prefix='';
 if(isGlm) prefix='# GLM 模型经 zcode 通道转发（按 glm- 前缀分流），账号为容器登录态\ncurl ';
 else if(isQoder) prefix='# Qoder 模型经 qoder 通道转发（按 qoder- 前缀分流）\ncurl ';
 else if(isOpenCode) prefix='# OpenCode 免费模型经 OW Bridge sidecar 转发（按 opencode- 前缀分流）\ncurl ';
 else if(!anthropic) prefix='# 成功响应头 X-Account / X-Account-Realm 标明本次服务的账号与平台\ncurl ';
 else prefix='curl ';
 $('api-example').textContent=prefix+$('api-endpoint').value+` \\\n${auth} \\\n  -H "Content-Type: application/json" \\\n  -d '${json}'`;
}
document.querySelectorAll('[data-protocol]').forEach(button=>button.addEventListener('click',()=>setProtocol(button.dataset.protocol)));
$('access-model').addEventListener('change',renderAccess);
$('copy-example').addEventListener('click',async()=>{if($('copy-example').disabled)return;try{await navigator.clipboard.writeText($('api-example').textContent);notice('调用示例已复制，请替换 API Key 占位符');}catch{notice('浏览器不允许自动复制，请手动复制下方示例');}});
$('go-chat').addEventListener('click',()=>{if(activeRequest||$('go-chat').disabled)return;const model=$('access-model').value;if(model.startsWith('glm-')){if(zcodeModels.includes(model))$('zcode-model').value=model;showPage('zcode');return;}if(model.startsWith('qoder-')){if(qoderModels.includes(model))$('qoder-model').value=model;showPage('qoder');return;}if(model.startsWith('opencode-')){if(opencodeModels.includes(model))$('opencode-model').value=model;showPage('opencode');return;}$('model').value=model;updateEfforts();setWorkBuddyView('chat');showPage('workbuddy');});
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
for(const [inputId,formId] of [['prompt','chat-form'],['zcode-prompt','zcode-form'],['qoder-prompt','qoder-form'],['opencode-prompt','opencode-form']])$(inputId).addEventListener('keydown',event=>{
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
// Agent 接入（路由别名）。后端契约见 console/route.go：
// GET /admin/route 返回 {enabled, auto_model, auto_fallback, default_auto_fallback,
// auto_preview, aliases, channels}；POST /admin/route/save 接收
// {models:[{alias,channel,model,enabled,note}], auto_fallback}。
// 路由判定完全在服务端，页面只负责展示与编辑；保存即生效，无需重启。
const routeChannelLabels={core:'核心账号池',glm:'GLM 通道',qoder:'Qoder 通道',opencode:'OpenCode 通道'};
function routeChannelLabel(channel){return routeChannelLabels[channel]||channel||'—';}
async function loadRoutes(){
 if(!csrf)return;
 try{renderRoutes(await jsonAPI('route'));}
 catch(error){notice(error.message);}
}
function renderRoutes(state){
 routesState=state;
 const enabled=!!state.enabled;
 $('routes-disabled').hidden=enabled;$('routes-content').hidden=!enabled;
 if(!enabled)return;
 const preview=state.auto_preview||{};
 const badge=$('routes-auto-badge');
 if(preview.routed_model&&!preview.fallback){badge.textContent='自动优选';badge.className='badge';}
 else if(preview.routed_model){badge.textContent='用兜底模型';badge.className='badge warn';}
 else {badge.textContent='未知';badge.className='badge warn';}
 $('routes-auto-text').textContent=preview.routed_model?`现在自动挑中的是 ${preview.routed_model}（${routeChannelLabel(preview.channel)}）· ${preview.reason||''}`:'暂时看不出 auto 会挑哪个模型。';
 $('routes-default-fallback').textContent=state.default_auto_fallback||'cn:auto';
 $('routes-auto-fallback').value=state.auto_fallback||'';
 const select=$('routes-alias-channel'),previous=select.value;
 select.replaceChildren();
 for(const channel of state.channels||[])if(channel.enabled)select.appendChild(new Option(routeChannelLabel(channel.channel),channel.channel));
 const enabledChannels=(state.channels||[]).filter(channel=>channel.enabled).map(channel=>channel.channel);
 if(enabledChannels.includes(previous))select.value=previous;
 fillRouteModelOptions(select.value);
 renderRouteAliases(state.aliases||[]);
 renderRouteChannels(state.channels||[]);
 renderRouteExample(state);
}
function renderRouteAliases(aliases){
 const body=$('routes-alias-body');body.replaceChildren();
 $('routes-alias-empty').hidden=aliases.length>0;
 for(const entry of aliases){
  const tr=document.createElement('tr');
  tr.append(cell(entry.alias),cell(routeChannelLabel(entry.channel)),cell(entry.model),cell(entry.public_model),badgeCell(entry.enabled?'启用':'停用',!entry.enabled),cell(entry.note||'—'));
  const actions=document.createElement('td');
  const edit=document.createElement('button');edit.className='secondary';edit.textContent='编辑';edit.addEventListener('click',()=>startRouteEdit(entry));
  const toggle=document.createElement('button');toggle.className='secondary';toggle.textContent=entry.enabled?'停用':'启用';toggle.addEventListener('click',()=>saveRouteAliases(currentRouteAliases().map(item=>item.alias===entry.alias?{...item,enabled:!entry.enabled}:item)));
  const remove=document.createElement('button');remove.className='quiet';remove.textContent='删除';let removeArmed=false;
  remove.addEventListener('click',()=>{
   if(!removeArmed){removeArmed=true;remove.textContent='再次点击确认删除';return;}
   saveRouteAliases(currentRouteAliases().filter(item=>item.alias!==entry.alias));
  });
  actions.append(edit,toggle,remove);tr.append(actions);body.append(tr);
 }
}
function currentRouteAliases(){
 return ((routesState&&routesState.aliases)||[]).map(entry=>({alias:entry.alias,channel:entry.channel,model:entry.model,enabled:entry.enabled!==false,note:entry.note||''}));
}
function renderRouteChannels(channels){
 const container=$('routes-channels');container.replaceChildren();
 for(const channel of channels){
  const article=document.createElement('article');article.className='panel';
  const title=document.createElement('h3');title.textContent=routeChannelLabel(channel.channel);article.append(title);
  const meta=document.createElement('p');meta.className='muted small';
  meta.textContent=channel.enabled?(channel.reachable?`${(channel.models||[]).length} 个模型`:'通道当前不可达'):'通道未启用';
  article.append(meta);
  const list=document.createElement('p');list.className='badge-row';
  for(const model of channel.models||[]){
   const tag=document.createElement('button');tag.type='button';tag.className='badge';tag.textContent=model;
   tag.addEventListener('click',()=>pickRouteModel(channel.channel,model));
   list.append(tag);
  }
  article.append(list);container.append(article);
 }
}
function pickRouteModel(channel,model){
 $('routes-alias-channel').value=channel;fillRouteModelOptions(channel);$('routes-alias-model').value=model;
}
function fillRouteModelOptions(channel){
 const list=$('routes-model-options');list.replaceChildren();
 const source=((routesState&&routesState.channels)||[]).find(item=>item.channel===channel);
 for(const model of (source&&source.models)||[])list.appendChild(new Option(model,model));
}
function renderRouteExample(state){
 const alias=((state.aliases||[]).find(entry=>entry.enabled)||{}).alias||state.auto_model||'gateway-auto';
 const body={model:alias,messages:[{role:'user',content:'你好'}],stream:true};
 $('routes-example').textContent=`curl ${location.origin}/v1/chat/completions \\\n  -H "Authorization: Bearer <你的 API Key>" \\\n  -H "Content-Type: application/json" \\\n  -d '${JSON.stringify(body,null,2)}'`;
}
async function saveRouteAliases(models){
 if(routesBusy)return;
 const generation=sessionGeneration;routesBusy=true;$('routes-alias-save').disabled=true;
 try{
  const result=await jsonAPI('route/save',{models,auto_fallback:$('routes-auto-fallback').value.trim()});
  if(generation!==sessionGeneration)return;
  routesEditing=null;$('routes-alias-name').value='';$('routes-alias-model').value='';$('routes-alias-note').value='';
  renderRoutes(result);notice('路由配置已保存，下一次请求即生效');
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){routesBusy=false;$('routes-alias-save').disabled=false;}}
}
function startRouteEdit(entry){
 routesEditing=entry.alias;
 $('routes-alias-name').value=entry.alias;$('routes-alias-channel').value=entry.channel;
 fillRouteModelOptions(entry.channel);
 $('routes-alias-model').value=entry.model;$('routes-alias-note').value=entry.note||'';
}
$('routes-alias-form').addEventListener('submit',async event=>{
 event.preventDefault();if(routesBusy)return;
 const alias=$('routes-alias-name').value.trim(),channel=$('routes-alias-channel').value,model=$('routes-alias-model').value.trim(),note=$('routes-alias-note').value.trim();
 if(!alias||!model||!channel)return;
 let models=currentRouteAliases();
 if(routesEditing&&routesEditing!==alias)models=models.filter(item=>item.alias!==routesEditing);
 const index=models.findIndex(item=>item.alias===alias);
 if(index>=0)models[index]={...models[index],channel,model,note};
 else models=[...models,{alias,channel,model,enabled:true,note}];
 await saveRouteAliases(models);
});
$('routes-alias-reset').addEventListener('click',()=>{routesEditing=null;$('routes-alias-name').value='';$('routes-alias-model').value='';$('routes-alias-note').value='';});
$('routes-fallback-form').addEventListener('submit',async event=>{event.preventDefault();await saveRouteAliases(currentRouteAliases());});
$('routes-alias-channel').addEventListener('change',()=>fillRouteModelOptions($('routes-alias-channel').value));
$('routes-refresh').addEventListener('click',()=>loadRoutes());
$('routes-copy-example').addEventListener('click',async()=>{try{await navigator.clipboard.writeText($('routes-example').textContent);notice('配置片段已复制，请替换 API Key 占位符');}catch{notice('浏览器不允许自动复制，请手动复制下方片段');}});
$('reveal-key').addEventListener('click',async()=>{try{$('api-key').value=(await jsonAPI('access',{})).api_key;$('api-key').type='text';}catch(e){notice(e.message);}});
$('copy-key').addEventListener('click',async()=>{const generation=sessionGeneration;try{if(!$('api-key').value)$('api-key').value=(await jsonAPI('access',{})).api_key;await navigator.clipboard.writeText($('api-key').value);if(generation===sessionGeneration)notice('API Key 已复制');}catch{if(generation!==sessionGeneration)return;$('api-key').type='text';$('api-key').select();notice('浏览器不允许自动复制，请手动复制选中的密钥');}});
jsonAPI('session').then(signedIn).catch(()=>{});

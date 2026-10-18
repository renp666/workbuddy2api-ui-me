const $ = (id) => document.getElementById(id);
let csrf = '', modelList = [], accounts = [], history = [], conversation = newConversation(), activeRequest, flowID, flowTimer;
// pinState：{cn:{uid,nickname,exists}|null, global:null|...}，null=该平台未锁定。
let pinState = {cn:null, global:null}, lastStatus = null;
let page = 'overview', workbuddyView = 'accounts', sessionGeneration = 0;
let protocol = 'openai';
let taskState = {items:[],active_run:null,latest_runs:[]}, taskHistory = [], taskBefore = null, taskStarting = false, taskPollTimer, taskRenderKey, detailController, detailGeneration = 0;
let usageRange = 'today', usageChannel = 'all', usageGeneration = 0;
let usagePage = 1, usagePageSize = 50, usageDetailRows = [];
let zcodeModels = [], zcodeHistory = [], zcodeStatus = null, zcodePollTimer, zcodeBusy = false, zcodeAuthURL = '', zcodeLogoutArmed = false, zcodeProviderTouched = false, zcodePlanTouched = false, zcodeView = 'status', zcodeViewTouched = false, zcodeEnabled = false;
let qoderModels = [], qoderHistory = [], qoderStatus = null, qoderPollTimer, qoderBusy = false, qoderAuthURL = '', qoderLogoutArmed = false, qoderControl = false, qoderLoggedIn = false, qoderLoginTimer = null, qoderView = 'status', qoderViewTouched = false, qoderEnabled = false;
let opencodeModels = [], opencodeHistory = [], opencodeStatus = null, opencodeBusy = false, opencodeView = 'status', opencodeEnabled = false;
let freellmModels = [], freellmHistory = [], freellmStatus = null, freellmBusy = false, freellmEnabled = false, freellmView = 'status', freellmViewTouched = false;
let ccrStatus = null, ccrEnabled = false, ccrBusy = false;
let autoclawModels = [], autoclawHistory = [], autoclawStatus = null, autoclawBusy = false, autoclawView = 'status', autoclawEnabled = false, autoclawAccounts = [], autoclawLoginBusy = false, autoclawCodeTimer = null;
let traeModels = [], traeHistory = [], traeStatus = null, traeBusy = false, traeView = 'status', traeEnabled = false, traeAccounts = [], traeLoginBusy = false, traePollTimer = null, traeAuthURL = '';
// traeai 是 trae 同镜像的国际版实例（WB2A_TRAEAI_URL，traeai- 前缀）。
// traeaiEntitlements：uid → {subscription,error,fast_request_per}，只在点「刷新权益」
// 之后才有内容；权益（档位）不在 /status 里，容器只在 /admin/traeai/credits 回报。
let traeaiModels = [], traeaiHistory = [], traeaiStatus = null, traeaiBusy = false, traeaiView = 'status', traeaiEnabled = false, traeaiAccounts = [], traeaiLoginBusy = false, traeaiPollTimer = null, traeaiAuthURL = '', traeaiEntitlements = new Map();
let raccoonModels = [], raccoonHistory = [], raccoonStatus = null, raccoonBusy = false, raccoonView = 'status', raccoonEnabled = false, raccoonAccounts = [];
let routesState = null, routesBusy = false, routesEditing = null, routeTestBusy = false;
let probeSnapshot = {running:false,done:0,total:0,channels:{}}, probeTarget = '', probeTimer, probeBusy = false;
// modelsGroupByAccount：core 模型表是否按可用账号分层展示（默认开）。
let modelsGroupByAccount = true;
let overviewUsageRange = 'today', overviewUsageChannel = 'all', overviewUsageItems = [], overviewGeneration = 0;
// healthState 是 GET /admin/health 的最近一轮结论（console 自己探的网关与全部已登记通道）。
let healthState = null, healthGeneration = 0, healthPollTimer;
// errorsWindow 是错误统计区块当前的窗口档（15m/1h/24h，默认 15m，与后端默认一致）；
// errorsState 是 GET /admin/errors 的最近一份聚合（成功+失败样本，重启从零累计）。
let errorsWindow = '15m', errorsState = null, errorsGeneration = 0;
let modelHealth = new Map();
let selectedTaskRun, historyGeneration = 0, historyLoading = false, taskHistoryKey;
const taskIntents = new Map(), taskReads = new Set();
function newConversation() { return `web-${Date.now()}-${Math.random().toString(36).slice(2)}`; }
function notice(text = '') { $('notice').textContent = text; $('notice').hidden = !text; $('announce').textContent = text; }
// runTopRefresh 给顶栏刷新按钮加统一反馈：请求在飞时禁用并改写成「刷新中…」，落定后恢复原文案，
// 并在旁边记下这次刷新的完成时刻。此前点完按钮没有任何可见变化，数据与上一轮一样时根本看不出
// 刷没刷（问题5）。失败时各加载器自己 notice 报错，这里只负责「点了有反应」和「刚刷过」。
function runTopRefresh(button,action){
 button.addEventListener('click',async()=>{
  if(button.disabled)return;
  const label=button.textContent;
  button.disabled=true;button.textContent='↻ 刷新中…';
  try{await action();const stamp=$('refresh-stamp');stamp.textContent=`最近刷新 ${new Date().toLocaleTimeString('zh-CN',{hour12:false})}`;stamp.hidden=false;}
  finally{button.disabled=false;button.textContent=label;}
 });
}
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
 freellmModels=[];freellmHistory=[];freellmStatus=null;freellmEnabled=false;freellmBusy=false;freellmView='status';freellmViewTouched=false;$('freellm-model').replaceChildren();clearMessages($('freellm-messages'));$('freellm-prompt').value='';$('freellm-usage').textContent='用量将在上游返回后显示';$('freellm-disabled').hidden=true;$('freellm-content').hidden=true;$('nav-freellm').classList.remove('nav-muted');ccrStatus=null;ccrEnabled=false;ccrBusy=false;$('ccr-disabled').hidden=true;$('ccr-content').hidden=true;$('nav-ccr').classList.remove('nav-muted');
 autoclawModels=[];autoclawHistory=[];autoclawStatus=null;autoclawEnabled=false;autoclawBusy=false;autoclawLoginBusy=false;autoclawView='status';autoclawAccounts=[];clearTimeout(autoclawCodeTimer);autoclawCodeTimer=null;$('autoclaw-model').replaceChildren();clearMessages($('autoclaw-messages'));$('autoclaw-prompt').value='';$('autoclaw-usage').textContent='用量将在上游返回后显示';$('autoclaw-disabled').hidden=true;$('autoclaw-content').hidden=true;$('nav-autoclaw').classList.remove('nav-muted');$('autoclaw-health-wrap').hidden=true;$('autoclaw-health-body').replaceChildren();$('autoclaw-accounts-body').replaceChildren();$('autoclaw-phone').value='';$('autoclaw-code').value='';$('autoclaw-oauth-url').value='';$('autoclaw-login-hint').textContent='';$('autoclaw-global-hint').textContent='';const sc=$('autoclaw-sendcode');sc.disabled=false;sc.textContent='发送验证码';
 traeModels=[];traeHistory=[];traeStatus=null;traeEnabled=false;traeBusy=false;traeLoginBusy=false;traeView='status';traeAccounts=[];traeAuthURL='';clearTimeout(traePollTimer);traePollTimer=null;$('trae-model').replaceChildren();clearMessages($('trae-messages'));$('trae-prompt').value='';$('trae-usage').textContent='用量将在上游返回后显示';$('trae-disabled').hidden=true;$('trae-content').hidden=true;$('nav-trae').classList.remove('nav-muted');$('trae-health-wrap').hidden=true;$('trae-health-body').replaceChildren();$('trae-accounts-body').replaceChildren();$('trae-auth-row').hidden=true;$('trae-login-hint').textContent='';$('trae-import-url').value='';$('trae-import-hint').textContent='';$('trae-schedule-body').replaceChildren();$('trae-schedule-hint').textContent='';$('trae-usage-body').replaceChildren();
 traeaiModels=[];traeaiHistory=[];traeaiStatus=null;traeaiEnabled=false;traeaiBusy=false;traeaiLoginBusy=false;traeaiView='status';traeaiAccounts=[];traeaiAuthURL='';traeaiEntitlements=new Map();clearTimeout(traeaiPollTimer);traeaiPollTimer=null;$('traeai-model').replaceChildren();clearMessages($('traeai-messages'));$('traeai-prompt').value='';$('traeai-usage').textContent='用量将在上游返回后显示';$('traeai-disabled').hidden=true;$('traeai-content').hidden=true;$('nav-traeai').classList.remove('nav-muted');$('traeai-health-wrap').hidden=true;$('traeai-health-body').replaceChildren();$('traeai-accounts-body').replaceChildren();$('traeai-auth-row').hidden=true;$('traeai-login-hint').textContent='';$('traeai-import-url').value='';$('traeai-import-hint').textContent='';$('traeai-schedule-body').replaceChildren();$('traeai-schedule-hint').textContent='';$('traeai-usage-body').replaceChildren();$('traeai-edition-note').textContent='';$('traeai-edition-note').hidden=true;
 raccoonModels=[];raccoonHistory=[];raccoonStatus=null;raccoonEnabled=false;raccoonBusy=false;raccoonView='status';raccoonAccounts=[];$('raccoon-model').replaceChildren();clearMessages($('raccoon-messages'));$('raccoon-prompt').value='';$('raccoon-usage').textContent='用量将在上游返回后显示';$('raccoon-disabled').hidden=true;$('raccoon-content').hidden=true;$('nav-raccoon').classList.remove('nav-muted');$('raccoon-health-wrap').hidden=true;$('raccoon-health-body').replaceChildren();$('raccoon-accounts-body').replaceChildren();$('raccoon-import-json').value='';$('raccoon-import-hint').textContent='';
 routesState=null;routesBusy=false;routesEditing=null;routeTestBusy=false;modelSwitchOverrides=new Map();capState=null;capByKeys=new Map();capDim='coding';capCategory='';capFilter='';$('routes-alias-test-progress').hidden=true;$('routes-alias-test-progress').textContent='';$('routes-disabled').hidden=true;$('routes-content').hidden=true;$('routes-alias-body').replaceChildren();$('routes-channels').replaceChildren();$('routes-alias-model').replaceChildren();$('routes-alias-model-custom').value='';$('routes-alias-model-custom').hidden=true;$('routes-alias-editing').hidden=true;$('routes-alias-editing').textContent='';$('routes-example').textContent='';$('routes-alias-name').value='';$('routes-alias-model').value='';$('routes-alias-note').value='';$('routes-auto-fallback').value='';$('cap-filter').value='';
 probeSnapshot={running:false,done:0,total:0,channels:{}};probeTarget='';probeBusy=false;clearTimeout(probeTimer);probeTimer=undefined;for(const n of Object.keys(probeChannelNames)){$('probe-'+n+'-progress').hidden=true;$('probe-'+n+'-progress').textContent='';}
$('refresh-stamp').hidden=true;$('refresh-stamp').textContent='';
 overviewUsageItems=[];overviewGeneration++;healthState=null;healthGeneration++;clearTimeout(healthPollTimer);healthPollTimer=undefined;
 modelHealth.clear();errorsState=null;errorsGeneration++;errorsWindow='15m';setErrorsWindowButtons();
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
 freellmEnabled = !!session.freellm_enabled; $('nav-freellm').classList.toggle('nav-muted', !freellmEnabled);
 ccrEnabled = !!session.ccr_enabled; $('nav-ccr').classList.toggle('nav-muted', !ccrEnabled);
 autoclawEnabled = !!session.autoclaw_enabled; $('nav-autoclaw').classList.toggle('nav-muted', !autoclawEnabled);
 traeEnabled = !!session.trae_enabled; $('nav-trae').classList.toggle('nav-muted', !traeEnabled);
 raccoonEnabled = !!session.raccoon_enabled; $('nav-raccoon').classList.toggle('nav-muted', !raccoonEnabled);
 await refreshStatus(); await refreshModels(); await loadPins();
 void loadModelHeat(); void loadModelSwitch(); void loadGlobalHeat(); void loadOverview(true); void restoreProbe(); void loadHealth(true); void loadErrorStats(true);
}
const pageNames = {overview:'运行概览',workbuddy:'WorkBuddy 通道',zcode:'Zcode 通道',qoder:'Qoder 通道',opencode:'OpenCode 通道',freellm:'FreeLLMAPI 通道',ccr:'CCR 网关',autoclaw:'AutoClaw 通道',trae:'Trae 通道',traeai:'Trae 国际版通道',raccoon:'小浣熊通道',usage:'调用统计',routes:'Agent 接入',access:'API 接入'};
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
 if (page === 'overview' && value !== 'overview') stopHealthPoll();
 page = value;
 document.querySelectorAll('[data-page]').forEach(el => el.hidden = el.dataset.page !== page);
 document.querySelectorAll('.nav').forEach(el => el.classList.toggle('active', el.dataset.view === page));
 // 刷新按钮吸在顶栏，一次只留当前页那一个：留着别页的按钮会把人带去刷错的通道。
 document.querySelectorAll('[data-refresh-for]').forEach(el => el.hidden = el.dataset.refreshFor !== page);
 // Every caller is a deliberate navigation (nav button, channel card, 前往测试).
 // Without this the new page opens at the previous page's scroll depth, so a
 // short page like API 接入 can land mid-form with its heading above the fold.
 window.scrollTo(0, 0);
 renderBreadcrumb();
 if (page !== 'access') { $('api-key').value = ''; $('api-key').type = 'password'; }
 if (page === 'workbuddy') setWorkBuddyView(workbuddyView);
 if (page === 'workbuddy' && workbuddyView === 'tasks') loadTaskPage();
 if (page === 'overview') { loadOverview(); loadHealth(); loadErrorStats(); loadMissingChannelStatuses(); scheduleHealthPoll(); }
 if (page === 'usage') { loadUsage(); loadUsageDaily(); }
 if (page === 'zcode') loadZcode();
 if (page === 'qoder') loadQoder();
 if (page === 'opencode') loadOpenCode();
 if (page === 'freellm') loadFreellm();
 if (page === 'ccr') loadCCR();
 if (page === 'autoclaw') loadAutoClaw();
 if (page === 'trae') loadTrae();
 if (page === 'traeai') loadTraeai();
 if (page === 'raccoon') loadRaccoon();
 if (page === 'routes') loadRoutes();
 // The access tab lists GLM and Qoder channel models too; fetch statuses once lazily
 // so its select is complete even when the operator never opens those tabs.
 if (page === 'access') loadMissingChannelStatuses();
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
// numCell 数值列：等宽右对齐才能竖着比位数，表头要挂同样的 class 才对得齐。
// 小字副行（如耗时是网页测的还是上游回报的）跟在数字下面，不再另起一列。
function numCell(text, small) { const td = document.createElement('td'); td.className = 'num'; td.textContent = text; if (small) { const s=document.createElement('small');s.textContent=small;td.append(s); } return td; }
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
// channelHasCredits 报告这条通道是否采集积分倍率。opencode/autoclaw/raccoon 是
// 免费通道，上游不下发倍率——它们照常参与模型停用开关，只是没有「积分消耗」列
// （缺失的倍率不能拿「未知」冒充，更不能伪造 0），排序也沿用探测状态口径。
function channelHasCredits(channel){return channel!=='opencode'&&channel!=='autoclaw'&&channel!=='raccoon'&&channel!=='freellm';}
// modelSwitchEffective 报告行的当前生效状态：人工覆盖优先；不在覆盖表里的模型
// 一律视为启用——调用是「统一对外开放」的，未配置的直名默认放行，只有显式停用
// （写入禁用表）才拒绝。倍率不参与默认态判定（trae 的倍率是套餐额度消耗率，
// 免费三通道根本没有倍率，都当不了停用依据）。
function modelSwitchEffective(row){
 if(modelSwitchOverrides.has(row.id))return modelSwitchOverrides.get(row.id);
 return true;
}
const modelChannels={zcode:{body:'zcode-models-body',empty:'zcode-models-empty',namePrefix:'GLM · '},
 qoder:{body:'qoder-models-body',empty:'qoder-models-empty',namePrefix:'Qoder · '},
 opencode:{body:'opencode-models-body',empty:'opencode-models-empty',namePrefix:'opencode-OC · '},
 freellm:{body:'freellm-models-body',empty:'freellm-models-empty',namePrefix:'freellm-'},
 autoclaw:{body:'autoclaw-models-body',empty:'autoclaw-models-empty',namePrefix:'AutoClaw · '},
 trae:{body:'trae-models-body',empty:'trae-models-empty',namePrefix:'Trae · '},
 traeai:{body:'traeai-models-body',empty:'traeai-models-empty',namePrefix:'Trae 国际版 · '},
 raccoon:{body:'raccoon-models-body',empty:'raccoon-models-empty',namePrefix:'小浣熊 · '},
 core:{body:'core-models-body',empty:'core-models-empty',namePrefix:''}};
// alias_disabled / model_disabled 是别名测试在发出上游请求之前就判定的结果，
// 与探测失败（真的打了上游才失败）区分开，页面上一眼能看出该去哪儿改。
// 前端已认识的键（timeout/access/error/model_error/auth/rate_limit/empty/alias_disabled/
// model_disabled）语义必须原样不变；健康检查 M1 只做加法，补上后端新口径的两个键
// （upstream_error/pin）与保留值 form_error，未登记的键仍原样回显，不吞掉信息。
const modelFailureLabels={timeout:'探测超时',access:'地区限制',error:'调用失败',model_error:'模型错误',auth:'未授权',rate_limit:'限流',upstream_error:'上游错误',pin:'账号锁定',form_error:'响应串味',empty:'空返回',alias_disabled:'别名已停用',model_disabled:'模型已停用'};
const modelProbeSources={probe:'探测',request:'真实请求'};
// 公共模型名带通道前缀（opencode-OC · X / glm-X / opencode/x）与 realm 前缀（cn:/global:），去掉后作为跨源对齐键。
function modelKey(id){return String(id||'').replace(/^opencode-OC · /,'').replace(/^OC · /,'').replace(/^opencode\//,'').replace(/^(?:cn|global):/,'').replace(/^glm-/,'').replace(/^qoder-/,'').replace(/^autoclaw-/,'').replace(/^trae-/,'').replace(/^traeai-/,'').replace(/^raccoon-/,'').replace(/^freellm-/,'').toLowerCase().replace(/[^a-z0-9\u4e00-\u9fa5]+/g,'');}
function badgeCell(text,tone){const td=document.createElement('td');const tag=document.createElement('span');tag.className='badge'+(tone?' '+tone:'');tag.textContent=text;td.append(tag);return td;}
function chipsCell(labels,small){const td=document.createElement('td');if(labels.length){const row=document.createElement('span');row.className='badge-row';for(const label of labels){const tag=document.createElement('span');tag.className='badge';tag.textContent=label;row.append(tag);}td.append(row);}else td.textContent='—';if(small){const note=document.createElement('small');note.textContent=small;td.append(note);}return td;}
// 五档语义：ok=探测确认能调，warn=能调但打了折（仅对话），bad=探测确认调不通，
// unknown=根本没有探测数据。未探测不给告警色也不给绿色，它和「调不通」是两件事。
// order 是排序档位：可用最优，其次仅对话，再次未探测（没有探测数据），最后才是真不可用。
function modelProbeState(result){
 if(!result)return{label:'未探测',tone:'unknown',order:2};
 if(result.ok===true)return result.chatOnly?{label:'仅对话',tone:'warn',order:1}:{label:'可用',tone:'ok',order:0};
 const category=typeof result.category==='string'?result.category:'';
 return{label:modelFailureLabels[category]||category||'不可用',tone:'bad',order:3};
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
// applyModelSwitchState 用一份带 overrides 的响应刷新模型列表。整组开关端点
// （route/switch）与 /admin/model-switch 都回这个字段，两处共用同一套渲染，
// 不重复写一遍「重画每个通道」的循环。
function applyModelSwitchState(state){
 if(!state||typeof state.overrides!=='object'||!state.overrides)return false;
 modelSwitchOverrides=new Map(Object.entries(state.overrides));
 for(const channel of Object.keys(modelHeatSources)){const source=modelHeatSources[channel];renderModelTable(channel,source.list,source.health);}
 return true;
}
async function loadModelSwitch(){
 try{
  if(applyModelSwitchState(await jsonAPI('model-switch')))return;
 }catch(error){/* 端点缺失时按默认态渲染，不打断页面 */}
 modelSwitchOverrides=new Map();
 for(const channel of Object.keys(modelHeatSources)){const source=modelHeatSources[channel];renderModelTable(channel,source.list,source.health);}
}
// modelSwitchAction 切换单个模型并落盘。走整组端点：指向这个模型的别名会一起变，
// 与别名表的开关保持同一状态（用户口径：操作一边，另一边同步）。
async function modelSwitchAction(btn){
 const enable=btn.dataset.action==='enable-model';
 const model=btn.dataset.model;
 btn.disabled=true;
 try{
  const state=await jsonAPI('route/switch',{model,enabled:enable});
  applyModelSwitchState(state);
  // 别名页读的是同一份配置，同步缓存里的别名状态，切过去不会看到旧值。
  if(routesState&&Array.isArray(state.aliases))routesState.aliases=state.aliases;
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
 // 是否采集倍率决定排序与积分列：免费三通道没有倍率数据，保持「探测状态 → 耗时」排序。
 const hasCredits=channelHasCredits(channel);
 // 去重身份始终按 id（core 用完整公共名，其余通道用跨源对齐键），展示名不参与身份判定：
 // 目录条目可能带 name（如小浣熊的上游真实展示名），只改行标签，不改选路键与探测对齐。
 // upstreamName 单独留一份：行标签是给人看的，id 是选路与别名表单里真正在用的那个（问题4）。
 const ensure=(id,label,upstreamName)=>{const identity=channel==='core'?String(id||''):modelKey(id);if(!identity)return null;if(!rows.has(identity))rows.set(identity,{key:modelKey(id),label:String(label||id),upstreamName:String(upstreamName||''),id:String(id),cap:null,result:null,efforts:null,credits:null,creditValue:null,accounts:null,tier:null});return rows.get(identity);};
 for(const item of(Array.isArray(list)?list:[])){
  if(!item||!item.id)continue;
  const upstreamName=typeof item.name==='string'&&item.name?item.name:'';
  const row=ensure(item.id,upstreamName||item.id,upstreamName);if(!row)continue;
  row.efforts=Array.isArray(item.reasoning_supported_efforts)?item.reasoning_supported_efforts:null;
  row.cap=caps.get(row.key)||row.cap;
  row.credits=typeof item.credits==='string'?item.credits:null;
  row.creditValue=parseCreditRule(row.credits);
  row.accounts=Array.isArray(item.accounts)?item.accounts:null;
  // tier 是上游侧的分档判定（autoclaw 由 bridge 标出 Agent 目标），不是探测结论，两者分开读。
  row.tier=typeof item.tier==='string'&&item.tier?item.tier:null;
 }
 for(const[id,result]of Object.entries(results)){
  const cap=caps.get(modelKey(id))||null;
  const row=ensure(id,cap&&cap.name?`${prefix}${cap.name}`:id);if(!row)continue;
  row.cap=cap||row.cap;
  row.result=result&&typeof result==='object'?result:null;
 }
 return[...rows.values()].map(row=>{
  const result=row.result;
  const variants=[...new Set([...modelVariantNames(result),...modelVariantNames(row.cap)])];
  const ms=result?Number(result.durationMs):NaN;
  return{key:row.key,label:row.label,upstreamName:row.upstreamName,id:row.id,credits:row.credits,creditValue:row.creditValue,accounts:row.accounts,tier:row.tier,state:modelProbeState(result),
   caps:modelCapabilityBadges(row.cap,row.efforts&&row.efforts.length?['推理']:null),
   limit:modelLimitText(row.cap),
   variants:variants.length?`档位 ${variants.join(' / ')}`:'',
   duration:result?formatProbeDuration(result.durationMs):'—',
   durationMs:Number.isFinite(ms)&&ms>=0?ms:null,
   speed:speedText(result),
   source:result&&modelProbeSources[result.source]?modelProbeSources[result.source]:'',
   note:result&&typeof result.error==='string'&&result.error?result.error:'—'};
 }).sort(hasCredits?creditSort:probeSort);
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
// probeSort 是不参与开关的免费通道（opencode/autoclaw/raccoon）的原排序：
// 这些通道不采集倍率，保持「探测状态 → 耗时」口径。trae 有倍率，走 creditSort。
function probeSort(a,b){
 return a.state.order-b.state.order||((a.durationMs??Infinity)-(b.durationMs??Infinity));
}
// modelTableGroupsByAccount：哪些通道的模型表支持按账号分层。core 的归属来自
// /v1/models 的 accounts，autoclaw 的归属来自 bridge 逐账号下发的目录；两者共用同一套分组渲染。
function modelTableGroupsByAccount(channel){return channel==='core'||channel==='autoclaw';}
// modelLabelCell 渲染模型表第一列。目录条目带 name 时行标签用的是上游展示名（人看的），
// 但选路、探测对齐和别名表单里真正用的是 id——表头写的就是「模型 ID」，
// 不把 id 写出来就没人能把这一行和别名表单里选的那个对上（问题4）。
function modelLabelCell(row){return cell(row.label,row.upstreamName&&row.upstreamName!==row.id?row.id:'');}
function renderModelTable(channel,list,health){
 const config=modelChannels[channel];
 modelHeatSources[channel]={list,health};
 const body=$(config.body);body.replaceChildren();
 const rows=channelModelRows(channel,list,health);
 const hasCredits=channelHasCredits(channel);
 const cols=hasCredits?11:10;
 const appendRow=row=>{
  const tr=document.createElement('tr');
  // 只有异常行抬底色：可用/未知是常态，整表染色等于没染色，失败行反而被淹掉。
  if(row.state.tone==='warn'||row.state.tone==='bad')tr.className='row-'+row.state.tone;
  // tier 徽标排在能力徽标之前：它说「这个模型属于哪一档」，与「能不能调通」是两件事。
  const caps=row.tier?['Agent',...row.caps]:row.caps;
  tr.append(modelLabelCell(row),chipsCell(caps,[row.limit,row.variants].filter(Boolean).join(' · ')),badgeCell(row.state.label,row.state.tone),numCell(row.duration,row.source),numCell(row.speed),cell(row.note),modelHeatCell(row.key),globalHeatCell(row.key));
  // 所有通道都追加「操作」开关列：停用=对话请求返回已停用；采集倍率的通道再加
  // 「积分消耗」列，免费三通道没有倍率数据，不设该列（缺失不等于 0）。
  if(hasCredits){ tr.append(creditCell(row)); }
  tr.append(switchOpCell(row),modelHealthCell(channel,row.id,health&&health.modelResults&&health.modelResults[row.id]));
  body.append(tr);
 };
 if(modelTableGroupsByAccount(channel)&&modelsGroupByAccount&&rows.some(row=>Array.isArray(row.accounts)&&row.accounts.length)){
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
   // autoclaw 的 global: 模型由 bridge 标成归属国内版账号，不加这句会被读成「国际版账号也能跑」。
   if(channel==='autoclaw'){const note=document.createElement('small');note.textContent='含 global: 前缀的模型实际由国内版账号承接';th.append(note);}
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
 if(row.creditValue===null||row.creditValue===undefined)return numCell('未知');
 return numCell(`×${row.creditValue}`);
}
// switchOpCell 构造模型开关单元格：启用中的模型显示「停用」按钮，
// 停用中的显示「启用」按钮，并带一个状态徽标说明当前生效态与默认来源。
function switchOpCell(row){
 const td=document.createElement('td');
 const enabled=modelSwitchEffective(row);
 const badge=document.createElement('span');
 badge.className='badge'+(enabled?' ok':' idle');
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
// 通道键：core→"core"、zcode→"glm"、qoder→"qoder"、autoclaw→"autoclaw"、trae→"trae"、raccoon→"raccoon"（与后端 routeChannel 同一口径）。
// 这张表同时是「页面上有哪几颗探测按钮」的唯一真源：下面的按钮禁用、进度行、事件绑定、
// 退出登录复位都按它的键走，新通道只在这里加一行。
const probeChannelNames={core:'core',zcode:'glm',qoder:'qoder',autoclaw:'autoclaw',trae:'trae',traeai:'traeai',raccoon:'raccoon',freellm:'freellm'};
function setProbeButtonsDisabled(disabled){for(const n of Object.keys(probeChannelNames))$('probe-'+n).disabled=disabled;}
function renderProbeProgress(){
 const running=!!probeSnapshot.running,total=Number(probeSnapshot.total)||0,done=Number(probeSnapshot.done)||0;
 for(const name of Object.keys(probeChannelNames)){
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
 if(ch.autoclaw&&autoclawEnabled&&autoclawStatus){
  // autoclaw 状态接口的 health 同时含 /healthz 探活快照（healthy/total，卡片用）
  // 与最近一次探测的 modelResults；这里用刚完成的探测结论覆盖 modelResults，
  // 保留其余字段，与 core 的做法一致。
  renderModelTable('autoclaw',autoclawStatus.models,{...autoclawStatus.health,modelResults:ch.autoclaw});
 }
 if(ch.trae&&traeEnabled&&traeStatus){
  // trae 状态接口的 health 只承载探测的 modelResults（无 healthy/total）；
  // 这里用刚完成的探测结论覆盖，保留其余字段。
  renderModelTable('trae',traeStatus.models,{...traeStatus.health,modelResults:ch.trae});
 }
 if(ch.traeai&&traeaiEnabled&&traeaiStatus){
  renderModelTable('traeai',traeaiStatus.models,{...traeaiStatus.health,modelResults:ch.traeai});
 }
 if(ch.raccoon&&raccoonEnabled&&raccoonStatus){
  renderModelTable('raccoon',raccoonStatus.models,{...raccoonStatus.health,modelResults:ch.raccoon});
 }
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
for(const name of Object.keys(probeChannelNames))$('probe-'+name).addEventListener('click',()=>startProbe(name));
$('models-group-toggle').addEventListener('click',()=>{
 modelsGroupByAccount=!modelsGroupByAccount;
 const btn=$('models-group-toggle');
 btn.setAttribute('aria-pressed',String(modelsGroupByAccount));
 btn.textContent=modelsGroupByAccount?'▤ 按账号分层':'▤ 平铺列表';
 const source=modelHeatSources.core;
 renderModelTable('core',source?source.list:modelList,source?source.health:null);
 // 分层开关共用：autoclaw 表若已渲染过，跟着重画一次。
 const auto=modelHeatSources.autoclaw;
 if(auto)renderModelTable('autoclaw',auto.list,auto.health);
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
// 通道可达性读 console 自己的后台探活（GET /admin/health，一轮覆盖网关与全部已登记通道）：
// 服务端主动探，与操作者有没有点开某个通道页签无关，所以各通道的状态同时准确、同时刷新。
// 此前这里只读各页签缓存，没访问过就显示「未加载」，于是出现「WorkBuddy 有值、其他通道没有」
// ——那是加载时机造成的假缺口，不是数据缺失。模型数不在探活结论里，仍取页签缓存，
// 进入概览时按需补拉（loadMissingChannelStatuses）。两者都不伪造在线。
const healthChannelKeys={core:'workbuddy',glm:'zcode',qoder:'qoder',opencode:'opencode',autoclaw:'autoclaw',trae:'trae',traeai:'traeai',raccoon:'raccoon',freellm:'freellm'};
function healthChannelResult(channel){
 if(!healthState||!Array.isArray(healthState.channels))return null;
 return healthState.channels.find(item=>healthChannelKeys[item.channel]===channel)||null;
}
function healthCheckedAtText(){
 const at=healthState&&healthState.checked_at?new Date(healthState.checked_at):null;
 return at&&!Number.isNaN(at.getTime())?at.toLocaleTimeString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false}):'';
}
// config_hint 只在「路由里有别名指向未配置上游地址的通道」时由后端给出（多半是 console
// 用缺少对应 overlay 的 compose 链重建）。这类通道不会变红，而是整条从概览列表里消失，
// 所以必须在列表上方显式说出来——否则「通道少了一半」会被当成本来就这么多。
function healthConfigHint(){
 return healthState&&healthState.config_hint?String(healthState.config_hint):'';
}
// traeaiEnabled 的来源与其余通道不同：POST /admin/session 的载荷里没有 traeai_enabled 这一位，
// 登录当下唯一能拿到「这条旁路配了没有」的口径是 GET /admin/health 每条通道的 enabled
// （后端 routeChannelOrder 全量下发，含未启用的通道）。所以每轮探活落地后同步一次，
// 页签自己的 GET /admin/traeai 回报 enabled 时再确认一次。两处都只写实测到的配置事实：
// 探活还没结论时保持「未启用」的观感（侧栏置灰），不替后端推断它已经配上了。
function syncTraeaiAvailability(){
 const result=healthChannelResult('traeai');
 if(result)setTraeaiEnabled(!!result.enabled);
}
function setTraeaiEnabled(enabled){
 traeaiEnabled=enabled;
 $('nav-traeai').classList.toggle('nav-muted',!enabled);
}
async function loadHealth(quiet){
 if(!csrf)return;
 const generation=healthGeneration;
 try{
  const data=await jsonAPI('health');
  if(generation!==healthGeneration)return;
  healthState=data;
  syncTraeaiAvailability();
  if(page==='overview')renderOverview();
 }catch(error){if(!quiet&&generation===healthGeneration)notice(error.message);}
}
// 服务端后台探活是 30 秒一轮（healthProbeInterval），前端跟着同一节奏刷新；
// 只在概览页可见时轮询，离开即停，不给别的页面加负担。
function healthPageVisible(){return page==='overview'&&csrf&&!document.hidden;}
function scheduleHealthPoll(){
 clearTimeout(healthPollTimer);healthPollTimer=undefined;
 if(healthPageVisible())healthPollTimer=setTimeout(async()=>{await loadHealth(true);scheduleHealthPoll();},30000);
}
function stopHealthPoll(){clearTimeout(healthPollTimer);healthPollTimer=undefined;}
// 概览与接入页都要「各通道的模型数」，两处共用：只补还没加载过的通道，已有的不重复打。
function loadMissingChannelStatuses(){
 if(zcodeEnabled&&!zcodeStatus)void loadZcode();
 if(qoderEnabled&&!qoderStatus)void loadQoder();
 if(opencodeEnabled&&!opencodeStatus)void loadOpenCode();
 if(freellmEnabled&&!freellmStatus)void loadFreellm();
 if(autoclawEnabled&&!autoclawStatus)void loadAutoClaw();
 if(traeEnabled&&!traeStatus)void loadTrae();
 if(traeaiEnabled&&!traeaiStatus)void loadTraeai();
 if(raccoonEnabled&&!raccoonStatus)void loadRaccoon();
}
function overviewChannelStatus(channel){
 const result=healthChannelResult(channel);
 if(result&&!result.enabled)return{label:'未启用',tone:'idle'};
 if(result&&result.probe_state==='failed')return{label:'模型不可用',tone:'bad'};
 if(channel==='workbuddy'){
  // core 的探活只回答「桥接通不通」，账号池里有没有账号是另一件事，两者一起读才准。
  if(result&&!result.reachable)return{label:'不可达',tone:'bad'};
  // 没有真调用结论时不给绿灯——「没测过」不是「健康」，否则就是假绿。
  if(result&&result.probe_state!=='ok')return{label:'未探测',tone:'unknown'};
  if(!lastStatus)return{label:'未知',tone:'unknown'};
  return lastStatus.total?{label:'运行中',tone:'ok'}:{label:'等待账号',tone:'idle'};
 }
 if(result){
  if(!result.reachable)return{label:'不可达',tone:'bad'};
  // 端口活着但没有真调用结论时标「未探测」，不给绿灯（未知 ≠ 健康）。
  if(result.probe_state!=='ok')return{label:'未探测',tone:'unknown'};
  return{label:'在线',tone:'ok'};
 }
 // 探活还没出结论（或部署没开探活）时退回页签缓存，仍如实标「未加载」，不伪造在线。
 const st=channelStatusOf(channel);
 if(!st)return{label:'未加载',tone:'unknown'};
 if(!st.enabled)return{label:'未启用',tone:'idle'};
 return st.reachable?{label:'在线',tone:'ok'}:{label:'不可达',tone:'bad'};
}
// 红灯原因挂在状态徽标的 title 上：总览每通道只放一个徽标，但排查时要能直接看到
// 「为什么红、下一步做什么」——优先通道侧的可执行提示（如 raccoon 凭证失效需重导），
// 其次首个失败模型的原因，最后才是可达性说明。
function overviewChannelHint(channel){
 const result=healthChannelResult(channel);
 if(!result)return '';
 return result.probe_hint||result.probe_detail||result.detail||'';
}
function overviewChannelModelCount(channel){
 if(channel==='workbuddy')return modelList.length;
 const st=channelStatusOf(channel);
 if(!st||!st.enabled)return null;
 return Number(st.model_count)||0;
}
function renderOverview(){
 const stamp=$('overview-health-stamp');
 if(stamp){const at=healthCheckedAtText();const base=at?`每个通道一行；状态由网关自探，最近一轮 ${at}`:'每个通道一行；状态由网关自探，等待首次结论';const cfgHint=healthConfigHint();stamp.textContent=cfgHint?`${base} ⚠ ${cfgHint}`:base;}
 const body=$('overview-channels-body');body.replaceChildren();
 const byChannel=new Map();
 for(const e of overviewUsageItems){const c=usageChannelOf(e);if(!byChannel.has(c))byChannel.set(c,[]);byChannel.get(c).push(e);}
 for(const c of usageChannelOrder){
  const enabled=c==='workbuddy'||(c==='zcode'&&zcodeEnabled)||(c==='qoder'&&qoderEnabled)||(c==='opencode'&&opencodeEnabled)||(c==='autoclaw'&&autoclawEnabled)||(c==='trae'&&traeEnabled)||(c==='traeai'&&traeaiEnabled)||(c==='raccoon'&&raccoonEnabled)||(c==='freellm'&&freellmEnabled);
  if(!enabled)continue;
  const s=summarizeUsage(byChannel.get(c)||[]);
  const status=overviewChannelStatus(c);
  const hint=overviewChannelHint(c);
  const count=overviewChannelModelCount(c);
  const credit=s.calls?formatCreditGroup(s.credit,s.creditMissing,s.calls):'—';
  const tr=document.createElement('tr');
  tr.className='ch-'+c+' ch-band';
  const td=document.createElement('td');const tag=document.createElement('span');tag.className='badge'+(status.tone?' '+status.tone:'');tag.textContent=status.label;if(hint)tag.title=hint;td.append(tag);
  tr.append(channelCell(usageChannelNames[c],c),td,numCell(count===null?'—':String(count)),numCell(String(s.calls)),tokenCell(s.prompt,s.promptMissing,s.calls),tokenCell(s.completion,s.completionMissing,s.calls),numCell(credit));
  body.append(tr);
 }
 const items=overviewUsageChannel==='all'?overviewUsageItems:overviewUsageItems.filter(e=>usageChannelOf(e)===overviewUsageChannel);
 const s=summarizeUsage(items);
 $('overview-usage-calls').textContent=String(s.calls);
 setMetric('overview-usage-tokens',usageTokenTotal(s));
 $('overview-usage-credit').textContent=formatCreditGroup(s.credit,s.creditMissing,s.calls);
 const notes=[];
 if(s.promptMissing)notes.push(`${s.promptMissing} 条记录未回报输入 token，未计入合计`);
 if(s.completionMissing)notes.push(`${s.completionMissing} 条记录未回报输出 token，未计入合计`);
 if(s.creditMissing)notes.push(`${s.creditMissing} 条记录未回报积分扣费，未计入合计`);
 $('overview-usage-note').textContent=notes.join('；');$('overview-usage-note').hidden=!notes.length;
 renderTrend(items);
 renderCards();
 renderErrorStats();
}
// ── 错误统计区块（健康检查 M1 · F5，PRD §2-F5）──
// 数据源是 GET /admin/errors?window=15m|1h|24h：窗口内的成功/失败调用聚合（成功是错误率
// 的分母，所以窗口是「无数据=无统计」而不是「只记失败」）。这里只渲染服务端给的数，不在
// 前端重算错误率或补零——样本为 0 时错误率写「—」，绝不把「没观测」粉饰成 0%。
const errorsWindowButtons=[['15m','errors-window-15m'],['1h','errors-window-1h'],['24h','errors-window-24h']];
const errorsWindowLabels={'15m':'近 15 分钟','1h':'近 1 小时','24h':'近 24 小时'};
function errorCategoryLabel(category){return modelFailureLabels[category]||category||'未知';}
// formatErrorRate 把 0..1 的错误率写成一位小数百分比；非数值（含未观测）写「—」。
function formatErrorRate(value){
 const n=Number(value);
 return Number.isFinite(n)?(n*100).toFixed(1)+'%':'—';
}
function formatErrorTime(ts){
 const at=new Date(Number(ts));
 return Number.isNaN(at.getTime())?'':at.toLocaleTimeString('zh-CN',{hour12:false});
}
function setErrorsWindowButtons(){
 // 老测试夹具的假元素不一定实现 setAttribute（只实现了 textContent/hidden 等）；
 // 缺方法时静默跳过，按钮态是纯展示，不该把页面初始化拖挂。
 for(const [key,id] of errorsWindowButtons){const button=$(id);if(button&&typeof button.setAttribute==='function')button.setAttribute('aria-pressed',String(key===errorsWindow));}
}
// setErrorsWindow 切换三档窗口：只改档位并重取聚合，样本切片由后端做（前端不缓存各档）。
function setErrorsWindow(key){
 if(key===errorsWindow||!errorsWindowButtons.some(entry=>entry[0]===key))return;
 errorsWindow=key;errorsGeneration++;setErrorsWindowButtons();
 if(page==='overview')void loadErrorStats();
}
async function loadErrorStats(quiet){
 if(!csrf)return;
 const generation=++errorsGeneration;
 try{
  const data=await jsonAPI('errors?window='+errorsWindow);
  if(generation!==errorsGeneration)return;
  errorsState=data;
  renderErrorStats();
 }catch(error){
  if(generation!==errorsGeneration)return;
  // 失败信息落在区块自己的时间戳行上，不抢整页的 notice（健康页同时有多个加载器）。
  const stamp=$('errors-stamp');if(stamp)stamp.textContent=`错误统计读取失败：${error.message}`;
  if(!quiet)notice(error.message);
 }
}
function renderErrorStats(){
 const data=errorsState||{window:errorsWindow,ok:0,fail:0,error_rate:0,channels:[],categories:[],recent:[]};
 const calls=(data.ok||0)+(data.fail||0);
 $('errors-calls').textContent=String(calls);
 $('errors-fail').textContent=String(data.fail||0);
 // 样本为 0 时错误率是「没有观测」而不是「0%」：写「—」，不伪造一个漂亮数字。
 $('errors-rate').textContent=calls?formatErrorRate(data.error_rate):'—';
 const stamp=$('errors-stamp');
 if(stamp)stamp.textContent=`窗口：${errorsWindowLabels[errorsWindow]||errorsWindow} · 样本 ${calls} 次`;
 renderErrorCategories(data);
 renderErrorChannels(data);
 renderErrorRecent(data);
 renderErrorModels(data);
}
// renderErrorCategories 类别分布：失败次数 + 占比条（占比分母是窗口内失败总数）。
function renderErrorCategories(data){
 const body=$('errors-categories-body');body.replaceChildren();
 const rows=data.categories||[];
 $('errors-categories-empty').hidden=rows.length>0;
 for(const row of rows){
  const tr=document.createElement('tr');
  tr.append(cell(errorCategoryLabel(row.category)),numCell(String(row.fail)),shareCell(row.share,1));
  body.append(tr);
 }
}
// renderErrorChannels 按通道错误数表：一行一条通道，类别与失败最多的模型挂在同一行。
function renderErrorChannels(data){
 const body=$('errors-channels-body');body.replaceChildren();
 const rows=data.channels||[];
 $('errors-channels-empty').hidden=rows.length>0;
 for(const row of rows){
  const key=channelKeyOf(row.channel);
  const tr=document.createElement('tr');tr.className='ch-'+key+' ch-band';
  const categories=Object.entries(row.categories||{}).map(([name,count])=>`${errorCategoryLabel(name)} ×${count}`).join('、')||'—';
  const top=(row.top_models||[])[0];
  tr.append(channelCell(usageChannelNames[key]||row.channel,row.channel),numCell(String(row.ok||0)),numCell(String(row.fail||0)),numCell(formatErrorRate(row.error_rate)),cell(categories),cell(top?`${top.model}（失败 ${top.fail}）`:'—'));
  body.append(tr);
 }
}
// renderErrorRecent 最近 20 条错误事件流水：每张卡一张事件，摘要收进 <details> 里，
// 展开才看得到（摘要可能很长，默认全展开会把整页撑爆）。摘要已由服务端脱敏。
function renderErrorRecent(data){
 const wrap=$('errors-recent');wrap.replaceChildren();
 const rows=(data.recent||[]).slice(0,20);
 $('errors-recent-empty').hidden=rows.length>0;
 for(const ev of rows){
  const card=document.createElement('article');card.className='panel task-card';
  const head=document.createElement('div');head.className='task-card-heading';
  const title=document.createElement('h3');title.textContent=`${errorCategoryLabel(ev.category)} · ${ev.channel||'未知通道'}`;
  const when=document.createElement('span');when.className='muted small';when.textContent=formatErrorTime(ev.ts);
  head.append(title,when);
  const meta=document.createElement('p');meta.className='muted small';meta.textContent=errorEventMeta(ev);
  card.append(head,meta);
  if(ev.error_snippet){
   const details=document.createElement('details');
   const summary=document.createElement('summary');summary.textContent='展开上游摘要';
   const pre=document.createElement('pre');pre.className='task-log';pre.textContent=ev.error_snippet;
   details.append(summary,pre);card.append(details);
  }
  wrap.append(card);
 }
}
// errorEventMeta 拼一张错误卡片的元信息行：别名 / 模型 / api / 模式 / 上游状态 / 耗时。
// 缺失的字段整段省略，不写「undefined」也不编占位值。
function errorEventMeta(ev){
 const parts=[];
 if(ev.alias)parts.push(`别名 ${ev.alias}`);
 if(ev.model_resolved)parts.push(`模型 ${ev.model_resolved}`);
 if(ev.api)parts.push(usageAPINames[ev.api]||ev.api);
 if(ev.mode)parts.push(usageModeNames[ev.mode]||ev.mode);
 if(ev.upstream_status)parts.push(`上游 ${ev.upstream_status}`);
 if(Number.isFinite(Number(ev.duration_ms))&&Number(ev.duration_ms)>0)parts.push(`${ev.duration_ms} ms`);
 return parts.join(' · ')||'无更多字段';
}
for(const [key,id] of errorsWindowButtons)$(id).addEventListener('click',()=>setErrorsWindow(key));
setErrorsWindowButtons();
// cardStats/cardActions 描述每张通道卡片的统计行与快捷入口。WorkBuddy 卡片直接读账号池
// 快照（lastStatus/accounts），旁路卡片复用各页签已加载的状态缓存，缺失时如实显示「—」，
// 不伪造在线或凭据态；未启用的通道不渲染卡片（与通道总览表同一口径）。
// channelStatusOf 取某通道已加载的状态缓存（各页签共用同一份），未加载返回 null。
// 这层映射只写一次，卡片、通道总览、模型数都从这里取，别再各摆一串三元。
function channelStatusOf(channel){
 return channel==='zcode'?zcodeStatus:channel==='qoder'?qoderStatus:channel==='opencode'?opencodeStatus:channel==='autoclaw'?autoclawStatus:channel==='trae'?traeStatus:channel==='traeai'?traeaiStatus:channel==='raccoon'?raccoonStatus:channel==='freellm'?freellmStatus:null;
}
// cardStats 描述每张通道卡片的统计格。前四项是七条通道共有的同一组口径
// （模型 / 调用 / Tokens / 积分），后两项是该通道自己的账号或凭据口径。
// 固定六格是为了让七张卡片一样高：此前 WorkBuddy 报五项、旁路只报两项，
// 网格拉伸后短卡片底部会空出一大块，那正是「不协调」的来源。
// 缺失一律给「—」，不伪造在线，也不把未观测当 0。
function cardStats(c,s){
 const count=overviewChannelModelCount(c);
 const tokens=usageTokenTotal(s);
 return [
  ['模型',count===null?'—':String(count)],
  ['调用',String(s.calls)],
  ['Tokens',tokens==null?'—':formatTokens(tokens)],
  ['积分',formatCreditGroup(s.credit,s.creditMissing,s.calls)],
 ].concat(cardAccountStats(c));
}
// cardAccountStats 各通道自己的账号/凭据口径：有账号池的报「账号 / 可用」，
// 没有账号池的报它实际具备的那两件事（套餐与凭据、阶段与免登录）。
// 七张卡片的第二行因此始终是「同一类问题」，横向扫一排就能比。
function cardAccountStats(c){
 if(c==='workbuddy')return [['账号',lastStatus?String(lastStatus.total):'—'],['可用',lastStatus?String(lastStatus.healthy):'—']];
 const st=channelStatusOf(c),on=!!(st&&st.enabled);
 if(c==='zcode')return [['套餐',on?(st.plan==='coding-plan'?'Coding':st.plan==='start-plan'?'Start':'—'):'—'],['凭据',on?(st.control?(st.logged_in?'已登录':'未登录'):'—'):'—']];
 if(c==='qoder')return [['凭据',on?(st.control?(st.logged_in?'已登录':'未登录'):'PAT'):'—'],['控制端',on?(st.reachable?'在线':'不可达'):'—']];
 if(c==='opencode')return [['阶段',on&&st.health&&st.health.phase?st.health.phase:'—'],['账号','免登录']];
 if(c==='freellm')return [['key 池',on&&Number.isFinite(Number(st.healthy_keys))?`${st.healthy_keys} 健康`:'—'],['受限',on&&Number.isFinite(Number(st.limited_keys))?String(st.limited_keys):'—']];
 if(c==='autoclaw')return [['账号',on&&st.health?`${st.health.healthy} / ${st.health.total}`:'—'],['可用',on&&st.health?String(st.health.healthy):'—']];
 const list=on&&Array.isArray(st.accounts)?st.accounts:[];
 const usable=list.filter(a=>a.enabled&&!a.disabled&&!a.cooling).length;
 return [['账号',list.length?String(list.length):'—'],['可用',list.length?String(usable):'—']];
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
 const st=channelStatusOf(c);
 if(!st||!st.enabled||!st.control)return base;
 if(!st.logged_in)return{label:'未登录',tone:'warn'};
 if(c==='zcode'&&!st.proxy_running)return{label:'未启用',tone:'idle'};
 return base;
}
function renderCards(){
 const wrap=$('overview-cards');if(!wrap)return;wrap.replaceChildren();
 const byChannel=new Map();
 for(const e of overviewUsageItems){const c=usageChannelOf(e);if(!byChannel.has(c))byChannel.set(c,[]);byChannel.get(c).push(e);}
 for(const c of usageChannelOrder){
  const enabled=c==='workbuddy'||(c==='zcode'&&zcodeEnabled)||(c==='qoder'&&qoderEnabled)||(c==='opencode'&&opencodeEnabled)||(c==='autoclaw'&&autoclawEnabled)||(c==='trae'&&traeEnabled)||(c==='traeai'&&traeaiEnabled)||(c==='raccoon'&&raccoonEnabled)||(c==='freellm'&&freellmEnabled);
  if(!enabled)continue;
  const status=cardStatus(c);
  // 每张卡片顶沿一条本色细带、名字用本色文字版，其余回到内容区那套白面与语义徽章：
  // 通道色只做识别，状态一律交给徽章。告警与不可达只压一点描边色，不整块换底。
  const card=document.createElement('article');card.className='channel-card ch-'+c+(status.tone==='bad'?' bad':status.tone==='warn'?' warn':'');
  const head=document.createElement('div');head.className='channel-card-head';
  const name=document.createElement('span');name.className='channel-card-name';name.textContent=usageChannelNames[c];
  const badge=document.createElement('span');badge.className='badge'+(status.tone?' '+status.tone:'');badge.textContent=status.label;
  head.append(name,badge);card.append(head);
  const stats=document.createElement('div');stats.className='channel-card-stats';
  for(const [k,v] of cardStats(c,summarizeUsage(byChannel.get(c)||[]))){const d=document.createElement('div');const s=document.createElement('span');s.textContent=k;const strong=document.createElement('strong');strong.textContent=v;d.append(s,strong);stats.append(d);}
  card.append(stats);
  const actions=document.createElement('div');actions.className='channel-card-actions';
  for(const a of cardActions(c)){const btn=document.createElement('button');btn.className='secondary';btn.textContent=a.label;btn.addEventListener('click',()=>{if(a.sub&&a.view==='workbuddy')setWorkBuddyView(a.sub);showPage(a.view);});actions.append(btn);}
  card.append(actions);
  wrap.append(card);
 }
}
// renderTrend 用内联 SVG 画「每日调用次数」折线图：全部通道时每个已启用通道一条线，筛选
// 单通道时只画该通道。配色不再在脚本里另养一份——线、点、图例一律挂 stroke-/fill-/sw- 类，
// 色值只在 style.css 的 --ch-* 令牌里存在一份，改通道色不会让图例和折线分家。
// 按 Asia/Shanghai 本地日分桶，缺失观测天然不计入。范围不足两天时不画折线，改用文字提示，
// 避免用单点误导趋势。
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
 let yMax=1;for(const m of series.values())for(const v of m.values())if(v>yMax)yMax=v;
 const maxText=String(yMax);
 // 同一条留位规则：次数标签没有千分位，五位数一样会顶出 viewBox 左边。
 const W=620,H=210,padL=axisPadL(maxText,36),padR=14,padT=14,padB=30,plotW=W-padL-padR,plotH=H-padT-padB;
 const x=i=>padL+(days.length===1?plotW/2:i*plotW/(days.length-1));
 const y=v=>padT+plotH-(v/yMax)*plotH;
 const svg=mk('svg',{viewBox:`0 0 ${W} ${H}`,class:'trend-svg',role:'img','aria-label':'每日调用次数折线图'});
 svg.append(mk('line',{x1:padL,y1:y(0),x2:padL+plotW,y2:y(0),class:'trend-axis'}));
 svg.append(mk('line',{x1:padL,y1:y(yMax),x2:padL+plotW,y2:y(yMax),class:'trend-grid'}));
 const maxLabel=mk('text',{x:padL-6,y:y(yMax)+4,class:'trend-tick','text-anchor':'end'});maxLabel.textContent=maxText;svg.append(maxLabel);
 const zeroLabel=mk('text',{x:padL-6,y:y(0)+4,class:'trend-tick','text-anchor':'end'});zeroLabel.textContent='0';svg.append(zeroLabel);
 for(const i of axisTicks(days.length)){const t=mk('text',{x:x(i),y:padT+plotH+16,class:'trend-tick','text-anchor':'middle'});t.textContent=days[i].slice(5);svg.append(t);}
 for(const [c,m] of series){
  const pts=days.map((d,i)=>`${x(i)},${y(m.get(d)||0)}`).join(' ');
  svg.append(mk('polyline',{points:pts,fill:'none',class:'trend-line stroke-'+c,'stroke-width':2,'stroke-linejoin':'round','stroke-linecap':'round'}));
  days.forEach((d,i)=>{const v=m.get(d)||0;svg.append(mk('circle',{cx:x(i),cy:y(v),r:2.6,class:'fill-'+c}));});
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
// 冷却的两种成因，运维处置完全相反：硬冷却 = 积分耗尽（等签到或换号），
// 软冷却 = 被限流（等着就行）。core 用 cool_kind 区分，页面此前只显示「冷却中」，
// 两者长得一模一样，看不出该做什么。
// 另：cool_remaining_sec 带 omitempty，熔断冷却时 core 会省略它——所以副行不能只在
// 「有剩余秒数」时才出现，否则那类冷却连为什么在冷却都看不到。
const coolKindLabels={hard_credit:'硬冷却（积分耗尽）',soft_rate:'软冷却（限流）'};
function coolSubText(a){
 const kind=coolKindLabels[a.cool_kind]||'';
 const left=a.cool_remaining_sec?`约 ${Math.ceil(a.cool_remaining_sec/60)} 分钟后恢复`:'';
 return [kind,left].filter(Boolean).join(' · ');
}
function renderAccounts(data) {
 lastStatus = data;
 accounts = data.accounts || [];
 const stateEl = $('service-state');
 stateEl.textContent = data.total ? '网关运行中' : '运行中 · 等待添加账号';
 stateEl.className = 'badge rail-state ' + (data.total ? 'ok' : 'idle');
 $('accounts-body').replaceChildren(); $('accounts-empty').hidden = accounts.length > 0;
 for (const a of accounts) {
  const tr=document.createElement('tr');const state=a.disabled?'已禁用':a.cooling?'冷却中':'可用';
  const accountTone={'可用':'ok','冷却中':'warn','已禁用':'idle'}[state];
  const status=cell('');const badge=document.createElement('span');badge.className='badge '+accountTone;badge.textContent=state;status.append(badge);
  const limits=(a.rate_limited_models||[]).map(m => `${m.model} 至 ${new Date(m.until).toLocaleString()}`).join('；');
  tr.append(cell(a.nickname||a.uid,`${a.realm === 'global'?'国际版':'国内版'} · ${a.uid}`),status,cell(a.credits_known ? formatCredit(a.credits) : a.credits>0 ? `${formatCredit(a.credits)}（历史）` : '待确认'),numCell(`${a.success_count||0} / ${a.err_total||0}`),numCell(a.in_flight==null?'—':String(a.in_flight)),cell(limits||a.disabled_reason||a.reason||'—', coolSubText(a)), pinOpCell(a));
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
function realmLabel(realm) { return realm === 'global' ? '国际版' : realm === 'cn' ? '国内版' : realm === 'glm' ? 'GLM·智谱' : realm === 'qoder' ? 'Qoder' : realm === 'opencode' ? 'OpenCode' : realm === 'autoclaw' ? 'AutoClaw' : realm === 'trae' ? 'Trae' : realm === 'traeai' ? 'Trae 国际版' : realm === 'raccoon' ? '小浣熊' : realm === 'freellm' ? 'FreeLLMAPI' : ''; }
function modelRealm(model) { return model.realm || (model.id.startsWith('global:') ? 'global' : model.id.startsWith('glm-') ? 'glm' : model.id.startsWith('qoder-') ? 'qoder' : model.id.startsWith('opencode-') ? 'opencode' : model.id.startsWith('autoclaw-') ? 'autoclaw' : model.id.startsWith('trae-') ? 'trae' : model.id.startsWith('traeai-') ? 'traeai' : model.id.startsWith('raccoon-') ? 'raccoon' : model.id.startsWith('freellm-') ? 'freellm' : 'cn'); }
function modelOption(model) { const label = realmLabel(modelRealm(model)); const n = Array.isArray(model.accounts) ? model.accounts.length : 0; return `${label ? `${label} · ` : ''}${model.id}${n > 1 ? ` · ${n}账号` : ''}`; }
// accessModelOptions feeds the API-access select: core models plus the GLM
// channel models (the public /v1/models merges them, but /admin/models only
// covers core), so the access tab shows every model a client can actually call.
function accessModelOptions() {
 const items = modelList.map(m => ({...m}));
 for (const id of zcodeModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'glm'});
 for (const id of qoderModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'qoder'});
 for (const id of opencodeModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'opencode'});
 for (const id of freellmModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'freellm'});
 for (const id of autoclawModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'autoclaw'});
 for (const id of traeModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'trae'});
 for (const id of traeaiModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'traeai'});
 for (const id of raccoonModels) if (id && !items.some(m => m.id === id)) items.push({id, realm: 'raccoon'});
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
  const data=await jsonAPI('models');if(activeRequest)return;
  applyModelHealth(data);const models=data.data||[];
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
runTopRefresh($('refresh'),async()=>{await refreshStatus();await refreshModels();await loadHealth();await loadErrorStats();});
setInterval(()=>{if(csrf&&!document.hidden){refreshStatus();void loadPins();}},15000);

const taskNames={checkin:'签到',travel:'猫猫旅行',activity:'活跃上报',keepalive:'Token 保活',school:'开学季',cat:'夜猫子'};
const runStatuses={running:'运行中',success:'成功',partial_failure:'部分失败',failed:'失败',skipped:'已跳过',interrupted:'已中断（结果未确认）',unknown:'结果未确认'};
function formatTaskReward(value){return value===null||value===undefined?'未确认':String(value);}
function formatTaskAvailability(task){return task.enabled===false?'已禁用':task.next_at?'已启用':'暂不可用 / 未知';}
// 与 formatTaskAvailability 同一分支顺序，改一个必须改另一个：档色说的是同一个事实。
function taskAvailabilityTone(task){return task.enabled===false?'idle':task.next_at?'ok':'unknown';}
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
  const heading=document.createElement('div');heading.className='task-card-heading';const title=document.createElement('h3');title.textContent=taskNames[task.id]||task.id;const badge=document.createElement('span');badge.className='badge '+taskAvailabilityTone(task);badge.textContent=formatTaskAvailability(task);heading.append(title,badge);
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
// 协议入口名由 core 白名单把关（chat/messages/responses），页面只负责译名。
const usageAPINames={chat:'Chat',messages:'Messages',responses:'Responses'};
// 通道归类：旁路通道的用量由 console 以固定占位 uid（与通道同名）上报 core 账本，
// 其余 uid 都是核心账号池的真实账号。分布表按此口径聚合，明细按当前筛选过滤。
const usageChannelNames={workbuddy:'WorkBuddy',zcode:'GLM（Zcode）',qoder:'Qoder',opencode:'OpenCode',autoclaw:'AutoClaw',trae:'Trae',traeai:'Trae 国际版',raccoon:'小浣熊',freellm:'FreeLLMAPI'};
const usageChannelOrder=['workbuddy','zcode','qoder','opencode','autoclaw','trae','traeai','raccoon','freellm'];
// 客户端列的通道标签：client 一律是调用方自报的 User-Agent（旁路在 console 的 newUsageCapture
// 取公开口入站头，核心由 console 转发后在 core 的 OriginOf 取），和「通道」列是两个维度——
// 同一条 UA 能走任一通道，同一通道也混着多个调用方。认不出是谁发的，才前置通道标签
// 说明这束用量从哪条路进来；核心那一路本来就是客户端 UA，不加前缀。
// workbuddy 的标签留空即「原样显示」，键的顺序与 usageChannelOrder 一致（有判据按同序反查）。
const usageChannelTags={workbuddy:'',zcode:'GLM',qoder:'Qoder',opencode:'OpenCode',autoclaw:'AutoClaw',trae:'Trae',traeai:'Trae 国际版',raccoon:'小浣熊',freellm:'FreeLLMAPI'};
function usageChannelOf(entry){const uid=entry.uid||'';return uid==='zcode'||uid==='qoder'||uid==='opencode'||uid==='autoclaw'||uid==='trae'||uid==='traeai'||uid==='raccoon'||uid==='freellm'?uid:'workbuddy';}
// 客户端列的「准确名称」对照表：raw 串里认得出的关键词 → 这个调用方的真名。
// 只增不改、按序匹配、忽略大小写、含即中（UA 常带版本号，如「Hertz/0.10.1」也认得出）。
// 认得出就显示「真名（原始串）」，认不出就退回上面的通道标签，绝不猜。
// - Trae CN 的客户端走字节自家的 Hertz 框架，UA 只报「hertz」，光看串认不出是谁
//   （2026-10-14 从 Trae 发一次调用、落到 core 账号池那行实测）。
// - hermesApp 用 openai 的 Python SDK，UA 是 SDK 默认串；同族还有 1.86.0、2.8.1 不是它，
//   所以关键词钉到带版本号的整串。代价：以后再有程序用同一版本 SDK 也会被叫成 Hermes。
const clientAgentNames=[['hertz','Trae CN'],['openai/python 2.24.0','Hermes']];
function clientAgentName(raw){const s=String(raw||'').toLowerCase();for(const [needle,name] of clientAgentNames){if(s.includes(needle))return name;}return '';}
// clientLabel：认得出真名就显示「真名（原始串）」——通道另有专列，不重复挂标签；
// 认不出才退回通道标签（那条只说明用量从哪条路进来，不代表是谁发的）；
// 空值仍由调用点显示「—」，不编出「GLM · —」。
function clientLabel(raw,channel){
 if(!raw)return '';
 const agent=clientAgentName(raw);
 if(agent)return agent+'（'+raw+'）';
 const tag=usageChannelTags[channel];
 return tag?tag+' · '+raw:raw;
}
// 一束色认一条通道。routes 那侧的键是 core/glm，账本这侧是 workbuddy/zcode，
// 指的是同一束光；翻译只写这一处，别在调用点各摆一份映射。
// 通道类挂在单元格上，整行都是同一束光时（通道总览、通道分布）再挂到 <tr>，
// 占比条那类不在通道列里的图形才继承得到 --ch。
function channelKeyOf(name){return name==='core'?'workbuddy':name==='glm'?'zcode':name;}
function channelCell(name,raw){
 const td=document.createElement('td');td.className='ch-name ch-'+channelKeyOf(raw);td.textContent=name;return td;
}
// shareCell 占比条：分母是同一范围内的总量，按 10% 一档取整，宽度只能走 class
// （style-src 'self' 会静默丢掉内联 style）。四舍五入到最小一档，非零不画成空。
function shareCell(value,total){
 const wrap=document.createElement('span');wrap.className='share';
 const bar=document.createElement('i');
 const steps=total>0?Math.max(1,Math.round(value/total*10)):0;
 if(steps)bar.className='w'+steps;
 wrap.append(bar);return wrap;
}
// summarizeUsage 复刻 core usagelog.Summarize 口径：输入与输出各自判定未知（<0 不累加、
// 各自计入 prompt_missing / completion_missing），credit 缺失（null）不累加且计入 credit_missing。
// 一个通道只回报了输入、没回报输出是真实存在的响应，把输出当 0 会静悄悄少报总量。
function summarizeUsage(items){const s={calls:items.length,prompt:0,completion:0,credit:0,creditMissing:0,promptMissing:0,completionMissing:0};for(const e of items){if(e.prompt_tokens>=0)s.prompt+=e.prompt_tokens;else s.promptMissing++;if(e.completion_tokens>=0)s.completion+=e.completion_tokens;else s.completionMissing++;if(e.credit!=null)s.credit+=e.credit;else s.creditMissing++;}return s;}
// ── 数值展示口径：全站唯一一处，改这里全站生效 ──
// tokenParts 三档动态展示：<1万 直接给原值；≥1万 原值为主、副行给「万」值；
// ≥1亿 以「亿」为主、原值退到副行。未观测（<0）仍是「—」，不把缺失当 0。
// 副行只承载「同一个数的另一种单位」，调用点要么用 setMetric 落成 <small>，
// 要么用 tokenCell/cell 的副行参数，绝不把两段拼进同一个 textContent。
function tokenParts(value){
 const n=Math.round(Number(value));
 if(!Number.isFinite(n)||n<0)return{text:'—',sub:''};
 if(n<10000)return{text:n.toLocaleString('en-US'),sub:''};
 if(n<1e8)return{text:n.toLocaleString('en-US'),sub:(n/1e4).toFixed(2)+' 万'};
 return{text:(n/1e8).toFixed(2)+' 亿',sub:n.toLocaleString('en-US')};
}
function formatTokens(value){return tokenParts(value).text;}
// formatCredit 积分一律四舍五入保留 2 位小数；缺失（null/undefined）仍是「—」，零与缺失不同。
function formatCredit(value){
 if(value==null||value==='')return '—';
 const n=Number(value);
 return Number.isFinite(n)?n.toLocaleString('en-US',{minimumFractionDigits:2,maximumFractionDigits:2}):'—';
}
// formatCreditGroup 与上同形，但走积分口径（2 位小数）。
function formatCreditGroup(total,missing,calls){return calls>0&&missing===calls?'—':formatCredit(total);}
// usageTokenTotal Tokens 合计口径：输入与输出都没有任何观测时返回 null（调用点写「—」），
// 否则给出可展示的合计值（0 条观测不等于 0 消耗）。两处指标条共用，避免各写一遍判据。
function usageTokenTotal(s){return s.calls>0&&s.promptMissing===s.calls&&s.completionMissing===s.calls?null:s.prompt+s.completion;}
function formatUsageTokens(value){return formatTokens(value);}
function formatUsageCredit(value){return formatCredit(value);}
// tokenCell 表格里的 token 单元格：整组未回报给「—」，否则主值走三档主文本、
// 万/亿副值落进单元格的 <small>（numCell 的副行），与指标卡同一口径。
function tokenCell(total,missing,calls){
 if(calls>0&&missing===calls)return numCell('—');
 const parts=tokenParts(total);
 return numCell(parts.text,parts.sub);
}
// setMetric 指标卡写值：value 是数值时按三档给「主值 + 万/亿副值」（副值用嵌套 <small>，
// 直接继承 .metrics small 的 11px 灰字，不另立一套样式）；value 为 null 时如实写「—」。
function setMetric(id,value){
 const el=$(id);if(!el)return;
 if(value==null){el.textContent='—';return;}
 const parts=tokenParts(value);
 el.textContent=parts.text;
 if(parts.sub&&typeof document.createElement==='function'){const s=document.createElement('small');s.textContent=parts.sub;el.append(s);}
}
function renderUsage(data){
 const all=(data.items||[]);
 const items=all.filter(e=>usageChannel==='all'||usageChannelOf(e)===usageChannel).slice().reverse();
 const summary=summarizeUsage(items);
 $('usage-calls').textContent=String(summary.calls);
 setMetric('usage-tokens',usageTokenTotal(summary));
 $('usage-credit').textContent=formatCreditGroup(summary.credit,summary.creditMissing,summary.calls);
 const notes=[];
 if(summary.promptMissing)notes.push(`${summary.promptMissing} 条记录未回报输入 token，未计入合计`);
 if(summary.completionMissing)notes.push(`${summary.completionMissing} 条记录未回报输出 token，未计入合计`);
 if(summary.creditMissing)notes.push(`${summary.creditMissing} 条记录未回报积分扣费，未计入合计`);
 $('usage-note').textContent=notes.join('；');$('usage-note').hidden=!notes.length;
 // 鉴权失败留痕来自 core 的 denied 账本，按当前时间范围统计，与通道筛选无关；零失败不占位。
 const denied=data.denied||{},deniedCalls=denied.calls||0,deniedEl=$('usage-denied');
 deniedEl.hidden=!deniedCalls;
 if(deniedCalls){
  const sources=(denied.sources||[]).map(entry=>`${entry.src||'未知来源'} ×${entry.calls}`);
  deniedEl.textContent=`鉴权失败 ${deniedCalls} 次（本范围）${sources.length?'，来源：'+sources.join('、'):''}`;
 } else deniedEl.textContent='';
 const byChannel=new Map();
 for(const e of all){const c=usageChannelOf(e);if(!byChannel.has(c))byChannel.set(c,[]);byChannel.get(c).push(e);}
 const distBody=$('usage-by-channel-body');distBody.replaceChildren();
 const distRows=usageChannelOrder.filter(c=>byChannel.has(c));
 $('usage-by-channel-empty').hidden=distRows.length>0;
 for(const c of distRows){
  const s=summarizeUsage(byChannel.get(c));const credit=s.calls?formatCreditGroup(s.credit,s.creditMissing,s.calls):'—';
  const tr=document.createElement('tr');tr.className='ch-'+c+' ch-band';
  const calls=numCell(String(s.calls));calls.append(shareCell(s.calls,all.length));
  tr.append(channelCell(usageChannelNames[c],c),calls,tokenCell(s.prompt,s.promptMissing,s.calls),tokenCell(s.completion,s.completionMissing,s.calls),numCell(credit));
  distBody.append(tr);
 }
 $('usage-empty').hidden=items.length>0;
 usageDetailRows=items;
 renderUsageDetail();
}
// 明细一次建全部行会在月度几千条时拖慢首屏，长表本身也读不动。整段数据留在内存里
// 切片展示：上方指标条与通道分布按整个时间范围算，翻页不改任何汇总口径。
function renderUsageDetail(){
 const body=$('usage-body');body.replaceChildren();
 const pages=Math.max(1,Math.ceil(usageDetailRows.length/usagePageSize));
 if(usagePage>pages)usagePage=pages;
 if(usagePage<1)usagePage=1;
 const start=(usagePage-1)*usagePageSize;
 for(const e of usageDetailRows.slice(start,start+usagePageSize)){
  const channel=usageChannelOf(e);
  const tr=document.createElement('tr');tr.className='ch-'+channel;
  tr.append(cell(formatTaskTime(e.ts*1000)),channelCell(usageChannelNames[channel],channel),cell(e.account||e.uid||'—'),cell(e.model||'—'),cell(usageAPINames[e.api]||'—'),cell(usageModeNames[e.mode]||e.mode||'—'),numCell(formatUsageTokens(e.prompt_tokens)),numCell(formatUsageTokens(e.completion_tokens)),numCell(formatUsageCredit(e.credit)),cell(e.src||'—'),cell(clientLabel(e.client,channel)||'—'));
  body.append(tr);
 }
 renderUsagePager(pages);
}
// 页码窗口最多 7 格：两端固定，当前页左右各带一格，中间断档压成一个省略号。
function usagePageWindow(page,pages){
 if(pages<=7)return Array.from({length:pages},(_,i)=>i+1);
 const sorted=[...new Set([1,page-1,page,page+1,pages].filter(p=>p>=1&&p<=pages))].sort((a,b)=>a-b);
 const out=[];
 for(const [i,p] of sorted.entries()){if(i&&p-sorted[i-1]>1)out.push('…');out.push(p);}
 return out;
}
function renderUsagePager(pages){
 const bar=$('usage-pager-bar'),host=$('usage-pager');
 host.replaceChildren();
 bar.hidden=usageDetailRows.length===0;
 $('usage-pager-info').textContent=`共 ${usageDetailRows.length} 条 · 第 ${usagePage}/${pages} 页`;
 const goto=(page)=>()=>{usagePage=page;renderUsageDetail();};
 const step=(label,page,disabled)=>{const b=document.createElement('button');b.className='pager-btn';b.textContent=label;b.disabled=disabled;b.addEventListener('click',goto(page));host.append(b);};
 step('‹ 上一页',usagePage-1,usagePage<=1);
 for(const slot of usagePageWindow(usagePage,pages)){
  if(slot==='…'){const gap=document.createElement('span');gap.className='pager-gap';gap.textContent='…';host.append(gap);continue;}
  const b=document.createElement('button');b.className='pager-btn'+(slot===usagePage?' current':'');b.textContent=String(slot);
  if(slot===usagePage)b.setAttribute('aria-current','page');else b.addEventListener('click',goto(slot));
  host.append(b);
 }
 step('下一页 ›',usagePage+1,usagePage>=pages);
}
async function loadUsage(){
 if(!csrf)return;
 const generation=usageGeneration;
 try{const data=await jsonAPI('usage?range='+encodeURIComponent(usageRange));if(generation===usageGeneration)renderUsage(data);}
 catch(error){if(generation===usageGeneration)notice(error.message);}
}
$('usage-range').addEventListener('change',event=>{usageRange=event.target.value;usagePage=1;usageGeneration++;loadUsage();});
$('usage-channel').addEventListener('change',event=>{usageChannel=event.target.value;usagePage=1;usageGeneration++;loadUsage();});
$('usage-page-size').addEventListener('change',event=>{usagePageSize=Number(event.target.value);usagePage=1;renderUsageDetail();});
runTopRefresh($('usage-refresh'),()=>{usageGeneration++;usageDailyGeneration++;return Promise.all([loadUsage(),loadUsageDaily()]);});

// —— 调用统计·日度汇总视图：全历史指标条 / Token 热力图 / 每日按模型趋势 / 模型用量环形图 ——
// 数据来自 core 的 daily- 汇总（永久保留），因此逐次明细被 90 天裁掉之后，跨年的
// 指标与图形照样读得到；这里的「全历史」与页面所选窗口无关。
const usageDailyWindows = {'30d':30,'90d':90,'365d':365};
let usageWindow = '30d', usageDailyDays = [], usageDailyLifetime = null, usageDailyGeneration = 0;
// 模型配色沿用通道色，前五名之外并入「其他模型」并用中性灰，避免与任何模型撞色。
const modelColors = ['#6366f1','#06b6d4','#8b5cf6','#ec4899','#d97706'];
const otherModelColor = '#94a3b8';
const heatCut = ['hm0','hm1','hm2','hm3','hm4'];
function chartEl(tag,attrs){const el=document.createElementNS('http://www.w3.org/2000/svg',tag);for(const k in attrs)el.setAttribute(k,attrs[k]);return el;}
// Node 页面测试的 DOM 夹具默认没有 createElementNS，只有装过 SVG 夹具的用例才有；
// 三张图各自降级成一句文字，页面其余部分（指标条、明细表）照常可测可看。
function canDrawCharts(){return typeof document.createElementNS==='function';}
function chartTitle(parent,text){const t=chartEl('title',{});t.textContent=text;parent.append(t);}
// chartScroll 把 SVG 单独装进横向滚动容器：窄视口下只靠 viewBox 等比缩小的话，
// 10px 的轴标签会被压到 5px 以下，图形还在但字读不出来。图例留在容器外面，
// 滑动图形时不会跟着划出可视区。
function chartScroll(parent,svg){const box=document.createElement('div');box.className='chart-scroll';box.append(svg);parent.append(box);}
// axisLabelWidth 估算一行 y 轴标签的像素宽度。SVG 默认裁掉 viewBox 之外的内容，
// 而 y 轴标签是右对齐在 padL-6 上的：无头 Edge 实测「164,749,553」在 10px 字号下
// 宽 60.46px（数字 5.68、逗号 4.67），超过写死的 padL 就会把开头几位无声裁掉。
// 这里各留一点余量取 6.2 / 5.2，供 padL 反推留位。
function axisLabelWidth(label){let w=0;for(const c of String(label))w+=c===','?5.2:6.2;return w;}
function axisPadL(label,base){return Math.max(base,Math.ceil(axisLabelWidth(label))+12);}
// axisTicks 给出 x 轴该打标签的下标：均匀取最多 max 个，末位必须落在最后一个数据点上。
// 最后一个点离上一个候选标签不足一档时顶替掉它，而不是并排追加——并排就是压字。
function axisTicks(n,max){
 const limit=max||7;
 if(n<1)return [];
 const step=Math.max(1,Math.ceil(n/limit));
 const ticks=[];
 for(let i=0;i<n;i+=step)ticks.push(i);
 const last=n-1;
 if(ticks[ticks.length-1]!==last){
  if(last-ticks[ticks.length-1]<step)ticks[ticks.length-1]=last;
  else ticks.push(last);
 }
 return ticks;
}
function formatUsageInt(value){return Number(value||0).toLocaleString('en-US');}
// dayShift 只在 Asia/Shanghai 的日历日上加减：用宿主时区的 Date 边界会把窗口和
// 热力图的起始列挪错一天，读数会整体平移但看着仍然「像是对的」。
function dayShift(baseISO,delta){const p=baseISO.split('-').map(Number);const d=new Date(Date.UTC(p[0],p[1]-1,p[2]));d.setUTCDate(d.getUTCDate()+delta);return d.toISOString().slice(0,10);}
function todayISO(){return new Date().toLocaleDateString('en-CA',{timeZone:'Asia/Shanghai'});}
// dailyDark：那天有调用但一个 token 都没回报。它不是「0 消耗」，图形必须与
// 「没有调用」分开，否则热力图会把在用的一天画成空档。
function dailyDark(d){return d.calls>0&&d.prompt_missing===d.calls&&d.completion_missing===d.calls;}
function dailyTokens(d){return (d.prompt_tokens||0)+(d.completion_tokens||0);}
function windowDays(){return usageDailyDays.filter(d=>d.date>=dayShift(todayISO(),-(usageDailyWindows[usageWindow]-1)));}
// modelShare 把窗口内的按模型分解折成「前五名 + 其他模型」，按 Token 降序。
// Token 合计为 0 的模型不参与占比（分母只算已观测），单列出来供页面说明。
function modelShare(days){
 const byModel=new Map();
 for(const d of days)for(const m of (d.models||[])){
  const cur=byModel.get(m.model)||{model:m.model,tokens:0,calls:0};
  cur.tokens+=(m.prompt_tokens||0)+(m.completion_tokens||0);
  cur.calls+=m.calls||0;
  byModel.set(m.model,cur);
 }
 const sorted=[...byModel.values()].sort((a,b)=>b.tokens-a.tokens||(a.model<b.model?-1:1));
 const observed=sorted.filter(m=>m.tokens>0),unobserved=sorted.filter(m=>m.tokens<=0);
 const items=observed.slice(0,5).map((m,i)=>({...m,rank:i,color:modelColors[i]}));
 const rest=observed.slice(5);
 if(rest.length)items.push({model:'其他模型',tokens:rest.reduce((s,m)=>s+m.tokens,0),calls:rest.reduce((s,m)=>s+m.calls,0),color:otherModelColor,other:true});
 return {items,unobserved,total:items.reduce((s,m)=>s+m.tokens,0)};
}
// modelSwatchClass 让图例色块走 CSS 类而不是内联 style：控制台开着 CSP 的
// style-src 'self'，setAttribute('style',…) 会被静默丢弃，测试夹具里也没有 .style。
function modelSwatchClass(m){return 'trend-swatch '+(m.other?'sw-other':'sw-r'+m.rank);}
function renderUsageLifetime(){
 const lt=usageDailyLifetime;
 if(!lt){$('usage-life-line').textContent='正在读取日度汇总…';return;}
 $('usage-life-line').textContent=lt.calls>0?`全历史 ${lt.days} 个自然日有调用，累计 ${formatUsageInt(lt.calls)} 次（自 ${lt.first_date}）`:'账本里还没有成功调用记录。';
 setMetric('usage-life-tokens',lt.tokens_missing?null:lt.tokens);
 setMetric('usage-life-peak',!lt.peak_date||lt.peak_tokens_missing?null:lt.peak_tokens);
 $('usage-life-peak-note').textContent=lt.peak_date?`Token 最多的一天：${lt.peak_date}`:'Token 最多的那一天';
 $('usage-life-streak').textContent=String(lt.current_streak);
 $('usage-life-longest').textContent=String(lt.longest_streak);
}
// renderUsageHeat 铺满近一年（53 周 × 7 天），颜色分档按这一年内实际有 Token 的
// 日子取四分位，而不是写死阈值：网关流量量级差几个数量级，固定阈值会把整张图
// 刷成同一档。窗口选择只作用于趋势与占比，不切热力图。
function renderUsageHeat(){
 const wrap=$('usage-heat'),note=$('usage-heat-note');
 wrap.replaceChildren();
 const start=dayShift(todayISO(),-364);
 const days=usageDailyDays.filter(d=>d.date>=start&&d.date<=todayISO());
 if(!days.length){note.textContent='近一年没有任何日度汇总：这个网关还没有记录到成功调用。';note.hidden=false;return;}
 if(!canDrawCharts()){note.textContent='热力图需要浏览器绘制 SVG，当前环境不支持。';note.hidden=false;return;}
 const values=days.map(dailyTokens).filter(v=>v>0).sort((a,b)=>a-b);
 const quartile=p=>values.length?values[Math.min(values.length-1,Math.round(p*(values.length-1)))]:0;
 const cuts=[quartile(.25),quartile(.5),quartile(.75)];
 const level=v=>v<=0?0:v<=cuts[0]?1:v<=cuts[1]?2:v<=cuts[2]?3:4;
 const byDate=new Map(days.map(d=>[d.date,d]));
 const startD=new Date(start+'T00:00:00Z'),lead=startD.getUTCDay();
 const size=12,gap=3,step=size+gap,padL=26,padT=18;
 const weeks=Math.ceil((lead+365)/7),W=padL+weeks*step+6,H=padT+7*step+4;
 const svg=chartEl('svg',{viewBox:`0 0 ${W} ${H}`,class:'heat-svg',role:'img','aria-label':'近一年每日 Token 用量热力图'});
 const defs=chartEl('defs',{}),pat=chartEl('pattern',{id:'heat-dark',width:4,height:4,patternUnits:'userSpaceOnUse',patternTransform:'rotate(45)'});
 pat.append(chartEl('rect',{width:4,height:4,fill:'#fdf1dc'}),chartEl('line',{x1:0,y1:0,x2:0,y2:4,stroke:'#7a5210','stroke-width':1.5}));
 defs.append(pat);svg.append(defs);
 const months=[];
 for(let i=0;i<365;i++){
  const date=dayShift(start,i);
  if(date>todayISO())break;
  const d=byDate.get(date),col=Math.floor((lead+i)/7),row=(lead+i)%7;
  const x=padL+col*step,y=padT+row*step;
  const label=date.slice(5,7)+'月';
  // 月标签固定落在网格上方，跟着该月第一列的 x 走；贴着格子会飘进网格中间。
  if(Number(date.slice(8,10))<=7&&months[months.length-1]!==label){months.push(label);const t=chartEl('text',{x,y:padT-6,class:'heat-month'});t.textContent=label;svg.append(t);}
  const rect=chartEl('rect',{x,y,width:size,height:size,rx:2,'data-date':date,class:'heat-cell '+heatCut[0],'data-level':'0'});
  if(d&&d.calls){
   if(dailyDark(d)){rect.setAttribute('class','heat-cell heat-dark');rect.setAttribute('data-level','dark');}
   else{const lv=level(dailyTokens(d));rect.setAttribute('class','heat-cell '+heatCut[lv]);rect.setAttribute('data-level',String(lv));}
  }
  chartTitle(rect,`${date} · ${d?d.calls:0} 次调用 · ${!d?'无记录':dailyDark(d)?'Token 未观测':formatUsageInt(dailyTokens(d))+' tokens'}${d&&d.denied?` · 鉴权失败 ${d.denied} 次`:''}`);
  svg.append(rect);
 }
 for(const [row,name] of [[1,'一'],[3,'三'],[5,'五']]){const t=chartEl('text',{x:0,y:padT+row*step+10,class:'heat-month'});t.textContent=name;svg.append(t);}
 chartScroll(wrap,svg);
 const legend=document.createElement('div');legend.className='heat-legend';
 const swatch=(cls,text)=>{const s=document.createElement('span');s.className='heat-sw '+cls;const label=document.createElement('span');label.className='heat-sw-label';label.textContent=text;legend.append(s,label);};
 swatch(heatCut[0],'无调用');swatch(heatCut[1],'少');swatch(heatCut[2],'');swatch(heatCut[3],'');swatch(heatCut[4],'多');
 const darkCount=days.filter(d=>d.calls>0&&dailyDark(d)).length;
 swatch('heat-sw-dark',darkCount?`Token 未观测（${darkCount} 天）`:'Token 未观测');
 wrap.append(legend);
 note.hidden=true;
}
// renderUsageTrend 画按日（超过 60 个点则按周合并）堆叠柱：一根柱一个时间段，
// 柱内按模型分段。整段都没回报 Token 的时间段画不出高度，改由下方文字交代。
function renderUsageTrend(days,share){
 const wrap=$('usage-daily-trend'),note=$('usage-daily-note');
 wrap.replaceChildren();
 if(!days.length){note.textContent='统计窗口内没有日度汇总：把窗口放宽一档，或先在网关发起几次调用。';note.hidden=false;return;}
 const span=Math.max(1,Math.ceil(days.length/60));
 const buckets=[];
 for(let i=0;i<days.length;i+=span){
  const part=days.slice(i,i+span);
  buckets.push({label:span>1?`${part[0].date} ~ ${part[part.length-1].date}`:part[0].date,parts:part});
 }
 // 段序固定按图例里的排名（Token 降序）从下往上堆，不随当天数据抖动，
 // 同一份账本每次刷新画出的是同一张图。
 const top=new Set(share.items.filter(m=>!m.other).map(m=>m.model));
 const hasOther=share.items.some(m=>m.other);
 const colorOf=new Map(share.items.map(m=>[m.model,m.color]));
 const stacks=buckets.map(b=>{
  const acc=new Map();
  for(const d of b.parts)for(const m of (d.models||[])){
   const tokens=(m.prompt_tokens||0)+(m.completion_tokens||0);
   if(tokens<=0)continue;
   const key=top.has(m.model)?m.model:(hasOther?'其他模型':'');
   if(!key)continue;
   const cur=acc.get(key)||{model:key,tokens:0,calls:0};
   cur.tokens+=tokens;cur.calls+=m.calls||0;
   acc.set(key,cur);
  }
  return {label:b.label,segs:share.items.filter(m=>acc.has(m.model)).map(m=>acc.get(m.model)),tot:b.parts.reduce((s,d)=>s+dailyTokens(d),0)};
 });
 if(stacks.every(s=>!s.tot)){note.textContent='统计窗口内有调用，但上游都没有回报 Token，趋势柱与占比无从画起（次数与积分见下方明细表）。';note.hidden=false;return;}
 if(!canDrawCharts()){note.textContent='趋势图需要浏览器绘制 SVG，当前环境不支持。';note.hidden=false;return;}
 let yMax=1;for(const s of stacks)if(s.tot>yMax)yMax=s.tot;
 const maxText=formatUsageInt(yMax);
 // 绘图区左边按标签自身宽度留位：亿级读数在写死的 padL 下会被 viewBox 裁掉开头。
 const W=620,H=240,padL=axisPadL(maxText,48),padR=14,padT=14,padB=36,plotW=W-padL-padR,plotH=H-padT-padB;
 const band=plotW/stacks.length,barW=Math.min(20,band*.72);
 const x=i=>padL+band*i+(band-barW)/2;
 const y=v=>padT+plotH-(v/yMax)*plotH;
 const svg=chartEl('svg',{viewBox:`0 0 ${W} ${H}`,class:'trend-svg',role:'img','aria-label':'每日 Token 用量堆叠柱状图（按模型）'});
 svg.append(chartEl('line',{x1:padL,y1:y(0),x2:padL+plotW,y2:y(0),class:'trend-axis'}));
 const maxTick=chartEl('text',{x:padL-6,y:y(yMax)+4,class:'trend-tick','text-anchor':'end'});maxTick.textContent=maxText;svg.append(maxTick);
 const zeroTick=chartEl('text',{x:padL-6,y:y(0)+4,class:'trend-tick','text-anchor':'end'});zeroTick.textContent='0';svg.append(zeroTick);
 for(const i of axisTicks(stacks.length)){const s=stacks[i];const t=chartEl('text',{x:padL+band*i+band/2,y:padT+plotH+16,class:'trend-tick','text-anchor':'middle'});t.textContent=s.label.slice(5,10);chartTitle(t,s.label);svg.append(t);}
 stacks.forEach((s,i)=>{
  let acc=0;
  for(const seg of s.segs){
   const rect=chartEl('rect',{x:x(i),y:y(acc+seg.tokens),width:barW,height:Math.max(1,y(acc)-y(acc+seg.tokens)),fill:colorOf.get(seg.model),class:'trend-bar','data-model':seg.model,'data-date':s.label});
   chartTitle(rect,`${s.label} · ${seg.model} · ${formatUsageInt(seg.tokens)} tokens（${seg.calls} 次）`);
   svg.append(rect);acc+=seg.tokens;
  }
 });
 chartScroll(wrap,svg);
 const legend=document.createElement('div');legend.className='trend-legend';
 for(const m of share.items){
  const item=document.createElement('span');item.className='trend-legend-item';
  const sw=document.createElement('i');sw.className=modelSwatchClass(m);
  const label=document.createElement('span');label.textContent=m.model;
  item.append(sw,label);legend.append(item);
 }
 wrap.append(legend);
 const notes=[];
 const darkDays=days.filter(d=>d.calls&&dailyDark(d)).length;
 if(darkDays)notes.push(`${darkDays} 天有调用但未回报 Token，未计入柱高`);
 if(share.unobserved.length)notes.push(`Token 合计为 0 的模型：${share.unobserved.map(m=>`${m.model}（${m.calls} 次）`).join('、')}`);
 note.textContent=notes.join('；');note.hidden=!notes.length;
}
// renderUsageShare 画环形图 + 排名。环形只表达已观测 Token 的份额，中心读数与
// 排名合计同源，避免两处各自取整后对不上。
function renderUsageShare(share){
 const wrap=$('usage-model-donut'),legend=$('usage-model-legend');
 wrap.replaceChildren();legend.replaceChildren();
 if(!share.items.length||!share.total){
  const p=document.createElement('p');p.className='empty';p.textContent='统计窗口内没有任何可累计的 Token 观测，环形占比无从算起。';
  wrap.append(p);return;
 }
 if(canDrawCharts()){
  const R=52,C=2*Math.PI*R;
  const svg=chartEl('svg',{viewBox:'0 0 140 140',class:'donut-svg',role:'img','aria-label':`模型 Token 用量占比环形图，合计 ${formatUsageInt(share.total)} tokens`});
  svg.append(chartEl('circle',{cx:70,cy:70,r:R,fill:'none',class:'donut-track','stroke-width':24}));
  let offset=0;
  for(const m of share.items){
   const len=m.tokens/share.total*C;
   const seg=chartEl('circle',{cx:70,cy:70,r:R,fill:'none',stroke:m.color,'stroke-width':24,'stroke-dasharray':`${len} ${C-len}`,'stroke-dashoffset':-offset,transform:'rotate(-90 70 70)',class:'donut-seg','data-model':m.model});
   chartTitle(seg,`${m.model} · ${formatUsageInt(m.tokens)} tokens（${(m.tokens/share.total*100).toFixed(1)}%）`);
   svg.append(seg);offset+=len;
  }
  const totalText=chartEl('text',{x:70,y:68,class:'donut-total','text-anchor':'middle'});totalText.textContent=formatUsageInt(share.total);
  const unitText=chartEl('text',{x:70,y:84,class:'donut-unit','text-anchor':'middle'});unitText.textContent='Tokens';
  svg.append(totalText,unitText);
  wrap.append(svg);
 }
 share.items.forEach((m,i)=>{
  const row=document.createElement('div');row.className='donut-row';row.setAttribute('data-model',m.model);
  const sw=document.createElement('i');sw.className=modelSwatchClass(m);
  const name=document.createElement('span');name.className='donut-name';name.textContent=`${i+1}. ${m.model}`;name.title=m.model;
  const tokens=document.createElement('span');tokens.className='donut-value';tokens.textContent=formatUsageInt(m.tokens);
  const pct=document.createElement('span');pct.className='donut-pct';pct.textContent=(m.tokens/share.total*100).toFixed(1)+'%';
  const calls=document.createElement('span');calls.className='donut-calls';calls.textContent=Number(m.calls).toLocaleString('en-US')+' 次';
  row.append(sw,name,tokens,pct,calls);legend.append(row);
 });
}
function renderUsageDaily(){
 renderUsageLifetime();
 renderUsageHeat();
 const days=windowDays(),share=modelShare(days);
 renderUsageTrend(days,share);
 renderUsageShare(share);
}
async function loadUsageDaily(){
 if(!csrf)return;
 const generation=usageDailyGeneration;
 try{
  const data=await jsonAPI('usage-daily?window=365d');
  if(generation!==usageDailyGeneration)return;
  usageDailyDays=data.days||[];usageDailyLifetime=data.lifetime||null;
  renderUsageDaily();
 }catch(error){if(generation===usageDailyGeneration)$('usage-life-line').textContent=`日度汇总读取失败：${error.message}`;}
}
$('usage-window').addEventListener('change',event=>{usageWindow=usageDailyWindows[event.target.value]?event.target.value:'30d';renderUsageDaily();});

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
  badge.className='badge '+(status.reachable?'ok':'bad');
  $('zcode-status-text').textContent=status.reachable?`zcode-proxy 可达 · ${status.model_count} 个 GLM 模型`:'zcode-proxy 当前不可达，请确认容器已启动；若容器在运行，通常是 console 刚被重启、旁路网络失联，重启旁路容器即可恢复。';
 } else if(!loggedIn){
  badge.textContent='未登录';badge.className='badge warn';
  $('zcode-status-text').textContent='尚未保存 GLM 凭据，切换到「GLM 登录」完成登录后即可启用通道。';
 } else if(running){
  badge.textContent=status.reachable?'在线':'运行中 · 探测失败';
  badge.className='badge '+(status.reachable?'ok':'bad');
  $('zcode-status-text').textContent=status.reachable?`zcode-proxy 可达 · ${status.model_count} 个 GLM 模型`:'代理已启动，但模型探测失败，可能是上游凭据或额度异常。';
 } else {
  badge.textContent='已登录 · 未启用';badge.className='badge idle';
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
 // 备注的显隐由服务端下发的判据决定，不再自己拿 plan 猜：权益过滤时 plan 仍是
 // start-plan，只看 plan 会把「按权益裁短」说成「按档位只留 flash」，与事实不符。
 $('zcode-models-note').hidden=!status.model_filter;
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
runTopRefresh($('zcode-refresh'),loadZcode);
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
  badge.className='badge '+(status.reachable?'ok':'bad');
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
runTopRefresh($('qoder-refresh'),loadQoder);
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
// FreeLLMAPI 免费池：免登录、无 plan/provider 面板；状态、模型目录与测试对话全按 /admin/freellm 单端点走。
async function loadFreellm(){
 if(!csrf)return;
 try{renderFreellm(await jsonAPI('freellm'));}
 catch(error){notice(error.message);}
}
async function renderFreellm(session){
 freellmStatus = session;
 const on = freellmEnabled;
 $('freellm-disabled').hidden = on;
 $('freellm-content').hidden = !on;
 if(!on) return;
 renderFreellmHealth(session);
 const dataTab=on&&Array.isArray(session.models)&&session.models.length>0;
 if(freellmView!=='status'&&!dataTab)freellmView='status';
 $('freellm-view-models').hidden=!dataTab;
 $('freellm-view-chat').hidden=!dataTab;
 for(const view of ['status','models','chat']){
  $('freellm-view-'+view).classList.toggle('active',view===freellmView);
  $('freellm-pane-'+view).hidden=view!==freellmView;
 }
 if(dataTab){
  freellmModels=(session.models||[]).map(item=>item.id).filter(Boolean);
  renderModelTable('freellm',session.models,null);
  renderAccessModelSelect();renderAccess();
  const previous=$('freellm-model').value;
  $('freellm-model').replaceChildren(...freellmModels.map(id=>new Option(id,id)));
  if(freellmModels.includes(previous))$('freellm-model').value=previous;
  $('freellm-model').disabled=!freellmModels.length||!!activeRequest;
  $('freellm-send').disabled=!freellmModels.length||!!activeRequest;
 }
}
function renderFreellmHealth(session){
 const wrap=$('freellm-health-wrap'), body=$('freellm-health-body');
 if(!wrap||!body) return;
 const on=freellmEnabled;
 wrap.hidden=!on;
 body.replaceChildren();
 if(!on) return;
 // 三个口径分开读：进程活着（reachable）≠ 池里有能服务的上游（pool_ready）
 // ≠ 目录有货（model_count）。免费池最常见的误判就是「目录 259 条」被当成
 // 「259 条能用」——上游自己用 execution_status 区分，缺 key 的条目调用必 503。
 const counts=session.provider_counts;
 const pools=Array.isArray(session.providers)?session.providers:[];
 const quota=Array.isArray(session.quota_forecast&&session.quota_forecast.pools)?session.quota_forecast.pools:[];
 const rows=[
  ['进程',session.reachable?'在线':'不可达'],
  ['供应商池',session.pool_ready?`就绪 · 可服务上游 ${session.ready_upstreams||0} 个`:session.pool_reason?`暂无可用上游（${session.pool_reason}）`:'暂无可用上游'],
  ['模型目录',`共 ${session.model_count||0} 条 · 此刻可调用 ${session.callable_count||0} 条`],
  ['key 池',counts?`健康 ${counts.healthy||0} · 限流 ${counts.rate_limited||0} · 失效 ${counts.invalid||0} · 未知 ${counts.unknown||0}`:'未上报'],
 ];
 if((session.callable_count||0)===0&&(session.model_count||0)>0){
  rows.push(['为什么不可用','目录是上游的全网能力清单，与是否配好供应商 key 无关；配齐后此处自动出现可调用条目。']);
 }
 for(const p of pools.slice(0,8)){
  const bits=[p.status||'未知'];
  if(typeof p.keys==='number') bits.push(`${p.keys} 把 key`);
  if(p.requests_remaining_pct!=null) bits.push(`请求余量 ${p.requests_remaining_pct}%`);
  if(p.resume_at) bits.push(`${new Date(p.resume_at).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false})} 恢复`);
  if(p.last_error) bits.push(String(p.last_error));
  rows.push([p.name||p.platform||'平台',bits.join(' · ')]);
 }
 // 日帽余量来自 /v1/quota-forecast。used/remaining/limit 都可能是 null
 // （上游没报窗口就是未知），未知不拿 0 冒充。
 for(const q of quota.slice(0,6)){
  const bits=[];
  if(q.remaining_pct!=null) bits.push(`余 ${q.remaining_pct}%`);
  if(q.remaining!=null&&q.limit!=null) bits.push(`${q.remaining}/${q.limit} 次`);
  if(q.seconds_until_reset!=null) bits.push(`${Math.max(1,Math.round(q.seconds_until_reset/60))} 分钟后重置`);
  if(q.low_balance===true) bits.push('余额偏低，建议本窗口内少投');
  rows.push([q.pool||q.platform||'配额池',bits.length?bits.join(' · '):'窗口未知']);
 }
 for(const [k,v] of rows){const tr=document.createElement('tr');const td1=document.createElement('td');td1.textContent=k;const td2=document.createElement('td');td2.textContent=String(v);tr.append(td1,td2);body.append(tr);}
}
function renderOpenCode(status){
 opencodeStatus=status;
 const enabled=!!status.enabled;
 $('opencode-disabled').hidden=enabled;
 $('opencode-content').hidden=!enabled;
 if(!enabled)return;
 const badge=$('opencode-status-badge');
 badge.textContent=status.reachable?'在线':'不可达';
 badge.className='badge '+(status.reachable?'ok':'bad');
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
runTopRefresh($('opencode-refresh'),async()=>{
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

for(const view of ['status','models','chat'])$('freellm-view-'+view).addEventListener('click',()=>{freellmView=view;freellmViewTouched=true;if(freellmStatus)renderFreellm(freellmStatus);});
$('freellm-stop').addEventListener('click',()=>activeRequest?.abort());
runTopRefresh($('freellm-refresh'),async()=>{
 if(freellmBusy)return;
 const generation=sessionGeneration;freellmBusy=true;$('freellm-refresh').disabled=true;
 try{await loadFreellm();}finally{if(generation===sessionGeneration){freellmBusy=false;$('freellm-refresh').disabled=false;}}
});

// FreeLLMAPI 测试对话：POST /admin/freellm/chat（非流式），status 200/400 由 adminFreellmChat 判定。
$('freellm-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('freellm-prompt').value.trim();if(!text||!$('freellm-model').value)return;
 const generation=sessionGeneration;
 notice('');message('user',text,$('freellm-messages'));$('freellm-prompt').value='';const answer=message('assistant','正在等待模型…',$('freellm-messages'));
 const controller=new AbortController();activeRequest=controller;
 try{
  const response=await api('freellm/chat',{model:$('freellm-model').value,message:text},controller.signal);
  const result=await response.json().catch(()=>({}));
  if(!response.ok)throw new Error(typeof result.error==='string'?result.error:result.error?.message||`HTTP ${response.status}`);
  const content=result.content||result.message?.content?.[0]?.text||'模型未返回文本内容。';
  answer.content.textContent=content;
  freellmHistory=[{role:'user',content:text},{role:'assistant',content}];
  const u=result.usage||{};
  $('freellm-usage').textContent=(u.total_tokens!=null?`输入 ${u.prompt_tokens??'—'} · 输出 ${u.completion_tokens??'—'} · 总计 ${u.total_tokens} tokens`:'上游未返回用量')+' · 本次通道：FreeLLMAPI（免费池）';
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=msg;$('freellm-usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;}
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
 const isAutoClaw=model.startsWith('autoclaw-');
 const isTrae=model.startsWith('trae-');
 const isTraeAI=model.startsWith('traeai-');
 const isRaccoon=model.startsWith('raccoon-');
 if(anthropic&&(isGlm||isQoder||isOpenCode||isAutoClaw||isTrae||isTraeAI||isRaccoon)){$('copy-example').disabled=true;$('api-example').textContent=(isGlm?'GLM':isQoder?'Qoder':isOpenCode?'OpenCode':isAutoClaw?'AutoClaw':isTrae?'Trae':isTraeAI?'Trae 国际版':'小浣熊')+' 通道仅支持 OpenAI 协议（/v1/chat/completions），请切换协议后复制示例。';return;}
 const body={model,...(anthropic?{max_tokens:1024}:{}),messages:[{role:'user',content:'你好'}],stream:true};
 const auth=anthropic?'  -H "x-api-key: <你的 API Key>" \\\n  -H "anthropic-version: 2023-06-01"':'  -H "Authorization: Bearer <你的 API Key>"';
 const json=JSON.stringify(body,null,2).replace(/'/g,"'\\''");
 let prefix='';
 if(isGlm) prefix='# GLM 模型经 zcode 通道转发（按 glm- 前缀分流），账号为容器登录态\ncurl ';
 else if(isQoder) prefix='# Qoder 模型经 qoder 通道转发（按 qoder- 前缀分流）\ncurl ';
 else if(isOpenCode) prefix='# OpenCode 免费模型经 OW Bridge sidecar 转发（按 opencode- 前缀分流）\ncurl ';
 else if(isAutoClaw) prefix='# AutoClaw 模型经 autoclaw2api 桥接转发（按 autoclaw- 前缀分流），账号为容器内登录态\ncurl ';
 else if(isTrae) prefix='# Trae 模型经 trae2api 桥接转发（按 trae- 前缀分流），账号为容器内登录态\ncurl ';
 else if(isTraeAI) prefix='# Trae 国际版模型经 trae2api 桥接转发（按 traeai- 前缀分流），账号为容器内登录态\ncurl ';
 else if(isRaccoon) prefix='# 小浣熊模型经 raccoon2api 桥接转发（按 raccoon- 前缀分流），账号为导入的桌面端凭证\ncurl ';
 // freellm 不进上面那条 Anthropic 拒绝名单：上游 FreeLLMAPI 自己实现了 /v1/messages 与
 // /v1/responses（routes/anthropic.ts、routes/responses.ts），三种入口都转发得过去。
 else if(model.startsWith('freellm-')) prefix='# FreeLLMAPI 免费池模型经 sidecar 转发（按 freellm- 前缀分流），供应商 key 由 sidecar 自管\ncurl ';
 else if(!anthropic) prefix='# 成功响应头 X-Account / X-Account-Realm 标明本次服务的账号与平台\ncurl ';
 else prefix='curl ';
 $('api-example').textContent=prefix+$('api-endpoint').value+` \\\n${auth} \\\n  -H "Content-Type: application/json" \\\n  -d '${json}'`;
}
document.querySelectorAll('[data-protocol]').forEach(button=>button.addEventListener('click',()=>setProtocol(button.dataset.protocol)));
$('access-model').addEventListener('change',renderAccess);
$('copy-example').addEventListener('click',async()=>{if($('copy-example').disabled)return;try{await navigator.clipboard.writeText($('api-example').textContent);notice('调用示例已复制，请替换 API Key 占位符');}catch{notice('浏览器不允许自动复制，请手动复制下方示例');}});
$('go-chat').addEventListener('click',()=>{if(activeRequest||$('go-chat').disabled)return;const model=$('access-model').value;if(model.startsWith('glm-')){if(zcodeModels.includes(model))$('zcode-model').value=model;showPage('zcode');return;}if(model.startsWith('qoder-')){if(qoderModels.includes(model))$('qoder-model').value=model;showPage('qoder');return;}if(model.startsWith('opencode-')){if(opencodeModels.includes(model))$('opencode-model').value=model;showPage('opencode');return;}if(model.startsWith('autoclaw-')){if(autoclawModels.includes(model))$('autoclaw-model').value=model;showPage('autoclaw');return;}if(model.startsWith('trae-')){if(traeModels.includes(model))$('trae-model').value=model;showPage('trae');return;}if(model.startsWith('traeai-')){if(traeaiModels.includes(model))$('traeai-model').value=model;showPage('traeai');return;}if(model.startsWith('raccoon-')){if(raccoonModels.includes(model))$('raccoon-model').value=model;showPage('raccoon');return;}if(model.startsWith('freellm-')){if(freellmModels.includes(model))$('freellm-model').value=model;showPage('freellm');return;}$('model').value=model;updateEfforts();setWorkBuddyView('chat');showPage('workbuddy');});
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
for(const [inputId,formId] of [['prompt','chat-form'],['zcode-prompt','zcode-form'],['qoder-prompt','qoder-form'],['opencode-prompt','opencode-form'],['freellm-prompt','freellm-form'],['autoclaw-prompt','autoclaw-form'],['trae-prompt','trae-form'],['traeai-prompt','traeai-form'],['raccoon-prompt','raccoon-form']])$(inputId).addEventListener('keydown',event=>{
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
const routeChannelLabels={core:'核心账号池',glm:'GLM 通道',qoder:'Qoder 通道',opencode:'OpenCode 通道',freellm:'FreeLLMAPI 通道',autoclaw:'AutoClaw 通道',trae:'Trae 通道',traeai:'Trae 国际版通道',raccoon:'小浣熊通道'};
function routeChannelLabel(channel){return routeChannelLabels[channel]||channel||'—';}
async function loadRoutes(){
 if(!csrf)return;
 try{
  const [state,models]=await Promise.all([jsonAPI('route'),jsonAPI('models').catch(()=>null)]);
  if(models)applyModelHealth(models);renderRoutes(state);
  // 榜单只在别名页用得上，跟着这条路走懒加载；服务端 24h 缓存，重复点「刷新」不会打穿外网。
  if(state.enabled)void loadCapabilities();
 }catch(error){notice(error.message);}
}
function renderRoutes(state){
 routesState=state;
 const enabled=!!state.enabled;
 $('routes-disabled').hidden=enabled;$('routes-content').hidden=!enabled;
 if(!enabled)return;
 const preview=state.auto_preview||{};
 const badge=$('routes-auto-badge');
 if(preview.routed_model&&!preview.fallback){badge.textContent='自动优选';badge.className='badge ok';}
 else if(preview.routed_model){badge.textContent='用兜底模型';badge.className='badge warn';}
 else {badge.textContent='未知';badge.className='badge unknown';}
 $('routes-auto-text').textContent=preview.routed_model?`现在自动挑中的是 ${preview.routed_model}（${routeChannelLabel(preview.channel)}）· ${preview.reason||''}`:'暂时看不出 auto 会挑哪个模型。';
 $('routes-default-fallback').textContent=state.default_auto_fallback||'cn:auto';
 $('routes-auto-fallback').value=state.auto_fallback||'';
 const select=$('routes-alias-channel'),previous=select.value;
 select.replaceChildren();
 for(const channel of state.channels||[])if(channel.enabled)select.appendChild(new Option(routeChannelLabel(channel.channel),channel.channel));
 const enabledChannels=(state.channels||[]).filter(channel=>channel.enabled).map(channel=>channel.channel);
 if(enabledChannels.includes(previous))select.value=previous;
 // 没得保持（首次渲染、或原通道被停用）就显式落到第一个可用通道：不能只靠浏览器默认选中第一项，
 // 否则「走哪个通道」与下面「指向哪个模型」的候选可能对不上（问题3）。
 else if(enabledChannels.length)select.value=enabledChannels[0];
 fillRouteModelOptions(select.value);
 renderRouteAliases(state.aliases||[]);
 renderRouteChannels(state.channels||[]);
 renderRouteExample(state);
 renderCapabilities();
}
// routeAliasStateCell 一列里并排两个徽章：启停态（与模型表同口径）与探测结论。
// 两者是不同维度的东西——「已启用」是配置态，「不可用」是运行态——合成一个徽章
// 会丢信息：启用着但从没测过、和启用着但调不通，要做的下一步完全不同。
// 停用中只给启停徽章：配置上就没开，旧探测结论拿来显示只会误导。
function routeAliasStateCell(entry){
 const td=document.createElement('td');
 const row=document.createElement('span');row.className='badge-row';
 const enabled=document.createElement('span');enabled.className='badge'+(entry.enabled?' ok':' idle');
 enabled.textContent=entry.enabled?'已启用':'已停用';
 row.append(enabled);
 if(entry.enabled){
  const run=modelProbeState(entry.probe);
  const probe=document.createElement('span');probe.className='badge'+(run.tone?' '+run.tone:'');
  probe.textContent=run.label;
  row.append(probe);
 }
 td.append(row);
 return td;
}
// routeModelCell 渲染「真实模型」列：正文只放真实模型名，对外模型 ID 收进 title，
// 鼠标悬停即可对照。两个名字本来就是同一条目的两个视角，并排两列会把表撑宽近一倍。
function routeModelCell(entry){
 const td=cell(entry.model);
 td.title=`对外模型 ID：${entry.public_model||entry.model}`;
 return td;
}
// routeAliasNameCell 在别名后面挂一对排序把手。顺序就是 routes.json 的数组顺序，
// 点一下即重排并整表保存；首行禁上移、末行禁下移，避免点了没反应。
function routeAliasNameCell(entry,index,total){
 const td=document.createElement('td');
 const name=document.createElement('span');name.textContent=entry.alias;td.append(name);
 const order=document.createElement('span');order.className='route-order';
 const up=document.createElement('button');up.type='button';up.className='quiet route-move';up.textContent='↑';up.title='上移';up.disabled=index===0;
 up.addEventListener('click',()=>moveRouteAlias(index,-1));
 const down=document.createElement('button');down.type='button';down.className='quiet route-move';down.textContent='↓';down.title='下移';down.disabled=index===total-1;
 down.addEventListener('click',()=>moveRouteAlias(index,1));
 order.append(up,down);td.append(order);
 return td;
}
function renderRouteAliases(aliases){
 const body=$('routes-alias-body');body.replaceChildren();
 $('routes-alias-empty').hidden=aliases.length>0;
 aliases.forEach((entry,index)=>{
  const tr=document.createElement('tr');
  tr.append(routeAliasNameCell(entry,index,aliases.length),channelCell(routeChannelLabel(entry.channel),entry.channel),routeModelCell(entry),routeAliasStateCell(entry),numCell(speedText(entry.probe)),cell(entry.note||'—'));
  const actions=document.createElement('td');
  actions.className='route-actions';
  // 启停走整组端点：服务端会把「这个真实模型 + 所有指向它的别名」一次写成同一
  // 状态，所以模型列表里同一个模型的开关会跟着变，反之亦然。
  const toggle=document.createElement('button');toggle.className='secondary';toggle.textContent=entry.enabled?'停用':'启用';
  toggle.addEventListener('click',()=>routeAliasSwitch(entry,!entry.enabled));
  // 测试真发一条最小对话，失败把上游原文带回页面。批量测试跑着的时候一并禁用：
  // 两个请求会抢同一份上游额度，结论还会互相覆盖。
  const test=document.createElement('button');test.className='secondary route-test';test.textContent='测试';
  test.disabled=routeTestBusy;
  if(routeTestBusy)test.title='一键测试进行中，请等它跑完';
  test.addEventListener('click',()=>routeAliasTest(entry,test));
  const edit=document.createElement('button');edit.className='secondary';edit.textContent='编辑';edit.addEventListener('click',()=>startRouteEdit(entry));
  const remove=document.createElement('button');remove.className='quiet';remove.textContent='删除';let removeArmed=false;
  remove.addEventListener('click',()=>{
   if(!removeArmed){removeArmed=true;remove.textContent='再次点击确认删除';return;}
   saveRouteAliases(currentRouteAliases().filter(item=>item.alias!==entry.alias));
  });
  actions.append(toggle,test,edit,remove);tr.append(actions,modelHealthCell(entry.channel,entry.public_model||entry.model,entry.probe));body.append(tr);
 });
}
// moveRouteAlias 上下移动一条别名并整表保存；越界直接忽略（按钮本身也已禁用）。
function moveRouteAlias(index,delta){
 const list=currentRouteAliases();
 const target=index+delta;
 if(target<0||target>=list.length)return;
 const moved=list.splice(index,1)[0];
 list.splice(target,0,moved);
 void saveRouteAliases(list);
}
// routeAliasSwitch 改一个别名的启停。走整组端点：服务端同时改模型开关表与所有
// 指向该模型的别名，响应里同时带回 aliases 与 overrides，一次刷新两边。
async function routeAliasSwitch(entry,enabled){
 // 批量测试跑着的时候不让改启停：改完那半批的结论就对不上新的启停态了，
 // 用户会看到一张自相矛盾的表。按钮没禁用，所以要说清为什么点了没反应。
 if(routeTestBusy){notice('一键测试正在进行，等它跑完再改启停');return;}
 if(routesBusy)return;
 routesBusy=true;
 try{
  const state=await jsonAPI('route/switch',{model:entry.public_model||entry.model,enabled});
  renderRoutes(state);
  applyModelSwitchState(state);
  notice(`${entry.alias} 已${enabled?'启用':'停用'}`);
 }catch(error){notice(error.message);}
 finally{routesBusy=false;}
}
// routeAliasTest 对单个别名发一条最小对话。失败不吞：页面提示 + console.error
// 打出完整结论，方便直接复制排查。
async function routeAliasTest(entry,button){
 // 批量测试进行中时这个按钮已经被渲染成禁用态（见 renderRouteAliases）：两个请求
 // 会抢同一份上游额度，结论还会互相覆盖。这里不再重复解释，进度条就挂在表头上方。
 if(button.disabled)return;
 const label=button.textContent;button.disabled=true;button.textContent='测试中…';
 try{
  const result=await jsonAPI('route/test',{alias:entry.alias});
  if(result&&result.ok===true){
   const speed=speedText(result);
   notice(`${entry.alias} 可用 · ${formatProbeDuration(result.durationMs)}${speed!=='—'?` · ${speed}`:''}`);
  }else{
   console.error(`[别名测试] ${entry.alias} 失败`,result);
   notice(`${entry.alias} 测试失败：${routeTestDetail(result)}`);
  }
  // 结论已写回服务端探测缓存，重拉一次状态让状态与速度列跟着更新。
  await loadRoutes();
 }catch(error){
  console.error(`[别名测试] ${entry.alias} 请求失败`,error);
  notice(`${entry.alias} 测试失败：${error.message}`);
 }finally{button.disabled=false;button.textContent=label;}
}
// routeTestDetail 拼失败原因：优先用上游/服务端给的原文，没有再退回分类标签。
function routeTestDetail(result){
 const raw=result&&typeof result.error==='string'?result.error:'';
 if(raw)return raw;
 const category=result&&typeof result.category==='string'?result.category:'';
 return modelFailureLabels[category]||category||'未知原因';
}
// routeTestConcurrency 是「一键测试」同时打出去的上游请求数。取 2 与后端批量探测的
// probeConcurrency 同档：上游就是同一批账号，页面上开得比后端还猛只会互相挤。
const routeTestConcurrency=2;
// routeTestCandidates 挑出「一键测试」真正要跑的条目：
//  - 已停用的别名跳过并如实计数。服务端对停用别名直接回 alias_disabled，测了也不出结论，
//    把它算进「失败」会平白吓人一跳。
//  - 多个别名指向同一个通道同一个模型时只测一次。探测结论本来就按「通道 + 公共模型名」
//    存（见后端 aliasProbe），重复测只是白烧上游额度。
function routeTestCandidates(aliases){
 const seen=new Set(),list=[],skipped=[];
 for(const entry of aliases||[]){
  if(entry.enabled===false){skipped.push(entry.alias);continue;}
  const key=`${entry.channel}::${entry.public_model||entry.model}`;
  if(seen.has(key))continue;
  seen.add(key);list.push(entry);
 }
 return{list,skipped};
}
// routeAliasTestAll 把当前启用的别名逐个跑一遍，进度与结论都落在页面上。
// 结束后统一 loadRoutes()：批量测试的价值就在于「一眼看出哪几条红了」，
// 而红绿读的是服务端刚写回的探测缓存，所以必须重拉一次状态。
async function routeAliasTestAll(button){
 if(routeTestBusy||routesBusy)return;
 const aliases=(routesState&&routesState.aliases)||[];
 const{list,skipped}=routeTestCandidates(aliases);
 if(!list.length){notice('还没有可测试的别名：先在下面添加一个');return;}
 routeTestBusy=true;
 const label=button.textContent;button.disabled=true;button.textContent='测试中…';
 // 重渲染一次，让「测试」单条按钮跟着进入禁用态。
 renderRouteAliases(aliases);
 const progress=$('routes-alias-test-progress');progress.hidden=false;
 const skippedText=skipped.length?` · 跳过 ${skipped.length} 个已停用`:'';
 const failures=[];let ok=0,done=0;
 const update=()=>{progress.textContent=`正在测试 ${done}/${list.length} · 可用 ${ok} · 失败 ${failures.length}${skippedText}`;};
 update();
 try{
  const queue=list.slice();
  const worker=async()=>{
   while(queue.length){
    const entry=queue.shift();
    let result;
    try{result=await jsonAPI('route/test',{alias:entry.alias});}
    // 请求本身炸了（网络断、会话过期）也要算一条失败并留下原因，不能让进度卡住。
    catch(error){result={ok:false,category:'request_failed',error:error.message};}
    if(result&&result.ok===true)ok++;
    else failures.push({alias:entry.alias,model:entry.public_model||entry.model,result});
    done++;update();
   }
  };
  await Promise.all(Array.from({length:Math.min(routeTestConcurrency,list.length)},worker));
 }finally{
  // 先放开标志再刷新：loadRoutes 会重渲染别名表，带着旧标志渲染的话
  // 单条「测试」按钮会一直停在禁用态。
  routeTestBusy=false;
  try{await loadRoutes();}catch(error){/* 刷新失败不影响下面已拿到的汇总 */}
  progress.textContent=`测试完成 · 可用 ${ok} · 失败 ${failures.length}${skippedText}`
   +(failures.length?` · 失败：${failures.map(item=>item.alias).join('、')}`:'');
  if(failures.length)console.error('[一键测试] 失败清单',failures);
  notice(failures.length
   ?`一键测试完成：${ok} 个可用，${failures.length} 个失败（原因见状态列与控制台日志）`
   :`一键测试完成：${list.length} 个全部可用`);
  button.disabled=false;button.textContent=label;
 }
}
// currentRouteAliases 把服务端下发的别名还原成保存用的形态。fallbacks 与 strategy
// 必须原样带上：页面没有编辑候选链的控件，而 route/save 是全量替换语义，不透传
// 就等于「改个备注或换个顺序会顺手清空候选链」。
function currentRouteAliases(){
 return ((routesState&&routesState.aliases)||[]).map(entry=>({
  alias:entry.alias,
  channel:entry.channel,
  model:entry.model,
  fallbacks:Array.isArray(entry.fallbacks)?entry.fallbacks.map(item=>({channel:item.channel,model:item.model})):[],
  strategy:entry.strategy||'priority',
  enabled:entry.enabled!==false,
  note:entry.note||''
 }));
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
// customRouteModelValue 是「自定义…」这一项的值。真实模型名不会等于它（下划线开头不在
// routeModelChars 里），因此可以拿它当哨兵判断要不要读手动输入框。
const customRouteModelValue='__custom__';
function pickRouteModel(channel,model){
 $('routes-alias-channel').value=channel;fillRouteModelOptions(channel,model);
}
// fillRouteModelOptions 只把「当前通道」的模型灌进下拉框。此前用的是 input+datalist：
// 已填值时浏览器按输入内容过滤，唤不出整份候选（只能先删掉才选得动），且切换通道后
// 旧通道的候选会留在同一份 datalist 里（问题2、问题3）。改成 select 后两个问题一起消失。
// keepValue 是希望保持选中的模型名：在本通道清单里就选中它，不在就落到「自定义…」
// 并回填手动输入框——别名可以指向当前没广播的模型，不能因此把它悄悄改掉。
function fillRouteModelOptions(channel,keepValue){
 const select=$('routes-alias-model'),custom=$('routes-alias-model-custom');
 const previous=keepValue!==undefined?keepValue:select.value;
 select.replaceChildren();
 select.appendChild(new Option('请选择模型…',''));
 const source=((routesState&&routesState.channels)||[]).find(item=>item.channel===channel);
 const known=new Set();
 for(const model of (source&&source.models)||[]){if(known.has(model))continue;known.add(model);select.appendChild(new Option(model,model));}
 select.appendChild(new Option('自定义…（手动填）',customRouteModelValue));
 if(previous&&previous!==customRouteModelValue&&known.has(previous)){select.value=previous;custom.value='';}
 else if(previous&&previous!==customRouteModelValue){select.value=customRouteModelValue;custom.value=previous;}
 else{select.value='';custom.value='';}
 syncCustomRouteModelInput();
}
// 选到「自定义…」才需要手动输入框，平时收起来：两个框同时可填会让人以为要各填一遍。
function syncCustomRouteModelInput(){
 const select=$('routes-alias-model'),custom=$('routes-alias-model-custom');
 const manual=select.value===customRouteModelValue;
 custom.hidden=!manual;custom.required=manual;select.required=!manual;
}
// routeModelChoice 读表单当前指向的模型名：下拉里选的就是它，选「自定义…」时读手动输入框。
function routeModelChoice(){
 const select=$('routes-alias-model');
 return (select.value===customRouteModelValue?$('routes-alias-model-custom').value:select.value).trim();
}
// resetRouteEdit 把表单退回「新增」状态并清掉编辑提示，保存成功与「取消编辑」共用。
function resetRouteEdit(){
 routesEditing=null;
 $('routes-alias-name').value='';$('routes-alias-note').value='';
 fillRouteModelOptions($('routes-alias-channel').value,'');
 const hint=$('routes-alias-editing');hint.hidden=true;hint.textContent='';
}
function renderRouteExample(state){
 const alias=((state.aliases||[]).find(entry=>entry.enabled)||{}).alias||state.auto_model||'gateway-auto';
 const body={model:alias,messages:[{role:'user',content:'你好'}],stream:true};
 $('routes-example').textContent=`curl ${location.origin}/v1/chat/completions \\\n  -H "Authorization: Bearer <你的 API Key>" \\\n  -H "Content-Type: application/json" \\\n  -d '${JSON.stringify(body,null,2)}'`;
}
async function saveRouteAliases(models){
 // 排序、删除、改备注都走这里。批量测试跑着的时候一律先拦下：整表替换会换掉
 // 正在被测的别名集合，后半批的结论就没法对号入座了。
 if(routeTestBusy){notice('一键测试正在进行，等它跑完再改别名');return;}
 if(routesBusy)return;
 const generation=sessionGeneration;routesBusy=true;$('routes-alias-save').disabled=true;
 try{
  const result=await jsonAPI('route/save',{models,auto_fallback:$('routes-auto-fallback').value.trim()});
  if(generation!==sessionGeneration)return;
  resetRouteEdit();
  renderRoutes(result);notice('路由配置已保存，下一次请求即生效');
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){routesBusy=false;$('routes-alias-save').disabled=false;}}
}
function startRouteEdit(entry){
 routesEditing=entry.alias;
 $('routes-alias-name').value=entry.alias;$('routes-alias-channel').value=entry.channel;
 // keepValue 传 entry.model：别名可能指向当前没广播的模型，这时落到「自定义…」并回填，
 // 不能因为下拉里没有就把它悄悄清空。
 fillRouteModelOptions(entry.channel,entry.model);
 $('routes-alias-note').value=entry.note||'';
 const hint=$('routes-alias-editing');hint.hidden=false;hint.textContent=`正在编辑「${entry.alias}」`;
 // 编辑栏在别名表下面，点「编辑」后原地不动会看不到表单（问题1）。滚到面板顶部。
 $('routes-alias-panel').scrollIntoView({behavior:'smooth',block:'start'});
 $('routes-alias-name').focus({preventScroll:true});
}
// ---------- 模型能力 TOP（别名页） ----------
// 契约见 console/capability.go：GET /admin/capabilities 返回 {available, snapshot_at, note, rows}，
// rows 已按 OpenRouter most-popular 顺序排列，heat 从 1 起，且与模型页的「全球热度」列同源，
// 两列不可能给出打架的名次。
// 口径（不得偏离）：
//  - 候选只从 routesState.channels 里已启用通道的模型中挑，对齐键沿用 modelKey（与 globalHeat 同规则）；
//    匹配不到就只读展示，不给池外模型生成可点元素。
//  - 指数为 null 是「没有数据」，留空不补零；快照日期是采集时间，不是评测发布日期。
//  - 花费用 route 下发的 model_costs（core 的 credits 原始串）：glm-/qoder- 上游不公布就写
//    「未公布」，opencode 按通道性质记免费，未公布绝不当成 0 价。
const capDims=[{id:'coding',label:'编程'},{id:'agentic',label:'智能体'},{id:'intelligence',label:'综合智能'},{id:'design',label:'设计'},{id:'context',label:'长上下文'},{id:'all',label:'全量榜单'}];
// 外部快照有 1400+ 个模型，整表塞进 DOM 在手机远程操作时会卡住首屏；
// 数据仍然全量在内存里，筛选作用于全表，只是渲染窗口截到前 100 行并注明。
const capAllLimit=100;
let capState=null,capByKeys=new Map(),capDim='coding',capCategory='',capFilter='';
function capRowsList(){return capState&&Array.isArray(capState.rows)?capState.rows:[];}
async function loadCapabilities(){
 try{capState=await jsonAPI('capabilities');}catch(error){capState={available:false,rows:[]};}
 capByKeys=new Map();
 for(const row of capRowsList()){
  for(const raw of[row.id,row.slug]){
   const key=orModelKey(raw);
   if(key&&!capByKeys.has(key))capByKeys.set(key,row);
  }
 }
 renderCapabilities();
}
// capMetric 把当前维度的读数统一成 {value,text}，value 一律「越大越好」便于排序：
// 设计类目用 -名次，全量视图用 -热度，其余直接取指数。无数据返回 null，不参与排序。
function capMetric(row){
 if(!row)return null;
 if(capDim==='design'){
  const entry=(row.arena||[]).find(item=>item.category===capCategory&&item.rank>0);
  return entry?{value:-entry.rank,text:'#'+entry.rank}:null;
 }
 if(capDim==='context')return row.context>0?{value:Number(row.context),text:Number(row.context).toLocaleString('en-US')}:null;
 if(capDim==='all')return {value:-row.heat,text:'#'+row.heat};
 const value=row[capDim];
 return typeof value==='number'?{value,text:value.toFixed(1)}:null;
}
function capCost(model,channel,costs){
 if(channel==='opencode')return{text:'免费',value:0,known:true};
 const value=parseCreditRule(costs[model]);
 return typeof value==='number'?{text:'×'+value.toFixed(2),value,known:true}:{text:'未公布',known:false};
}
function capPool(){
 const costs=(routesState&&routesState.model_costs)||{},out=[];
 for(const channel of(routesState&&routesState.channels)||[]){
  if(!channel.enabled)continue;
  for(const model of channel.models||[]){
   const cost=capCost(model,channel.channel,costs);
   out.push({channel:channel.channel,model,row:capByKeys.get(modelKey(model))||null,cost,
    gate:modelSwitchEffective({id:model})});
  }
 }
 return out;
}
// capRanked 是当前维度下能对齐到外部数据的池内候选，好的一端在前；同分时先给已放行的模型。
function capRanked(){
 return capPool().map(item=>({...item,metric:capMetric(item.row)})).filter(item=>item.metric)
  .sort((a,b)=>b.metric.value-a.metric.value||(a.gate===b.gate?0:a.gate?-1:1));
}
function capTierRules(list){
 const known=list.filter(item=>item.cost.known);
 const zero=known.filter(item=>item.cost.value===0);
 const cheapest=known.slice().sort((a,b)=>a.cost.value-b.cost.value||b.metric.value-a.metric.value);
 return[
  {title:'免费兜底',rule:'倍率明示为 0 里分最高',hit:zero[0]||null,none:'池内没有倍率为 0 的候选；要走这档，先在模型积分开关页启用 0 积分模型。'},
  {title:'最省',rule:'倍率最低，同价取分高',hit:cheapest[0]||null,none:'池内模型的积分倍率都未公布，没法比花费。'},
  {title:'上限',rule:'不看花费，本维度分最高',hit:list[0]||null,none:'池内模型在这个维度上都没有外部数据。'},
 ];
}
function capCategories(){
 const counts=new Map();
 for(const row of capRowsList())for(const entry of row.arena||[])if(entry.category)counts.set(entry.category,(counts.get(entry.category)||0)+1);
 return [...counts.entries()].sort((a,b)=>b[1]-a[1]||(a[0]<b[0]?-1:1)).map(entry=>entry[0]);
}
function capScoreText(row){
 const parts=[['综合','intelligence'],['编程','coding'],['智能体','agentic']].filter(pair=>typeof row[pair[1]]==='number').map(pair=>pair[0]+' '+row[pair[1]].toFixed(1));
 return parts.length?parts.join(' · '):'—';
}
function capHasCategory(row,name){return !!row&&(row.arena||[]).some(entry=>entry.category===name&&entry.rank>0);}
function capBestArena(row){
 let best=null;
 for(const entry of row.arena||[])if(entry.rank>0&&(!best||entry.rank<best.rank))best=entry;
 return best?`设计最佳 #${best.rank}（${best.category}）`:'';
}
function capContextText(row){return row.context>0?Number(row.context).toLocaleString('en-US'):'—';}
function capEmpty(text){const node=$('cap-empty');node.textContent=text;node.hidden=!text;}
function capActionCell(hit){
 const td=document.createElement('td');
 if(!hit){td.textContent='—';return td;}
 const button=document.createElement('button');button.className='secondary';button.textContent='填入表单';button.title='填进上方别名表单，仍需起名并保存';
 button.addEventListener('click',()=>{pickRouteModel(hit.channel,hit.model);notice(`已填入 ${hit.model}，起个名字后点「保存别名」才生效`);});
 td.append(button);return td;
}
function capTable(columns,items){
 const head=$('cap-head'),body=$('cap-body');head.replaceChildren();body.replaceChildren();
 // num 列的表头要跟着 body 一起右对齐，否则表头在左、数字在右，一列看着像两列。
 for(const column of columns){const th=document.createElement('th');th.scope='col';if(column.num)th.className='num';th.textContent=column.label;head.append(th);}
 for(const item of items){const tr=document.createElement('tr');for(const column of columns)tr.append(column.cell(item));body.append(tr);}
}
function capMatched(text){return !capFilter||String(text).toLowerCase().includes(capFilter);}
function renderCapabilities(){
 const dims=$('cap-dims');dims.replaceChildren();
 for(const dim of capDims){
  const button=document.createElement('button');button.type='button';button.textContent=dim.label;
  button.setAttribute('aria-pressed',String(dim.id===capDim));
  button.addEventListener('click',()=>{capDim=dim.id;renderCapabilities();});
  dims.append(button);
 }
 $('cap-category-wrap').hidden=capDim!=='design';
 if(!capState){$('cap-stamp').textContent='正在读取外部榜单…';$('cap-note').hidden=true;$('cap-tiers').replaceChildren();capEmpty('正在读取外部榜单…');capTable([],[]);return;}
 if(!capState.available){
  $('cap-stamp').textContent='外部榜单未取到';
  $('cap-note').hidden=true;$('cap-tiers').replaceChildren();capEmpty('外部榜单当前拿不到：源站不可达或未返回数据。别名功能本身不受影响，稍后可点顶栏的「↻ 刷新别名」重试。');capTable([],[]);return;
 }
 $('cap-stamp').textContent=`快照 ${formatProbeTime(capState.snapshot_at)} · ${capRowsList().length} 个模型`;
 const note=$('cap-note');note.textContent=capState.note||'';note.hidden=!note.textContent;
 const pool=capPool();
 if(capDim==='design'){
  const list=capCategories();
  if(!capCategory)capCategory=list.find(name=>pool.some(item=>capHasCategory(item.row,name)))||list[0]||'';
  const select=$('cap-category');select.replaceChildren();
  for(const name of list)select.appendChild(new Option(name,name));
  select.value=capCategory;
 }
 if(capDim==='all'){
  renderCapTiers(null);
  const items=capAllItems(pool),shown=items.slice(0,capAllLimit);
  if(shown.length<items.length){
   note.textContent+=`本视图按热度只渲染前 ${capAllLimit} 行（当前命中 ${items.length} 个），要找其余模型请在筛选里输关键词。`;
   note.hidden=false;
  }
  capTable(capAllColumns(),shown);
  return;
 }
 const ranked=capRanked().filter(item=>capMatched(`${item.model} ${item.row.name||''} ${item.row.id||''}`));
 renderCapTiers(capRanked());
 if(!ranked.length){capEmpty(pool.length?'这个维度下，通道目录里没有能对齐到外部数据的模型。榜单按模型名对齐，日期后缀等差异不强行归一，所以匹配不到就如实留空。':'通道目录还没准备好，先确认上游可达。');capTable([],[]);return;}
 capEmpty('');
 capTable(capPoolColumns(),ranked);
}
// 档位永远按整份候选算，筛选只影响表格，避免「筛完只剩一个，档位也跟着变矮」。
function renderCapTiers(list){
 const wrap=$('cap-tiers');wrap.replaceChildren();
 if(!list){
  const article=document.createElement('article');
  const name=document.createElement('span');name.textContent='当前视图';
  const strong=document.createElement('strong');strong.textContent='全量榜单';
  const small=document.createElement('small');small.textContent='这张表供人工判断，三档候选请按上面的能力维度查看。';
  article.append(name,strong,small);wrap.append(article);return;
 }
 for(const tier of capTierRules(list)){
  const article=document.createElement('article');
  const name=document.createElement('span');name.textContent=`${tier.title} · ${tier.rule}`;
  const strong=document.createElement('strong');strong.textContent=tier.hit?(tier.hit.row.name||tier.hit.model):'暂无';
  article.append(name,strong);
  if(!tier.hit){const small=document.createElement('small');small.textContent=tier.none;article.append(small);wrap.append(article);continue;}
  const small=document.createElement('small');
  small.textContent=`${tier.hit.metric.text} · 倍率 ${tier.hit.cost.text} · ${routeChannelLabel(tier.hit.channel)}${tier.hit.gate?'':' · 当前未放行，需在模型积分开关启用'}`;
  article.append(small);
  const button=document.createElement('button');button.className='quiet';button.textContent='填入表单';button.title='填进上方别名表单，仍需起名并保存';
  button.addEventListener('click',()=>{pickRouteModel(tier.hit.channel,tier.hit.model);notice(`已填入 ${tier.hit.model}，起个名字后点「保存别名」才生效`);});
  article.append(button);wrap.append(article);
 }
}
function capPoolColumns(){
 const label=(capDims.find(dim=>dim.id===capDim)||{}).label||'得分';
 // 得分与上下文是同一量纲上要竖着比的数（'#' 名次同理），按全站口径走 .num：
 // 等宽右对齐。此前全表用 cell，数字左对齐、比例字体，位数对不齐，与其余 22 张表不一致。
 return[
  {label:'模型',cell:item=>cell(item.row.name||item.model,String(item.model))},
  {label:'通道',cell:item=>channelCell(routeChannelLabel(item.channel),item.channel)},
  {label:label,num:true,cell:item=>numCell(item.metric.text,capDim==='design'?'类目 '+capCategory:'')},
  {label:'其它指数',cell:item=>cell(capScoreText(item.row),capBestArena(item.row))},
  {label:'上下文',num:true,cell:item=>numCell(capContextText(item.row))},
  {label:'倍率',cell:item=>badgeCell(item.cost.text,item.cost.known?'':'unknown')},
  {label:'放行',cell:item=>badgeCell(item.gate?'已放行':'未放行',item.gate?'ok':'warn')},
  {label:'操作',cell:item=>capActionCell(item)},
 ];
}
function capAllItems(pool){
 const hit=new Map();
 for(const item of pool)if(item.row&&!hit.has(item.row))hit.set(item.row,item);
 return capRowsList().filter(row=>capMatched(`${row.id||''} ${row.name||''} ${row.slug||''}`)).map(row=>({row,pool:hit.get(row)||null}));
}
function capNumber(value){return typeof value==='number'?value.toFixed(1):'—';}
function capAllColumns(){
 // 综合/编程/智能体/上下文是纯数字，走 .num 右对齐等宽。
 // 首列「热度」保持原样：td:first-child 的 13px/650 是主字段口径，
 // 而 td.num 排在它之后会把字号压回 12px，名次列反而不该抢模型名的位置。
 return[
  {label:'热度',cell:item=>cell('#'+item.row.heat)},
  {label:'模型',cell:item=>cell(item.row.name||item.row.id,String(item.row.id))},
  {label:'池内',cell:item=>badgeCell(item.pool?routeChannelLabel(item.pool.channel):'池外',item.pool?'':'idle')},
  {label:'综合',num:true,cell:item=>numCell(capNumber(item.row.intelligence))},
  {label:'编程',num:true,cell:item=>numCell(capNumber(item.row.coding))},
  {label:'智能体',num:true,cell:item=>numCell(capNumber(item.row.agentic))},
  {label:'设计',cell:item=>cell(capBestArena(item.row)||'—')},
  {label:'上下文',num:true,cell:item=>numCell(capContextText(item.row))},
  {label:'操作',cell:item=>capActionCell(item.pool)},
 ];
}
$('routes-alias-form').addEventListener('submit',async event=>{
 event.preventDefault();if(routesBusy)return;
 const alias=$('routes-alias-name').value.trim(),channel=$('routes-alias-channel').value,model=routeModelChoice(),note=$('routes-alias-note').value.trim();
 if(!alias||!model||!channel)return;
 let models=currentRouteAliases();
 if(routesEditing&&routesEditing!==alias)models=models.filter(item=>item.alias!==routesEditing);
 const index=models.findIndex(item=>item.alias===alias);
 if(index>=0)models[index]={...models[index],channel,model,note};
 else models=[...models,{alias,channel,model,enabled:true,note}];
 await saveRouteAliases(models);
});
$('routes-alias-reset').addEventListener('click',resetRouteEdit);
$('routes-alias-test-all').addEventListener('click',()=>routeAliasTestAll($('routes-alias-test-all')));
$('routes-fallback-form').addEventListener('submit',async event=>{event.preventDefault();await saveRouteAliases(currentRouteAliases());});
// 换通道必须整份换候选（问题3）：旧通道的模型留在下拉里就会选串。传 '' 让下拉回到「请选择模型…」。
$('routes-alias-channel').addEventListener('change',()=>fillRouteModelOptions($('routes-alias-channel').value,''));
// 选到「自定义…」才亮手动输入框（问题2：下拉选中即可用，不必先清空再输）。
$('routes-alias-model').addEventListener('change',syncCustomRouteModelInput);
$('cap-category').addEventListener('change',()=>{capCategory=$('cap-category').value;renderCapabilities();});
$('cap-filter').addEventListener('input',()=>{capFilter=$('cap-filter').value.trim().toLowerCase();renderCapabilities();});
runTopRefresh($('routes-refresh'),()=>loadRoutes());
$('routes-copy-example').addEventListener('click',async()=>{try{await navigator.clipboard.writeText($('routes-example').textContent);notice('配置片段已复制，请替换 API Key 占位符');}catch{notice('浏览器不允许自动复制，请手动复制下方片段');}});
$('reveal-key').addEventListener('click',async()=>{try{$('api-key').value=(await jsonAPI('access',{})).api_key;$('api-key').type='text';}catch(e){notice(e.message);}});
$('copy-key').addEventListener('click',async()=>{const generation=sessionGeneration;try{if(!$('api-key').value)$('api-key').value=(await jsonAPI('access',{})).api_key;await navigator.clipboard.writeText($('api-key').value);if(generation===sessionGeneration)notice('API Key 已复制');}catch{if(generation!==sessionGeneration)return;$('api-key').type='text';$('api-key').select();notice('浏览器不允许自动复制，请手动复制选中的密钥');}});
async function loadCCR(){ if(!csrf)return; try{renderCCR(await jsonAPI('ccr'));}catch(error){notice(error.message);} }
function ccrControls(){ const s=ccrStatus||{},usable=!!s.reachable&&!!s.auth_ready&&!ccrBusy;
 $('ccr-start').disabled=!usable||s.state==='running';
 $('ccr-stop').disabled=!usable||s.state!=='running';
 $('ccr-link').disabled=ccrBusy;
}
function renderCCR(status){
 ccrStatus=status;
 const enabled=!!status.enabled;
 $('ccr-disabled').hidden=enabled;
 $('ccr-content').hidden=!enabled;
 if(!enabled){ccrControls();return;}
 const running=status.state==='running';
 let tone='bad',label='不可达',text='';
 if(!status.auth_ready){label='未配置';text='未读取到 CCR 管理 Token：请把 CCR 的 service.json 只读挂载给控制台，并用 WB2A_CCR_TOKEN_FILE 指向它。';}
 else if(status.auth_mismatch){tone='warn';label='Token 不匹配';text='CCR 管理服务在线，但拒绝了当前的管理 Token：CCR 每次重启都会轮换 Token，容器里可能还是旧的那份。请重建控制台容器（docker compose up -d --force-recreate）重新读取挂载。';}
 else if(!status.reachable){label='不可达';text='CCR 管理服务不可达：请在宿主机执行 ccr ui 拉起管理服务后点右上角刷新。控制台没有宿主进程权限，无法替你启动 ccr 本身。';}
 else if(running){tone='ok';label='运行中';text=`CCR 模型网关运行中 · ${status.endpoint||'—'} · 进程号 ${status.pid||'—'}`;}
 else{tone='warn';label='已停止';text=`CCR 管理服务在线，模型网关已停止${status.state?`（${status.state}）`:''}，点「启动网关」拉起。`;}
 $('ccr-status-badge').textContent=label;
 $('ccr-status-badge').className='badge '+tone;
 $('ccr-status-text').textContent=text;
 const rows=[];
 if(status.reachable){
  if(status.endpoint)rows.push(['网关地址',status.endpoint]);
  if(status.pid)rows.push(['进程号',String(status.pid)]);
  if(status.last_started_at)rows.push(['上次启动',formatProbeTime(status.last_started_at)]);
 }
 const body=$('ccr-detail-body');body.replaceChildren();
 for(const [k,v] of rows){const tr=document.createElement('tr');tr.append(cell(k),cell(v));body.append(tr);}
 $('ccr-detail-wrap').hidden=!rows.length;
 ccrControls();
}
async function ccrAction(action){ if(ccrBusy)return; const generation=sessionGeneration; ccrBusy=true; ccrControls(); notice('');
 try{await jsonAPI('ccr/gateway',{action});if(generation!==sessionGeneration)return;await loadCCR();}
 catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){ccrBusy=false;ccrControls();}}
}
$('ccr-start').addEventListener('click',()=>ccrAction('start'));
$('ccr-stop').addEventListener('click',()=>ccrAction('stop'));
runTopRefresh($('ccr-refresh'),()=>loadCCR());
$('ccr-link').addEventListener('click',async()=>{ const generation=sessionGeneration; ccrBusy=true; ccrControls();
 try{const result=await jsonAPI('ccr/link');if(generation!==sessionGeneration)return;window.open(result.ui_url,'_blank','noopener');}
 catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){ccrBusy=false;ccrControls();}}
});

// ── AutoClaw 通道（autoclaw2api 旁路，autoclaw- 前缀）──
// 状态接口只回 {enabled,reachable,models,accounts,health:{healthy,total}}；bridge 的
// 业务错误是 200 + {"error":...}，jsonAPI 不抛，autoclawResult 统一转成异常。
function autoclawResult(result){
 if(result&&typeof result.error==='string'&&result.error)throw new Error(result.error);
 return result;
}
async function loadAutoClaw(){
 if(!csrf)return;
 try{renderAutoClaw(await jsonAPI('autoclaw'));}
 catch(error){notice(error.message);}
}
function renderAutoClaw(status){
 autoclawStatus=status;
 const enabled=!!status.enabled;
 $('autoclaw-disabled').hidden=enabled;
 $('autoclaw-content').hidden=!enabled;
 if(!enabled)return;
 const reachable=!!status.reachable;
 const badge=$('autoclaw-status-badge');
 const health=status.health;
 const healthy=health&&Number.isFinite(Number(health.healthy))?Number(health.healthy):null;
 const total=health&&Number.isFinite(Number(health.total))?Number(health.total):null;
 let tone='bad',label='不可达',text='autoclaw2api 容器不可达：请确认 overlay 已随 compose 启动；若容器在运行，通常是 console 刚被重启、旁路网络失联，重启旁路容器即可恢复。';
 if(reachable){
  if(total===0){tone='warn';label='无账号';text='容器可达，但还没有登录任何账号：到「账号登录」子页添加。';}
  else if(healthy===0){tone='warn';label='全部冷却';text=`容器可达 · ${total} 个账号全部冷却或停用，等待恢复后自动继续。`;}
  else{tone='ok';label='在线';text=`autoclaw2api 可达 · ${status.model_count} 个模型 · 健康账号 ${healthy} / ${total}`;}
 }
 badge.textContent=label;badge.className='badge '+tone;
 $('autoclaw-status-text').textContent=text;
 const rows=[];
 if(health)rows.push(['健康账号',`${health.healthy??'—'} / ${health.total??'—'}`]);
 if(status.mode)rows.push(['账号模式',status.mode==='global'?'国际版':'国内版']);
 if(reachable)rows.push(['模型数',String(status.model_count||0)]);
 const detail=$('autoclaw-health-body');detail.replaceChildren();
 for(const [k,v] of rows){const tr=document.createElement('tr');tr.append(cell(String(k)),cell(String(v)));detail.append(tr);}
 $('autoclaw-health-wrap').hidden=!rows.length;
 // 登录子页在容器不可达时也保留（那是排障入口），其余数据子页只在可达时开放。
 const dataTab=reachable;
 if(autoclawView!=='status'&&autoclawView!=='login'&&!dataTab)autoclawView='status';
 $('autoclaw-view-accounts').hidden=!dataTab;
 $('autoclaw-view-models').hidden=!dataTab;
 $('autoclaw-view-chat').hidden=!dataTab;
 for(const view of ['status','login','accounts','models','chat']){
  $('autoclaw-view-'+view).classList.toggle('active',view===autoclawView);
  $('autoclaw-pane-'+view).hidden=view!==autoclawView;
 }
 autoclawModels=(status.models||[]).map(item=>item.id).filter(Boolean);
 renderAccessModelSelect();renderAccess();
 const previous=$('autoclaw-model').value;
 $('autoclaw-model').replaceChildren();
 for(const id of autoclawModels)$('autoclaw-model').append(new Option(id,id));
 if(autoclawModels.includes(previous))$('autoclaw-model').value=previous;
 renderModelTable('autoclaw',status.models,health||null);
 renderAutoClawAccounts(status.accounts||[]);
 $('autoclaw-model').disabled=!autoclawModels.length||!!activeRequest;
 $('autoclaw-send').disabled=!autoclawModels.length||!!activeRequest;
 autoclawControls();autoclawActionControls();
}
function autoclawControls(){
 for(const id of ['autoclaw-model','autoclaw-prompt','autoclaw-send'])$(id).disabled=!!activeRequest;
 $('autoclaw-stop').hidden=!activeRequest;
}
function autoclawActionControls(){
 $('autoclaw-refresh').disabled=autoclawBusy;
 $('autoclaw-credits-refresh').disabled=autoclawBusy;
 $('autoclaw-maintain').disabled=autoclawBusy;
}
function autoclawLoginControls(){
 $('autoclaw-sendcode').disabled=autoclawLoginBusy||autoclawCodeTimer!==null;
 for(const id of ['autoclaw-login-start','autoclaw-import-desktop','autoclaw-oauth'])$(id).disabled=autoclawLoginBusy;
}
function startAutoClawCodeCountdown(){
 clearTimeout(autoclawCodeTimer);let remain=60;
 const btn=$('autoclaw-sendcode');btn.disabled=true;
 const tick=()=>{
  if(remain>0){btn.textContent=`重新发送（${remain}s）`;remain--;autoclawCodeTimer=setTimeout(tick,1000);return;}
  clearTimeout(autoclawCodeTimer);autoclawCodeTimer=null;btn.disabled=autoclawLoginBusy;btn.textContent='发送验证码';
 };
 tick();
}
function renderAutoClawAccounts(list){
 autoclawAccounts=Array.isArray(list)?list:[];
 const usable=autoclawAccounts.filter(a=>!a.disabled&&!a.cooling).length;
 const cooling=autoclawAccounts.filter(a=>a.cooling&&!a.disabled).length;
 const disabled=autoclawAccounts.filter(a=>a.disabled).length;
 $('autoclaw-stat-total').textContent=String(autoclawAccounts.length);
 $('autoclaw-stat-usable').textContent=String(usable);
 $('autoclaw-stat-cooling').textContent=String(cooling);
 $('autoclaw-stat-disabled').textContent=String(disabled);
 const body=$('autoclaw-accounts-body');body.replaceChildren();
 for(const a of autoclawAccounts){
  const tr=document.createElement('tr');
  const state=a.disabled?'已停用':a.cooling?'冷却中':'可用';
  const tone=a.disabled?'idle':a.cooling?'warn':'ok';
  const status=cell('');const badge=document.createElement('span');badge.className='badge '+tone;badge.textContent=state;status.append(badge);
  const region=a.region==='global'?'国际版':'国内版';
  const ac=(label,action,cls)=>{const btn=document.createElement('button');btn.className=cls;btn.textContent=label;btn.dataset.acact=action;btn.dataset.uid=a.uid;btn.dataset.region=a.region||'cn';return btn;};
  const op=document.createElement('td');
  if(a.disabled)op.append(ac('启用','enable','secondary'),' ',ac('删除','delete','quiet'));
  else op.append(ac('停用','disable','secondary'),' ',ac('签到','checkin','quiet'),' ',ac('删除','delete','quiet'));
  tr.append(cell(a.nickname||a.uid,a.uid),cell(region),status,numCell(a.points==null?'—':formatCredit(a.points)),numCell(`${a.success_count||0} / ${a.err_count||0}`),cell(a.reason||'—',a.cool_remaining_sec?`约 ${Math.ceil(a.cool_remaining_sec/60)} 分钟后恢复`:''),op);
  body.append(tr);
 }
 $('autoclaw-accounts-empty').hidden=autoclawAccounts.length>0;
}
async function autoclawAccountAction(action,uid,region){
 if(autoclawBusy)return;
 const generation=sessionGeneration;
 if(action==='delete'&&!confirm(`确定删除账号 ${uid}？凭据将从容器中移除，需要重新登录才能恢复。`))return;
 autoclawBusy=true;autoclawActionControls();notice('');
 try{
  let done='账号状态已更新';
  if(action==='enable'||action==='disable'){
   autoclawResult(await jsonAPI('autoclaw/accounts/toggle',{uid,region,enable:action==='enable'}));
  }else if(action==='delete'){
   autoclawResult(await jsonAPI('autoclaw/accounts/delete',{uid,region}));done='账号已删除';
  }else if(action==='checkin'){
   const result=autoclawResult(await jsonAPI('autoclaw/checkin',{region,uid}));
   const rs=Array.isArray(result.results)?result.results:[];
   const ok=rs.filter(x=>x&&x.ok).length,already=rs.filter(x=>x&&x.already_completed).length;
   const fail=rs.filter(x=>x&&x.error).length;
   done=fail?`签到结束：${ok} 成功 / ${already} 已签 / ${fail} 失败`:`签到完成：${ok} 成功 / ${already} 已签`;
  }
  if(generation!==sessionGeneration)return;
  notice(done);
  // 签到/启停之后补一次维护（积分刷新 + 会话保养），让积分列尽快反映奖励。
  if(action==='checkin'){try{autoclawResult(await jsonAPI('autoclaw/maintain',{}));}catch{/* 维护失败不掩盖签到结果 */}}
  await loadAutoClaw();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){autoclawBusy=false;autoclawActionControls();}}
}
$('autoclaw-accounts-body').addEventListener('click',event=>{
 const btn=event.target.closest('button[data-acact]');if(!btn||btn.disabled)return;
 autoclawAccountAction(btn.dataset.acact,btn.dataset.uid,btn.dataset.region);
});
$('autoclaw-credits-refresh').addEventListener('click',async()=>{
 if(autoclawBusy)return;const generation=sessionGeneration;autoclawBusy=true;autoclawActionControls();notice('');
 try{
  const result=autoclawResult(await jsonAPI('autoclaw/credits/refresh',{}));
  if(generation!==sessionGeneration)return;
  const list=Array.isArray(result.accounts)?result.accounts:[];
  const failed=list.filter(x=>x&&x.error);
  notice(failed.length?`积分刷新完成，${failed.length} 个账号失败：${failed[0].error}`:`积分已刷新（${list.length} 个账号）`);
  await loadAutoClaw();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){autoclawBusy=false;autoclawActionControls();}}
});
$('autoclaw-maintain').addEventListener('click',async()=>{
 if(autoclawBusy)return;const generation=sessionGeneration;autoclawBusy=true;autoclawActionControls();notice('');
 try{
  autoclawResult(await jsonAPI('autoclaw/maintain',{}));
  if(generation!==sessionGeneration)return;
  notice('维护完成：积分已刷新，会话已保养');
  await loadAutoClaw();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){autoclawBusy=false;autoclawActionControls();}}
});
$('autoclaw-sendcode-form').addEventListener('submit',async event=>{
 event.preventDefault();if(autoclawLoginBusy)return;
 const phone=$('autoclaw-phone').value.trim();
 if(!/^1\d{10}$/.test(phone)){$('autoclaw-login-hint').textContent='请输入 11 位国内手机号。';return;}
 const generation=sessionGeneration;autoclawLoginBusy=true;autoclawLoginControls();
 $('autoclaw-login-hint').textContent='正在发送验证码…';
 try{
  const result=autoclawResult(await jsonAPI('autoclaw/login/cn/sendcode',{phone}));
  if(generation!==sessionGeneration)return;
  $('autoclaw-login-hint').textContent=(result&&result.msg)?result.msg:'验证码已发送，请查收短信。';
  startAutoClawCodeCountdown();
 }catch(error){if(generation===sessionGeneration)$('autoclaw-login-hint').textContent=error.message;}
 finally{if(generation===sessionGeneration){autoclawLoginBusy=false;autoclawLoginControls();}}
});
$('autoclaw-login-form').addEventListener('submit',async event=>{
 event.preventDefault();if(autoclawLoginBusy)return;
 const phone=$('autoclaw-phone').value.trim(),code=$('autoclaw-code').value.trim();
 if(!phone||!code){$('autoclaw-login-hint').textContent='请先获取并填写短信验证码。';return;}
 const generation=sessionGeneration;autoclawLoginBusy=true;autoclawLoginControls();
 $('autoclaw-login-hint').textContent='正在登录…';
 try{
  autoclawResult(await jsonAPI('autoclaw/login/cn',{phone,code}));
  if(generation!==sessionGeneration)return;
  $('autoclaw-phone').value='';$('autoclaw-code').value='';
  $('autoclaw-login-hint').textContent='登录成功，账号已加入容器账号池。';
  notice('AutoClaw 账号登录成功');
  await loadAutoClaw();
  autoclawView='accounts';if(autoclawStatus)renderAutoClaw(autoclawStatus);
 }catch(error){if(generation===sessionGeneration)$('autoclaw-login-hint').textContent=error.message;}
 finally{if(generation===sessionGeneration){autoclawLoginBusy=false;autoclawLoginControls();}}
});
$('autoclaw-import-desktop').addEventListener('click',async()=>{
 if(autoclawLoginBusy)return;const generation=sessionGeneration;autoclawLoginBusy=true;autoclawLoginControls();
 $('autoclaw-global-hint').textContent='正在从桌面客户端导入凭据…';
 try{
  const result=autoclawResult(await jsonAPI('autoclaw/login/global/import-desktop',{}));
  if(generation!==sessionGeneration)return;
  $('autoclaw-global-hint').textContent=(result&&result.msg)?result.msg:'导入完成，国际版账号已加入容器账号池。';
  notice('AutoClaw 国际版凭据导入成功');
  await loadAutoClaw();
 }catch(error){if(generation===sessionGeneration)$('autoclaw-global-hint').textContent=error.message;}
 finally{if(generation===sessionGeneration){autoclawLoginBusy=false;autoclawLoginControls();}}
});
$('autoclaw-oauth-form').addEventListener('submit',async event=>{
 event.preventDefault();if(autoclawLoginBusy)return;
 const callback=$('autoclaw-oauth-url').value.trim();
 if(!callback){$('autoclaw-global-hint').textContent='请粘贴浏览器回调 URL。';return;}
 const generation=sessionGeneration;autoclawLoginBusy=true;autoclawLoginControls();
 $('autoclaw-global-hint').textContent='正在导入 OAuth 凭据…';
 try{
  const result=autoclawResult(await jsonAPI('autoclaw/login/global/oauth',{callback_url:callback}));
  if(generation!==sessionGeneration)return;
  $('autoclaw-oauth-url').value='';
  $('autoclaw-global-hint').textContent=(result&&result.msg)?result.msg:'导入完成，国际版账号已加入容器账号池。';
  notice('AutoClaw OAuth 导入成功');
  await loadAutoClaw();
 }catch(error){if(generation===sessionGeneration)$('autoclaw-global-hint').textContent=error.message;}
 finally{if(generation===sessionGeneration){autoclawLoginBusy=false;autoclawLoginControls();}}
});
for(const view of ['status','login','accounts','models','chat'])$('autoclaw-view-'+view).addEventListener('click',()=>{autoclawView=view;if(autoclawStatus)renderAutoClaw(autoclawStatus);});
runTopRefresh($('autoclaw-refresh'),async()=>{
 if(autoclawBusy)return;const generation=sessionGeneration;autoclawBusy=true;autoclawActionControls();
 try{await loadAutoClaw();}finally{if(generation===sessionGeneration){autoclawBusy=false;autoclawActionControls();}}
});
$('autoclaw-stop').addEventListener('click',()=>activeRequest?.abort());
$('autoclaw-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('autoclaw-prompt').value.trim();if(!text||!$('autoclaw-model').value)return;
 const generation=sessionGeneration;
 notice('');message('user',text,$('autoclaw-messages'));$('autoclaw-prompt').value='';const answer=message('assistant','正在等待模型…',$('autoclaw-messages'));
 const controller=new AbortController();activeRequest=controller;autoclawControls();
 let content='',usage,finished=false;const outgoing=[...autoclawHistory,{role:'user',content:text}];
 try{
  const response=await api('autoclaw/chat',{model:$('autoclaw-model').value,messages:outgoing,stream:true},controller.signal);
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
  autoclawHistory=[...outgoing,{role:'assistant',content}];
  $('autoclaw-usage').textContent=(usage?`输入 ${usage.prompt_tokens??'—'} · 输出 ${usage.completion_tokens??'—'} · 总计 ${usage.total_tokens??'—'} tokens`:'上游未返回用量')+' · 本次通道：AutoClaw';
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=(content?content+'\n\n':'')+msg;$('autoclaw-usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;autoclawControls();}
});

// ── Trae 通道（trae2api 旁路，trae- 前缀）──
// 状态接口把容器 /status 与 /admin/api/accounts 按 uid 合并后下发；控制链路用标准 HTTP
// 状态码表达业务结果（400/404/409 原样透传），jsonAPI 的 !ok 分支已能抛错，这里不再包一层。
async function loadTrae(){
 if(!csrf)return;
 try{renderTrae(await jsonAPI('trae'));}
 catch(error){notice(error.message);}
}
function renderTrae(status){
 traeStatus=status;
 const enabled=!!status.enabled;
 $('trae-disabled').hidden=enabled;
 $('trae-content').hidden=!enabled;
 if(!enabled)return;
 const reachable=!!status.reachable;
 const accounts=Array.isArray(status.accounts)?status.accounts:[];
 const badge=$('trae-status-badge');
 let tone='bad',label='不可达',text='trae2api 容器不可达：请确认 overlay 已随 compose 启动；若容器在运行，通常是 console 刚被重启、旁路网络失联，重启旁路容器即可恢复。';
 if(reachable){
  if(!accounts.length){tone='warn';label='无账号';text='容器可达，但还没有导入账号：到「账号登录」子页用 OAuth 或回调 URL 添加。';}
  else{tone='ok';label='在线';text=`trae2api 可达 · ${status.model_count||0} 个模型 · ${accounts.length} 个账号`;}
 }
 badge.textContent=label;badge.className='badge '+tone;
 $('trae-status-text').textContent=text;
 // 容器不可达时 /status 不存在，凭据与账号数一律留空而不是编造。
 const rows=[];
 if(reachable){rows.push(['模型数',String(status.model_count||0)]);rows.push(['账号数',String(accounts.length)]);}
 const detail=$('trae-health-body');detail.replaceChildren();
 for(const [k,v] of rows){const tr=document.createElement('tr');tr.append(cell(String(k)),cell(String(v)));detail.append(tr);}
 $('trae-health-wrap').hidden=!rows.length;
 // 登录子页在容器不可达时也保留（那是排障入口），其余数据子页只在可达时开放。
 const dataTab=reachable;
 if(traeView!=='status'&&traeView!=='login'&&!dataTab)traeView='status';
 for(const view of ['accounts','schedule','models','chat','usage'])$('trae-view-'+view).hidden=!dataTab;
 for(const view of ['status','login','accounts','schedule','models','chat','usage']){
  $('trae-view-'+view).classList.toggle('active',view===traeView);
  $('trae-pane-'+view).hidden=view!==traeView;
 }
 traeModels=(status.models||[]).map(item=>item.id).filter(Boolean);
 renderAccessModelSelect();renderAccess();
 const previous=$('trae-model').value;
 $('trae-model').replaceChildren();
 for(const id of traeModels)$('trae-model').append(new Option(id,id));
 if(traeModels.includes(previous))$('trae-model').value=previous;
 renderModelTable('trae',status.models,status.health||null);
 renderTraeAccounts(accounts);
 $('trae-model').disabled=!traeModels.length||!!activeRequest;
 $('trae-send').disabled=!traeModels.length||!!activeRequest;
 traeControls();traeActionControls();
}
function traeControls(){
 for(const id of ['trae-model','trae-prompt','trae-send'])$(id).disabled=!!activeRequest;
 $('trae-stop').hidden=!activeRequest;
}
function traeActionControls(){
 for(const id of ['trae-refresh','trae-credits-refresh','trae-maintain','trae-schedule-refresh','trae-checkin-now','trae-refresh-now'])$(id).disabled=traeBusy;
}
function traeRecoveryHint(a){
 if(!a.cooling||!a.until)return '';
 const until=new Date(a.until);
 if(Number.isNaN(until.getTime()))return '';
 return '约 '+until.toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false})+' 恢复';
}
function renderTraeAccounts(list){
 traeAccounts=Array.isArray(list)?list:[];
 const usable=traeAccounts.filter(a=>!a.disabled&&!a.cooling).length;
 const cooling=traeAccounts.filter(a=>a.cooling&&!a.disabled).length;
 const disabled=traeAccounts.filter(a=>a.disabled).length;
 $('trae-stat-total').textContent=String(traeAccounts.length);
 $('trae-stat-usable').textContent=String(usable);
 $('trae-stat-cooling').textContent=String(cooling);
 $('trae-stat-disabled').textContent=String(disabled);
 const body=$('trae-accounts-body');body.replaceChildren();
 for(const a of traeAccounts){
  const tr=document.createElement('tr');
  const state=a.disabled?'已停用':a.cooling?'冷却中':'可用';
  const tone=a.disabled?'idle':a.cooling?'warn':'ok';
  const status=cell('');const badge=document.createElement('span');badge.className='badge '+tone;badge.textContent=state;status.append(badge);
  const expires=a.expires_at?new Date(Number(a.expires_at)*1000).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false}):'—';
  const ta=(label,action,cls)=>{const btn=document.createElement('button');btn.className=cls;btn.textContent=label;btn.dataset.traact=action;btn.dataset.uid=a.uid;return btn;};
  const op=document.createElement('td');
  if(a.disabled)op.append(ta('启用','enable','secondary'),' ',ta('删除','delete','quiet'));
  else op.append(ta('停用','disable','secondary'),' ',ta('刷新','refresh','quiet'),' ',ta('删除','delete','quiet'));
  // sidecar 只回报失败次数，没有成功计数；成功列留「—」而不是拿 0 充数。
  tr.append(cell(a.nickname||a.uid,a.uid),status,numCell(a.credits==null?'—':formatCredit(a.credits)),numCell('— / '+(a.err_count||0)),cell(expires,a.expired_soon?'Token 即将过期':''),cell(a.reason||'—',traeRecoveryHint(a)),op);
  body.append(tr);
 }
 $('trae-accounts-empty').hidden=traeAccounts.length>0;
}
async function traeAccountAction(action,uid){
 if(traeBusy)return;
 const generation=sessionGeneration;
 if(action==='delete'&&!confirm(`确定删除账号 ${uid}？凭据将从容器中移除，需要重新登录才能恢复。`))return;
 traeBusy=true;traeActionControls();notice('');
 try{
  if(action==='enable'||action==='disable')await jsonAPI('trae/accounts/patch',{uid,enabled:action==='enable'});
  else if(action==='delete'){await jsonAPI('trae/accounts/delete',{uid});if(generation===sessionGeneration)notice('账号已删除');}
  else if(action==='refresh')await jsonAPI('trae/accounts/refresh',{uid});
  if(generation!==sessionGeneration)return;
  await loadTrae();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeBusy=false;traeActionControls();}}
}
$('trae-accounts-body').addEventListener('click',event=>{
 const btn=event.target.closest('button[data-traact]');if(!btn||btn.disabled)return;
 traeAccountAction(btn.dataset.traact,btn.dataset.uid);
});
// 登录由容器侧 OAuth 回调驱动，console 只轮询 pending 状态；退出管理会话即中止轮询。
async function pollTraeLogin(generation,pendingId){
 while(generation===sessionGeneration){
  let state;
  try{state=await jsonAPI('trae/login?pending_id='+encodeURIComponent(pendingId));}
  catch(error){
   $('trae-login-badge').textContent='未登录';$('trae-login-badge').className='badge warn';
   $('trae-login-hint').textContent=error.message;return;
  }
  if(generation!==sessionGeneration)return;
  if(state.state==='success'){
   $('trae-login-badge').textContent='已登录';$('trae-login-badge').className='badge ok';
   $('trae-auth-row').hidden=true;traeAuthURL='';
   $('trae-login-hint').textContent=state.nickname?`账号 ${state.nickname} 已登录。`:'账号已登录。';
   notice('Trae 账号登录成功');
   traeView='accounts';await loadTrae();
   return;
  }
  if(state.state==='failed'){
   $('trae-login-badge').textContent='未登录';$('trae-login-badge').className='badge warn';
   $('trae-login-hint').textContent=`登录失败：${state.error||'未知错误'}，可重新发起。`;return;
  }
  if(state.state==='canceled'){$('trae-login-hint').textContent='登录已取消。';return;}
  await new Promise(resolve=>{traePollTimer=setTimeout(resolve,2000);});
 }
}
$('trae-login-form').addEventListener('submit',async event=>{
 event.preventDefault();if(traeLoginBusy)return;
 const generation=sessionGeneration;traeLoginBusy=true;$('trae-login-start').disabled=true;notice('');
 $('trae-login-hint').textContent='正在发起登录…';
 try{
  const result=await jsonAPI('trae/login',{});
  if(generation!==sessionGeneration)return;
  traeAuthURL=result.login_url||'';
  $('trae-auth-row').hidden=!traeAuthURL;
  if(traeAuthURL){
   $('trae-auth-link').href=traeAuthURL;
   try{window.open(traeAuthURL,'_blank','noopener');}catch(_){}
  }
  $('trae-login-badge').textContent='登录中';$('trae-login-badge').className='badge warn';
  $('trae-login-hint').textContent='请在新打开的页面完成授权，本页会自动等待结果。';
  void pollTraeLogin(generation,result.pending_id);
 }catch(error){
  if(generation===sessionGeneration){
   $('trae-login-badge').textContent='未登录';$('trae-login-badge').className='badge warn';
   $('trae-login-hint').textContent=error.message;
  }
 }finally{if(generation===sessionGeneration){traeLoginBusy=false;$('trae-login-start').disabled=false;}}
});
$('trae-import-form').addEventListener('submit',async event=>{
 event.preventDefault();if(traeLoginBusy)return;
 const text=$('trae-import-url').value.trim();
 if(!text){$('trae-import-hint').textContent='请粘贴浏览器回调 URL 或导出的凭据 JSON。';return;}
 const generation=sessionGeneration;traeLoginBusy=true;$('trae-import').disabled=true;notice('');
 $('trae-import-hint').textContent='正在导入…';
 try{
  // 按内容挑字段：回调链接进 callback_url，凭据 JSON 与桌面客户端 storage.json 全文进 json。
  // sidecar 的 importRequest 这两者是二选一，统一塞 callback_url 会让页面文案承诺的
  // 「也可粘贴凭据 JSON」必然失败（国际版与小浣熊页签本来就是按内容判的）。
  const payload=/^https?:\/\//i.test(text)?{callback_url:text}:{json:text};
  const result=await jsonAPI('trae/accounts/import',payload);
  if(generation!==sessionGeneration)return;
  $('trae-import-url').value='';
  $('trae-import-hint').textContent=`已导入 ${result.nickname||result.uid||'账号'}，建议刷新积分核对。`;
  notice('Trae 账号凭据导入成功');
  await loadTrae();
 }catch(error){
  if(generation===sessionGeneration)$('trae-import-hint').textContent=error.status===409?'该账号已有更新的凭证，未覆盖（409）。':error.message;
 }finally{if(generation===sessionGeneration){traeLoginBusy=false;$('trae-import').disabled=false;}}
});
function renderTraeSchedule(data){
 const rows=[];
 if(data.checkin_hour!==undefined&&data.checkin_hour!==null&&data.checkin_hour!=='')rows.push(['签到时刻',`${String(data.checkin_hour).padStart(2,'0')}:00`]);
 if((data.refresh_hours||[]).length)rows.push(['凭据刷新时段',data.refresh_hours.join(', ')]);
 if(data.next_fire)rows.push(['下次执行',String(data.next_fire)]);
 if(data.last_checkin)rows.push(['上次签到',String(data.last_checkin)]);
 if(data.last_refresh)rows.push(['上次刷新',String(data.last_refresh)]);
 const body=$('trae-schedule-body');body.replaceChildren();
 for(const [k,v] of rows){const tr=document.createElement('tr');tr.append(cell(String(k)),cell(v));body.append(tr);}
 $('trae-schedule-empty').hidden=rows.length>0;
}
function renderTraeUsage(data){
 const days=Array.isArray(data.days)?data.days:[];
 const body=$('trae-usage-body');body.replaceChildren();
 for(const day of days){
  const tr=document.createElement('tr');
  tr.append(cell(String(day.date||'—')),numCell(String(day.requests||0)),numCell(String(day.errors||0)),numCell(String(day.prompt_tokens||0)),numCell(String(day.completion_tokens||0)),numCell(String(day.total_tokens||0)));
  body.append(tr);
 }
 $('trae-usage-empty').hidden=days.length>0;
}
$('trae-credits-refresh').addEventListener('click',async()=>{
 if(traeBusy)return;const generation=sessionGeneration;traeBusy=true;traeActionControls();notice('');
 try{
  const result=await jsonAPI('trae/credits');
  if(generation!==sessionGeneration)return;
  const list=Array.isArray(result.accounts)?result.accounts:[];
  const failed=list.filter(x=>x&&x.error);
  notice(failed.length?`积分刷新完成，${failed.length} 个账号失败：${failed[0].error}`:`积分已刷新（${list.length} 个账号）`);
  await loadTrae();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeBusy=false;traeActionControls();}}
});
$('trae-maintain').addEventListener('click',async()=>{
 if(traeBusy)return;const generation=sessionGeneration;traeBusy=true;traeActionControls();notice('');
 try{
  await jsonAPI('trae/schedule/checkin',{});
  if(generation!==sessionGeneration)return;
  notice('已触发签到：容器将在本轮执行签到与积分刷新');
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeBusy=false;traeActionControls();}}
});
$('trae-schedule-refresh').addEventListener('click',async()=>{
 if(traeBusy)return;const generation=sessionGeneration;traeBusy=true;traeActionControls();
 try{renderTraeSchedule(await jsonAPI('trae/schedule'));}
 catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeBusy=false;traeActionControls();}}
});
$('trae-checkin-now').addEventListener('click',async()=>{
 if(traeBusy)return;const generation=sessionGeneration;traeBusy=true;traeActionControls();
 try{
  await jsonAPI('trae/schedule/checkin',{});
  if(generation!==sessionGeneration)return;
  $('trae-schedule-hint').textContent='已触发签到：容器将在本轮执行，完成后点「刷新排程」查看结果。';
 }catch(error){if(generation===sessionGeneration)$('trae-schedule-hint').textContent=error.message;}
 finally{if(generation===sessionGeneration){traeBusy=false;traeActionControls();}}
});
$('trae-refresh-now').addEventListener('click',async()=>{
 if(traeBusy)return;const generation=sessionGeneration;traeBusy=true;traeActionControls();
 try{
  await jsonAPI('trae/schedule/refresh',{});
  if(generation!==sessionGeneration)return;
  $('trae-schedule-hint').textContent='已触发凭据刷新：容器将在本轮执行，完成后点「刷新排程」查看结果。';
 }catch(error){if(generation===sessionGeneration)$('trae-schedule-hint').textContent=error.message;}
 finally{if(generation===sessionGeneration){traeBusy=false;traeActionControls();}}
});
$('trae-usage-refresh').addEventListener('click',async()=>{
 if(traeBusy)return;const generation=sessionGeneration;traeBusy=true;$('trae-usage-refresh').disabled=true;
 try{renderTraeUsage(await jsonAPI('trae/usage'));}
 catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeBusy=false;$('trae-usage-refresh').disabled=false;}}
});
for(const view of ['status','login','accounts','schedule','models','chat','usage'])$('trae-view-'+view).addEventListener('click',()=>{traeView=view;if(traeStatus)renderTrae(traeStatus);});
runTopRefresh($('trae-refresh'),async()=>{
 if(traeBusy)return;const generation=sessionGeneration;traeBusy=true;traeActionControls();
 try{await loadTrae();}finally{if(generation===sessionGeneration){traeBusy=false;traeActionControls();}}
});
$('trae-stop').addEventListener('click',()=>activeRequest?.abort());
$('trae-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('trae-prompt').value.trim();if(!text||!$('trae-model').value)return;
 const generation=sessionGeneration;
 notice('');message('user',text,$('trae-messages'));$('trae-prompt').value='';const answer=message('assistant','正在等待模型…',$('trae-messages'));
 const controller=new AbortController();activeRequest=controller;traeControls();
 let content='',usage,finished=false;const outgoing=[...traeHistory,{role:'user',content:text}];
 try{
  const response=await api('trae/chat',{model:$('trae-model').value,messages:outgoing,stream:true},controller.signal);
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
  traeHistory=[...outgoing,{role:'assistant',content}];
  $('trae-usage').textContent=(usage?`输入 ${usage.prompt_tokens??'—'} · 输出 ${usage.completion_tokens??'—'} · 总计 ${usage.total_tokens??'—'} tokens`:'上游未返回用量')+' · 本次通道：Trae';
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=(content?content+'\n\n':'')+msg;$('trae-usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;traeControls();}
});

// ── Trae 国际版通道（trae2api 旁路的 intl 实例，traeai- 前缀）──
// 与国内版同一个镜像、同一套控制协议，端点只差路径段 traeai，所以这一段是照着国内版
// 写的，但有两处必须不一样：
//   1. 权益是订阅档位。容器 /status 那条 pool 口径里的 credits 数值在国际版没有对应量
//      （上游只有 pay_status），所以账号表一律不读它，档位只来自 /admin/traeai/credits，
//      没刷到就写「未知」——未知额度不许变成零，也不许拿空字符串冒充已知。
//   2. 没有签到体系：POST /admin/traeai/schedule/checkin 直接 409，页签因此不放签到按钮，
//      也不把那条调用打出去（「一键签到」在这一路根本不存在）。
// 控制链路同样用标准 HTTP 状态码表达业务结果（400/404/409 原样透传），jsonAPI 的 !ok
// 分支已能抛错，这里不再包一层。
async function loadTraeai(){
 if(!csrf)return;
 try{renderTraeai(await jsonAPI('traeai'));}
 catch(error){notice(error.message);}
}
function renderTraeai(status){
 traeaiStatus=status;
 // 页签自己回报的 enabled 是「这条旁路配了没有」的一手口径，比后台探活那一轮更近。
 setTraeaiEnabled(!!status.enabled);
 const enabled=!!status.enabled;
 $('traeai-disabled').hidden=enabled;
 $('traeai-content').hidden=!enabled;
 if(!enabled)return;
 const reachable=!!status.reachable;
 const accounts=Array.isArray(status.accounts)?status.accounts:[];
 const badge=$('traeai-status-badge');
 let tone='bad',label='不可达',text='trae2api 国际版容器不可达：请确认 overlay 已随 compose 启动；若容器在运行，通常是 console 刚被重启、旁路网络失联，重启旁路容器即可恢复。';
 if(reachable){
  if(!accounts.length){tone='warn';label='无账号';text='容器可达，但还没有账号：到「登录与导入」子页走 OAuth 登录，或粘贴桌面客户端 storage.json、凭据 JSON、回调 URL 导入。';}
  else{tone='ok';label='在线';text=`trae2api 国际版可达 · ${status.model_count||0} 个模型 · ${accounts.length} 个账号`;}
 }
 badge.textContent=label;badge.className='badge '+tone;
 $('traeai-status-text').textContent=text;
 // 区域说明由后端下发（edition_note），页面原样显示；没下发就整条收起，不自己编一句。
 const note=typeof status.edition_note==='string'?status.edition_note.trim():'';
 const noteEl=$('traeai-edition-note');noteEl.textContent=note;noteEl.hidden=!note;
 // 容器不可达时 /status 不存在，区域、模型数与账号数一律留空而不是编造。
 const rows=[];
 if(reachable){
  if(status.edition_label)rows.push(['区域版本',String(status.edition_label)]);
  if(status.edition)rows.push(['区域标识',String(status.edition)]);
  if(status.agent_host)rows.push(['上游入口',String(status.agent_host)]);
  rows.push(['模型数',String(status.model_count||0)]);
  rows.push(['账号数',String(accounts.length)]);
 }
 const detail=$('traeai-health-body');detail.replaceChildren();
 for(const [k,v] of rows){const tr=document.createElement('tr');tr.append(cell(String(k)),cell(String(v)));detail.append(tr);}
 $('traeai-health-wrap').hidden=!rows.length;
 // 登录与导入子页在容器不可达时也保留（那是排障入口），其余数据子页只在可达时开放。
 const dataTab=reachable;
 if(traeaiView!=='status'&&traeaiView!=='login'&&!dataTab)traeaiView='status';
 for(const view of ['accounts','schedule','models','chat','usage'])$('traeai-view-'+view).hidden=!dataTab;
 for(const view of ['status','login','accounts','schedule','models','chat','usage']){
  $('traeai-view-'+view).classList.toggle('active',view===traeaiView);
  $('traeai-pane-'+view).hidden=view!==traeaiView;
 }
 traeaiModels=(status.models||[]).map(item=>item.id).filter(Boolean);
 renderAccessModelSelect();renderAccess();
 const previous=$('traeai-model').value;
 $('traeai-model').replaceChildren();
 for(const id of traeaiModels)$('traeai-model').append(new Option(id,id));
 if(traeaiModels.includes(previous))$('traeai-model').value=previous;
 renderModelTable('traeai',status.models,status.health||null);
 renderTraeaiAccounts(accounts);
 $('traeai-model').disabled=!traeaiModels.length||!!activeRequest;
 $('traeai-send').disabled=!traeaiModels.length||!!activeRequest;
 traeaiControls();traeaiActionControls();
}
function traeaiControls(){
 for(const id of ['traeai-model','traeai-prompt','traeai-send'])$(id).disabled=!!activeRequest;
 $('traeai-stop').hidden=!activeRequest;
}
function traeaiActionControls(){
 for(const id of ['traeai-refresh','traeai-credits-refresh','traeai-schedule-refresh','traeai-refresh-now'])$(id).disabled=traeaiBusy;
}
function traeaiRecoveryHint(a){
 if(!a.cooling||!a.until)return '';
 const until=new Date(a.until);
 if(Number.isNaN(until.getTime()))return '';
 return '约 '+until.toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false})+' 恢复';
}
// 权益档位文本：只认「刷新权益」那一次上游明确回报的 subscription 字符串。
// 没刷过、刷失败、上游没给档位名，一律是「未知」——绝不回落到 /status 的 credits 数值，
// 那个字段在国际版本就没有对应量，拿它显示等于凭空造一个积分余额。
function traeaiEntitlementText(uid){
 const hit=traeaiEntitlements.get(String(uid));
 return hit&&typeof hit.subscription==='string'&&hit.subscription?hit.subscription:'未知';
}
function traeaiEntitlementHint(uid){
 const hit=traeaiEntitlements.get(String(uid));
 const fast=hit?Number(hit.fast_request_per):NaN;
 return Number.isFinite(fast)&&fast>0?'快速请求额度 '+fast:'';
}
function traeaiAccountNote(a){
 const hit=traeaiEntitlements.get(String(a.uid));
 if(hit&&hit.error)return String(hit.error);
 return a.reason||'—';
}
function renderTraeaiAccounts(list){
 traeaiAccounts=Array.isArray(list)?list:[];
 const usable=traeaiAccounts.filter(a=>!a.disabled&&!a.cooling).length;
 const cooling=traeaiAccounts.filter(a=>a.cooling&&!a.disabled).length;
 const disabled=traeaiAccounts.filter(a=>a.disabled).length;
 $('traeai-stat-total').textContent=String(traeaiAccounts.length);
 $('traeai-stat-usable').textContent=String(usable);
 $('traeai-stat-cooling').textContent=String(cooling);
 $('traeai-stat-disabled').textContent=String(disabled);
 const body=$('traeai-accounts-body');body.replaceChildren();
 for(const a of traeaiAccounts){
  const tr=document.createElement('tr');
  const state=a.disabled?'已停用':a.cooling?'冷却中':'可用';
  const tone=a.disabled?'idle':a.cooling?'warn':'ok';
  const status=cell('');const badge=document.createElement('span');badge.className='badge '+tone;badge.textContent=state;status.append(badge);
  const expires=a.expires_at?new Date(Number(a.expires_at)*1000).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false}):'—';
  const ta=(label,action,cls)=>{const btn=document.createElement('button');btn.className=cls;btn.textContent=label;btn.dataset.traact=action;btn.dataset.uid=a.uid;return btn;};
  const op=document.createElement('td');
  if(a.disabled)op.append(ta('启用','enable','secondary'),' ',ta('删除','delete','quiet'));
  else op.append(ta('停用','disable','secondary'),' ',ta('刷新','refresh','quiet'),' ',ta('删除','delete','quiet'));
  // sidecar 只回报失败次数，没有成功计数；成功列留「—」而不是拿 0 充数。
  tr.append(cell(a.nickname||a.uid,a.uid),status,cell(traeaiEntitlementText(a.uid),traeaiEntitlementHint(a.uid)),numCell('— / '+(a.err_count||0)),cell(expires,a.expired_soon?'Token 即将过期':''),cell(traeaiAccountNote(a),traeaiRecoveryHint(a)),op);
  body.append(tr);
 }
 $('traeai-accounts-empty').hidden=traeaiAccounts.length>0;
}
async function traeaiAccountAction(action,uid){
 if(traeaiBusy)return;
 const generation=sessionGeneration;
 if(action==='delete'&&!confirm(`确定删除账号 ${uid}？凭据将从容器中移除，需要重新登录或重新导入才能恢复。`))return;
 traeaiBusy=true;traeaiActionControls();notice('');
 try{
  if(action==='enable'||action==='disable')await jsonAPI('traeai/accounts/patch',{uid,enabled:action==='enable'});
  else if(action==='delete'){await jsonAPI('traeai/accounts/delete',{uid});if(generation===sessionGeneration)notice('账号已删除');}
  else if(action==='refresh')await jsonAPI('traeai/accounts/refresh',{uid});
  if(generation!==sessionGeneration)return;
  await loadTraeai();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeaiBusy=false;traeaiActionControls();}}
}
$('traeai-accounts-body').addEventListener('click',event=>{
 const btn=event.target.closest('button[data-traact]');if(!btn||btn.disabled)return;
 return traeaiAccountAction(btn.dataset.traact,btn.dataset.uid);
});
// 登录由容器侧 OAuth 回调驱动，console 只轮询 pending 状态；退出管理会话即中止轮询。
// 回调落点（/authorize）两条区域共用，后端按「最近一次登录由哪条通道发起」转给对应容器。
async function pollTraeaiLogin(generation,pendingId){
 while(generation===sessionGeneration){
  let state;
  try{state=await jsonAPI('traeai/login?pending_id='+encodeURIComponent(pendingId));}
  catch(error){
   $('traeai-login-badge').textContent='未登录';$('traeai-login-badge').className='badge warn';
   $('traeai-login-hint').textContent=error.message;return;
  }
  if(generation!==sessionGeneration)return;
  if(state.state==='success'){
   $('traeai-login-badge').textContent='已登录';$('traeai-login-badge').className='badge ok';
   $('traeai-auth-row').hidden=true;traeaiAuthURL='';
   $('traeai-login-hint').textContent=state.nickname?`账号 ${state.nickname} 已登录。`:'账号已登录。';
   notice('Trae 国际版账号登录成功');
   traeaiView='accounts';await loadTraeai();
   return;
  }
  if(state.state==='failed'){
   $('traeai-login-badge').textContent='未登录';$('traeai-login-badge').className='badge warn';
   $('traeai-login-hint').textContent=`登录失败：${state.error||'未知错误'}，可重新发起。`;return;
  }
  if(state.state==='canceled'){$('traeai-login-hint').textContent='登录已取消。';return;}
  await new Promise(resolve=>{traeaiPollTimer=setTimeout(resolve,2000);});
 }
}
$('traeai-login-form').addEventListener('submit',async event=>{
 event.preventDefault();if(traeaiLoginBusy)return;
 const generation=sessionGeneration;traeaiLoginBusy=true;$('traeai-login-start').disabled=true;notice('');
 $('traeai-login-hint').textContent='正在发起登录…';
 try{
  const result=await jsonAPI('traeai/login',{});
  if(generation!==sessionGeneration)return;
  traeaiAuthURL=result.login_url||'';
  $('traeai-auth-row').hidden=!traeaiAuthURL;
  if(traeaiAuthURL){
   $('traeai-auth-link').href=traeaiAuthURL;
   try{window.open(traeaiAuthURL,'_blank','noopener');}catch(_){}
  }
  $('traeai-login-badge').textContent='登录中';$('traeai-login-badge').className='badge warn';
  $('traeai-login-hint').textContent='请在新打开的页面完成授权，本页会自动等待结果。';
  void pollTraeaiLogin(generation,result.pending_id);
 }catch(error){
  if(generation===sessionGeneration){
   $('traeai-login-badge').textContent='未登录';$('traeai-login-badge').className='badge warn';
   $('traeai-login-hint').textContent=error.message;
  }
 }finally{if(generation===sessionGeneration){traeaiLoginBusy=false;$('traeai-login-start').disabled=false;}}
});
$('traeai-import-form').addEventListener('submit',async event=>{
 event.preventDefault();if(traeaiLoginBusy)return;
 const text=$('traeai-import-url').value.trim();
 if(!text){$('traeai-import-hint').textContent='请粘贴浏览器回调 URL、导出的凭据 JSON，或桌面客户端 storage.json 全文。';return;}
 const generation=sessionGeneration;traeaiLoginBusy=true;$('traeai-import').disabled=true;notice('');
 $('traeai-import-hint').textContent='正在导入…';
 try{
  // console 把请求体原样转给容器，字段名得对得上容器的判别：
  // 回调链接走 callback_url，凭据 JSON 与 storage.json 全文走 json。
  const payload=/^https?:\/\//i.test(text)?{callback_url:text}:{json:text};
  const result=await jsonAPI('traeai/accounts/import',payload);
  if(generation!==sessionGeneration)return;
  $('traeai-import-url').value='';
  $('traeai-import-hint').textContent=`已导入 ${result.nickname||result.uid||'账号'}，建议刷新权益核对档位。`;
  notice('Trae 国际版账号凭据导入成功');
  await loadTraeai();
 }catch(error){
  if(generation===sessionGeneration)$('traeai-import-hint').textContent=error.status===409?'该账号已有更新的凭证，未覆盖（409）。':error.message;
 }finally{if(generation===sessionGeneration){traeaiLoginBusy=false;$('traeai-import').disabled=false;}}
});
function renderTraeaiSchedule(data){
 const rows=[];
 if((data.refresh_hours||[]).length)rows.push(['凭据刷新时段',data.refresh_hours.join(', ')]);
 if(data.next_fire)rows.push(['下次执行',String(data.next_fire)]);
 if(data.last_refresh)rows.push(['上次刷新',String(data.last_refresh)]);
 const body=$('traeai-schedule-body');body.replaceChildren();
 for(const [k,v] of rows){const tr=document.createElement('tr');tr.append(cell(String(k)),cell(String(v)));body.append(tr);}
 $('traeai-schedule-empty').hidden=rows.length>0;
}
function renderTraeaiUsage(data){
 const days=Array.isArray(data.days)?data.days:[];
 const body=$('traeai-usage-body');body.replaceChildren();
 for(const day of days){
  const tr=document.createElement('tr');
  tr.append(cell(String(day.date||'—')),numCell(String(day.requests||0)),numCell(String(day.errors||0)),numCell(String(day.prompt_tokens||0)),numCell(String(day.completion_tokens||0)),numCell(String(day.total_tokens||0)));
  body.append(tr);
 }
 $('traeai-usage-empty').hidden=days.length>0;
}
$('traeai-credits-refresh').addEventListener('click',async()=>{
 if(traeaiBusy)return;const generation=sessionGeneration;traeaiBusy=true;traeaiActionControls();notice('');
 try{
  const result=await jsonAPI('traeai/credits');
  if(generation!==sessionGeneration)return;
  const list=Array.isArray(result.accounts)?result.accounts:[];
  // 档位只在这一次请求之后才有值：按 uid 存上游明确回报的 subscription，缺字段就不存，
  // 渲染层随之显示「未知」。这里不折算、不补零。
  for(const a of list){
   if(!a||a.uid==null)continue;
   traeaiEntitlements.set(String(a.uid),{
    subscription:typeof a.subscription==='string'?a.subscription:'',
    error:typeof a.error==='string'?a.error:'',
    fast_request_per:a.fast_request_per
   });
  }
  const failed=list.filter(x=>x&&x.error);
  const missing=list.filter(x=>x&&!(typeof x.subscription==='string'&&x.subscription)).length;
  if(failed.length)notice(`权益刷新完成，${failed.length} 个账号失败：${failed[0].error}`);
  else if(missing)notice(`权益已刷新（${list.length} 个账号），${missing} 个账号上游未回报档位`);
  else notice(`权益已刷新（${list.length} 个账号）`);
  await loadTraeai();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeaiBusy=false;traeaiActionControls();}}
});
$('traeai-schedule-refresh').addEventListener('click',async()=>{
 if(traeaiBusy)return;const generation=sessionGeneration;traeaiBusy=true;traeaiActionControls();
 try{renderTraeaiSchedule(await jsonAPI('traeai/schedule'));}
 catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeaiBusy=false;traeaiActionControls();}}
});
$('traeai-refresh-now').addEventListener('click',async()=>{
 if(traeaiBusy)return;const generation=sessionGeneration;traeaiBusy=true;traeaiActionControls();
 try{
  await jsonAPI('traeai/schedule/refresh',{});
  if(generation!==sessionGeneration)return;
  $('traeai-schedule-hint').textContent='已触发凭据刷新：容器将在本轮执行，完成后点「刷新排程」查看结果。';
 }catch(error){if(generation===sessionGeneration)$('traeai-schedule-hint').textContent=error.message;}
 finally{if(generation===sessionGeneration){traeaiBusy=false;traeaiActionControls();}}
});
$('traeai-usage-refresh').addEventListener('click',async()=>{
 if(traeaiBusy)return;const generation=sessionGeneration;traeaiBusy=true;$('traeai-usage-refresh').disabled=true;
 try{renderTraeaiUsage(await jsonAPI('traeai/usage'));}
 catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){traeaiBusy=false;$('traeai-usage-refresh').disabled=false;}}
});
for(const view of ['status','login','accounts','schedule','models','chat','usage'])$('traeai-view-'+view).addEventListener('click',()=>{traeaiView=view;if(traeaiStatus)renderTraeai(traeaiStatus);});
runTopRefresh($('traeai-refresh'),async()=>{
 if(traeaiBusy)return;const generation=sessionGeneration;traeaiBusy=true;traeaiActionControls();
 try{await loadTraeai();}finally{if(generation===sessionGeneration){traeaiBusy=false;traeaiActionControls();}}
});
$('traeai-stop').addEventListener('click',()=>activeRequest?.abort());
$('traeai-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('traeai-prompt').value.trim();if(!text||!$('traeai-model').value)return;
 const generation=sessionGeneration;
 notice('');message('user',text,$('traeai-messages'));$('traeai-prompt').value='';const answer=message('assistant','正在等待模型…',$('traeai-messages'));
 const controller=new AbortController();activeRequest=controller;traeaiControls();
 let content='',usage,finished=false;const outgoing=[...traeaiHistory,{role:'user',content:text}];
 try{
  const response=await api('traeai/chat',{model:$('traeai-model').value,messages:outgoing,stream:true},controller.signal);
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
  traeaiHistory=[...outgoing,{role:'assistant',content}];
  $('traeai-usage').textContent=(usage?`输入 ${usage.prompt_tokens??'—'} · 输出 ${usage.completion_tokens??'—'} · 总计 ${usage.total_tokens??'—'} tokens`:'上游未返回用量')+' · 本次通道：Trae 国际版';
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=(content?content+'\n\n':'')+msg;$('traeai-usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;traeaiControls();}
});

// ── 小浣熊通道（raccoon2api 旁路，raccoon- 前缀）──
// 状态接口把容器 /status 与 /admin/api/accounts 按 uid 合并后下发；控制链路用标准 HTTP
// 状态码表达业务结果（400 参数错、409 已有更新凭证原样透传），jsonAPI 的 !ok 分支已能
// 抛错。小浣熊没有网页登录（桌面登录态依赖宿主 DPAPI），登录环节等价于凭证导入。
async function loadRaccoon(){
 if(!csrf)return;
 try{renderRaccoon(await jsonAPI('raccoon'));}
 catch(error){notice(error.message);}
}
function renderRaccoon(status){
 raccoonStatus=status;
 const enabled=!!status.enabled;
 $('raccoon-disabled').hidden=enabled;
 $('raccoon-content').hidden=!enabled;
 if(!enabled)return;
 const reachable=!!status.reachable;
 const accounts=Array.isArray(status.accounts)?status.accounts:[];
 const badge=$('raccoon-status-badge');
 let tone='bad',label='不可达',text='raccoon2api 容器不可达：请确认 overlay 已随 compose 启动；若容器在运行，通常是 console 刚被重启、旁路网络失联，重启旁路容器即可恢复。';
 if(reachable){
  if(!accounts.length){tone='warn';label='无账号';text='容器可达，但还没有导入凭证：到「凭证导入」子页粘贴桌面端导出的凭证 JSON。';}
  else{tone='ok';label='在线';text=`raccoon2api 可达 · ${status.model_count||0} 个模型 · ${accounts.length} 个账号`;}
 }
 badge.textContent=label;badge.className='badge '+tone;
 $('raccoon-status-text').textContent=text;
 // 容器不可达时 /status 不存在，凭据与账号数一律留空而不是编造。
 const rows=[];
 if(reachable){rows.push(['模型数',String(status.model_count||0)]);rows.push(['账号数',String(accounts.length)]);}
 const detail=$('raccoon-health-body');detail.replaceChildren();
 for(const [k,v] of rows){const tr=document.createElement('tr');tr.append(cell(String(k)),cell(String(v)));detail.append(tr);}
 $('raccoon-health-wrap').hidden=!rows.length;
 // 导入子页在容器不可达时也保留（那是排障入口），其余数据子页只在可达时开放。
 const dataTab=reachable;
 if(raccoonView!=='status'&&raccoonView!=='import'&&!dataTab)raccoonView='status';
 for(const view of ['models','chat'])$('raccoon-view-'+view).hidden=!dataTab;
 for(const view of ['status','import','models','chat']){
  $('raccoon-view-'+view).classList.toggle('active',view===raccoonView);
  $('raccoon-pane-'+view).hidden=view!==raccoonView;
 }
 raccoonModels=(status.models||[]).map(item=>item.id).filter(Boolean);
 renderAccessModelSelect();renderAccess();
 const previous=$('raccoon-model').value;
 $('raccoon-model').replaceChildren();
 // 下拉显示上游真实展示名（目录带 name 时），值仍是公共 id：选路、会话记录与
 // go-chat 的 raccoonModels.includes 判断都只认 id，展示名仅影响可读性。
 for(const item of (Array.isArray(status.models)?status.models:[])){
  if(!item||!item.id)continue;
  const label=typeof item.name==='string'&&item.name?item.name:item.id;
  $('raccoon-model').append(new Option(label,item.id));
 }
 if(raccoonModels.includes(previous))$('raccoon-model').value=previous;
 renderModelTable('raccoon',status.models,status.health||null);
 renderRaccoonAccounts(accounts);
 $('raccoon-model').disabled=!raccoonModels.length||!!activeRequest;
 $('raccoon-send').disabled=!raccoonModels.length||!!activeRequest;
 raccoonControls();raccoonActionControls();
}
function raccoonControls(){
 for(const id of ['raccoon-model','raccoon-prompt','raccoon-send'])$(id).disabled=!!activeRequest;
 $('raccoon-stop').hidden=!activeRequest;
}
function raccoonActionControls(){
 for(const id of ['raccoon-refresh','raccoon-credits-refresh'])$(id).disabled=raccoonBusy;
}
function raccoonRecoveryHint(a){
 if(!a.cooling||!a.until)return '';
 const until=new Date(a.until);
 if(Number.isNaN(until.getTime()))return '';
 return '约 '+until.toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false})+' 恢复';
}
function renderRaccoonAccounts(list){
 raccoonAccounts=Array.isArray(list)?list:[];
 const off=a=>!!a.disabled||a.enabled===false;
 const usable=raccoonAccounts.filter(a=>!off(a)&&!a.cooling).length;
 const cooling=raccoonAccounts.filter(a=>a.cooling&&!off(a)).length;
 const disabled=raccoonAccounts.filter(a=>off(a)).length;
 $('raccoon-stat-total').textContent=String(raccoonAccounts.length);
 $('raccoon-stat-usable').textContent=String(usable);
 $('raccoon-stat-cooling').textContent=String(cooling);
 $('raccoon-stat-disabled').textContent=String(disabled);
 const body=$('raccoon-accounts-body');body.replaceChildren();
 for(const a of raccoonAccounts){
  const tr=document.createElement('tr');
  const stopped=off(a);
  const state=stopped?'已停用':a.cooling?'冷却中':'可用';
  const tone=stopped?'idle':a.cooling?'warn':'ok';
  const status=cell('');const b=document.createElement('span');b.className='badge '+tone;b.textContent=state;status.append(b);
  const expires=a.expires_at?new Date(Number(a.expires_at)*1000).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false}):'—';
  const ta=(label,action,cls)=>{const btn=document.createElement('button');btn.className=cls;btn.textContent=label;btn.dataset.raact=action;btn.dataset.uid=a.uid;return btn;};
  const op=document.createElement('td');
  if(stopped)op.append(ta('启用','enable','secondary'),' ',ta('删除','delete','quiet'));
  else op.append(ta('停用','disable','secondary'),' ',ta('刷新','refresh','quiet'),' ',ta('删除','delete','quiet'));
  // sidecar 只回报失败次数，没有成功计数；成功列留「—」而不是拿 0 充数。
  tr.append(cell(a.nickname||a.uid,a.uid),status,numCell(a.points==null?'—':formatCredit(a.points)),numCell('— / '+(a.err_count||0)),cell(expires,a.expired_soon?'Token 即将过期':''),cell(a.reason||'—',raccoonRecoveryHint(a)),op);
  body.append(tr);
 }
 $('raccoon-accounts-empty').hidden=raccoonAccounts.length>0;
}
async function raccoonAccountAction(action,uid){
 if(raccoonBusy)return;
 const generation=sessionGeneration;
 if(action==='delete'&&!confirm(`确定删除账号 ${uid}？凭据将从容器中移除，需要重新导入才能恢复。`))return;
 raccoonBusy=true;raccoonActionControls();notice('');
 try{
  if(action==='enable'||action==='disable')await jsonAPI('raccoon/accounts/patch',{uid,enabled:action==='enable'});
  else if(action==='delete'){await jsonAPI('raccoon/accounts/delete',{uid});if(generation===sessionGeneration)notice('账号已删除');}
  else if(action==='refresh')await jsonAPI('raccoon/accounts/refresh',{uid});
  if(generation!==sessionGeneration)return;
  await loadRaccoon();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){raccoonBusy=false;raccoonActionControls();}}
}
$('raccoon-accounts-body').addEventListener('click',event=>{
 const btn=event.target.closest('button[data-raact]');if(!btn||btn.disabled)return;
 raccoonAccountAction(btn.dataset.raact,btn.dataset.uid);
});
for(const view of ['status','import','models','chat'])$('raccoon-view-'+view).addEventListener('click',()=>{raccoonView=view;if(raccoonStatus)renderRaccoon(raccoonStatus);});
runTopRefresh($('raccoon-refresh'),async()=>{
 if(raccoonBusy)return;const generation=sessionGeneration;raccoonBusy=true;raccoonActionControls();
 try{await loadRaccoon();}finally{if(generation===sessionGeneration){raccoonBusy=false;raccoonActionControls();}}
});
$('raccoon-credits-refresh').addEventListener('click',async()=>{
 if(raccoonBusy)return;const generation=sessionGeneration;raccoonBusy=true;raccoonActionControls();notice('');
 try{
  const result=await jsonAPI('raccoon/credits');
  if(generation!==sessionGeneration)return;
  const list=Array.isArray(result.accounts)?result.accounts:[];
  const failed=list.filter(x=>x&&x.error);
  notice(failed.length?`积分刷新完成，${failed.length} 个账号失败：${failed[0].error}`:`积分已刷新（${list.length} 个账号）`);
  await loadRaccoon();
 }catch(error){if(generation===sessionGeneration)notice(error.message);}
 finally{if(generation===sessionGeneration){raccoonBusy=false;raccoonActionControls();}}
});
$('raccoon-copy-path').addEventListener('click',async()=>{
 try{await navigator.clipboard.writeText('%USERPROFILE%\\.box-agent\\config');notice('路径已复制，粘贴到资源管理器地址栏（Win+E 后 Ctrl+L）回车打开');}
 catch{notice('浏览器不允许自动复制，请手动输入 %USERPROFILE%\\.box-agent\\config');}
});
$('raccoon-pick-file').addEventListener('click',()=>$('raccoon-file').click());
$('raccoon-file').addEventListener('change',()=>{
 const file=$('raccoon-file').files&&$('raccoon-file').files[0];if(!file)return;
 const reader=new FileReader();
 reader.onload=()=>{
  $('raccoon-import-json').value=String(reader.result||'');
  $('raccoon-import').classList.add('attn');$('raccoon-import').focus();
  $('raccoon-import-hint').textContent=`已读取 ${file.name}，内容已填入下面的输入框，确认无误后点「导入」。`;
 };
 reader.onerror=()=>{$('raccoon-import-hint').textContent='读取文件失败，请改用文本编辑器打开后复制内容粘贴。';};
 reader.readAsText(file);
});
$('raccoon-import-form').addEventListener('submit',async event=>{
 event.preventDefault();if(raccoonBusy)return;
 const text=$('raccoon-import-json').value.trim();
 if(!text){$('raccoon-import-hint').textContent='请先点上面的「① 选择 auth.json 文件」，或直接粘贴 auth.json 的完整内容。';return;}
 if(!/^[{[]/.test(text)){$('raccoon-import-hint').textContent='这看起来是文件路径，不是凭证内容。请点上面的「① 选择 auth.json 文件」选择该文件，或用文本编辑器打开后复制里面的完整 JSON 粘贴进来。';return;}
 let payload;try{payload=JSON.parse(text);}catch{$('raccoon-import-hint').textContent='不是合法的 JSON，请粘贴完整凭证内容。';return;}
 const generation=sessionGeneration;raccoonBusy=true;$('raccoon-import').disabled=true;$('raccoon-import').classList.remove('attn');notice('');
 $('raccoon-import-hint').textContent='正在导入…';
 try{
  const result=await jsonAPI('raccoon/accounts/import',payload);
  if(generation!==sessionGeneration)return;
  $('raccoon-import-json').value='';
  $('raccoon-import-hint').textContent=`已导入 ${result.nickname||result.uid||'账号'}${result.needs_check?'；凭据临近有效期，建议到账号管理点「刷新」核对。':''}`;
  notice('小浣熊凭证导入成功');
  raccoonView='status';await loadRaccoon();
 }catch(error){
  if(generation===sessionGeneration)$('raccoon-import-hint').textContent=error.status===409?'该账号已有更新的凭证，未覆盖（409）。':error.message;
 }finally{if(generation===sessionGeneration){raccoonBusy=false;$('raccoon-import').disabled=false;}}
});
$('raccoon-stop').addEventListener('click',()=>activeRequest?.abort());
$('raccoon-form').addEventListener('submit',async event=>{
 event.preventDefault();if(activeRequest)return;const text=$('raccoon-prompt').value.trim();if(!text||!$('raccoon-model').value)return;
 const generation=sessionGeneration;
 notice('');message('user',text,$('raccoon-messages'));$('raccoon-prompt').value='';const answer=message('assistant','正在等待模型…',$('raccoon-messages'));
 const controller=new AbortController();activeRequest=controller;raccoonControls();
 let content='',usage,finished=false;const outgoing=[...raccoonHistory,{role:'user',content:text}];
 try{
  const response=await api('raccoon/chat',{model:$('raccoon-model').value,messages:outgoing,stream:true},controller.signal);
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
  raccoonHistory=[...outgoing,{role:'assistant',content}];
  $('raccoon-usage').textContent=(usage?`输入 ${usage.prompt_tokens??'—'} · 输出 ${usage.completion_tokens??'—'} · 总计 ${usage.total_tokens??'—'} tokens`:'上游未返回用量')+' · 本次通道：小浣熊';
 }catch(e){if(generation===sessionGeneration){const msg=e.name==='AbortError'?'已停止生成':e.message;answer.content.textContent=(content?content+'\n\n':'')+msg;$('raccoon-usage').textContent=msg;}}
 finally{controller.abort();activeRequest=undefined;raccoonControls();}
});

jsonAPI('session').then(signedIn).catch(()=>{});

// 健康只读 24h 快照；不要用 modelKey 折叠 cn/global 或不同通道的原生模型。
function modelHealthKey(channel,model){
 const c=channel==='zcode'?'glm':channel==='workbuddy'?'core':channel;
 const prefixes={qoder:'qoder-',opencode:'opencode-',autoclaw:'autoclaw-',trae:'trae-',traeai:'traeai-',raccoon:'raccoon-',freellm:'freellm-'};
 const prefix=prefixes[c];const name=String(model||'');
 return JSON.stringify([c,prefix&&name.startsWith(prefix)?name.slice(prefix.length):name]);
}
function applyModelHealth(data){
 modelHealth.clear();
 for(const row of data.health_models||[])modelHealth.set(JSON.stringify([row.channel,row.model]),row);
 for(const row of data.data||[])if(row.health)modelHealth.set(modelHealthKey('core',row.id),row);
 for(const [channel,source] of Object.entries(modelHeatSources))renderModelTable(channel,source.list,source.health);
 if(routesState&&Array.isArray(routesState.aliases))renderRouteAliases(routesState.aliases);
}
function modelHealthCell(channel,model,probe){
 const row=modelHealth.get(modelHealthKey(channel,model));
 const state=row?row.health:probe&&typeof probe.ok==='boolean'?(probe.ok?'ok':'fail'):'unknown';
 const tones={ok:'ok',degraded:'warn',fail:'bad',unknown:'unknown'};
 const td=badgeCell(tones[state]?state:'unknown',tones[state]||'unknown');
 const samples=row?row.health_samples:0;
 td.children[0].title=`24h 错误率 ${samples>0?formatErrorRate(row.health_error_rate):'—'} · 样本 ${samples||0} 次（unknown 不计分母）`;
 return td;
}
function formatLatency(value){return typeof value==='number'&&Number.isFinite(value)&&value>=0?`${value} ms`:'—';}
function renderErrorModels(data){
 const top=$('errors-top-body');top.replaceChildren();
 const topRows=(data.top_models||[]).slice(0,5);$('errors-top-empty').hidden=topRows.length>0;
 for(const row of topRows){
  const tr=document.createElement('tr');
  tr.append(cell(row.model),channelCell(routeChannelLabel(row.channel),row.channel),numCell(String(row.fail)),numCell(formatErrorRate(row.error_rate)));top.append(tr);
 }
 const latency=$('errors-latency-body');latency.replaceChildren();
 const latencyRows=data.latency||[];$('errors-latency-empty').hidden=latencyRows.length>0;
 for(const row of latencyRows){
  const tr=document.createElement('tr');
  tr.append(cell(row.model),channelCell(routeChannelLabel(row.channel),row.channel),numCell(formatLatency(row.p50_ms)),numCell(formatLatency(row.p95_ms)));latency.append(tr);
 }
 const daily=$('errors-429-body');daily.replaceChildren();
 const dailyRows=(data.r429_daily||[]).slice().sort((a,b)=>b.count-a.count||String(a.channel).localeCompare(String(b.channel))||String(a.model).localeCompare(String(b.model)));
 $('errors-429-empty').hidden=dailyRows.length>0;
 $('errors-429-count').textContent=String(dailyRows.reduce((sum,row)=>sum+row.count,0));
 for(const row of dailyRows.slice(0,5)){
  const tr=document.createElement('tr');tr.append(cell(row.model),channelCell(routeChannelLabel(row.channel),row.channel),numCell(String(row.count)));daily.append(tr);
 }
}

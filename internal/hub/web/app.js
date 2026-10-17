'use strict';
// long: 资源站文字始终通过 textContent 渲染，避免片名或简介中的 HTML 在管理台执行。
const $ = id => document.getElementById(id);
const el = (tag, text, cls) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = text; if (cls) n.className = cls; return n; };
// long: 每个菜单是独立 HTML；只初始化当前页面的控件，避免缺失其他页面节点时中断轮询。
let snapshot, detail, page = 1, lastSearch, currentPage = document.body.dataset.page || 'search', settingsLoaded = false, refreshing = false, connectionLost = false;
let collectionMode = 'home', homeTypeID = '', homeSourceID = '', homeInitialized = false, homeRequestSeq = 0, searchRequestSeq = 0, sourceSelectionInitialized = false, previousDefaultID = '';
let checkingSources = false;
let homeClasses = [];
const collectionPageSize = 20;
let homeCache, searchCache;
const labels = {queued:'等待下载',downloading:'下载中',paused:'已暂停',canceled:'已取消',failed:'下载失败',completed:'已下载'};
function notice(message, error = false) { $('notice').textContent = message; $('notice').className = error ? 'notice error' : 'notice'; $('notice').hidden = false; }
async function api(path, body) {
 const headers = {};
 const options = {headers};
 if (body !== undefined) { options.method = 'POST'; headers['Content-Type'] = 'application/json'; options.body = JSON.stringify(body); }
 const response = await fetch('/api/' + path, options);
 const result = await response.json();
 if (!response.ok) throw new Error(result.error || '操作失败，请稍后重试');
 return result;
}
async function act(button, task) {button.disabled = true; try {await task();} catch(e) {notice(e.message, true);} finally {button.disabled = false;}}
function button(text, action) { const b = el('button',text); b.type = 'button'; b.addEventListener('click', () => act(b, action)); return b; }
function icon(name) {
 const paths = {
  refresh:'M20 7v5h-5 M4 17v-5h5 M6 8a7 7 0 0 1 12-2l2 3 M18 16a7 7 0 0 1-12 2l-2-3',
  check:'m5 12 4 4L19 6', cross:'m6 6 12 12M18 6 6 18', help:'M9 8a3 3 0 0 1 6 0c0 2-3 2-3 5M12 17h.01',
  star:'m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-3-5.6 3 1.1-6.2L3 9.6l6.2-.9Z',
  power:'M12 3v8M6.3 5.7a8 8 0 1 0 11.4 0', clock:'M12 8v4l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0',
  play:'m9 5 11 7-11 7Z', pause:'M8 5v14M16 5v14', trash:'M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7',
  scan:'M8 3H3v5M16 3h5v5M21 16v5h-5M8 21H3v-5m4-9 3 3 6-6'
 };
 const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');
 for(const [key,value] of Object.entries({viewBox:'0 0 24 24',fill:'none',stroke:'currentColor','stroke-width':'1.7','stroke-linecap':'round','stroke-linejoin':'round','aria-hidden':'true'}))svg.setAttribute(key,value);
 const path=document.createElementNS(svg.namespaceURI,'path');path.setAttribute('d',paths[name]||paths.help);svg.append(path);return svg;
}
function setButtonIcon(node, name, label) {node.replaceChildren(icon(name));node.classList.add('icon-button');node.title=label;node.setAttribute('aria-label',label);}
function iconButton(name, label, action) {const node=button('',action);setButtonIcon(node,name,label);return node;}
function navigate(path, message) { if (message) sessionStorage.setItem('moviehubNotice', message); window.location.href = path; }
const flash = sessionStorage.getItem('moviehubNotice');
if (flash) { sessionStorage.removeItem('moviehubNotice'); notice(flash); }
function empty(target,text) { target.replaceChildren(el('div',text,'empty')); }
function updateSources() {const select = $('searchSource'), selected = select.value, defaultID = snapshot.state.sources.find(s => s.enabled && s.default)?.id || ''; select.replaceChildren(new Option('全部已启用资源站','')); snapshot.state.sources.filter(s => s.enabled).forEach(s => select.add(new Option(s.name,s.id))); if (!sourceSelectionInitialized || selected === previousDefaultID || (selected && !snapshot.state.sources.some(s => s.enabled && s.id === selected))) select.value = defaultID; else select.value = selected; sourceSelectionInitialized = true; previousDefaultID = defaultID; return defaultID;}
async function refresh() {
 if (refreshing) return;
 refreshing = true;
 try {
  snapshot = await api('state');
  // long: 页首不再占位展示正常连接；断线恢复后只撤掉本次断线提示。
  if (connectionLost) {$('notice').hidden=true;connectionLost=false;}
  $('queueCount').textContent=snapshot.state.jobs.filter(j=>['queued','downloading'].includes(j.status)).length||'';
  const defaultID=currentPage==='search'?updateSources():'';
  if (currentPage==='settings'&&!settingsLoaded) loadSettings();
  render();
  if (currentPage==='search'&&collectionMode==='home'&&(!homeInitialized||defaultID!==homeSourceID)) {homeTypeID='';page=1;loadHome();}
 } catch(e) {connectionLost=true;notice('服务连接失败：'+e.message,true);}
 finally {refreshing=false;}
}
function render() {if (!snapshot) return; if(currentPage === 'jobs') renderJobs(); if(currentPage === 'follows') renderFollows(); if(currentPage === 'sources') renderSources(); if(currentPage !== 'settings') return; const st=snapshot.state; $('embyStatus').textContent = (snapshot.embyKeySet ? 'API Key 已保存。' : '尚未保存 API Key。') + (st.emby.error || (st.emby.pending ? '等待媒体库扫描。' : st.emby.lastRequest.startsWith('0001') ? '尚未发起扫描。' : '上次请求扫描：' + new Date(st.emby.lastRequest).toLocaleString())); $('toolStatus').textContent = Object.entries(snapshot.tools).map(([k,v]) => ({ffmpeg:'FFmpeg',ffprobe:'FFprobe'}[k])+ (v ? ' 可用' : ' 未找到')).join('；');}
function loadSettings() {const form = $('settingsForm'), cfg = snapshot.state.settings; for (const [key,value] of Object.entries(cfg)) {const field = form.elements.namedItem(key); if (!field) continue; if(field.type === 'checkbox') field.checked = value; else field.value = value;} form.elements.embyKey.value = ''; settingsLoaded = true;}
if ($('settingsForm')) $('settingsForm').onsubmit = e => {e.preventDefault(); const form=e.target; act(form.querySelector('button.primary'),async() => {const cfg={...snapshot.state.settings}; for(const key of Object.keys(cfg)){const n=form.elements.namedItem(key); if(n) cfg[key]=n.type==='checkbox'?n.checked:n.type==='number'?Number(n.value):n.value;} cfg.embyKey=form.elements.embyKey.value; await api('settings',{settings:cfg,clearEmbyKey:form.elements.clearEmbyKey.checked}); form.elements.embyKey.value=''; form.elements.clearEmbyKey.checked=false; settingsLoaded=false; await refresh(); notice('设置已保存');});};
if ($('searchForm')) $('searchForm').onsubmit = e => {e.preventDefault(); collectionMode = 'search'; page = 1; lastSearch={q:$('keyword').value.trim(),sourceId:$('searchSource').value}; searchCache = null; search();};
function renderPoster(v) {
 const n=button('',() => openDetail(v)); n.className='poster';
 const art=el('div',undefined,'poster-art');
 const placeholder=el('span',undefined,'poster-placeholder');
 // long: 卡片标题和图片已有可读名称，占位图只负责视觉提示，避免读屏重复朗读“暂无海报”。
 placeholder.setAttribute('aria-hidden','true');
 placeholder.append(el('span','▣','poster-placeholder-mark'),el('span','暂无海报'));
 art.append(placeholder);
 if (/^https?:\/\//i.test(v.poster)){const img=el('img');img.src=v.poster;img.alt=v.title+'海报';img.loading='lazy';img.referrerPolicy='no-referrer';img.onerror=()=>img.remove();art.append(img);}
 n.append(art,el('h3',v.title),el('p',v.sourceName+(v.year?' / '+v.year:'')),el('span',v.remarks,'remarks'));
 return n;
}
function showCollection(videos, emptyText) {const root = $('results'); root.replaceChildren(); if (!videos.length) empty(root, emptyText); else videos.forEach(v => root.append(renderPoster(v)));}
// long: 资源站自行决定远端每页条数；把完整响应缓存后再切成本地 20 条页，翻页时不会丢掉多出的影片。
async function fillCollectionPage(cache, requestedPage, fetchPage) {
 while (cache.hasMore && cache.videos.length < requestedPage * collectionPageSize) {
  const remotePage = cache.remotePage + 1;
  const result = await fetchPage(remotePage);
  cache.remotePage = remotePage;
  cache.videos.push(...result.videos);
  cache.hasMore = result.hasMore;
  if (result.classes) cache.classes = result.classes;
  if (result.sourceName) cache.sourceName = result.sourceName;
  if (result.errors) Object.assign(cache.errors, result.errors);
 }
 return cache.videos.slice((requestedPage - 1) * collectionPageSize, requestedPage * collectionPageSize);
}
function updateCollectionPagination(cache, requestedPage, visibleCount) {
 $('pagination').hidden = !visibleCount && requestedPage === 1;
 $('pageNumber').textContent = '第 ' + requestedPage + ' 页';
 $('previousPage').disabled = requestedPage <= 1;
 $('nextPage').disabled = !cache.hasMore && cache.videos.length <= requestedPage * collectionPageSize;
}
function renderHomeCategories() {const root=$('homeCategories'); root.replaceChildren(); if(collectionMode !== 'home' || !homeClasses.length) {root.hidden=true;return;} root.hidden=false; const add=(name,id)=>{const b=button(name,async()=>{homeTypeID=id;page=1;await loadHome();});b.className='category-chip'+(homeTypeID===id?' selected':'');root.append(b);};add('全部','');homeClasses.forEach(c=>{if(c.type_id && c.type_name) add(c.type_name,String(c.type_id));});}
async function loadHome() {
 if (!snapshot) return;
 const request=++homeRequestSeq, requestedPage=page, requestedType=homeTypeID;
 const defaultID=snapshot.state.sources.find(s=>s.enabled && s.default)?.id || '';
 homeSourceID=defaultID;homeInitialized=true;collectionMode='home';
 $('goHome').hidden=true;$('reloadHome').hidden=false;$('searchErrors').textContent='';
 $('resultsTitle').textContent='默认站源片库';
 $('searchSummary').textContent=defaultID?'正在读取 '+snapshot.state.sources.find(s=>s.id===defaultID).name+'…':'没有可用的默认站源';
 $('pagination').hidden=true;
 if (!defaultID) {homeCache=null;homeClasses=[];renderHomeCategories();const root=$('results');empty(root,'请先到资源站页面刷新清单并运行一键检测');root.querySelector('.empty').append(button('管理资源站',()=>navigate('/sources.html')));return;}
 const key=defaultID+'\u0000'+requestedType;
 if (!homeCache || homeCache.key!==key) homeCache={key,videos:[],remotePage:0,hasMore:true,classes:[],sourceName:''};
 const cache=homeCache;
 try {
  const videos=await fillCollectionPage(cache,requestedPage,async remotePage=>{
   const result=await api('home?'+new URLSearchParams({page:remotePage,typeId:requestedType}));
   return {videos:result.videos,hasMore:result.hasMore,classes:remotePage===1?result.classes||[]:null,sourceName:result.source.name};
  });
  if(collectionMode!=='home'||request!==homeRequestSeq||cache!==homeCache)return;
  homeClasses=cache.classes;renderHomeCategories();
  $('resultsTitle').textContent=requestedType?'分类片库':'默认站源片库';
  $('searchSummary').textContent=cache.sourceName+' · 第 '+requestedPage+' 页';
  showCollection(videos,requestedType?'该站源未返回此分类的影片，可尝试具体类型':'该站源暂时没有影片');
  updateCollectionPagination(cache,requestedPage,videos.length);
 } catch(e) {if(collectionMode==='home'&&request===homeRequestSeq){const root=$('results');empty(root,'首页加载失败：'+e.message);root.querySelector('.empty').append(button('重试',loadHome));$('searchSummary').textContent='默认站源暂不可用';}}
}
async function search() {
 if (!lastSearch) return;
 await act($('searchButton'),async()=>{
  const request=++searchRequestSeq, requestedPage=page, query={...lastSearch};
  collectionMode='search';$('homeCategories').hidden=true;$('goHome').hidden=false;$('reloadHome').hidden=true;$('pagination').hidden=true;
  $('searchSummary').textContent='正在搜索资源站…';$('resultsTitle').textContent='搜索“'+query.q+'”';
  const key=query.sourceId+'\u0000'+query.q;
  if (!searchCache||searchCache.key!==key) searchCache={key,videos:[],remotePage:0,hasMore:true,errors:{}};
  const cache=searchCache;
  const videos=await fillCollectionPage(cache,requestedPage,async remotePage=>{
   const result=await api('search?'+new URLSearchParams({...query,page:remotePage}));
   return {videos:result.videos,hasMore:Object.values(result.pages).some(n=>n>remotePage),errors:result.errors};
  });
  if(collectionMode!=='search'||request!==searchRequestSeq||cache!==searchCache)return;
  showCollection(videos,'没有找到剧集，可以换个剧名或资源站再试');
  $('searchSummary').textContent='本页 '+videos.length+' 条 · 已载入 '+cache.videos.length+' 条结果';
  const errors=Object.keys(cache.errors);
  $('searchErrors').textContent=errors.length?errors.length+' 个资源站未响应：'+errors.join('、'):'';
  updateCollectionPagination(cache,requestedPage,videos.length);
 });
}
if ($('goHome')) $('goHome').onclick=()=>{page=1;homeTypeID='';loadHome();};
if ($('reloadHome')) $('reloadHome').onclick=()=>act($('reloadHome'),async()=>{page=1;homeCache=null;await loadHome();});
if ($('previousPage')) $('previousPage').onclick=()=>{if(page>1){page--;collectionMode==='home'?loadHome():search();}};
if ($('nextPage')) $('nextPage').onclick=()=>{page++;collectionMode==='home'?loadHome():search();};
function renderFollows(){const root=$('followList');root.replaceChildren();if(!snapshot.state.follows.length){empty(root,'还没有追更，打开剧集详情后选择“加入追更”');return;}snapshot.state.follows.forEach(f=>{const row=el('article',undefined,'follow-row'),main=el('div',undefined,'follow-main');main.append(el('h3',f.video.title),el('p',f.video.sourceName+' / 第 '+f.season+' 季 / '+f.known.length+' 集已记录 / 累计发现 '+f.newCount+' 集更新'),el('p','上次检查：'+new Date(f.lastCheck).toLocaleString()));if(f.lastError)main.append(el('p',f.lastError,'error-text'));const actions=el('div',undefined,'actions');const action=(name,cmd)=>actions.append(button(name,async()=>{await api('follows/'+f.id+'/'+cmd,{});await refresh();}));actions.append(button('查看分集',()=>openDetail({sourceId:f.video.sourceId,id:f.video.id,season:f.season,lineId:f.lineId})));action(f.autoDownload?'自动下载：开':'自动下载：关','auto');action(f.enabled?'暂停追更':'恢复追更','toggle');actions.append(button('移除',async()=>{if(confirm('移除此剧的追更？下载任务和文件会保留。')){await api('follows/'+f.id+'/remove',{});await refresh();}}));row.append(main,el('span',f.enabled?'追更中':'已暂停','badge'),actions);root.append(row);});}
function renderSources(){
 const root=$('sourceList'), sources=snapshot.state.sources;root.replaceChildren();
 if(!checkingSources){const available=sources.filter(s=>s.enabled).length, passed=sources.filter(s=>s.checkStatus==='ok').length, chosen=sources.find(s=>s.default);$('sourceSummary').textContent=sources.length?`${sources.length} 个站源 · ${passed} 个通过检测 · ${available} 个启用${chosen?' · 默认：'+chosen.name:''}`:'清单尚未导入';}
 if(!sources.length){empty(root,'站源清单尚未导入，点击“刷新清单”重试');return;}
 sources.forEach(src=>{
  const row=el('article',undefined,'source-row'+(src.enabled?'':' source-disabled'));
  const main=el('div',undefined,'source-main'),title=el('div',undefined,'source-title');title.append(el('h3',src.name));
  main.append(title);let host=src.api;try{host=new URL(src.api).host;}catch{}
  const address=el('p',host,'source-address');address.title=src.api;main.append(address);
  const status=src.checkStatus==='ok'?'可搜索':src.checkStatus==='unreachable'?'连接失败':src.checkStatus==='unsearchable'?'不可搜索':'未检测';
  const state=el('span',undefined,'source-state '+(src.checkStatus==='ok'?'good':src.checkStatus?'bad':''));state.append(icon(src.checkStatus==='ok'?'check':src.checkStatus?'cross':'help'));state.title=[status,src.checkError].filter(Boolean).join('：');state.setAttribute('role','img');state.setAttribute('aria-label',state.title);
  const metrics=el('div',undefined,'source-metrics'),latency=el('span',src.latencyMs?src.latencyMs+' ms':'—','source-latency');latency.title='搜索延迟';metrics.append(state,latency);
  const toggle=iconButton('power',(src.enabled?'停用 ':'启用 ')+src.name,async()=>{try{await api('sources/'+encodeURIComponent(src.id),{enabled:!src.enabled});}finally{await refresh();}});toggle.classList.toggle('is-on',src.enabled);toggle.setAttribute('aria-pressed',String(src.enabled));
  const choose=iconButton('star',src.default?'当前默认站源':src.enabled?'设为默认站源：'+src.name:'请先启用 '+src.name,async()=>{await api('sources/'+encodeURIComponent(src.id)+'/default',{});await refresh();notice('默认站源已切换为 '+src.name);});
  choose.classList.toggle('is-default',src.default);choose.setAttribute('aria-pressed',String(src.default));choose.disabled=!src.enabled||src.default;
  const actions=el('div',undefined,'source-actions');actions.append(choose,toggle);
  row.append(main,metrics,actions);root.append(row);
 });
}
if ($('refreshSources')) {setButtonIcon($('refreshSources'),'refresh','刷新站源清单');$('refreshSources').onclick=()=>act($('refreshSources'),async()=>{await api('sources/refresh',{});await refresh();notice('站源清单已刷新');});}
if ($('checkSources')) {setButtonIcon($('checkSources'),'scan','检测连通性与可搜索性');$('checkSources').onclick=()=>act($('checkSources'),async()=>{checkingSources=true;$('sourceSummary').textContent='正在检测…';try{const result=await api('sources/check',{});await refresh();notice(`检测完成：${result.available} 个可搜索，停用 ${result.disabled} 个${result.defaultSource?' · 默认 '+result.defaultSource:''}`);}finally{checkingSources=false;if(snapshot)renderSources();}});}
if ($('checkFollows')) $('checkFollows').onclick=()=>act($('checkFollows'),async()=>{await api('follows/check',{});await refresh();notice('追更检查完成，个别站源失败可在对应剧目查看');});
if ($('scanEmby')) $('scanEmby').onclick=()=>act($('scanEmby'),async()=>{await api('emby/scan',{});await refresh();notice('已安排媒体库扫描，稍后查看扫描结果');});
refresh();setInterval(refresh,4000);

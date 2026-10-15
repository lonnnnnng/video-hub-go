'use strict';

let versionInfo,updatePolling,updateStarted=0,updateRequest=false,updatePollPending=false;
function releaseDate(value){
 if(!value)return '发行时间待获取';
 const date=new Date(value);
 return Number.isNaN(date.getTime())?'发行时间待获取':new Intl.DateTimeFormat('zh-CN',{timeZone:'Asia/Shanghai',year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}).format(date).replaceAll('/','-');
}
function renderVersion(info,showState=true){
 versionInfo=info;
 $('currentVersion').textContent=info.version==='dev'?'开发版本':info.version;
 const date=$('releaseTime');date.textContent=info.version==='dev'?'尚未发行':releaseDate(info.releasedAt);date.title='发行时间（北京时间）';
 if(info.releasedAt)date.dateTime=info.releasedAt;
 const running=info.state==='running';
 $('checkUpdate').disabled=running||updateRequest;
 const apply=$('applyUpdate');apply.hidden=!info.available&&!running;
 apply.disabled=!info.supported||running||updateRequest;
 apply.textContent=running?'更新中…':'更新 '+info.latestVersion;
 apply.title=info.supported?'下载更新并重启服务':info.reason;
 const message=$('updateStatus');message.classList.toggle('update-error',info.state==='failed');
 if(showState&&(running||info.state==='failed'||info.state==='completed')){
  message.hidden=false;message.textContent=info.message;
  if(info.state==='completed'&&info.version===info.targetVersion)message.textContent='已更新至 '+info.version;
 }
 if(running)startUpdatePolling();else stopUpdatePolling();
}
function stopUpdatePolling(){if(updatePolling){clearInterval(updatePolling);updatePolling=undefined;}}
function startUpdatePolling(){
 if(updatePolling)return;
 updateStarted=Date.now();
 updatePolling=setInterval(async()=>{
  // long: 服务重启期间请求可能较慢，上一轮完成后再查询，避免浏览器堆积重复请求。
  if(updatePollPending)return;
  if(Date.now()-updateStarted>10*60*1000){stopUpdatePolling();$('updateStatus').textContent='更新等待超时，请刷新页面或检查服务日志。';$('checkUpdate').disabled=false;return;}
  updatePollPending=true;
  try{
   const info=await api('update');renderVersion(info);
   // long: 只在本页跟踪的更新完成时重载，确保新静态资源生效，历史完成记录不会引起刷新循环。
   if(info.state==='completed'&&info.version===info.targetVersion)window.location.reload();
  }
  catch{$('updateStatus').textContent='服务正在重启，等待重新连接…';}
  finally{updatePollPending=false;}
 },2500);
}
$('checkUpdate').onclick=async()=>{
 if(updateRequest||versionInfo?.state==='running')return;
 updateRequest=true;
 $('checkUpdate').disabled=true;
 const message=$('updateStatus');message.hidden=false;message.classList.remove('update-error');message.textContent='正在检测更新…';
 try{
  const info=await api('update/check',{});renderVersion(info);
  if(info.state!=='running')message.textContent=info.available?'发现 '+info.latestVersion+' · '+releaseDate(info.latestReleasedAt)+(info.supported?'': '。'+info.reason):info.version==='dev'?'开发版本，最新正式版为 '+info.latestVersion:'当前已是最新版本';
 }catch(error){message.textContent=error.message;message.classList.add('update-error');}
 finally{updateRequest=false;if(versionInfo)renderVersion(versionInfo,false);else $('checkUpdate').disabled=false;}
};
$('applyUpdate').onclick=async()=>{
 if(!versionInfo?.available||!versionInfo.supported||updateRequest)return;
 if(!confirm('更新至 '+versionInfo.latestVersion+'？服务将重启，正在下载的任务会中断并在启动后重新排队。'))return;
 updateRequest=true;renderVersion(versionInfo);
 try{
  await api('update/apply',{version:versionInfo.latestVersion});
  renderVersion({...versionInfo,state:'running',targetVersion:versionInfo.latestVersion,message:'已启动更新，正在下载并校验…'});
 }catch(error){$('updateStatus').hidden=false;$('updateStatus').textContent=error.message;$('updateStatus').classList.add('update-error');}
 finally{updateRequest=false;if(versionInfo)renderVersion(versionInfo,false);}
};
// long: 侧栏版本单独读取，站源搜索失败不会遮住版本；更新期间跨页面仍从服务端恢复进度。
api('update').then(renderVersion).catch(()=>{$('currentVersion').textContent='版本暂不可用';});
window.addEventListener('pagehide',stopUpdatePolling);

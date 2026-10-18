'use strict';

let deletingJobs=[],batchDeletion=false;
let deletionPending=false,batchPending=false;
const selectedJobIDs=new Set();
const localVideo=$('localVideo'),localDialog=$('localPlayerDialog'),deleteDialog=$('deleteJobDialog');

function stopLocalPlayback() {
 localVideo.pause();localVideo.removeAttribute('src');localVideo.load();localDialog.removeAttribute('data-job-id');
}
function playDownloaded(job) {
 stopLocalPlayback();
 $('localPlayerTitle').textContent=job.title+' / '+job.episode.name;
 $('localPlayerStatus').textContent='正在读取已下载视频…';
 localDialog.dataset.jobId=job.id;
 // long: 队列只播放服务按任务 ID 定位的已下载 MP4，不回到站源，也不把本地路径交给浏览器。
 localVideo.src='/api/jobs/'+encodeURIComponent(job.id)+'/media';
 localDialog.showModal();
 localVideo.play().catch(error=>{if(error.name!=='AbortError'&&localDialog.open&&!localVideo.error)$('localPlayerStatus').textContent='请点击播放器中的播放按钮。';});
}
function openDeleteJob(job) {
 openDeleteJobs([job],false);
}
function renderDeleteTargets() {
 $('deleteJobTitle').textContent=batchDeletion?'删除 '+deletingJobs.length+' 项任务':'删除 '+deletingJobs[0].title+' / '+deletingJobs[0].episode.name;
 $('deleteJobTargets').hidden=!batchDeletion;
 $('deleteJobTargets').replaceChildren(...deletingJobs.map(job=>el('li',job.title+' / '+job.episode.name)));
}
function openDeleteJobs(jobs,batch) {
 if(deletionPending||batchPending||!jobs.length)return;
 // long: 确认框固定本次任务清单，后台刷新和新增任务不会扩大删除范围。
 deletingJobs=[...jobs];batchDeletion=batch;
 renderDeleteTargets();
 $('deleteFiles').checked=false;
 $('deleteJobError').hidden=true;
 $('confirmDeleteJob').disabled=false;
 deleteDialog.showModal();
}
function filteredJobs() {
 const filter=$('jobFilter').value,query=$('jobSearch').value.trim().toLocaleLowerCase();
 return [...snapshot.state.jobs].reverse().filter(job=>(!filter||(filter==='active'?['queued','downloading'].includes(job.status):job.status===filter))&&(!query||(job.title+' '+job.episode.name).toLocaleLowerCase().includes(query)));
}
function selectedJobs(action) {
 return snapshot?snapshot.state.jobs.filter(job=>selectedJobIDs.has(job.id)&&(!action||(action==='pause'?['queued','downloading']:['paused','failed','canceled']).includes(job.status))):[];
}
function updateJobSelection() {
 const jobs=snapshot?filteredJobs():[],selected=selectedJobs(),visibleSelected=jobs.filter(job=>selectedJobIDs.has(job.id)).length;
 const busy=deletionPending||batchPending;
 $('jobSearch').disabled=busy;$('jobFilter').disabled=busy;$('cleanupTemporary').disabled=busy;
 const all=$('selectAllJobs');all.checked=jobs.length>0&&visibleSelected===jobs.length;all.indeterminate=visibleSelected>0&&visibleSelected<jobs.length;all.disabled=busy||!jobs.length;
 const hidden=selected.length-visibleSelected;
 $('jobSelectionCount').textContent=busy?'正在处理任务…':'已选 '+selected.length+' 项'+(hidden?'（'+hidden+' 项不在当前筛选）':'');
 $('clearJobSelection').disabled=busy||!selected.length;
 for(const [id,action,label] of [['batchPauseJobs','pause','批量暂停'],['batchResumeJobs','resume','继续 / 重试']]){
  const count=selectedJobs(action).length;
  $(id).textContent=label+(count?' ('+count+')':'');$(id).disabled=busy||!count;
 }
 $('batchDeleteJobs').disabled=busy||!selected.length;
 for(const input of document.querySelectorAll('.job-choice input')){
  input.checked=selectedJobIDs.has(input.value);input.disabled=busy;input.closest('.task').classList.toggle('task-selected',input.checked);
 }
}
function showBatchResult(results,jobs,label) {
 const failed=results.filter(result=>!result.success),root=$('jobBatchResult');root.replaceChildren();root.hidden=false;
 root.append(el('p',label+'：成功 '+(results.length-failed.length)+' 项'+(failed.length?'，失败 '+failed.length+' 项。失败任务保留选择，可重试。':'。')));
 if(failed.length){
  const names=new Map(jobs.map(job=>[job.id,job.title+' / '+job.episode.name])),list=el('ul');
  for(const result of failed)list.append(el('li',(names.get(result.id)||result.id)+'：'+result.error));
  root.append(list);
 }
}
async function runBatchAction(action) {
 if(batchPending||deletionPending)return;
 const jobs=selectedJobs(action);if(!jobs.length)return;
 batchPending=true;renderJobs();
 try{
  const {results}=await api('jobs/batch',{ids:jobs.map(job=>job.id),action});
  for(const result of results)if(result.success)selectedJobIDs.delete(result.id);
  showBatchResult(results,jobs,action==='pause'?'批量暂停':'继续 / 重试');await refresh();
 }catch(error){notice(error.message,true);}
 finally{batchPending=false;renderJobs();}
}
function formatBytes(bytes) {
 if(!Number.isFinite(bytes)||bytes<=0)return '大小未知';
 const units=['B','KB','MB','GB','TB'];let i=0;
 while(bytes>=1024&&i<units.length-1){bytes/=1024;i++;}
 return bytes.toLocaleString('zh-CN',{maximumFractionDigits:i?1:0})+' '+units[i];
}
function renderJobs() {
 if(!snapshot)return;
 const focusedChoice=document.activeElement?.matches('.job-choice input')?document.activeElement.value:undefined;
 const root=$('jobList');root.replaceChildren();
 const all=snapshot.state.jobs;
 const existing=new Set(all.map(job=>job.id));
 // long: 选择按任务 ID 保存，轮询重绘后恢复；已不存在的任务及时移除，新增任务不会自动选中。
 for(const id of selectedJobIDs)if(!existing.has(id))selectedJobIDs.delete(id);
 const jobs=filteredJobs();
 $('jobSummary').textContent=jobs.length+' / '+all.length+' 项';
 if(!jobs.length){empty(root,all.length?'没有符合筛选条件的任务':'还没有任务，搜索剧集后选择分集下载');updateJobSelection();return;}
 for(const job of jobs){
  const row=el('article',undefined,'task'),main=el('div',undefined,'task-main');
  const title=el('h3',job.title);title.title=job.title;
  const choice=el('label',undefined,'job-choice'),checkbox=el('input');checkbox.type='checkbox';checkbox.value=job.id;checkbox.setAttribute('aria-label','选择 '+job.title+' / '+job.episode.name);
  checkbox.onchange=()=>{if(checkbox.checked)selectedJobIDs.add(job.id);else selectedJobIDs.delete(job.id);updateJobSelection();};choice.append(checkbox);
  const heading=el('div',undefined,'task-heading');heading.append(choice,title);
  main.append(heading,el('p',job.episode.name+' · 第 '+job.season+' 季'+(job.attempts>1?' · 尝试 '+job.attempts+' 次':''),'task-meta'));
  const metrics=el('div',undefined,'task-metrics');
  if(job.status==='downloading'){
   const merging=job.progress>=0.95&&!job.speed;
   const line=el('div',undefined,'task-transfer');
   line.append(el('strong',merging?'合并与校验中':job.speed||'等待响应…'),el('span',Math.round(job.progress*100)+'%'));
   const progress=el('progress');progress.max=1;progress.value=job.progress;progress.setAttribute('aria-label',job.title+' '+job.episode.name+' 下载进度');
   metrics.append(line,progress);
  }else if(job.status==='completed'){
   const size=el('strong',formatBytes(job.sizeBytes),'task-size');size.title=job.output||'已下载文件';metrics.append(size);
   const date=new Date(job.completedAt),valid=Number.isFinite(date.getTime())&&date.getFullYear()>1;
   const completed=el('time',valid?'完成 '+new Intl.DateTimeFormat('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}).format(date).replaceAll('/','-'):'完成时间未知','task-completed');
   if(valid){completed.dateTime=job.completedAt;completed.title='MP4 合成完成时间（当前时区）';}
   metrics.append(completed);
   const media=job.embyMedia||{},enabled=snapshot.state.settings.embyEnabled;
   const state=enabled?(media.state==='confirmed'?'Emby 已识别':media.state==='error'?'Emby 入库待处理':'等待 Emby 扫描'):'Emby 自动扫描未启用';
   metrics.append(el('span',state,media.state==='confirmed'&&enabled?'good':'task-wait'));
  }else if(job.status==='queued'&&new Date(job.nextAttempt)>new Date()){
   metrics.append(el('span',new Date(job.nextAttempt).toLocaleTimeString('zh-CN')+' 后重试','task-wait'));
  }
  const actions=el('div',undefined,'actions task-actions');
  const action=(name,cmd)=>actions.append(button(name,async()=>{await api('jobs/'+encodeURIComponent(job.id)+'/'+cmd,{});await refresh();}));
  if(job.status==='completed')actions.append(button('播放',()=>playDownloaded(job)));
  if(['queued','downloading'].includes(job.status))action('暂停','pause');
  if(['paused','failed','canceled'].includes(job.status))action(job.status==='paused'?'继续':'重试','resume');
  if(['queued','paused','failed','canceled'].includes(job.status))actions.append(button(job.status==='queued'?'调整线路/季号':'换线路重试',()=>openConfigureJob(job)));
  if(!['completed','canceled'].includes(job.status))action('取消','cancel');
  const remove=iconButton('trash','删除 '+job.title+' / '+job.episode.name,()=>openDeleteJob(job));remove.classList.add('danger-button');actions.append(remove);
  for(const control of actions.children)control.disabled=deletionPending||batchPending;
  row.append(main,metrics,el('span',job.status==='queued'&&job.waitingForSpace?'等待磁盘空间':labels[job.status]||job.status,'badge '+(job.status==='completed'?'good':job.status==='failed'?'bad':'')),actions);
  if(job.error){const error=el('p',job.error,'error-text task-error');row.append(error);}
  if(job.cleanupError)row.append(el('p','临时清理：'+job.cleanupError,'error-text task-error'));
  if(snapshot.state.settings.embyEnabled&&job.embyMedia?.error)row.append(el('p',job.embyMedia.error,'error-text task-error'));
  root.append(row);
 }
 updateJobSelection();
 // long: 自动刷新替换任务行后恢复正在使用的复选框焦点，让键盘连续选择不会被轮询打断。
 if(focusedChoice)Array.from(root.querySelectorAll('.job-choice input')).find(input=>input.value===focusedChoice)?.focus({preventScroll:true});
}
function changeJobFilter() {
 // long: 手动切换筛选清空选择，避免用户批量删除上一筛选中已隐藏的任务。
 selectedJobIDs.clear();$('jobBatchResult').hidden=true;renderJobs();
}
$('jobFilter').onchange=changeJobFilter;
$('jobSearch').oninput=changeJobFilter;
$('selectAllJobs').onchange=()=>{for(const job of filteredJobs()){if($('selectAllJobs').checked)selectedJobIDs.add(job.id);else selectedJobIDs.delete(job.id);}updateJobSelection();};
$('clearJobSelection').onclick=()=>{selectedJobIDs.clear();updateJobSelection();};
$('batchPauseJobs').onclick=()=>runBatchAction('pause');
$('batchResumeJobs').onclick=()=>runBatchAction('resume');
$('batchDeleteJobs').onclick=()=>openDeleteJobs(selectedJobs(),true);
let configuringJob,configureVideo,configureSequence=0,configurationPending=false;
function renderConfigureEpisodes(){
 const line=configureVideo.lines.find(line=>line.id===$('configureLine').value);
 const episodes=line?.episodes||[];
 $('configureEpisode').replaceChildren(...episodes.map(ep=>{const option=el('option',ep.name);option.value=ep.key;return option;}));
 // long: 标签不同的线路不能自动认定为同一集，保留空选择让用户核对；稳定集名相同时才预选。
 $('configureEpisode').value=episodes.some(ep=>ep.key===configuringJob.episode.key)?configuringJob.episode.key:'';
}
async function openConfigureJob(job){
 const sequence=++configureSequence;
 const video=await api('detail?'+new URLSearchParams({sourceId:job.sourceId,id:job.videoId}));
 if(sequence!==configureSequence)return;
 configuringJob=job;configureVideo=video;
 $('configureJobTitle').textContent=job.status==='queued'?'调整线路和季号':'换线路重试';
 $('configureJobOriginal').textContent=job.title+' / '+job.episode.name+' · 原第 '+job.season+' 季';
 $('configureSeason').value=job.season;
 $('configureLine').replaceChildren(...video.lines.map(line=>{const option=el('option',line.name);option.value=line.id;return option;}));
 $('configureLine').value=video.lines.some(line=>line.id===job.lineId)?job.lineId:video.lines[0]?.id||'';
 renderConfigureEpisodes();$('configureJobError').hidden=true;
 $('saveConfigureJob').textContent=job.status==='queued'?'保存调整':'保存并重试';
 $('configureJobDialog').showModal();
}
$('configureLine').onchange=renderConfigureEpisodes;
$('cancelConfigureJob').onclick=()=>{if(!configurationPending)$('configureJobDialog').close();};
$('configureJobDialog').addEventListener('cancel',event=>{if(configurationPending)event.preventDefault();});
$('saveConfigureJob').onclick=()=>act($('saveConfigureJob'),async()=>{
 const season=Number($('configureSeason').value);
 $('configureJobError').hidden=true;configurationPending=true;
 try{
  if(!Number.isInteger(season)||season<1||season>999)throw new Error('季号必须为 1–999 的整数');
  if(!$('configureEpisode').value)throw new Error('请选择新线路中对应的分集');
  await api('jobs/'+encodeURIComponent(configuringJob.id)+'/reconfigure',{lineId:$('configureLine').value,episodeKey:$('configureEpisode').value,season,retry:configuringJob.status!=='queued',expectedUpdatedAt:configuringJob.updatedAt});
  $('configureJobDialog').close();await refresh();notice('任务线路和季号已更新');
 }catch(error){$('configureJobError').textContent=error.message;$('configureJobError').hidden=false;}
 finally{configurationPending=false;}
});
$('cleanupTemporary').onclick=()=>act($('cleanupTemporary'),async()=>{
 if(!window.confirm('清理已完成任务的临时分片？程序会先校验正式 MP4，保留视频、NFO、海报和任务记录。'))return;
 await api('jobs/cleanup',{});notice('临时清理已安排，校验及清理将在后台执行。');
});
$('closeLocalPlayer').onclick=()=>localDialog.close();
localDialog.addEventListener('close',stopLocalPlayback);
window.addEventListener('pagehide',stopLocalPlayback);
localVideo.addEventListener('playing',()=>{$('localPlayerStatus').textContent='正在播放已下载视频';});
localVideo.addEventListener('waiting',()=>{if(localDialog.open)$('localPlayerStatus').textContent='正在读取视频…';});
localVideo.addEventListener('error',()=>{if(localDialog.open)$('localPlayerStatus').textContent='文件不存在、无法读取或浏览器不支持其编码。';});
localVideo.addEventListener('ended',()=>{$('localPlayerStatus').textContent='播放结束';});
$('cancelDeleteJob').onclick=()=>deleteDialog.close();
deleteDialog.addEventListener('cancel',event=>{if(deletionPending)event.preventDefault();});
$('confirmDeleteJob').onclick=()=>act($('confirmDeleteJob'),async()=>{
 if(!deletingJobs.length||deletionPending)return;
 const jobs=[...deletingJobs],removeFiles=$('deleteFiles').checked;
 deletionPending=true;$('cancelDeleteJob').disabled=true;$('deleteFiles').disabled=true;
 $('deleteJobError').hidden=true;$('confirmDeleteJob').textContent='正在删除…';renderJobs();
 if(jobs.some(job=>localDialog.dataset.jobId===job.id))localDialog.close();
 try {
  const action=removeFiles?'remove-files':'remove';
  const {results}=batchDeletion?await api('jobs/batch',{ids:jobs.map(job=>job.id),action}):await api('jobs/'+encodeURIComponent(jobs[0].id)+'/'+action,{}).then(()=>({results:[{id:jobs[0].id,success:true}]}));
  for(const result of results)if(result.success)selectedJobIDs.delete(result.id);
  const failed=results.filter(result=>!result.success),succeeded=results.length-failed.length;
  if(batchDeletion)showBatchResult(results,jobs,removeFiles?'删除任务及文件':'删除任务（保留文件）');
  if(failed.length){
   const errors=new Map(failed.map(result=>[result.id,result.error]));deletingJobs=jobs.filter(job=>errors.has(job.id));renderDeleteTargets();
   $('deleteJobError').textContent='已删除 '+succeeded+' 项，失败 '+failed.length+' 项。可重试剩余任务。\n'+deletingJobs.map(job=>job.title+' / '+job.episode.name+'：'+errors.get(job.id)).join('\n');$('deleteJobError').hidden=false;
  }else{deleteDialog.close();notice('已删除 '+succeeded+' 项任务，'+(removeFiles?'文件已清理':'文件已保留'));}
  await refresh();
 }catch(error){$('deleteJobError').textContent=error.message;$('deleteJobError').hidden=false;await refresh();}
 finally{deletionPending=false;$('cancelDeleteJob').disabled=false;$('deleteFiles').disabled=false;$('confirmDeleteJob').textContent='确认删除';renderJobs();}
});

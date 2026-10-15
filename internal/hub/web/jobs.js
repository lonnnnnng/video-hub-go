'use strict';

let deletingJob;
let deletionPending=false;
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
 if(deletionPending)return;
 deletingJob=job;
 $('deleteJobTitle').textContent='删除 '+job.title+' / '+job.episode.name;
 $('deleteFiles').checked=false;
 $('deleteJobError').hidden=true;
 $('confirmDeleteJob').disabled=false;
 deleteDialog.showModal();
}
function formatBytes(bytes) {
 if(!Number.isFinite(bytes)||bytes<=0)return '大小未知';
 const units=['B','KB','MB','GB','TB'];let i=0;
 while(bytes>=1024&&i<units.length-1){bytes/=1024;i++;}
 return bytes.toLocaleString('zh-CN',{maximumFractionDigits:i?1:0})+' '+units[i];
}
function renderJobs() {
 if(!snapshot)return;
 const root=$('jobList');root.replaceChildren();
 const filter=$('jobFilter').value,query=$('jobSearch').value.trim().toLocaleLowerCase();
 const all=snapshot.state.jobs;
 const jobs=[...all].reverse().filter(job=>(!filter||(filter==='active'?['queued','downloading'].includes(job.status):job.status===filter))&&(!query||(job.title+' '+job.episode.name).toLocaleLowerCase().includes(query)));
 $('jobSummary').textContent=jobs.length+' / '+all.length+' 项';
 if(!jobs.length){empty(root,all.length?'没有符合筛选条件的任务':'还没有任务，搜索剧集后选择分集下载');return;}
 for(const job of jobs){
  const row=el('article',undefined,'task'),main=el('div',undefined,'task-main');
  const title=el('h3',job.title);title.title=job.title;
  main.append(title,el('p',job.episode.name+' · 第 '+job.season+' 季'+(job.attempts>1?' · 尝试 '+job.attempts+' 次':''),'task-meta'));
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
  }else if(job.status==='queued'&&new Date(job.nextAttempt)>new Date()){
   metrics.append(el('span',new Date(job.nextAttempt).toLocaleTimeString('zh-CN')+' 后重试','task-wait'));
  }
  const actions=el('div',undefined,'actions task-actions');
  const action=(name,cmd)=>actions.append(button(name,async()=>{await api('jobs/'+encodeURIComponent(job.id)+'/'+cmd,{});await refresh();}));
  if(job.status==='completed')actions.append(button('播放',()=>playDownloaded(job)));
  if(['queued','downloading'].includes(job.status))action('暂停','pause');
  if(['paused','failed','canceled'].includes(job.status))action(job.status==='paused'?'继续':'重试','resume');
  if(!['completed','canceled'].includes(job.status))action('取消','cancel');
  const remove=iconButton('trash','删除 '+job.title+' / '+job.episode.name,()=>openDeleteJob(job));remove.classList.add('danger-button');actions.append(remove);
  row.append(main,metrics,el('span',labels[job.status]||job.status,'badge '+(job.status==='completed'?'good':job.status==='failed'?'bad':'')),actions);
  if(job.error){const error=el('p',job.error,'error-text task-error');row.append(error);}
  root.append(row);
 }
}
$('jobFilter').onchange=renderJobs;
$('jobSearch').oninput=renderJobs;
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
 if(!deletingJob)return;
 const job=deletingJob,removeFiles=$('deleteFiles').checked;
 deletionPending=true;$('cancelDeleteJob').disabled=true;$('deleteFiles').disabled=true;
 $('deleteJobError').hidden=true;
 if(localDialog.dataset.jobId===job.id)localDialog.close();
 try {
  await api('jobs/'+encodeURIComponent(job.id)+'/'+(removeFiles?'remove-files':'remove'),{});
  deleteDialog.close();await refresh();notice(removeFiles?'任务及文件已删除':'任务已删除，文件已保留');
 }catch(error){$('deleteJobError').textContent=error.message;$('deleteJobError').hidden=false;await refresh();}
 finally{deletionPending=false;$('cancelDeleteJob').disabled=false;$('deleteFiles').disabled=false;}
});

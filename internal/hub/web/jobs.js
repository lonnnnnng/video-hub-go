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
function renderJobs() {
 const root=$('jobList');root.replaceChildren();
 const filter=$('jobFilter').value;
 const jobs=[...snapshot.state.jobs].reverse().filter(job=>!filter||(filter==='active'?['queued','downloading'].includes(job.status):job.status===filter));
 if(!jobs.length){empty(root,'当前没有任务，搜索剧集后选择分集下载');return;}
 for(const job of jobs){
  const row=el('article',undefined,'task'),main=el('div',undefined,'task-main');
  main.append(el('h3',job.title+' / '+job.episode.name),el('p','第 '+job.season+' 季 / 已尝试 '+job.attempts+' 次'+(job.speed?' / '+job.speed:'')));
  if(job.status==='downloading'){
   const progress=el('progress');progress.max=1;progress.value=job.progress;progress.setAttribute('aria-label','下载进度');
   main.append(progress,el('small',Math.round(job.progress*100)+'%（当前轨道）'));
  }
  if(job.error)main.append(el('p',job.error,'error-text'));
  if(job.output)main.append(el('small',job.output));
  if(job.status==='queued'&&!job.nextAttempt.startsWith('0001')&&new Date(job.nextAttempt)>new Date())main.append(el('small','下次尝试：'+new Date(job.nextAttempt).toLocaleTimeString()));
  const actions=el('div',undefined,'actions');
  const action=(name,cmd)=>actions.append(button(name,async()=>{await api('jobs/'+encodeURIComponent(job.id)+'/'+cmd,{});await refresh();}));
  if(job.status==='completed')actions.append(button('播放',()=>playDownloaded(job)));
  if(['queued','downloading'].includes(job.status))action('暂停','pause');
  if(['paused','failed','canceled'].includes(job.status))action('重试','resume');
  if(!['completed','canceled'].includes(job.status))action('取消','cancel');
  const remove=iconButton('trash','删除 '+job.title+' / '+job.episode.name,()=>openDeleteJob(job));remove.classList.add('danger-button');actions.append(remove);
  row.append(main,el('span',labels[job.status]||job.status,'badge '+(job.status==='completed'?'good':job.status==='failed'?'bad':'')),actions);root.append(row);
 }
}
$('jobFilter').onchange=renderJobs;
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

'use strict';

let detailLine, detailSeason = 1, playingEpisode, hlsPlayer;
let playbackActive = false;
let playbackSequence = 0, detailSequence = 0;
const previewVideo = $('previewVideo');

function inferSeason(title) {
 const match = title.match(/第\s*([0-9一二三四五六七八九十百零〇两]+)\s*季/i) || title.match(/\bS(\d{1,3})(?:\b|E\d)/i);
 if (!match) return 1;
 if (/^\d+$/.test(match[1])) return Math.min(999, Math.max(1, Number(match[1])));
 const digits = {零:0,〇:0,一:1,二:2,两:2,三:3,四:4,五:5,六:6,七:7,八:8,九:9};
 let result = 0, digit = 0;
 for (const char of match[1]) {
  if (char === '十' || char === '百') { result += (digit || 1) * (char === '百' ? 100 : 10); digit = 0; }
  else digit = digits[char] || 0;
 }
 return Math.min(999, Math.max(1, result + digit));
}

function setPlayerStatus(message, error = false) {
 $('playerStatus').textContent = message;
 $('playerStatus').classList.toggle('error-text', error);
 $('retryPlayback').hidden = !error || !playingEpisode;
}

function stopPreview() {
 playbackSequence++;
 playbackActive = false;
 // long: 切集或关闭详情时取消浏览器与站源的连接，预览流量始终不经过服务端。
 if (hlsPlayer) { hlsPlayer.destroy(); hlsPlayer = null; }
 previewVideo.pause();
 previewVideo.removeAttribute('src');
 previewVideo.load();
}

function highlightPlaying() {
 $('episodes').querySelectorAll('.episode-play').forEach(node => {
  const active = node.dataset.key === playingEpisode?.key;
  node.classList.toggle('playing', active);
  node.setAttribute('aria-pressed', String(active));
 });
 const index = detailLine?.episodes.findIndex(ep => ep.key === playingEpisode?.key) ?? -1;
 $('previousEpisode').disabled = index <= 0;
 $('nextEpisode').disabled = index < 0 || index >= detailLine.episodes.length - 1;
}

function playEpisode(episode) {
 if (!episode) return;
 stopPreview();
 const request = playbackSequence;
 playingEpisode = episode;
 $('playingEpisode').textContent = episode.name;
 $('playerEmpty').hidden = true;
 highlightPlaying();
 setPlayerStatus('正在连接片单…');
 try {
  let address;
  try { address = new URL(episode.url); }
  catch { throw new Error('该分集没有有效的 HTTP/HTTPS 播放地址。'); }
  if (!['http:','https:'].includes(address.protocol)) throw new Error('该分集没有有效的 HTTP/HTTPS 播放地址。');
  playbackActive = true;
  const start = () => {
   if (request !== playbackSequence || !$('detailDialog').open) return;
   previewVideo.play().catch(error => {
   if (request === playbackSequence && error.name !== 'AbortError') setPlayerStatus('已准备好，请点击播放器中的播放按钮。');
   });
  };
  if (window.Hls?.isSupported()) {
   hlsPlayer = new Hls({enableWorker:true,maxBufferLength:20,maxMaxBufferLength:40});
   hlsPlayer.on(Hls.Events.MANIFEST_PARSED, start);
   hlsPlayer.on(Hls.Events.ERROR, (_, data) => {
    if (!data.fatal || request !== playbackSequence) return;
    const message = data.type === Hls.ErrorTypes.MEDIA_ERROR ? '当前浏览器无法解码这条视频，请更换站源。' : '片单或分片直连失败，可能是跨域、网络或站源限制，请重试或更换站源。';
    playbackActive = false;
    setPlayerStatus(message, true);
    hlsPlayer?.destroy(); hlsPlayer = null;
   });
   hlsPlayer.attachMedia(previewVideo);
   // long: 直接使用详情中的原始分集地址，让浏览器获取片单、分片和密钥，不回退到服务端转发。
   hlsPlayer.loadSource(episode.url);
  } else if (previewVideo.canPlayType('application/vnd.apple.mpegurl')) {
   previewVideo.src = episode.url;
   start();
  } else {
   setPlayerStatus('当前浏览器不支持 HLS 播放，请使用新版 Chrome、Edge 或 Safari。', true);
  }
 } catch (error) {
  playbackActive = false;
  if (request === playbackSequence) setPlayerStatus(error.message, true);
 }
}

function updateSelection() {
 const boxes = [...$('episodes').querySelectorAll('input[type="checkbox"]')];
 const count = boxes.filter(box => box.checked).length;
 $('selectionCount').textContent = '已选 ' + count + ' 集';
 $('queueEpisodes').disabled = !count;
 $('selectAll').textContent = count && count === boxes.length ? '清空选择' : '全选下载';
}

function renderEpisodes() {
 const root = $('episodes'); root.replaceChildren();
 const episodes = detailLine?.episodes || [];
 $('episodeHint').textContent = episodes.length + ' 集';
 $('addFollow').disabled = !episodes.length;
 $('selectAll').disabled = !episodes.length;
 $('startPlayback').disabled = !episodes.length;
 $('startPlayback').textContent = episodes.length ? '播放 ' + episodes[0].name : '暂无可播放分集';
 if (!episodes.length) { empty(root, '该站源没有返回可播放的 HLS 分集。'); updateSelection(); return; }
 for (const episode of episodes) {
  const row = el('div', undefined, 'episode');
  const choice = el('label', undefined, 'episode-choice');
  const box = el('input'); box.type = 'checkbox'; box.value = episode.key;
  box.setAttribute('aria-label', '下载 ' + episode.name);
  box.onchange = updateSelection;
  choice.append(box);
  const play = button(episode.name, () => playEpisode(episode));
  play.className = 'episode-play'; play.dataset.key = episode.key;
  play.setAttribute('aria-label', '播放 ' + episode.name); play.setAttribute('aria-pressed','false');
  row.append(choice, play); root.append(row);
 }
 updateSelection();
}

async function openDetail(video) {
 const request = ++detailSequence;
 const result = await api('detail?' + new URLSearchParams({sourceId:video.sourceId,id:video.id}));
 if (request !== detailSequence) return;
 stopPreview();
 detail = result;
 // long: 剧名推断只作为季号初值，用户可以核对线路和季号后再创建下载或追更。
 detailLine = detail.lines.find(line => line.id === video.lineId) || detail.lines[0];
 detailSeason = video.season || inferSeason(detail.title);
 $('detailSeason').value = detailSeason;
 $('detailLine').replaceChildren(...detail.lines.map(line => { const option=el('option',line.name);option.value=line.id;return option; }));
 $('detailLine').value = detailLine?.id || '';
 playingEpisode = null;
 $('detailTitle').textContent = detail.title;
 $('detailMeta').textContent = [detail.sourceName,detail.year,detail.kind,detail.remarks].filter(Boolean).join(' / ');
 $('detailPlot').textContent = detail.plot;
 $('plotDisclosure').hidden = !detail.plot;
 $('plotDisclosure').open = false;
 $('playerEmpty').hidden = false;
 $('playingEpisode').textContent = '选择分集开始播放';
 setPlayerStatus('点击分集名称播放，勾选方框加入下载。');
 $('previousEpisode').disabled = $('nextEpisode').disabled = true;
 $('backfill').checked = false; $('autoDownload').checked = true;
 $('detailActionStatus').hidden = true;
 renderEpisodes();
 if (!$('detailDialog').open) $('detailDialog').showModal();
}

function selection() {
 const season=Number($('detailSeason').value);
 if(!Number.isInteger(season)||season<1||season>999)throw new Error('季号必须为 1–999 的整数');
 return {sourceId:detail.sourceId,videoId:detail.id,lineId:detailLine?.id || '',season,keys:[...$('episodes').querySelectorAll('input:checked')].map(node => node.value),autoDownload:$('autoDownload').checked,backfill:$('backfill').checked};
}

$('detailLine').onchange=()=>{stopPreview();playingEpisode=null;detailLine=detail.lines.find(line=>line.id===$('detailLine').value);renderEpisodes();highlightPlaying();$('playerEmpty').hidden=false;setPlayerStatus('线路已切换，请重新选择分集。');};

async function detailAction(node, action) {
 node.disabled = true; $('detailActionStatus').hidden = true;
 try { await action(); }
 catch(error) { $('detailActionStatus').textContent = error.message; $('detailActionStatus').classList.add('error-text'); $('detailActionStatus').hidden = false; }
 finally { node.disabled = false; updateSelection(); }
}

$('closeDetail').onclick = () => $('detailDialog').close();
$('detailDialog').addEventListener('close', stopPreview);
window.addEventListener('pagehide', stopPreview);
$('startPlayback').onclick = () => playEpisode(detailLine?.episodes[0]);
$('retryPlayback').onclick = () => playEpisode(playingEpisode);
for (const [id, offset] of [['previousEpisode',-1],['nextEpisode',1]]) {
 $(id).onclick = () => {
  const index = detailLine.episodes.findIndex(episode => episode.key === playingEpisode?.key);
  playEpisode(detailLine.episodes[index + offset]);
 };
}
previewVideo.addEventListener('playing', () => { if (playbackActive) setPlayerStatus('正在播放'); });
previewVideo.addEventListener('waiting', () => { if (playbackActive) setPlayerStatus('正在缓冲…'); });
previewVideo.addEventListener('error', () => { if (playbackActive) { playbackActive = false; setPlayerStatus('视频直连加载或解码失败，请重试或更换站源。', true); } });
previewVideo.addEventListener('ended', () => { if (playbackActive) setPlayerStatus('本集播放结束，可选择下一集。'); });
$('selectAll').onclick = () => {
 const boxes = [...$('episodes').querySelectorAll('input')], all = boxes.every(node => node.checked);
 boxes.forEach(node => { node.checked = !all; }); updateSelection();
};
$('queueEpisodes').onclick = () => detailAction($('queueEpisodes'), async () => {
 const chosen = selection();
 if (!chosen.keys.length) throw new Error('请先勾选要下载的分集');
 const result = await api('jobs', chosen);
 $('detailDialog').close(); navigate('/jobs.html', '已加入 ' + result.queued + ' 集；已有任务会自动去重');
});
$('addFollow').onclick = () => detailAction($('addFollow'), async () => {
 await api('follows', selection()); $('detailDialog').close(); navigate('/follows.html', '已加入追更');
});

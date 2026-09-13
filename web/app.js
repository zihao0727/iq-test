'use strict';
const $ = (s) => document.querySelector(s);
const labels = {pass:'通过',fail:'未通过',error:'请求失败',running:'检测中',generated:'已生成'};
let state = null, view = 'overview', kind = 'all', artLimit = 4, historyLimit = 30, offset = 0, loading = false;
const fmt = (date, full = false) => new Date(date).toLocaleString('zh-CN', {timeZone:'Asia/Shanghai', month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit', ...(full ? {second:'2-digit'} : {}), hour12:false});
function el(tag, className, text) { const n=document.createElement(tag); if(className)n.className=className; if(text!==undefined)n.textContent=text; return n; }
function badge(status) { return el('span',`badge ${status}`,labels[status]||status); }
function iconButton(label, click) {
  const b=el('button','icon-button'); b.title=label; b.setAttribute('aria-label',label);
  const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');
  const use=document.createElementNS('http://www.w3.org/2000/svg','use');
  use.setAttribute('href','/icons.svg#arrow-up-right'); svg.append(use); b.append(svg); b.onclick=click; return b;
}
function updateCountdown() {
  if(!state)return;
  const remaining=Math.max(0, Math.ceil((new Date(state.next).getTime()-Date.now()-offset)/1000));
  $('#countdown').textContent=!state.configured?'--:--':state.running?'检测中':`${String(Math.floor(remaining/60)).padStart(2,'0')}:${String(remaining%60).padStart(2,'0')}`;
}
function render() {
  if(!state)return;
  const now=Date.now()+offset, recent=state.records.filter(r=>now-new Date(r.started).getTime()<86400000);
  const candy=recent.filter(r=>r.kind==='candy'), valid=candy.filter(r=>['pass','fail'].includes(r.status)), passes=valid.filter(r=>r.status==='pass').length;
  const accuracy=valid.length?Math.round(passes/valid.length*100):0;
  $('#accuracy').textContent=valid.length?`${accuracy}%`:'—';
  $('#progress').value=accuracy;
  $('#accuracy-note').textContent=valid.length?`${passes} / ${valid.length} 个有效回答通过`:'等待有效样本';
  $('#total').replaceChildren(document.createTextNode(String(candy.length)),el('small','','轮'));
  const failed=recent.filter(r=>r.status==='fail').length, errors=recent.filter(r=>r.status==='error').length;
  $('#abnormal').replaceChildren(document.createTextNode(String(failed+errors)),el('small','','项'));
  $('#error-note').textContent=`未通过 ${failed} · 请求失败 ${errors}`;
  $('#schedule-note').textContent=state.configured?'每 10 分钟自动运行':'等待服务端配置 API_KEY';
  $('#config-status').textContent=state.configured?'自动巡检已启用':'等待配置';
  $('#config-dot').className=`dot ${state.configured?'':'off'}`;
  $('#api-site').textContent=new URL(state.endpoint).host;
  $('#run-button').disabled=!state.configured || !state.manualEnabled || state.running;
  $('#run-button').title=!state.manualEnabled?'服务端配置 ADMIN_TOKEN 后可手动运行':state.running?'检测正在进行':'管理员手动运行';
  $('#last-time').textContent=state.records.length?fmt(state.records[0].started):'尚无记录';
  $('#next-time').textContent=state.configured?fmt(state.next):'等待配置';
  const notice=state.storageError?'默认巡检记录保存异常。':!state.configured?'默认巡检暂未启用，访客自定义检测不受影响。':'';
  $('#notice').textContent=notice; $('#notice').hidden=!notice;
  const last=state.records.find(r=>r.kind==='candy');
  $('#latest-status').className=`badge ${last?.status||''}`;
  $('#latest-status').textContent=last?labels[last.status]:state.configured?'等待首轮结果':'等待配置';
  $('#latest-text').textContent=last?`${fmt(last.started)} · ${labels[last.status]}${last.durationMs?' · '+(last.durationMs/1000).toFixed(1)+'s':''}`:'尚无检测记录';
  $('#latest-button').disabled=!last;
  $('#latest-button').onclick=()=>last&&showDetail(last.id);
  $('#timeline').replaceChildren();
  const slot=600000, edge=Math.floor(now/slot)*slot;
  for(let i=0;i<144;i++){
    const start=edge-(143-i)*slot;
    const r=candy.find(r=>{const t=new Date(r.started).getTime();return t>=start&&t<start+slot;});
    const b=el('button',r?.status||'');
    b.title=`${fmt(start)} · ${r?labels[r.status]:'无数据'}`;
    b.setAttribute('aria-label',b.title);
    if(r)b.onclick=()=>showDetail(r.id);
    $('#timeline').append(b);
  }
  renderGallery(); renderHistory(); updateCountdown();
}
let gallerySignature='';
function renderGallery(){
  const arts=state.records.filter(r=>r.kind==='pelican'&&r.status==='generated');
  $('#art-count').textContent=`${arts.length} 幅作品`;
  $('#art-empty').hidden=arts.length>0;
  $('#more-art').hidden=arts.length<=artLimit;
  const shown=arts.slice(0,artLimit), signature=shown.map(r=>r.id).join(',');
  if(signature===gallerySignature)return;
  gallerySignature=signature; $('#gallery').replaceChildren();
  shown.forEach(r=>{
    const card=el('article','artwork'), frame=el('iframe');
    frame.setAttribute('sandbox','allow-scripts allow-same-origin'); frame.setAttribute('scrolling','no'); frame.loading='eager'; frame.title=`鹈鹕骑行 ${fmt(r.started)}`; frame.src=`/art/${r.id}`;
    frame.addEventListener('load',()=>{fitPreview(frame);setTimeout(()=>fitPreview(frame),120);});
    const foot=el('div','artwork-footer'), info=el('div');
    info.append(el('strong','',`鹈鹕骑行 · ${r.id.slice(0,6)}`),el('span','',`${fmt(r.started)} · ${(r.durationMs/1000).toFixed(1)}s`));
    foot.append(info,iconButton('查看作品与原始输出',()=>showDetail(r.id))); card.append(frame,foot); $('#gallery').append(card);
  });
}
function renderHistory(){
  const rows=state.records.filter(r=>(kind==='all'||r.kind===kind)&&($('#status-filter').value==='all'||r.status===$('#status-filter').value));
  $('#history-count').textContent=`${rows.length} 条记录`; $('#history-empty').hidden=rows.length>0; $('#more-history').hidden=rows.length<=historyLimit;
  $('#history-rows').replaceChildren();
  rows.slice(0,historyLimit).forEach(r=>{
    const tr=el('tr');
    tr.append(el('td','',fmt(r.started,true)),el('td','',r.kind==='candy'?'糖果题':'鹈鹕动画'));
    const status=el('td'); status.append(badge(r.status)); tr.append(status);
    tr.append(el('td','',r.durationMs?`${(r.durationMs/1000).toFixed(1)}s`:'—'),el('td','',r.outputTokens?String(r.outputTokens):'—'));
    const action=el('td'); action.append(iconButton('查看检测详情',()=>showDetail(r.id))); tr.append(action); $('#history-rows').append(tr);
  });
}
async function showDetail(id){
  const dialog=$('#detail-dialog'); $('#detail-title').textContent='检测详情'; $('#detail-body').replaceChildren(el('p','','正在读取…')); dialog.showModal();
  try {
    const response=await fetch(`/api/records/${id}`); if(!response.ok)throw new Error('记录不存在或已过期');
    const r=await response.json(); $('#detail-title').textContent=r.kind==='candy'?'黑袋糖果题':'鹈鹕骑行';
    const body=$('#detail-body');body.replaceChildren();
    const meta=el('div','detail-meta');meta.append(badge(r.status),el('span','',fmt(r.started,true)),el('span','',`${(r.durationMs/1000).toFixed(1)}s`),el('span','',`${r.outputTokens} output tokens`));body.append(meta);
    if(r.error)body.append(el('p','',r.error));
    if(r.kind==='pelican'&&r.status==='generated'){const frame=el('iframe');frame.setAttribute('sandbox','allow-scripts allow-same-origin');frame.setAttribute('scrolling','no');frame.title='鹈鹕动画隔离预览';frame.src=`/art/${r.id}`;frame.addEventListener('load',()=>fitPreview(frame));body.append(frame);}
    body.append(el('h3','','原始输出'),el('pre','',r.output||'尚无文本输出'));
  }catch(e){$('#detail-body').replaceChildren(el('p','',e.message));}
}
function fitPreview(frame){
  // The sandboxed document handles its own scaling in preview.js.
  frame.style.overflow='hidden';
}
async function sync(){
  if(loading)return;loading=true;
  try{
    const resp=await fetch('/api/status',{cache:'no-store'});if(!resp.ok)throw new Error('status');
    state=await resp.json();offset=new Date(state.serverTime).getTime()-Date.now();
    $('#connection').textContent='实时连接';$('#connection-dot').className='dot';render();
  }catch{
    $('#connection').textContent='连接中断';$('#connection-dot').className='dot bad';
    $('#notice').hidden=false;$('#notice').textContent='无法连接检测服务，当前数据可能已过期。请检查服务是否运行，稍后自动重试。';
  }finally{loading=false;}
}
document.querySelectorAll('[data-view]').forEach(b=>b.onclick=()=>{
  view=b.dataset.view;document.querySelectorAll('[data-view]').forEach(n=>{if(n===b)n.setAttribute('aria-current','page');else n.removeAttribute('aria-current');});
  $('#overview-view').hidden=view!=='overview';$('#gallery-view').hidden=view==='history';$('#history-view').hidden=view!=='history';
  $('#page-title').replaceChildren(document.createTextNode({overview:'模型观测台',gallery:'动画作品集',history:'检测运行记录'}[view]),el('span','title-period','.'));
  if(view==='gallery'){artLimit=12;if(state)renderGallery();}
});
document.querySelectorAll('[data-kind]').forEach(b=>b.onclick=()=>{kind=b.dataset.kind;historyLimit=30;document.querySelectorAll('[data-kind]').forEach(n=>n.setAttribute('aria-pressed',String(n===b)));if(state)renderHistory();});
$('#status-filter').onchange=()=>{historyLimit=30;if(state)renderHistory();};
$('#more-history').onclick=()=>{historyLimit+=30;renderHistory();};
$('#more-art').onclick=()=>{artLimit+=8;renderGallery();};
$('#refresh').onclick=sync;$('#method-button').onclick=()=>$('#method-dialog').showModal();
$('#run-button').onclick=()=>{$('#admin-token').value='';$('#run-error').textContent='';$('#run-dialog').showModal();};
document.querySelectorAll('.close-dialog').forEach(b=>b.onclick=()=>b.closest('dialog').close());
$('#detail-dialog').addEventListener('close',()=>$('#detail-body').replaceChildren());
$('#run-dialog').addEventListener('close',()=>{$('#admin-token').value='';});
$('#run-form').onsubmit=async e=>{
  e.preventDefault();const button=e.submitter;button.disabled=true;$('#run-error').textContent='';
  try{
    const response=await fetch('/api/run',{method:'POST',headers:{Authorization:`Bearer ${$('#admin-token').value}`}});
    const data=await response.json();if(!response.ok)throw new Error(data.error||'请求失败');
    $('#run-dialog').close();await sync();
  }catch(error){$('#run-error').textContent=error.message;}finally{button.disabled=false;$('#admin-token').value='';}
};
let visitorKind='both', visitorSubmitting=false, visitorPolling=false;
const visitorJobs=new Map();
function updateVisitorButton(){
  const running=visitorSubmitting||[...visitorJobs.values()].some(j=>j.running);
  $('#visitor-submit').disabled=running;
  $('#visitor-submit span').textContent=running?'检测进行中…':{both:'运行两项检测',candy:'运行糖果题检测',pelican:'生成鹈鹕动画'}[visitorKind];
  document.querySelectorAll('[data-test]').forEach(b=>b.disabled=running);
}
document.querySelectorAll('[data-test]').forEach(b=>b.onclick=()=>{
  visitorKind=b.dataset.test;
  document.querySelectorAll('[data-test]').forEach(n=>n.setAttribute('aria-pressed',String(n===b)));
  updateVisitorButton();
});
$('#toggle-key').onclick=()=>{
  const hidden=$('#visitor-key').type==='password';
  $('#visitor-key').type=hidden?'text':'password';
  $('#toggle-key').title=hidden?'隐藏密钥':'显示密钥';
  $('#toggle-key').setAttribute('aria-label',$('#toggle-key').title);
  $('#toggle-key use').setAttribute('href',`/icons.svg#${hidden?'eye-off':'eye'}`);
};
function visitorError(text){
  $('#visitor-error').textContent=text;$('#visitor-error').hidden=!text;
}
function renderVisitorResults(){
  if(!visitorJobs.size)return;
  $('#visitor-count').textContent=`${visitorJobs.size} 次检测`;
  $('#visitor-results').replaceChildren();
  [...visitorJobs.values()].reverse().forEach(job=>{
    job.records.forEach(r=>{
      const row=el('div','visitor-result'),top=el('div','visitor-result-top'),meta=el('div','visitor-result-meta');
      top.append(el('h3','',r.kind==='candy'?'黑袋糖果题':'鹈鹕骑行'),badge(r.status));
      meta.append(el('span','',fmt(r.started)),el('span','',r.durationMs?`${(r.durationMs/1000).toFixed(1)}s`:'等待返回'));
      if(r.status!=='running'){
        const details=el('button','text-button','查看结果');
        details.onclick=()=>showVisitorDetail(job,r);
        meta.append(details);
      }
      row.append(top,meta);
      if(r.error)row.append(el('p','visitor-result-error',r.error));
      $('#visitor-results').append(row);
    });
  });
  updateVisitorButton();
}
function showVisitorDetail(job,r){
  $('#detail-title').textContent=r.kind==='candy'?'自测 / 黑袋糖果题':'自测 / 鹈鹕骑行';
  const body=$('#detail-body');body.replaceChildren();
  const meta=el('div','detail-meta');meta.append(badge(r.status),el('span','',fmt(r.started,true)),el('span','',`${r.outputTokens} output tokens`));
  body.append(meta);
  if(r.error)body.append(el('p','',r.error));
  if(r.kind==='pelican'&&r.status==='generated'){
    const frame=el('iframe');frame.setAttribute('sandbox','allow-scripts allow-same-origin');frame.setAttribute('scrolling','no');frame.title='访客鹈鹕动画隔离预览';frame.src=`/visitor-art/${job.id}/pelican`;frame.addEventListener('load',()=>fitPreview(frame));body.append(frame);
  }
  body.append(el('h3','','原始输出'),el('pre','',r.output||'尚无文本输出'));
  $('#detail-dialog').showModal();
}
async function pollVisitors(){
  if(visitorPolling)return;
  visitorPolling=true;
  try{
    for(const [id,job] of visitorJobs){
      if(!job.running)continue;
      try{
        const response=await fetch(`/api/visitor/${id}`,{cache:'no-store'});
        if(response.status===404){
          job.running=false;job.records.forEach(r=>{if(r.status==='running'){r.status='error';r.error='会话已过期或服务已重启';}});
        }else if(response.ok){
          visitorJobs.set(id,await response.json());
        }else{
          throw new Error('poll');
        }
      }catch{
        if(Date.now()-new Date(job.created).getTime()>300000){
          job.running=false;job.records.forEach(r=>{if(r.status==='running'){r.status='error';r.error='结果同步超时，请检查网络后重新检测';}});
        }else{visitorError('自测结果连接中断，正在重试同步。');}
      }
    }
    renderVisitorResults();
  }finally{visitorPolling=false;}
}
$('#visitor-form').onsubmit=async e=>{
  e.preventDefault();visitorError('');
  if(visitorSubmitting||[...visitorJobs.values()].some(j=>j.running))return;
  visitorSubmitting=true;updateVisitorButton();
  const input={endpoint:$('#visitor-url').value.trim(),key:$('#visitor-key').value.trim(),kind:visitorKind,consent:$('#visitor-consent').checked};
  try{
    const response=await fetch('/api/visitor',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(input),signal:AbortSignal.timeout(15000)});
    const data=await response.json();if(!response.ok)throw new Error(data.error||'自测请求失败');
    $('#visitor-key').value='';$('#visitor-key').type='password';
    $('#toggle-key').title='显示密钥';$('#toggle-key').setAttribute('aria-label','显示密钥');$('#toggle-key use').setAttribute('href','/icons.svg#eye');
    const started=new Date().toISOString();
    visitorJobs.set(data.id,{id:data.id,created:started,running:true,records:(visitorKind==='both'?['candy','pelican']:[visitorKind]).map(kind=>({kind,status:'running',started,outputTokens:0}))});
    if(visitorJobs.size>12)visitorJobs.delete(visitorJobs.keys().next().value);
    renderVisitorResults();await pollVisitors();
  }catch(error){
    visitorError(error.name==='TimeoutError'?'提交超时，请稍后检查后重试，避免重复调用。':error.message);
  }finally{input.key='';visitorSubmitting=false;updateVisitorButton();}
};
const compactStyle=document.createElement('style');
compactStyle.textContent=`main{padding-top:24px}.heading{margin-bottom:18px}.heading h1{margin-bottom:6px}.backend-strip{min-height:40px;margin-bottom:18px}.metrics{padding-top:0;padding-bottom:18px;margin-bottom:18px}.metric>strong{margin:10px 0 6px}.section-heading{margin-bottom:16px}.legend{margin:12px 0}.schedule{padding:12px 0 16px}.gallery-section{padding-top:16px}.artwork,.artwork iframe,#detail-body iframe{overflow:hidden!important;scrollbar-width:none}.artwork iframe::-webkit-scrollbar,#detail-body iframe::-webkit-scrollbar{display:none}.artwork iframe{height:230px}.artwork-footer{padding:9px 12px}.artwork-footer strong{margin-bottom:3px}footer{margin-top:18px;padding:14px 0}`;
document.head.appendChild(compactStyle);
sync();setInterval(sync,15000);setInterval(updateCountdown,1000);setInterval(pollVisitors,3000);

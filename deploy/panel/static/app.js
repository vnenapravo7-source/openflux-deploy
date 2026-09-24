const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];
let latest = null;

const transportNames = {yandex:"Yandex Docs",vyandex:"Yandex Volga",mailru:"Mail.ru Docs",cupsonline:"Cups.online"};
const api = async (path, options={}) => {
  options.headers = {...(options.headers||{}), "X-OpenFlux-Action":"1"};
  const res = await fetch(path, options);
  const body = await res.json().catch(()=>({}));
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body;
};
const bytes = (n=0) => {
  const units=["Б","КБ","МБ","ГБ"]; let i=0;
  while(n>=1024 && i<units.length-1){n/=1024;i++}
  return `${n.toFixed(i?1:0)} ${units[i]}`;
};
const duration = (s=0) => {
  const d=Math.floor(s/86400),h=Math.floor((s%86400)/3600),m=Math.floor((s%3600)/60);
  return d?`${d} д ${h} ч`:h?`${h} ч ${m} мин`:`${m} мин`;
};
function toast(text,error=false){const el=$("#toast");el.textContent=text;el.className=error?"show error":"show";setTimeout(()=>el.className="",3500)}

function navigate(name){
  $$(".nav").forEach(x=>x.classList.toggle("active",x.dataset.view===name));
  $$(".view").forEach(x=>x.classList.toggle("active",x.id===`view-${name}`));
  $("#pageTitle").textContent={overview:"Обзор",settings:"Подключение",nodes:"Ноды",logs:"Журнал"}[name];
}
$$('[data-view]').forEach(x=>x.onclick=()=>navigate(x.dataset.view));
$$('[data-goto]').forEach(x=>x.onclick=()=>navigate(x.dataset.goto));

function drawChart(points=[]){
  const c=$("#trafficChart"), dpr=devicePixelRatio||1, rect=c.getBoundingClientRect();
  c.width=Math.max(1,rect.width*dpr);c.height=250*dpr;
  const x=c.getContext("2d");x.scale(dpr,dpr);const w=rect.width,h=250,pad=18;
  x.clearRect(0,0,w,h);x.strokeStyle="#1f2a36";x.lineWidth=1;
  for(let i=0;i<5;i++){const y=pad+(h-pad*2)*i/4;x.beginPath();x.moveTo(0,y+.5);x.lineTo(w,y+.5);x.stroke()}
  if(points.length<2)return;
  const max=Math.max(1024,...points.flatMap(p=>[p.rx,p.tx]));
  const line=(key,color)=>{x.beginPath();points.forEach((p,i)=>{const px=i*w/(Math.max(points.length-1,1)),py=h-pad-(p[key]/max)*(h-pad*2);i?x.lineTo(px,py):x.moveTo(px,py)});x.strokeStyle=color;x.lineWidth=2;x.stroke()};
  line("rx","#7bf7c8");line("tx","#6bb9ff");
  x.fillStyle="#627080";x.font="10px system-ui";x.fillText(`${bytes(max)}/с`,6,12);
}

function setForm(c){
  $("#enabled").checked=c.enabled;$("#transport").value=c.transport;$("#docUrl").value=c.url||"";$("#mode").value=c.mode;
  $("#codec").value=c.codec;$("#localIp").value=c.local_ip||"";$("#encryptionKey").value=c.encryption_key_file||"";
  $("#autoUpdate").checked=c.auto_update;$("#debug").checked=c.debug;transportChanged();
}
function transportChanged(){
  const cups=$("#transport").value==="cupsonline";$("#urlField").style.display=cups?"none":"grid";$("#docUrl").required=!cups;
  $("#urlLabel").textContent=$("#transport").value==="mailru"?"Публичная ссылка Mail.ru":"Публичная ссылка на документ";
}
$("#transport").onchange=transportChanged;

function renderNodes(nodes=[]){
  $("#nodeCount").textContent=nodes.length;
  $("#nodesList").innerHTML=nodes.length?nodes.map(n=>`<div class="node ${n.online?'':'offline'}">
    <div class="node-name"><b>${escapeHTML(n.name)}</b><small>${escapeHTML(n.base_url)}</small></div>
    <div class="node-metric"><span>Статус</span><b>${n.online?(n.running?'Работает':'Остановлена'):'Нет связи'}</b></div>
    <div class="node-metric"><span>Трафик</span><b>↓ ${bytes(n.rx)} · ↑ ${bytes(n.tx)}</b></div>
    <div class="node-metric"><span>Транспорт</span><b>${transportNames[n.transport]||'—'}</b></div>
    <div class="node-actions"><button data-node-action="restart" data-id="${n.id}" title="Перезапустить">↻</button><button data-node-action="update" data-id="${n.id}" title="Обновить">⇣</button><button data-node-action="delete" data-id="${n.id}" title="Удалить">×</button></div>
    ${n.error?`<small>${escapeHTML(n.error)}</small>`:''}</div>`).join(""):'<div class="empty">Подключённых нод пока нет.</div>';
  $$('[data-node-action]').forEach(b=>b.onclick=()=>nodeAction(b.dataset.id,b.dataset.nodeAction));
}
function escapeHTML(s=""){const d=document.createElement("div");d.textContent=s;return d.innerHTML}

async function refresh(){
  try{
    const s=await api("/api/state");latest=s;
    const pill=$("#statusPill");pill.className=`status ${s.running?'online':'offline'}`;pill.innerHTML=`<i></i>${s.running?'Работает':'Остановлен'}`;
    $("#runText").textContent=s.running?"Онлайн":"Остановлен";$("#uptime").textContent=s.running?`аптайм ${duration(s.uptime)}`:(s.last_error||"ожидает запуска");
    const last=s.traffic.at(-1)||{};$("#rxNow").textContent=`${bytes(last.rx)}/с`;$("#txNow").textContent=`${bytes(last.tx)}/с`;
    $("#version").textContent=(s.upstream_version||"unknown").slice(0,12);$("#panelVersion").textContent=`панель ${s.panel_version}${s.updating?' · обновляется…':''}`;
    $("#currentTransport").textContent=transportNames[s.config.transport]||s.config.transport;$("#currentMode").textContent=s.config.mode.toUpperCase();$("#currentCodec").textContent=s.config.codec;$("#currentPid").textContent=s.pid||"—";
    $("#logs").textContent=s.logs.length?s.logs.join("\n"):"Записей пока нет.";$("#logs").scrollTop=$("#logs").scrollHeight;
    drawChart(s.traffic);renderNodes(s.nodes);if(!$("#configForm").dataset.dirty)setForm(s.config);
  }catch(e){console.error("OpenFlux refresh failed",e);$("#statusPill").className="status offline";$("#statusPill").title=e.message;$("#statusPill").innerHTML="<i></i>Нет связи"}
}

$("#configForm").addEventListener("input",()=>$("#configForm").dataset.dirty="1");
$("#configForm").onsubmit=async e=>{e.preventDefault();const c={enabled:$("#enabled").checked,transport:$("#transport").value,url:$("#docUrl").value.trim(),mode:$("#mode").value,codec:$("#codec").value,local_ip:$("#localIp").value.trim(),encryption_key_file:$("#encryptionKey").value.trim(),debug:$("#debug").checked,auto_update:$("#autoUpdate").checked};try{await api("/api/config",{method:"PUT",headers:{"Content-Type":"application/json"},body:JSON.stringify(c)});delete $("#configForm").dataset.dirty;toast("Настройки сохранены и применены");refresh()}catch(e){toast(e.message,true)}};
async function localAction(action){try{await api(`/api/${action}`,{method:"POST"});toast(action==="update"?"Обновление запущено":"OpenFlux перезапущен");setTimeout(refresh,900)}catch(e){toast(e.message,true)}}
$("#restartBtn").onclick=$("#restartTop").onclick=()=>localAction("restart");$("#updateBtn").onclick=()=>localAction("update");
$("#showAddNode").onclick=()=>$("#nodeForm").classList.remove("hidden");$("#cancelNode").onclick=()=>$("#nodeForm").classList.add("hidden");
$("#nodeForm").onsubmit=async e=>{e.preventDefault();const n={name:$("#nodeName").value.trim(),base_url:$("#nodeUrl").value.trim(),token:$("#nodeToken").value,tls_sha256:$("#nodeFingerprint").value.trim()};try{await api("/api/nodes",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(n)});e.target.reset();e.target.classList.add("hidden");toast("Нода подключена");refresh()}catch(e){toast(e.message,true)}};
async function nodeAction(id,action){try{if(action==="delete"&&!confirm("Удалить ноду из панели? На самом сервере ничего не изменится."))return;await api(`/api/nodes/${id}${action==="delete"?'':`/${action}`}`,{method:action==="delete"?"DELETE":"POST"});toast(action==="delete"?"Нода удалена":"Команда отправлена");setTimeout(refresh,700)}catch(e){toast(e.message,true)}}
$("#copyLogs").onclick=async()=>{await navigator.clipboard.writeText($("#logs").textContent);toast("Журнал скопирован")};
addEventListener("resize",()=>latest&&drawChart(latest.traffic));refresh();setInterval(refresh,5000);

const $ = selector => document.querySelector(selector);
const $$ = selector => [...document.querySelectorAll(selector)];
const names = {yandex:"Yandex Docs",vyandex:"Yandex Volga",boards:"Yandex Board",mailru:"Mail.ru Docs",cupsonline:"Cups.online",direct:"Direct TCP"};
let state = null, users = [], editing = null, editingUser = null, editUserAction = "", selectedLog = "", activeView = "overview";
const revealedKeys = new Map(), revealedShares = new Map();
let pendingUpdatePrompt=false, shownUpdateSignature="", currentUpdateSignature="";

async function api(path, options={}) {
  const response = await fetch(path, {credentials:"same-origin",...options,headers:{"X-OpenFlux-Action":"1",...(options.headers||{})}});
  const body = await response.json().catch(()=>({}));
  if (!response.ok) { const error = new Error(body.error || `HTTP ${response.status}`); error.status=response.status; throw error; }
  return body;
}
const json = value => ({headers:{"Content-Type":"application/json"},body:JSON.stringify(value)});
const esc = value => { const div=document.createElement("div"); div.textContent=String(value??""); return div.innerHTML; };
const escAttr = value => esc(value).replaceAll('"',"&quot;").replaceAll("'","&#39;");
const bytes = (input=0) => { let n=Number(input)||0, i=0; const unit=["Б","КБ","МБ","ГБ"]; while(n>=1024&&i<3){n/=1024;i++} return `${n.toFixed(i?1:0)} ${unit[i]}`; };
const duration = input => { const s=Number(input)||0,d=Math.floor(s/86400),h=Math.floor(s%86400/3600),m=Math.floor(s%3600/60); return d?`${d} д ${h} ч`:h?`${h} ч ${m} мин`:`${m} мин`; };
function toast(message,error=false){const el=$("#toast");el.textContent=message;el.className=error?"show error":"show";setTimeout(()=>el.className="",3500)}
function showLogin(){state=null;pendingUpdatePrompt=false;shownUpdateSignature="";revealedKeys.clear();revealedShares.clear();$$(".connection-key code,.connection-share code").forEach(el=>el.textContent="");$$(".connection-share img").forEach(el=>el.removeAttribute("src"));$$(".connection-key,.connection-share").forEach(el=>el.classList.add("hidden"));$("#updateModal").classList.add("hidden");navigate("overview");$("#appShell").classList.add("hidden");$("#loginScreen").classList.remove("hidden");}
function showApp(){ $("#loginScreen").classList.add("hidden");$("#appShell").classList.remove("hidden"); }
function navigate(view){
  if(state?.me.role!=="admin"&&(view==="users"||view==="nodes"))view="overview";
  activeView=view;
  $("#quickMultiTop").classList.toggle("hidden",view!=="connections");
  $$(".nav").forEach(el=>el.classList.toggle("active",el.dataset.view===view));
  $$(".view").forEach(el=>el.classList.toggle("active",el.id===`view-${view}`));
  $("#pageTitle").textContent={overview:"Обзор",instructions:"Инструкции",connections:"Подключения",account:"Мой профиль",users:"Пользователи",nodes:"Ноды",logs:"Журнал"}[view];
  if(view==="users")loadUsers();
  if(view==="instructions")loadInstructions();
  if(view==="overview"&&state?.me.role==="admin")drawChart(state.traffic||[]);
}
$$('[data-view]').forEach(el=>el.onclick=()=>navigate(el.dataset.view));
$$('[data-goto]').forEach(el=>el.onclick=()=>navigate(el.dataset.goto));

function drawChart(points){
  const canvas=$("#trafficChart"), rect=canvas.getBoundingClientRect(); if(!rect.width)return;
  const dpr=devicePixelRatio||1,w=rect.width,h=250,pad=18;canvas.width=w*dpr;canvas.height=h*dpr;
  const ctx=canvas.getContext("2d");ctx.scale(dpr,dpr);ctx.strokeStyle="#1f2a36";
  for(let i=0;i<5;i++){const y=pad+(h-pad*2)*i/4;ctx.beginPath();ctx.moveTo(0,y);ctx.lineTo(w,y);ctx.stroke()}
  if(points.length<2)return;
  const max=Math.max(1024,...points.flatMap(p=>[p.rx,p.tx]));
  for(const [key,color] of [["rx","#7bf7c8"],["tx","#6bb9ff"]]){
    ctx.beginPath();points.forEach((point,i)=>{const x=i*w/(points.length-1),y=h-pad-(point[key]/max)*(h-pad*2);i?ctx.lineTo(x,y):ctx.moveTo(x,y)});ctx.strokeStyle=color;ctx.lineWidth=2;ctx.stroke();
  }
  ctx.fillStyle="#627080";ctx.font="10px system-ui";ctx.fillText(`${bytes(max)}/с`,6,12);
}

function connectionCard(c){
  const soloYandexAuth=c.yandex_auth_required&&!c.transports?.length&&c.transport==="yandex";
  const online=c.running&&!soloYandexAuth;
  const status=soloYandexAuth?"Yandex ждёт проверку на сервере":c.running?`Работает · ${duration(c.uptime)}`:c.enabled?`Не запущено${c.last_error?": "+c.last_error:""}`:"Выключено";
  const links=c.transports||[],cups=c.transport==="cupsonline"||links.some(link=>link.type==="cupsonline");
  const revealedKey=revealedKeys.get(c.id)||"",revealedShare=revealedShares.get(c.id);
  const code=cups?`<div class="client-code"><small>Код для клиента Cups.online (поле URL)</small>${c.client_code?`<code>${esc(c.client_code)}</code><button class="secondary small" data-connection-action="copy" data-id="${esc(c.id)}">Копировать код</button>`:`<span>Код появится здесь после запуска и создания комнат.</span>`}</div>`:"";
  const summary=links.length?links.map(link=>`${names[link.type]||link.type} ${link.priority}`).join(" → "):names[c.transport]||c.transport;
  return `<div class="connection-card ${online?'running':''}">
    <div class="connection-main"><div><strong>${esc(c.name)}</strong><small>${esc(summary)} · ${esc(c.mode.toUpperCase())} · ${esc(c.codec)}${links.length?" · защищённая сессия":""}${state.me.role==="admin"?` · ${esc(users.find(u=>u.id===c.owner_id)?.username||c.owner_id)}`:""}</small></div><span class="status ${online?'online':'offline'}"><i></i>${esc(status)}</span></div>
    ${links.length?`<div class="connection-url">${links.filter(link=>link.url).map(link=>`${esc(names[link.type]||link.type)}: ${esc(link.url)}`).join("<br>")}${c.direct_listen?`<br>Direct: ${esc(c.direct_listen)}`:""}</div>`:c.transport==="direct"?`<div class="connection-url">Direct: ${esc(c.direct_listen||"порт не назначен")}</div>`:c.transport!=="cupsonline"?`<div class="connection-url">${esc(c.url||"Ссылка не указана")}</div>`:""}
    ${c.session_context?`<div class="connection-url">Контекст шифрования: ${esc(c.session_context)}</div>`:""}
    ${c.yandex_auth_required?`<div class="connection-url">Серверный Yandex получил SmartCaptcha и ещё не открыл документ. Проверка, пройденная на телефоне, сама по себе не подтверждает авторизацию exit-ноды.</div>`:""}
    ${c.negotiate&&!links.length?`<div class="connection-url">Протокол Session: на телефоне нужен режим Session с одним транспортом. Обычное одиночное подключение Android несовместимо — для него снимите галочку «Протокол Session» в настройках.</div>`:""}
    ${code}
    ${c.key_managed?`<div class="connection-key ${revealedKey?'':'hidden'}"><small>Общий ключ — не передавайте посторонним</small><code>${esc(revealedKey)}</code></div>`:""}
    <div class="connection-share ${revealedShare?'':'hidden'}"><small>QR и ссылка содержат ключ доступа. Показывайте их только своим устройствам.</small><img alt="QR-код OpenFlux для подключения" ${revealedShare?`src="${escAttr(revealedShare.qr)}"`:""}><code>${esc(revealedShare?.link||"")}</code><button class="secondary small" data-connection-action="copy-share" data-id="${esc(c.id)}">Копировать ссылку</button></div>
    <div class="connection-actions"><button class="secondary small" data-connection-action="share" data-id="${esc(c.id)}">${revealedShare?'Скрыть ссылку и QR':'Ссылка и QR'}</button>${c.key_managed?`<button class="secondary small" data-connection-action="key" data-id="${esc(c.id)}">${revealedKey?'Скрыть ключ':'Показать и копировать ключ'}</button>`:""}<button class="secondary small" data-connection-action="edit" data-id="${esc(c.id)}">Настроить</button><button class="secondary small" data-connection-action="restart" data-id="${esc(c.id)}">Перезапустить</button><button class="secondary small" data-connection-action="logs" data-id="${esc(c.id)}">Журнал</button><button class="danger small" data-connection-action="delete" data-id="${esc(c.id)}">Удалить</button></div>
  </div>`;
}
function renderConnections(){
  const connections=state.connections||[];
  const empty='<div class="empty">Подключений пока нет. Нажмите «+ Подключение».</div>';
  $("#connectionsList").innerHTML=connections.length?connections.map(connectionCard).join(""):empty;
  $("#overviewConnections").innerHTML=connections.length?connections.slice(0,3).map(connectionCard).join(""):empty;
  const logSelect=$("#logConnection");if(!connections.some(c=>c.id===selectedLog))selectedLog=connections[0]?.id||"";
  logSelect.innerHTML=connections.map(c=>`<option value="${esc(c.id)}">${esc(c.name)}</option>`).join("");logSelect.value=selectedLog;
  const log=connections.find(c=>c.id===selectedLog);$("#logs").textContent=log?.logs?.length?log.logs.join("\n"):"Записей пока нет.";
}
function renderNodes(nodes=[]){
  $("#nodeCount").textContent=nodes.length;
  $("#nodesList").innerHTML=nodes.length?nodes.map(n=>`<div class="node ${n.online?'':'offline'}"><div class="node-name"><b>${esc(n.name)}</b><small>${esc(n.base_url)}</small></div><div class="node-metric"><span>Статус</span><b>${n.online?(n.running?'Работает':'Остановлена'):'Нет связи'}</b></div><div class="node-metric"><span>Подключений</span><b>${n.connections||0}</b></div><div class="node-metric"><span>Трафик</span><b>↓ ${bytes(n.rx)} · ↑ ${bytes(n.tx)}</b></div><div class="node-actions"><button data-node-action="restart" data-id="${esc(n.id)}" title="Перезапустить">↻</button><button data-node-action="update" data-id="${esc(n.id)}" title="Обновить">⇣</button><button data-node-action="delete" data-id="${esc(n.id)}" title="Удалить">×</button></div>${n.error?`<small>${esc(n.error)}</small>`:""}</div>`).join(""):'<div class="empty">Подключённых нод пока нет.</div>';
}
function renderUsers(){
  $("#usersList").innerHTML=users.map(u=>`<div class="user-row"><div><b>${esc(u.username)}</b><small>${u.role==="admin"?"Администратор":"Пользователь"} · ${state.connections.filter(c=>c.owner_id===u.id).length} подключений</small></div><div class="header-actions">${u.id===state.me.id?`<button class="secondary small" data-user-action="self" data-id="${esc(u.id)}">Мой профиль</button>`:`<button class="secondary small" data-user-action="username" data-id="${esc(u.id)}">Изменить логин</button><button class="secondary small" data-user-action="password" data-id="${esc(u.id)}">Сменить пароль</button><button class="danger small" data-user-action="delete" data-id="${esc(u.id)}">Удалить</button>`}</div></div>`).join("");
  $("#connectionOwner").innerHTML=users.map(u=>`<option value="${esc(u.id)}">${esc(u.username)}</option>`).join("");
}
function maybeOfferUpdates(serverNew,panelNew){
  if(!pendingUpdatePrompt||state.checking_versions||!state.last_version_check)return;
  pendingUpdatePrompt=false;
  if(!serverNew&&!panelNew)return;
  const signature=`${serverNew?`${state.latest_upstream||""}${state.server_patch_pending?":board-json-fix":""}`:""}|${panelNew?state.latest_panel:""}`;
  const key=`openflux-update-reminder:${state.me.id}`;
  try{if(localStorage.getItem(key)===signature)return}catch(_){}
  if(shownUpdateSignature===signature)return;
  shownUpdateSignature=currentUpdateSignature=signature;
  $("#updateModalText").textContent=serverNew&&panelNew?"Есть обновления серверной части и панели. Что обновить сейчас?":state.server_patch_pending?"Доступно исправление Yandex Board для серверной части.":serverNew?"Доступна новая версия серверной части.":"Доступна новая версия панели.";
  $("#modalServerUpdate").classList.toggle("hidden",!serverNew);
  $("#modalPanelUpdate").classList.toggle("hidden",!panelNew);
  $("#suppressUpdateReminder").checked=false;
  $("#updateModal").classList.remove("hidden");
  $("#dismissUpdateModal").focus();
}
function dismissUpdateModal(){
  if($("#suppressUpdateReminder").checked&&state?.me?.id){
    try{localStorage.setItem(`openflux-update-reminder:${state.me.id}`,currentUpdateSignature)}catch(_){}
  }
  $("#updateModal").classList.add("hidden");
}
async function loadUsers(){if(state?.me.role!=="admin")return;try{users=await api("/api/users");renderUsers();renderConnections();}catch(e){toast(e.message,true)}}
function openUserEditor(user,action){
  editingUser=user.id;editUserAction=action;
  $("#editUserTitle").textContent=`${action==="username"?"Изменить логин":"Сменить пароль"}: ${user.username}`;
  $("#editUserForm").reset();$("#editUsername").value=user.username;
  $("#editUsernameField").classList.toggle("hidden",action!=="username");$("#editUsername").required=action==="username";
  $("#editPasswordField").classList.toggle("hidden",action!=="password");$("#editPassword").required=action==="password";
  $("#editPasswordConfirmField").classList.toggle("hidden",action!=="password");$("#editPasswordConfirm").required=action==="password";
  $("#editUserForm").classList.remove("hidden");$("#editUserForm").scrollIntoView({behavior:"smooth"});
}
function render(){
  showApp();const admin=state.me.role==="admin",connections=state.connections||[],running=connections.filter(c=>c.running).length;
  $$(".admin-only").forEach(el=>el.classList.toggle("hidden",!admin));
  $("#accountLabel").textContent=`${state.me.username} · ${admin?"АДМИНИСТРАТОР":"ПОЛЬЗОВАТЕЛЬ"}`;
  if(!$("#usernameForm").dataset.dirty)$("#accountUsername").value=state.me.username;
  $("#connectionCount").textContent=connections.length;$("#connectionTotal").textContent=connections.length;$("#runningTotal").textContent=`${running} работают`;
  const pill=$("#statusPill");pill.className=`status ${running?'online':'offline'}`;pill.innerHTML=`<i></i>${running} / ${connections.length} работают`;
  $("#version").textContent=(state.upstream_version||"unknown").slice(0,12);$("#panelVersion").textContent=`панель ${state.panel_version} · ${(state.panel_revision||"unknown").slice(0,8)}`;
  if(admin){
    const point=(state.traffic||[]).at(-1)||{},busy=!!state.updating||!!state.updating_panel;
    $("#rxNow").textContent=`${bytes(point.rx)}/с`;$("#txNow").textContent=`${bytes(point.tx)}/с`;$("#autoUpdate").checked=!!state.auto_update;
    $("#rollbackServerBtn").disabled=busy||!state.server_rollback_available;
    $("#rollbackPanelBtn").disabled=busy||!state.panel_rollback_available;
    $("#rollbackHint").classList.toggle("hidden",!!state.rollback_capable);
    $("#checkUpdatesBtn").disabled=!!state.checking_versions;
    $("#checkUpdatesBtn").textContent=state.checking_versions?"Проверяем…":"Проверить версии";
    $("#versionCheckStatus").textContent=state.checking_versions?"Проверяю версии сервера и панели…":state.version_check_error?`Ошибка проверки: ${state.version_check_error}`:state.last_version_check?`Проверено: ${new Date(state.last_version_check).toLocaleString("ru-RU")}. ${state.latest_upstream&&state.latest_panel?"Результаты ниже.":"Не удалось получить все версии."}`:"Версии ещё не проверены";
    const serverNew=!!state.latest_upstream&&state.latest_upstream!==state.upstream_version;
    const panelNew=!!state.latest_panel&&state.latest_panel!==state.panel_revision;
    const serverFix=!!state.server_patch_pending,serverUpdate=serverNew||serverFix;
    $("#updateBtn").disabled=busy||!!state.checking_versions||!serverUpdate;
    $("#updateBtn").textContent=serverFix&&!serverNew?"Исправить Board":"Обновить сервер";
    $("#updatePanelBtn").disabled=busy||!!state.checking_versions||!panelNew;
    $("#serverVersionStatus").textContent=serverFix?"Доступно исправление Board (HTTP 415)":state.latest_upstream?`Последняя ревизия: ${state.latest_upstream.slice(0,8)}${serverNew?" · доступно обновление":" · актуально"}`:"";
    $("#panelVersionStatus").textContent=state.latest_panel?`Последняя ревизия: ${state.latest_panel.slice(0,8)}${panelNew?" · доступно обновление":" · актуально"}`:"";
    $("#serverRollbackTarget").textContent=state.previous_server_revision&&state.previous_server_revision!=="unknown"?`Резервная версия: ${state.previous_server_revision.slice(0,8)}${state.previous_server_revision===state.upstream_version?" · совпадает с текущей":""}`:"Резервной версии пока нет";
    $("#panelRollbackTarget").textContent=state.previous_panel_revision&&state.previous_panel_revision!=="unknown"?`Резервная версия: ${state.previous_panel_revision.slice(0,8)}${state.previous_panel_revision===state.panel_revision?" · совпадает с текущей":""}`:"Резервной версии пока нет";
    $("#updateNotice").classList.toggle("hidden",!serverUpdate&&!panelNew);
    $("#updateNoticeText").textContent=serverUpdate&&panelNew?"Для сервера и панели доступны обновления.":serverFix&&!serverNew?"Доступно исправление Yandex Board.":serverNew?"Доступна новая версия сервера.":"Доступна новая версия панели.";
    $("#offerServerUpdate").classList.toggle("hidden",!serverUpdate);$("#offerPanelUpdate").classList.toggle("hidden",!panelNew);
    $("#offerServerUpdate").disabled=busy||!!state.checking_versions;$("#offerPanelUpdate").disabled=busy||!!state.checking_versions;
    $("#serverUpdateStatus").textContent=state.update_error||(state.updating?(state.server_action==="rollback"?"Откатываем серверную часть…":"Обновляем серверную часть…"):(state.server_action==="rollback"?"Откат завершён; автообновление сервера выключено.":state.version_check_error||""));
    const serverUpdateLog=$("#serverUpdateLog");serverUpdateLog.textContent=state.server_update_log||"";serverUpdateLog.classList.toggle("hidden",!state.server_update_log);
    $("#panelUpdateStatus").textContent=state.panel_update_error||(state.updating_panel?(state.panel_action==="rollback"?"Откатываем панель; она будет перезапущена…":"Панель обновляется; ход работы показан ниже…"):"");
    const updateLog=$("#panelUpdateLog");updateLog.textContent=state.panel_update_log||"";updateLog.classList.toggle("hidden",!state.panel_update_log);
    drawChart(state.traffic||[]);renderNodes(state.nodes||[]);maybeOfferUpdates(serverUpdate,panelNew)
  }
  else $("#updateNotice").classList.add("hidden");
  renderConnections();if(activeView==="users")renderUsers();
}
async function refresh(){try{state=await api("/api/state");render();}catch(e){if(e.status===401)showLogin();else if(state)toast(`Нет связи с панелью: ${e.message}`,true);else showLogin();}}

function sessionRows(){return $$("#sessionTransportRows .session-transport-row").map(row=>({type:row.querySelector(".session-type").value,url:row.querySelector(".session-url input").value.trim(),priority:Number(row.querySelector(".session-priority").value)}))}
function refreshSessionRows(){const advanced=$("#sessionMode").checked;for(const row of $$("#sessionTransportRows .session-transport-row")){const kind=row.querySelector(".session-type").value,url=row.querySelector(".session-url"),input=url.querySelector("input");url.classList.toggle("hidden",kind==="direct");input.required=advanced&&kind!=="direct"&&kind!=="cupsonline"&&$("#enabled").checked;row.querySelector(".session-priority").required=advanced;input.placeholder=kind==="boards"?"https://boards.yandex.ru/guest/?hash=…":"https://…"}$("#directListen").required=false;$("#directSetupNotice").classList.toggle("hidden",!(advanced?sessionRows().some(link=>link.type==="direct"):$("#transport").value==="direct")||state?.direct_ports_ready!==false)}
function addSessionRow(link={}){const row=document.createElement("div");row.className="session-transport-row";row.innerHTML=`<label><span>Транспорт</span><select class="session-type">${["direct","yandex","vyandex","boards","mailru","cupsonline"].map(type=>`<option value="${type}" ${type===(link.type||"yandex")?"selected":""}>${esc(names[type]||"Direct TCP")}</option>`).join("")}</select></label><label><span>Приоритет</span><input class="session-priority" type="number" min="1" max="1000" required value="${Number(link.priority)||50}"></label><label class="session-url"><span>Ссылка</span><input type="url" value="${escAttr(link.url||"")}" autocomplete="off"></label><button class="secondary small" type="button" aria-label="Удалить транспорт">×</button>`;row.querySelector(".session-type").onchange=refreshSessionRows;row.querySelector("button").onclick=()=>{row.remove();refreshSessionRows()};$("#sessionTransportRows").appendChild(row);refreshSessionRows()}
function sessionChanged(){const advanced=$("#sessionMode").checked;$("#sessionOptions").classList.toggle("hidden",!advanced);$("#legacyTransportField").classList.toggle("hidden",advanced);$("#legacyNegotiateField").classList.toggle("hidden",advanced);$("#codec").disabled=advanced;if(advanced)$("#codec").value="batched";$("#encryptionKey").required=false;$("#sessionContextUrl").disabled=!advanced;$("#directListen").disabled=!advanced;$("#maxPacketSize").disabled=!advanced;transportChanged();refreshSessionRows()}
function transportChanged(){const kind=$("#transport").value,cups=kind==="cupsonline",boards=kind==="boards",advanced=$("#sessionMode").checked;$("#urlField").classList.toggle("hidden",advanced||cups||kind==="direct");$("#legacyDirectListenField").classList.toggle("hidden",advanced||kind!=="direct");$("#docUrl").required=!advanced&&!cups&&kind!=="direct"&&$("#enabled").checked;$("#urlLabel").textContent=boards?"Публичная ссылка на доску":"Публичная ссылка на документ";$("#docUrl").placeholder=boards?"https://boards.yandex.ru/guest/?hash=…":"https://…";$("#urlHint").textContent=boards?"Нужна гостевая ссылка с параметром hash. Клиент на телефоне тоже должен поддерживать boards.":"Доступ по ссылке должен быть разрешён.";
  const guide={yandex:{url:"https://docs.360.yandex.ru/",label:"Открыть Yandex Docs ↗",tip:'<strong>Снять</strong> галочку «Перейти на новый редактор». Ссылку взять через «Поделиться» в документе.'},vyandex:{url:"https://docs.360.yandex.ru/",label:"Открыть Yandex Volga ↗",tip:'<strong>Оставить</strong> галочку «Перейти на новый редактор». Ссылку взять через «Поделиться» в документе.'},mailru:{url:"https://doc.mail.ru/",label:"Открыть Mail.ru Docs ↗",tip:'Ссылку взять через «Поделиться» в документе.'},boards:{url:"https://boards.yandex.ru/",label:"Открыть Yandex Board ↗",tip:'Скопируйте гостевую ссылку на доску с параметром hash.'}}[kind];
  $("#transportGuide").classList.toggle("hidden",advanced||!guide);
  if(guide){$("#transportServiceLink").href=guide.url;$("#transportServiceLink").textContent=guide.label;$("#transportTip").innerHTML=guide.tip}
}
$("#transport").onchange=()=>{transportChanged();refreshSessionRows()};$("#enabled").onchange=()=>{transportChanged();refreshSessionRows()};$("#sessionMode").onchange=sessionChanged;$("#addSessionTransport").onclick=()=>{if(sessionRows().length<8)addSessionRow({type:"yandex",priority:50})};
const copyDirectInstaller=async()=>{try{await navigator.clipboard.writeText('OPENFLUX_INSTALL_MODE=docker bash -c "$(wget -qO- https://raw.githubusercontent.com/vnenapravo7-source/openflux-deploy/main/deploy.sh)"');toast("Команда скопирована. Запустите её на VPS от root.")}catch(e){toast(e.message,true)}};
$("#copyDirectSetup").onclick=copyDirectInstaller;$("#copyQuickDirectSetup").onclick=copyDirectInstaller;
function openConnection(c){
  editing=c?.id||null;$("#editorTitle").textContent=c?`Настроить: ${c.name}`:"Новое подключение";
  $("#connectionName").value=c?.name||"";$("#enabled").checked=c?.enabled??true;$("#transport").value=c?.transport||"yandex";
  $("#mode").value=c?.mode||"l4";$("#codec").value=c?.codec||"batched";$("#docUrl").value=c?.url||"";$("#legacyNegotiate").checked=!!c?.negotiate;
  $("#localIp").value=c?.local_ip||"";$("#encryptionKey").value=c?.key_managed?"":c?.encryption_key_file||"";$("#debug").checked=c?.debug??true;
  $("#sessionMode").checked=!!c?.transports?.length;$("#sessionTransportRows").replaceChildren();(c?.transports?.length?c.transports:[{type:c?.transport||"yandex",url:c?.url||"",priority:100}]).forEach(addSessionRow);$("#sessionContextUrl").value=c?.session_context_url||"";$("#directListen").value=c?.direct_listen||"";$("#legacyDirectListen").value=c?.direct_listen||"";$("#maxPacketSize").value=c?.max_packet_size||"";
  if(state.me.role==="admin")$("#connectionOwner").value=c?.owner_id||state.me.id;
  $("#connectionOwner").disabled=!!c;sessionChanged();$("#connectionEditor").classList.remove("hidden");navigate("connections");$("#connectionEditor").scrollIntoView({behavior:"smooth"});
}
$("#newConnectionTop").onclick=()=>{$("#quickMultiCard").classList.add("hidden");openConnection()};
$("#quickMultiTop").onclick=()=>{$("#connectionEditor").classList.add("hidden");$("#quickMultiForm").reset();$("#quickDirectSetupNotice").classList.toggle("hidden",state?.direct_ports_ready!==false);$("#quickMultiCard").classList.remove("hidden");navigate("connections");$("#quickMultiCard").scrollIntoView({behavior:"smooth"})};
$("#cancelQuickMulti").onclick=()=>$("#quickMultiCard").classList.add("hidden");
$("#cancelConnection").onclick=()=>$("#connectionEditor").classList.add("hidden");
$("#quickMultiForm").onsubmit=async event=>{
  event.preventDefault();
  const form=event.target,submit=form.querySelector('[type="submit"]');submit.disabled=true;
  const c={name:$("#quickMultiName").value.trim(),owner_id:state.me.id,enabled:true,transport:"yandex",url:"",mode:"l4",codec:"batched",local_ip:"",encryption_key_file:"",negotiate:false,transports:[{type:"direct",priority:100,url:""},{type:"yandex",priority:75,url:$("#quickMultiYandex").value.trim()},{type:"mailru",priority:25,url:$("#quickMultiMail").value.trim()}],session_context_url:"",direct_listen:"",max_packet_size:0,debug:true};
  try{await api("/api/connections",{method:"POST",...json(c)});form.reset();$("#quickMultiCard").classList.add("hidden");toast("Мульти-подключение создано");await refresh()}
  catch(e){toast(e.message,true)}finally{submit.disabled=false}
};
$("#connectionForm").onsubmit=async event=>{
  event.preventDefault();const advanced=$("#sessionMode").checked;if(advanced&&!sessionRows().length)return toast("Добавьте хотя бы один транспорт",true);const c={name:$("#connectionName").value.trim(),owner_id:state.me.role==="admin"?$("#connectionOwner").value:state.me.id,enabled:$("#enabled").checked,transport:advanced?"yandex":$("#transport").value,url:advanced?"":$("#docUrl").value.trim(),mode:$("#mode").value,codec:$("#codec").value,local_ip:$("#localIp").value.trim(),encryption_key_file:$("#encryptionKey").value.trim(),negotiate:advanced?false:$("#legacyNegotiate").checked,transports:advanced?sessionRows():[],session_context_url:advanced?$("#sessionContextUrl").value.trim():"",direct_listen:advanced?$("#directListen").value.trim():$("#transport").value==="direct"?$("#legacyDirectListen").value.trim():"",max_packet_size:advanced?Number($("#maxPacketSize").value)||0:0,debug:$("#debug").checked};
  try{await api(editing?`/api/connections/${editing}`:"/api/connections",{method:editing?"PUT":"POST",...json(c)});$("#connectionEditor").classList.add("hidden");toast("Подключение сохранено");await refresh();}catch(e){toast(e.message,true)}
};
document.addEventListener("click",async event=>{
  const button=event.target.closest("[data-connection-action], [data-node-action], [data-user-action]");if(!button)return;
  const id=button.dataset.id,action=button.dataset.connectionAction||button.dataset.nodeAction||button.dataset.userAction;
  try{
    if(button.dataset.connectionAction){const c=state.connections.find(item=>item.id===id);if(!c)return;
      if(action==="edit")return openConnection(c);
      if(action==="logs"){selectedLog=id;renderConnections();return navigate("logs")}
      if(action==="copy"){if(!c.client_code)return;await navigator.clipboard.writeText(c.client_code);return toast("Код Cups.online скопирован")}
      if(action==="copy-share"){const link=revealedShares.get(id)?.link;if(!link)return;await navigator.clipboard.writeText(link);return toast("Ссылка OpenFlux скопирована. Она содержит ключ доступа.")}
      if(action==="share"){
        if(revealedShares.has(id)){revealedShares.delete(id);renderConnections();return}
        let host="";
        if(c.transport==="direct"||c.transports?.some(link=>link.type==="direct")){
          host=prompt("Публичный IP-адрес или домен VPS для Direct:",location.hostname)||"";
          if(!host.trim())return;
        }
        const result=await api(`/api/connections/${id}/share`,{method:"POST",...json({host:host.trim()})});
        revealedShares.set(id,result);renderConnections();
        return;
      }
      if(action==="key"){
        if(revealedKeys.has(id)){revealedKeys.delete(id);renderConnections();return}
        const result=await api(`/api/connections/${id}/key`,{method:"POST"});revealedKeys.set(id,result.key);renderConnections();
        try{await navigator.clipboard.writeText(result.key);toast("Ключ показан и скопирован. Не передавайте посторонним.")}catch(_){toast("Ключ показан. Скопируйте его вручную.")}return;
      }
      if(action==="delete"&&!confirm(`Удалить подключение «${c.name}»?`))return;
      await api(`/api/connections/${id}${action==="restart"?"/restart":""}`,{method:action==="delete"?"DELETE":"POST"});toast(action==="delete"?"Подключение удалено":"Подключение перезапущено");return refresh();
    }
    if(button.dataset.nodeAction){if(action==="delete"&&!confirm("Удалить ноду из панели? На сервере ничего не изменится."))return;await api(`/api/nodes/${id}${action==="delete"?"":`/${action}`}`,{method:action==="delete"?"DELETE":"POST"});toast(action==="delete"?"Нода удалена":"Команда отправлена");return refresh()}
    if(button.dataset.userAction){const user=users.find(u=>u.id===id);if(!user)return;
      if(action==="self")return navigate("account");
      if(action==="username"||action==="password")return openUserEditor(user,action);
      if(action==="delete"){if(!confirm(`Удалить ${user.username} и все его подключения?`))return;await api(`/api/users/${id}`,{method:"DELETE"});toast("Пользователь удалён");await refresh()}return loadUsers();
    }
  }catch(e){toast(e.message,true)}
});
$("#logConnection").onchange=event=>{selectedLog=event.target.value;renderConnections()};
$("#copyLogs").onclick=async()=>{try{await navigator.clipboard.writeText($("#logs").textContent);toast("Журнал скопирован")}catch(e){toast(e.message,true)}};
$("#loginForm").onsubmit=async event=>{event.preventDefault();try{await api("/api/login",{method:"POST",...json({username:$("#loginName").value.trim(),password:$("#loginPassword").value})});$("#loginPassword").value="";$("#loginError").textContent="";await refresh();if(state?.me.role==="admin"){await loadUsers();pendingUpdatePrompt=true;checkUpdates(true)}}catch(e){$("#loginError").textContent=e.message}};
$("#logoutBtn").onclick=async()=>{try{await api("/api/logout",{method:"POST"})}catch(_){}showLogin()};
$("#userForm").onsubmit=async event=>{event.preventDefault();try{await api("/api/users",{method:"POST",...json({username:$("#userName").value.trim(),password:$("#userPassword").value,role:$("#userRole").value})});event.target.reset();toast("Пользователь создан");await loadUsers()}catch(e){toast(e.message,true)}};
$("#cancelUserEdit").onclick=()=>$("#editUserForm").classList.add("hidden");
$("#editUserForm").onsubmit=async event=>{event.preventDefault();if(!editingUser)return;
  try{
    if(editUserAction==="username")await api(`/api/users/${editingUser}/username`,{method:"PUT",...json({username:$("#editUsername").value.trim()})});
    if(editUserAction==="password"){
      const password=$("#editPassword").value;if(password!==$("#editPasswordConfirm").value){toast("Новые пароли не совпадают",true);return}
      await api(`/api/users/${editingUser}/password`,{method:"PUT",...json({password})});
    }
    event.target.reset();event.target.classList.add("hidden");editingUser=null;toast("Данные пользователя изменены; его сеансы завершены");await refresh();await loadUsers();
  }catch(e){toast(e.message,true)}
};
$("#usernameForm").addEventListener("input",()=>$("#usernameForm").dataset.dirty="1");
$("#usernameForm").onsubmit=async event=>{event.preventDefault();const username=$("#accountUsername").value.trim();try{await api(`/api/users/${state.me.id}/username`,{method:"PUT",...json({username,current_password:$("#usernameCurrentPassword").value})});event.target.reset();delete event.target.dataset.dirty;showLogin();$("#loginName").value=username;toast("Логин изменён. Войдите снова.")}catch(e){toast(e.message,true)}};
$("#passwordForm").onsubmit=async event=>{event.preventDefault();const password=$("#passwordNew").value;if(password!==$("#passwordConfirm").value){toast("Новые пароли не совпадают",true);return}try{await api(`/api/users/${state.me.id}/password`,{method:"PUT",...json({password,current_password:$("#passwordCurrent").value})});event.target.reset();showLogin();toast("Пароль изменён. Войдите снова.")}catch(e){toast(e.message,true)}};
$("#autoUpdate").onchange=async event=>{try{await api("/api/settings",{method:"PUT",...json({auto_update:event.target.checked})});toast("Настройка обновления сохранена")}catch(e){event.target.checked=!event.target.checked;toast(e.message,true)}};
$("#updateBtn").onclick=async()=>{try{await api("/api/update",{method:"POST"});toast("Обновление запущено");await refresh()}catch(e){toast(e.message,true)}};
$("#updatePanelBtn").onclick=async()=>{try{await api("/api/update-panel",{method:"POST"});toast("Обновление панели запущено. После перезапуска войдите снова.");await refresh()}catch(e){toast(e.message,true)}};
$("#rollbackServerBtn").onclick=async()=>{if(!confirm("Откатить серверную часть на предыдущую версию? Автообновление будет выключено."))return;try{await api("/api/rollback-server",{method:"POST"});toast("Откат сервера запущен");await refresh()}catch(e){toast(e.message,true)}};
$("#rollbackPanelBtn").onclick=async()=>{if(!confirm("Откатить панель на предыдущую версию? После перезапуска потребуется повторный вход."))return;try{await api("/api/rollback-panel",{method:"POST"});toast("Откат панели запущен");await refresh()}catch(e){toast(e.message,true)}};
async function checkUpdates(force=false,manual=false){if(state?.me.role!=="admin")return;try{await api(`/api/check-updates${force?"?force=1":""}`,{method:"POST"});if(manual)toast("Проверка версий запущена; результат появится в разделе «Обслуживание».");await refresh()}catch(e){pendingUpdatePrompt=false;toast(`Проверка версий: ${e.message}`,true)}}
$("#checkUpdatesBtn").onclick=()=>checkUpdates(true,true);
$("#dismissUpdateModal").onclick=dismissUpdateModal;
$("#modalServerUpdate").onclick=()=>{dismissUpdateModal();$("#updateBtn").click()};
$("#modalPanelUpdate").onclick=()=>{dismissUpdateModal();$("#updatePanelBtn").click()};
$("#offerServerUpdate").onclick=()=>$("#updateBtn").click();$("#offerPanelUpdate").onclick=()=>$("#updatePanelBtn").click();
$("#showAddNode").onclick=()=>$("#nodeForm").classList.remove("hidden");$("#cancelNode").onclick=()=>$("#nodeForm").classList.add("hidden");
$("#nodeForm").onsubmit=async event=>{event.preventDefault();try{await api("/api/nodes",{method:"POST",...json({name:$("#nodeName").value.trim(),base_url:$("#nodeUrl").value.trim(),token:$("#nodeToken").value,tls_sha256:$("#nodeFingerprint").value.trim()})});event.target.reset();event.target.classList.add("hidden");toast("Нода подключена");await refresh()}catch(e){toast(e.message,true)}};
addEventListener("resize",()=>state?.me.role==="admin"&&drawChart(state.traffic||[]));
refresh().then(()=>{if(state?.me.role==="admin"){loadUsers();pendingUpdatePrompt=true;checkUpdates(true)}});setInterval(()=>{if(state)refresh()},5000);

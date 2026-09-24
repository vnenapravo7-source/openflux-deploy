let instructionBlocks=[],editingInstruction=null,editingMediaInstruction=null;
const instructionAttr=value=>String(value??"").replace(/[&<>"']/g,ch=>({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"})[ch]);

function instructionInline(value){
  const input=String(value??""),tokens=/\*\*([^*]+)\*\*|\*([^*]+)\*|\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|`([^`]+)`/g;
  let result="",cursor=0,match;
  while((match=tokens.exec(input))){
    result+=esc(input.slice(cursor,match.index));
    if(match[1])result+=`<strong>${esc(match[1])}</strong>`;
    else if(match[2])result+=`<em>${esc(match[2])}</em>`;
    else if(match[5])result+=`<code>${esc(match[5])}</code>`;
    else{
      try{const url=new URL(match[4]);if(!["https:","http:"].includes(url.protocol))throw Error();result+=`<a href="${instructionAttr(url.href)}" target="_blank" rel="noopener noreferrer">${esc(match[3])}</a>`}
      catch(_){result+=esc(match[0])}
    }
    cursor=tokens.lastIndex;
  }
  return result+esc(input.slice(cursor));
}
function instructionMarkdown(value){
  let html="",list=false;
  for(const line of String(value??"").split(/\r?\n/)){
    const item=line.match(/^\s*[-*] (.+)$/);
    if(item){if(!list){html+="<ul>";list=true}html+=`<li>${instructionInline(item[1])}</li>`;continue}
    if(list){html+="</ul>";list=false}
    if(line.startsWith("## "))html+=`<h4>${instructionInline(line.slice(3))}</h4>`;
    else if(line.startsWith("# "))html+=`<h3>${instructionInline(line.slice(2))}</h3>`;
    else if(line.trim())html+=`<p>${instructionInline(line)}</p>`;
  }
  if(list)html+="</ul>";
  return html;
}
function renderInstructions(){
  const admin=state?.me.role==="admin";
  $("#instructionBlocks").innerHTML=instructionBlocks.length?instructionBlocks.map((block,i)=>{
    const id=instructionAttr(block.id),title=block.title?`<h3>${esc(block.title)}</h3>`:"";
    const asset=`/api/instructions/assets/${encodeURIComponent(block.id)}`;
    const content=block.kind==="text"?`<div class="instruction-prose">${instructionMarkdown(block.body)}</div>`:
      block.kind==="image"?`<img class="instruction-image" src="${asset}" alt="${instructionAttr(block.title||block.file_name)}" loading="lazy">`:
      block.kind==="video"?`<video class="instruction-video" controls preload="metadata" src="${asset}"></video>`:
      `<a class="instruction-download" href="${asset}" download>Скачать: ${esc(block.file_name)} (${bytes(block.size)})</a>`;
    const actions=admin?`<div class="instruction-actions"><button class="secondary small" data-instruction-action="edit" data-id="${id}">Изменить</button><button class="secondary small" data-instruction-action="up" data-id="${id}" ${i===0?"disabled":""}>↑</button><button class="secondary small" data-instruction-action="down" data-id="${id}" ${i===instructionBlocks.length-1?"disabled":""}>↓</button><button class="danger small" data-instruction-action="delete" data-id="${id}">Удалить</button></div>`:"";
    return `<article class="instruction-block">${title}${content}${actions}</article>`
  }).join(""):'<div class="empty">Инструкций пока нет.</div>';
}
async function loadInstructions(){
  if(!state)return;
  try{instructionBlocks=await api("/api/instructions");renderInstructions()}
  catch(e){toast(`Инструкции: ${e.message}`,true)}
}
function openInstructionText(block=null){
  editingInstruction=block?.id||null;
  $("#instructionUploadForm").classList.add("hidden");
  $("#instructionTextForm").classList.remove("hidden");
  $("#instructionEditorTitle").textContent=block?"Изменить текстовый блок":"Новый текстовый блок";
  $("#instructionTitle").value=block?.title||"";
  $("#instructionBody").value=block?.body||"";
  $("#instructionPreview").innerHTML=instructionMarkdown($("#instructionBody").value);
  $("#instructionTextForm").scrollIntoView({behavior:"smooth"});
}
$("#addTextBlock").onclick=()=>openInstructionText();
$("#cancelTextBlock").onclick=()=>$("#instructionTextForm").classList.add("hidden");
$("#instructionBody").oninput=()=>$("#instructionPreview").innerHTML=instructionMarkdown($("#instructionBody").value);
$("#instructionTextForm").onsubmit=async event=>{event.preventDefault();
  try{await api(editingInstruction?`/api/instructions/${editingInstruction}`:"/api/instructions",{method:editingInstruction?"PUT":"POST",...json({title:$("#instructionTitle").value.trim(),body:$("#instructionBody").value})});
    $("#instructionTextForm").classList.add("hidden");editingInstruction=null;toast("Текстовый блок сохранён");await loadInstructions()}
  catch(e){toast(e.message,true)}
};
document.querySelectorAll("[data-format]").forEach(button=>button.onclick=()=>{
  const field=$("#instructionBody"),start=field.selectionStart,end=field.selectionEnd,selected=field.value.slice(start,end)||"текст";
  let insert="";
  switch(button.dataset.format){case "bold":insert=`**${selected}**`;break;case "italic":insert=`*${selected}*`;break;case "heading":insert=`## ${selected}`;break;case "list":insert=`- ${selected}`;break;case "link":insert=`[${selected}](https://example.com)`;break}
  field.setRangeText(insert,start,end,"select");field.focus();field.dispatchEvent(new Event("input"));
});
function openInstructionMedia(block=null){
  editingMediaInstruction=block?.id||null;
  $("#instructionTextForm").classList.add("hidden");$("#instructionUploadForm").reset();
  $("#instructionKind").value=block?.kind||"image";$("#instructionMediaTitle").value=block?.title||"";
  $("#instructionUploadForm").classList.remove("hidden");$("#instructionKind").dispatchEvent(new Event("change"));
  $("#uploadInstructionBtn").textContent=block?"Заменить файл":"Загрузить";
  $("#instructionUploadForm").scrollIntoView({behavior:"smooth"});
}
$("#addMediaBlock").onclick=()=>openInstructionMedia();
$("#cancelMediaBlock").onclick=()=>{$("#instructionUploadForm").classList.add("hidden");editingMediaInstruction=null};
$("#instructionKind").onchange=()=>{$("#instructionFile").accept={image:"image/jpeg,image/png,image/gif,image/webp",video:"video/mp4,video/webm,video/quicktime",file:""}[$("#instructionKind").value]};
$("#instructionUploadForm").onsubmit=async event=>{event.preventDefault();
  const button=$("#uploadInstructionBtn");button.disabled=true;button.textContent="Загружаем…";
  try{await api(editingMediaInstruction?`/api/instructions/${editingMediaInstruction}/asset`:"/api/instructions/upload",{method:editingMediaInstruction?"PUT":"POST",body:new FormData(event.target)});event.target.classList.add("hidden");editingMediaInstruction=null;toast("Файл сохранён в инструкциях");await loadInstructions()}
  catch(e){toast(e.message,true)}finally{button.disabled=false;button.textContent=editingMediaInstruction?"Заменить файл":"Загрузить"}
};
document.addEventListener("click",async event=>{
  const button=event.target.closest("[data-instruction-action]");if(!button)return;
  const block=instructionBlocks.find(item=>item.id===button.dataset.id);if(!block)return;
  const action=button.dataset.instructionAction;
  if(action==="edit"){
    if(block.kind==="text")return openInstructionText(block);
    return openInstructionMedia(block);
  }
  if(action==="delete"&&!confirm(`Удалить блок «${block.title||block.file_name||"текст"}» и его файл?`))return;
  try{if(action==="delete")await api(`/api/instructions/${block.id}`,{method:"DELETE"});
    else await api(`/api/instructions/${block.id}/move`,{method:"POST",...json({direction:action==="up"?-1:1})});
    await loadInstructions()}
  catch(e){toast(e.message,true)}
});

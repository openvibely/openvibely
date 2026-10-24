// Dependency setup belongs to the execution that requested the skill.
(() => {
 window.addEventListener('sse-task-event', event => {
  const update = event.detail;
  if (update?.type !== 'skill_dependency_progress' || !update.exec_id || !update.dependency_name) return;
  const root = document.querySelector('[data-execution-pair][data-exec-id="' + CSS.escape(update.exec_id) + '"]');
  if (!root) return;
  let status = root.querySelector('[data-skill-dependency-status="' + CSS.escape(update.dependency_name) + '"]');
  if (update.phase === 'ready') { status?.remove(); return; }
  if (!status) {
   status = document.createElement('p');
   status.dataset.skillDependencyStatus = update.dependency_name;
   status.setAttribute('role', 'status');
   root.append(status);
  }
  const name = update.dependency_name.charAt(0).toUpperCase() + update.dependency_name.slice(1);
  status.className = 'flex items-center gap-2 text-sm py-2';
  status.replaceChildren();
  if (update.phase === 'failed') {
   status.textContent = name + ' installation failed. See the skill result for details.';
  } else {
   const spinner = document.createElement('span');
   spinner.className = 'loading loading-spinner loading-sm text-primary';
   spinner.setAttribute('aria-hidden', 'true');
   const label = document.createElement('span');
   const action = update.phase === 'downloading' ? 'Downloading' : update.phase === 'preparing' ? 'Installing' : 'Checking';
   label.textContent = action + ' ' + name + '…';
   status.append(spinner, label);
  }
 });
})();

(() => {
 if (window.openVibelyDependencyApprovalsInstalled) return;
 window.openVibelyDependencyApprovalsInstalled = true;
 const element = (tag, text, className) => {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
 };
 function projectID(root) {
  return root?.closest('#chat-page-root, #task-detail-content')?.dataset.projectId || document.getElementById('project-selector')?.value || new URLSearchParams(location.search).get('project_id') || '';
 }
 async function request(url, body) {
  const response = await fetch(url, body === undefined ? {} : {method:'POST',headers:{'Content-Type':'application/json','X-OpenVibely-UI':'chat-input-answer'},body:JSON.stringify(body)});
  if (!response.ok) throw new Error(await response.text() || 'Approval request failed');
  return response.json();
 }
 function details(root, manifest) {
  const expansion = element('details');
  expansion.append(element('summary', 'Download details', 'cursor-pointer text-sm'));
  for (const dep of manifest.dependencies || []) {
   const pkg = dep.packages?.[manifest.platform];
   const fields = element('dl', undefined, 'text-sm space-y-1 py-2');
   for (const [label, value] of [['Tool', dep.name], ['Download URL', pkg?.url], ['Version', dep.version], ['Platform', manifest.platform], ['SHA-256', pkg?.sha256], ['Executable', pkg?.executable]]) {
    if (!value) continue;
    fields.append(element('dt', label, 'font-medium'), element('dd', value, 'break-all opacity-70'));
   }
   expansion.append(fields);
  }
  root.append(expansion);
 }
 window.renderSkillDependencyDetails = function(root, manifest) {
  if (!root) return;
  root.replaceChildren();root.hidden = !(manifest.dependencies || []).length;
  if (root.hidden) return;
  details(root, manifest);

 };
 let importGeneration = 0;
 window.previewImportedSkillDependencies = async function(input) {
  const generation = ++importGeneration;
  const submit=document.getElementById('skill_import_submit_btn');
  const root=document.getElementById('skill_import_dependency_details');
  const file=Array.from(input.files || []).find(file => file.name === 'SKILL.md');
  submit.disabled=true;root.hidden=false;root.textContent='Reading dependency declarations…';
  try {
   if (!file) throw new Error('Select a package containing SKILL.md.');
   const response=await fetch('/skills/dependency-preview',{method:'POST',headers:{'Content-Type':'text/plain'},body:await file.text()});
   if (!response.ok) throw new Error(await response.text());
   const manifest=await response.json();
   if (generation!==importGeneration || !root.isConnected) return;
   window.renderSkillDependencyDetails(root,manifest);submit.disabled=false;
  } catch(error) {if(generation===importGeneration)root.textContent=error.message;}
 };

 // The same server-rendered question card is used on Chat and task threads.
 window.submitChatInputAnswers = function(card, answers) {
  return request('/chat/input-requests/'+encodeURIComponent(card.dataset.chatInputRequestId)+'/answer', {
   project_id:card.dataset.projectId || projectID(card),answers
  });
 };
 function initDetails() {
  document.querySelectorAll('[data-chat-input-installation]').forEach(card=>{
   if(!card.dataset.chatInputInstallation || card.dataset.installationDetailsLoaded)return;
   try { window.renderSkillDependencyDetails(card.querySelector('[data-skill-dependency-details]'),JSON.parse(card.dataset.chatInputInstallation));card.dataset.installationDetailsLoaded='true'; } catch(_) {}
  });
 }
 document.addEventListener('click',async event=>{
  const option=event.target.closest?.('[data-chat-input-option]');
  const card=option?.closest('[data-chat-input-request-id]');
  if(!card?.dataset.chatInputInstallation)return;
  event.preventDefault();event.stopImmediatePropagation();
  if(card.dataset.submitted==='true')return;
  card.dataset.submitted='true';
  const status=card.querySelector('[role="status"]');status.hidden=false;status.classList.remove('hidden');status.textContent='Submitting answer…';
  try {
   const root=card.closest('[data-execution-pair][data-exec-id]');
   await window.submitChatInputAnswers(card,[{question_id:option.dataset.questionId,label:option.dataset.optionLabel}]);
   card.remove();
   // A load started before the answer would re-add the answered card.
   if(root)load(root,true);
  } catch(error) {delete card.dataset.submitted;status.textContent=error.message;}
 },true);
 document.addEventListener('keydown',event=>{
  const option=event.target.closest?.('[data-chat-input-option]');
  if(!option?.closest('[data-chat-input-installation]')?.dataset.chatInputInstallation || !['Enter',' '].includes(event.key))return;
  event.preventDefault();event.stopImmediatePropagation();option.click();
 },true);
 async function load(root,force) {
  // Chat already restores and refreshes these cards through its question flow.
  if(root.closest('#chat-page-root'))return;
  const project=projectID(root),exec=root.dataset.execId;
  if(!project || !exec || (!force && root.dataset.skillApprovalsLoaded===project))return;
  root.dataset.skillApprovalsLoaded=project;
  const generation=(Number(root.dataset.skillApprovalsGeneration)||0)+1;root.dataset.skillApprovalsGeneration=String(generation);
  try {
   const response=await fetch('/chat/input-requests?project_id='+encodeURIComponent(project)+'&execution_id='+encodeURIComponent(exec));
   if(!response.ok)throw new Error('Unable to load questions');
   const html=await response.text();
   if(!root.isConnected || projectID(root)!==project || Number(root.dataset.skillApprovalsGeneration)!==generation)return;
   const fragment=document.createElement('template');fragment.innerHTML=html;
   root.querySelectorAll('[data-chat-input-request-id]').forEach(card=>card.remove());
   root.append(fragment.content);initDetails();
   if(location.hash.startsWith('#chat-input-request-'))document.getElementById(location.hash.slice(1))?.scrollIntoView({block:'center'});
  } catch(_){delete root.dataset.skillApprovalsLoaded;}
 }
 const scan=()=>{initDetails();document.querySelectorAll('[data-execution-pair][data-exec-id]').forEach(root=>load(root,false));};
 document.addEventListener('DOMContentLoaded',scan);document.addEventListener('htmx:afterSwap',scan);
 document.addEventListener('htmx:configRequest',event=>{
  const target=new URLSearchParams(location.search).get('approval_execution_id');
  if(target && /^\/tasks\/[^/]+\/thread$/.test(event.detail.path || '') && !event.detail.parameters.poll && !event.detail.parameters.before)event.detail.parameters.approval_execution_id=target;
 });
 let scheduled=false;
 new MutationObserver(()=>{if(scheduled)return;scheduled=true;requestAnimationFrame(()=>{scheduled=false;scan();});}).observe(document.documentElement,{childList:true,subtree:true});
 window.addEventListener('sse-task-event',event=>{
  const update=event.detail;if(update?.type!=='skill_dependency_approval')return;
  const root=document.querySelector('[data-execution-pair][data-exec-id="'+CSS.escape(update.exec_id)+'"]');
  if(root)load(root,true);
  if(update.status==='pending')window.showToast?.('Skill needs dependency installation','Waiting for answer',null,{toastKey:'skill-approval-'+update.pending_input_id,clickURL:'/chat/input-requests/'+encodeURIComponent(update.pending_input_id)+'/open?project_id='+encodeURIComponent(update.project_id)});
 });
 scan();
})();

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
  status.textContent = update.phase === 'failed' ? update.message + '. See the skill result for details.' : 'Preparing tool — ' + update.message;
 });
})();

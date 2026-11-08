(async function () {
  const assert = (ok, message) => { if (!ok) throw Error(message); };
  const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
  const until = async (predicate, label) => {
    const deadline = Date.now() + 10000;
    while (!predicate()) { if (Date.now() > deadline) throw Error('Timeout: ' + label); await wait(25); }
  };
  const stage = name => fetch('/browser-stage?name=' + encodeURIComponent(name));
  await stage('waiting for native document');
  await until(() => document.readyState === 'complete' && window.openVibelyNavigate && window.showToast, 'app ready');
  const title = document.getElementById('desktop-project-titlebar');
  const tabs = document.getElementById('desktop-project-tabs');
  assert(title && tabs, 'desktop chrome missing');
  const baseline = [title.getBoundingClientRect().top, tabs.getBoundingClientRect().top];
  assert(baseline[0] >= 0, 'titlebar starts clipped');
  // Sample throughout navigation, not just after it: a transient shell scroll
  // must fail even if later application code moves the shell back into place.
  let displacement = '';
  const stable = () => {
    const actual = [title.getBoundingClientRect().top, tabs.getBoundingClientRect().top];
    if (actual.some((v, i) => Math.abs(v - baseline[i]) > 1) || document.body.scrollTop || document.documentElement.scrollTop ||
        Array.from(document.querySelectorAll('.drawer,.drawer-content')).some(el => el.scrollTop)) {
      displacement = 'shell moved: ' + JSON.stringify({baseline, actual, body: document.body.scrollTop, root: document.documentElement.scrollTop});
    }
  };
  const monitor = setInterval(stable, 16);
  const check = async () => { await wait(250); stable(); assert(!displacement, displacement); };
  const visible = (target, panel) => {
    if (!target || !panel) return false;
    const a = target.getBoundingClientRect(), b = panel.getBoundingClientRect();
    return a.bottom > b.top && a.top < b.bottom;
  };
  try {
    await stage('usage toast click');
    window.showToast('Anthropic weekly usage 95%', 'warning', null, {toastKey:'native-usage', clickURL:'/analytics?project_id=project&view=usage&account_key=anthropic'});
    const toast = document.querySelector('[data-toast-key="native-usage"]');
    assert(toast, 'usage toast missing');
    toast.click();
    await until(() => document.getElementById('usage-account-anthropic')?.getAttribute('aria-busy') === null && !!document.getElementById('usage-account-anthropic'), 'loaded account card');
    const main = document.getElementById('main-content');
    assert(main.scrollTop > 0, 'Analytics did not reveal account');
    assert(visible(document.getElementById('usage-account-anthropic'), main), 'account is outside panel');
    await check();

    await stage('approval link navigation');
    const approvalURL = '/tasks/audit?project_id=project&approval_execution_id=exec#chat-input-request-request';
    await window.openVibelyNavigate(approvalURL);
    const approvalVisible = () => visible(document.getElementById('chat-input-request-request'), document.getElementById('task-thread-messages'));
    await until(approvalVisible, 'approval visible');
    assert(location.hash === '#chat-input-request-request', 'approval fragment lost');
    await check();

    await stage('save and restore approval project tab');
    const select = document.getElementById('project-selector');
    select.value = 'other';
    select.dispatchEvent(new Event('change', {bubbles:true}));
    await until(() => !!document.getElementById('other-project'), 'other project');
    await check();
    const tab = document.querySelector('[data-project-tab="project"]');
    assert(tab, 'saved project tab missing');
    tab.click();
    await until(() => location.hash === '#chat-input-request-request' && approvalVisible(), 'saved approval URL restored');
    await check();

    await stage('nested sidebar keyboard navigation');
    await until(() => !!document.querySelector('[data-task-list-row="scheduled-24"]'), 'sidebar loaded');
    document.querySelectorAll('.task-list-group').forEach(group => group.open = true);
    document.querySelectorAll('.task-list-more').forEach(button => button.click());
    const outer = document.getElementById('task-list-groups');
    // A short sidebar reproduces a small window without platform-specific
    // native resize APIs. All markup and keyboard handlers remain production.
    outer.style.maxHeight = '400px';
    outer.scrollTop = 0;
    document.querySelector('[data-task-list-row="scheduled-24"]').closest('.task-list-rows').scrollTop = 0;
    await wait(50);
    assert(document.querySelector('[data-task-list-row="scheduled-24"]').getBoundingClientRect().top > outer.getBoundingClientRect().bottom, 'nested target must start outside sidebar');
    history.replaceState({}, '', '/tasks/scheduled-23?project_id=project');
    const apple = /Mac|iPhone|iPad/.test(navigator.platform);
    document.body.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowDown',code:'ArrowDown',shiftKey:true,metaKey:apple,ctrlKey:!apple,bubbles:true}));
    await until(() => !!document.querySelector('[data-task-list-row="scheduled-24"][aria-current="page"]'), 'keyboard selection');
    await until(() => {
      const row = document.querySelector('[data-task-list-row="scheduled-24"]');
      const a = row.getBoundingClientRect(), b = outer.getBoundingClientRect();
      return outer.scrollTop > 0 && row.closest('.task-list-rows').scrollTop > 0 && a.top >= b.top - 1 && a.bottom <= b.bottom + 1;
    }, 'selected row fully inside nested sidebar');
    await check();
    await stage('native panel checks passed');
    await fetch('/browser-result?status=pass');
  } finally { clearInterval(monitor); }
})().catch(error => fetch('/browser-result?status=' + encodeURIComponent(String(error) + '\n' + JSON.stringify({url:location.href, project:document.getElementById('project-selector')?.value, approval:!!document.getElementById('chat-input-request-request'), threadScroll:document.getElementById('task-thread-messages')?.scrollTop}) + '\n' + (error.stack || ''))));

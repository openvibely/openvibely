package components

import "testing"

func TestBrowserFunctional_TranscriptLayoutReleasesRemovedViews(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const NativeResizeObserver = window.ResizeObserver;
 const NativeMutationObserver = window.MutationObserver;
 const resizeTargets = new Set(), mutationTargets = new Set();
 window.ResizeObserver = class extends NativeResizeObserver {
  constructor(fn) { super(fn); this.targets = new Set(); }
  observe(target, options) { this.targets.add(target); resizeTargets.add(target); super.observe(target, options); }
  unobserve(target) { this.targets.delete(target); resizeTargets.delete(target); super.unobserve(target); }
  disconnect() { this.targets.forEach(target => resizeTargets.delete(target)); this.targets.clear(); super.disconnect(); }
 };
 window.MutationObserver = class extends NativeMutationObserver {
  constructor(fn) { super(fn); this.targets = new Set(); }
  observe(target, options) { this.targets.add(target); mutationTargets.add(target); super.observe(target, options); }
  disconnect() { this.targets.forEach(target => mutationTargets.delete(target)); this.targets.clear(); super.disconnect(); }
 };
 const tracker = {shouldAutoScroll: () => false};
 for (let visit = 0; visit < 12; visit++) {
  const messages = document.createElement('div');
  messages.style.cssText = 'height:100px;overflow:auto';
  document.body.appendChild(messages);
  window.observeChatTranscriptLayout({messages, tracker});
  for (let revision = 0; revision < 5; revision++) {
   const pair = document.createElement('div');
   pair.dataset.executionPair = 'true'; pair.textContent = 'revision ' + revision;
   messages.replaceChildren(pair);
   await paint();
   assert(resizeTargets.size === 2 && resizeTargets.has(messages) && resizeTargets.has(pair), 'resize observer retained a replaced pair');
  }
  // Reinitialization must release the previous observers and pending work.
  window.observeChatTranscriptLayout({messages, tracker});
  await paint();
  assert(resizeTargets.size === 2, 'reinitialization accumulated resize targets');
  messages.remove();
  await paint();
  assert(resizeTargets.size === 0, 'removed transcript still observed for resize');
  assert(!mutationTargets.has(messages), 'removed transcript still observed for mutations');
  assert(!tracker.onIntentChange, 'removed transcript retained by tracker callback');
 }
 return {visits:12, revisions:60};
 `)
}

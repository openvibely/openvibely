// Fit project pages into the remaining navigation space; keep overflow links
// in a top-layer menu so the scrolling rail cannot clip them.
(function () {
 'use strict';
 let nav, inner, more, menu, frame, resize, mutation, hiddenEntries = [];
 function close(focus) {
  if (!menu) return;
  if (typeof menu.hidePopover === 'function' && menu.matches(':popover-open')) menu.hidePopover();
  menu.hidden = true;
  if (more) {
   more.querySelector('button').setAttribute('aria-expanded', 'false');
   if (focus) more.querySelector('button').focus();
  }
 }
 function fit() {
  frame = null;
  if (!nav || !nav.isConnected || !nav.getClientRects().length) return;
  close(false);
  const entries = Array.from(nav.querySelectorAll('[data-plugin-nav-entry]'));
  more = nav.querySelector('[data-plugin-nav-overflow]');
  if (!more) return;
  entries.forEach(e => { e.hidden = false; });
  more.hidden = true;
  hiddenEntries = [];
  const bottom = inner.getBoundingClientRect().bottom - parseFloat(getComputedStyle(inner).paddingBottom || 0);
  // Use scroll-independent coordinates: opening a menu must not change which
  // links fit merely because the navigation was previously scrolled.
  const end = e => e.getBoundingClientRect().bottom + inner.scrollTop;
  if (!entries.length || end(entries[entries.length - 1]) <= bottom) return;
  more.hidden = false;
  for (let i = entries.length - 1; i >= 0 && end(more) > bottom; i--) {
   entries[i].hidden = true;
   hiddenEntries.unshift(entries[i]);
  }
 }
 function schedule() {
  if (!frame) frame = requestAnimationFrame(fit);
 }
 function open() {
  if (!hiddenEntries.length) return;
  if (!menu.hidden) { close(true); return; }
  menu.replaceChildren();
  hiddenEntries.forEach(entry => {
   const original = entry.querySelector('a');
   const link = original.cloneNode(true);
   link.removeAttribute('data-tip');
   link.removeAttribute('data-tip-side');
   link.className = 'ov-menu-option';
   link.setAttribute('role', 'menuitem');
   link.querySelectorAll('.sidebar-text').forEach(el => el.classList.remove('sidebar-text'));
   const row = document.createElement('li');
   row.setAttribute('role', 'none'); row.append(link); menu.append(row);
  });
  if (window.htmx) window.htmx.process(menu);
  menu.hidden = false;
  if (menu.showPopover) menu.showPopover();
  const rect = more.getBoundingClientRect();
  menu.style.left = Math.max(8, Math.min(rect.right + 8, window.innerWidth - menu.offsetWidth - 8)) + 'px';
  menu.style.top = Math.max(8, Math.min(rect.top, window.innerHeight - menu.offsetHeight - 8)) + 'px';
  more.querySelector('button').setAttribute('aria-expanded', 'true');
  menu.querySelector('a').focus();
 }
 function init() {
  const current = document.getElementById('plugin-pages-nav');
  if (!current) return;
  if (current === nav) { schedule(); return; }
  if (resize) resize.disconnect();
  if (mutation) mutation.disconnect();
  close(false);
  nav = current; inner = nav.closest('.sidebar-inner');
  if (!inner) return;
  if (!menu) {
   menu = document.createElement('ul');
   menu.id = 'plugin-pages-overflow-menu';
   menu.className = 'ov-menu ov-menu-actions menu';
   menu.setAttribute('role', 'menu'); menu.setAttribute('aria-label', 'More pages');
   if (typeof menu.showPopover === 'function') menu.setAttribute('popover', 'auto');
   menu.hidden = true;
   menu.style.cssText = 'position:fixed;inset:auto;margin:0;width:280px;max-width:calc(100vw - 16px);max-height:calc(100vh - 16px);overflow:auto;z-index:1000;';
   document.body.append(menu);
   menu.addEventListener('toggle', e => {
    if (e.newState === 'closed') { menu.hidden = true; if (more) more.querySelector('button').setAttribute('aria-expanded', 'false'); }
   });
   menu.addEventListener('click', e => { if (e.target.closest('a')) close(false); });
   menu.addEventListener('keydown', e => {
    const links = Array.from(menu.querySelectorAll('a'));
    let i = links.indexOf(document.activeElement);
    if (e.key === 'Escape') { e.preventDefault(); close(true); }
    else if (e.key === 'Tab') close(false);
    else if (['ArrowDown','ArrowUp','Home','End'].includes(e.key)) {
     e.preventDefault();
     i = e.key === 'Home' ? 0 : e.key === 'End' ? links.length - 1 : (i + (e.key === 'ArrowDown' ? 1 : -1) + links.length) % links.length;
     links[i].focus();
    }
   });
   document.addEventListener('pointerdown', e => { if (!menu.contains(e.target) && more && !more.contains(e.target)) close(false); });
  }
  nav.addEventListener('click', e => { if (e.target.closest('[data-plugin-nav-overflow] button')) open(); });
  nav.addEventListener('keydown', e => { if (e.key === 'ArrowDown' && e.target.closest('[data-plugin-nav-overflow] button')) { e.preventDefault(); open(); } });
  resize = new ResizeObserver(schedule); resize.observe(inner); resize.observe(nav);
  mutation = new MutationObserver(schedule); mutation.observe(nav, {childList:true, subtree:true});
  schedule();
 }
 document.addEventListener('DOMContentLoaded', init);
 document.addEventListener('htmx:afterSwap', function(event) {
  const target = event.detail && event.detail.target;
  if (!nav || !nav.isConnected || (target && (target === nav || target.contains(nav)))) init();
 });
 window.addEventListener('resize', schedule);
 if (document.readyState !== 'loading') init();
})();

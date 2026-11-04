// Pointer boundary for menus whose DOM ancestry is needed by delegated actions.
// A transparent top-layer surface covers everything except the live menu panels.
// Unlike click cancellation alone, it also blocks background :hover and cursors.
(function () {
  if (window.ovMenuBackdrop) return;
  window.ovMenuBackdrop = true;
  const selector = [
    '.dropdown > .dropdown-content', '[data-menu-content]',
    '[data-menu-submenu-portaled="true"]',
    '#conversation-model-picker', '#conversation-provider-models',
    '#chat-custom-select-panel', '#project-tab-menu',
    '.inspector-picker', '.schedule-datetime-popup',
    '#changes-actions-menu', '#schedule-context-menu',
    '#schedule-project-menu[open] > .ov-menu', '[data-project-results]'
  ].join(',');
  const backdrop = document.createElement('div');
  backdrop.id = 'ov-menu-backdrop';
  backdrop.setAttribute('popover', 'manual');
  backdrop.setAttribute('aria-hidden', 'true');
  let frame = 0, lastGeometry = '', panels = [], dirty = true;
  function schedule() {
    dirty = true;
    if (!frame) frame = requestAnimationFrame(sync);
  }
  function visible(panel, modal) {
    if (!panel.isConnected || (modal && !modal.contains(panel)) || panel.closest('[hidden],[inert]')) return false;
    const style = getComputedStyle(panel);
    return panel.getClientRects().length && style.visibility === 'visible';
  }
  function sync() {
    frame = 0;
    const modals = document.querySelectorAll('dialog:modal');
    const modal = modals[modals.length - 1];
    // Avoid scanning every task-card menu on every animation frame.
    if (dirty) { panels = Array.from(document.querySelectorAll(selector)); dirty = false; }
    panels = panels.filter(panel => visible(panel, modal));
    const open = panels;
    if (!open.length) {
      if (backdrop.matches(':popover-open')) backdrop.hidePopover();
      lastGeometry = '';
      return;
    }
    const parent = modal || document.body;
    if (backdrop.parentElement !== parent) parent.append(backdrop);
    // The project search input is part of its menu, not a dismissal target.
    const surfaces = open.map(panel => panel.matches('[data-project-results]') ? panel.closest('[data-project-picker]') : panel);
    const width = innerWidth, height = innerHeight;
    const rects = surfaces.map(panel => panel.getBoundingClientRect()).map(r => ({
      left: Math.max(0, r.left), right: Math.min(width, r.right),
      top: Math.max(0, r.top), bottom: Math.min(height, r.bottom)
    })).filter(r => r.right > r.left && r.bottom > r.top);
    const geometry = JSON.stringify([width, height, rects]);
    if (geometry !== lastGeometry) {
      lastGeometry = geometry;
      // Partition into horizontal strips, subtracting the UNION of panel bounds.
      // This keeps overlapping menus usable, without reparenting their contents
      // or changing their focus, positioning, form ownership, or HTMX ancestry.
      const edges = Array.from(new Set([0, height, ...rects.flatMap(r => [r.top, r.bottom])])).sort((a, b) => a - b);
      const tiles = [];
      function tile(left, top, right, bottom) {
        if (right <= left || bottom <= top) return;
        const div = document.createElement('div');
        div.style.cssText = `left:${left}px;top:${top}px;width:${right-left}px;height:${bottom-top}px`;
        tiles.push(div);
      }
      for (let i = 1; i < edges.length; i++) {
        const top = edges[i-1], bottom = edges[i];
        const spans = rects.filter(r => r.top < bottom && r.bottom > top).sort((a, b) => a.left - b.left);
        let left = 0;
        for (const span of spans) {
          tile(left, top, span.left, bottom);
          left = Math.max(left, span.right);
        }
        tile(left, top, width, bottom);
      }
      backdrop.replaceChildren(...tiles);
    }
    if (!backdrop.matches(':popover-open')) backdrop.showPopover();
    // Track layout transitions, scrolling and submenu movement while open only.
    frame = requestAnimationFrame(sync);
  }
  // Dismissal remains owned by each menu. Its outside-click listener receives
  // this surface as the target, so no underlying control can activate.
  backdrop.addEventListener('pointerdown', event => event.preventDefault());
  const observer = new MutationObserver(records => {
    if (records.some(record => record.target !== backdrop && !backdrop.contains(record.target))) schedule();
  });
  observer.observe(document.documentElement, {subtree: true, childList: true, attributes: true,
    attributeFilter: ['class', 'style', 'hidden', 'inert', 'open', 'data-menu-open', 'data-kanban-menu-open', 'data-menu-dismissed']});
  ['focusin', 'focusout', 'toggle', 'transitionrun', 'transitionend'].forEach(name => document.addEventListener(name, schedule, true));
  schedule();
})();

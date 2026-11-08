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
  const nativePopover = typeof backdrop.showPopover === 'function' && typeof backdrop.hidePopover === 'function';
  if (nativePopover) backdrop.setAttribute('popover', 'manual');
  else {
    backdrop.style.display = 'none';
    backdrop.style.zIndex = '2147483647';
  }
  function backdropOpen() {
    return nativePopover ? backdrop.matches(':popover-open') : backdrop.style.display === 'block';
  }
  function setBackdropOpen(open) {
    if (!nativePopover) { backdrop.style.display = open ? 'block' : 'none'; return; }
    if (open && !backdropOpen()) backdrop.showPopover();
    else if (!open && backdropOpen()) backdrop.hidePopover();
  }
  backdrop.setAttribute('aria-hidden', 'true');
  let frame = 0, lastGeometry = '', panels = [], dirty = true, pressHeld = false;
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
      setBackdropOpen(false);
      lastGeometry = '';
      return;
    }
    // A press that focuses a trigger opens its menu before release. Raising the
    // surface mid-press would retarget the release and dismiss the new menu.
    if (pressHeld && !backdropOpen()) return;
    const parent = modal || document.body;
    if (backdrop.parentElement !== parent) parent.append(backdrop);
    // The project search input is part of its menu, not a dismissal target.
    const surfaces = open.map(panel => panel.matches('[data-project-results]') ? panel.closest('[data-project-picker]') : panel);
    const width = innerWidth, height = innerHeight;
    const shapes = surfaces.map(panel => {
      const r = panel.getBoundingClientRect(), style = getComputedStyle(panel);
      const radius = value => {
        const parts = value.split(' ');
        const length = (part, size) => parseFloat(part) * (part.endsWith('%') ? size / 100 : 1);
        return [length(parts[0], r.width), length(parts[1] || parts[0], r.height)];
      };
      const corners = [style.borderTopLeftRadius, style.borderTopRightRadius,
        style.borderBottomRightRadius, style.borderBottomLeftRadius].map(radius);
      // CSS scales all corner radii together when their sums exceed a side.
      const [tl, tr, br, bl] = corners;
      const scale = Math.min(1, r.width / (tl[0] + tr[0]), r.width / (bl[0] + br[0]),
        r.height / (tl[1] + bl[1]), r.height / (tr[1] + br[1]));
      return {left:r.left, top:r.top, right:r.right, bottom:r.bottom,
        corners:corners.map(pair => pair.map(n => n * scale))};
    }).filter(r => r.right > r.left && r.bottom > r.top);
    const geometry = JSON.stringify([width, height, shapes]);
    // Panels animate open, so geometry can change every frame. Replacing the
    // pressed hit element would retarget the release and suppress its click.
    if (geometry !== lastGeometry && !backdrop.querySelector('[data-menu-backdrop-hit]:active')) {
      lastGeometry = geometry;
      // Each clip removes one rounded panel from the viewport. Nesting clips
      // intersects their outside regions, so overlapping panels remain usable.
      // Unlike rectangular holes, this also covers the transparent corners.
      const fragment = document.createDocumentFragment();
      let parent = fragment;
      for (const r of shapes) {
        const [tl, tr, br, bl] = r.corners, l = r.left, t = r.top, b = r.bottom, right = r.right;
        const path = `M0 0H${width}V${height}H0Z ` +
          `M${l+tl[0]} ${t}H${right-tr[0]}A${tr[0]} ${tr[1]} 0 0 1 ${right} ${t+tr[1]}` +
          `V${b-br[1]}A${br[0]} ${br[1]} 0 0 1 ${right-br[0]} ${b}` +
          `H${l+bl[0]}A${bl[0]} ${bl[1]} 0 0 1 ${l} ${b-bl[1]}` +
          `V${t+tl[1]}A${tl[0]} ${tl[1]} 0 0 1 ${l+tl[0]} ${t}Z`;
        const layer = document.createElement('div');
        layer.style.clipPath = `path(evenodd, "${path}")`;
        parent.append(layer);
        parent = layer;
      }
      const hit = document.createElement('div');
      hit.setAttribute('data-menu-backdrop-hit', '');
      parent.append(hit);
      backdrop.replaceChildren(fragment);
    }
    setBackdropOpen(true);
    // Track layout transitions, scrolling and submenu movement while open only.
    frame = requestAnimationFrame(sync);
  }
  // A pointer backdrop alone is not a keyboard boundary. Keep Tab navigation
  // within the live menu surfaces (including provider submenus and search).
  // Do not intercept focus restoration or dialogs opened by menu actions.
  window.addEventListener('keydown', event => {
    if (event.key !== 'Tab' || event.altKey || event.ctrlKey || event.metaKey) return;
    const modals = document.querySelectorAll('dialog:modal');
    const modal = modals[modals.length - 1];
    const open = Array.from(document.querySelectorAll(selector)).filter(panel => visible(panel, modal));
    if (!open.length) return;
    const surfaces = open.map(panel => panel.matches('[data-project-results]') ? panel.closest('[data-project-picker]') : panel);
    const candidates = new Set();
    for (const surface of surfaces) {
      if (surface.tabIndex >= 0) candidates.add(surface);
      surface.querySelectorAll('a[href],button,input,select,textarea,summary,iframe,[tabindex],[contenteditable]').forEach(el => candidates.add(el));
    }
    const items = Array.from(candidates).filter(el => el.tabIndex >= 0 && !el.matches(':disabled') && visible(el, modal));
    const tabbable = items.filter(el => {
      if (!el.matches('input[type="radio"]') || !el.name) return true;
      const group = items.filter(other => other.matches('input[type="radio"]') && other.name === el.name && other.form === el.form);
      return el === (group.find(other => other.checked) || group[0]);
    }).sort((a, b) => {
      const priority = (a.tabIndex || Infinity) - (b.tabIndex || Infinity);
      return priority || (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1);
    });
    event.preventDefault();
    event.stopImmediatePropagation();
    if (!tabbable.length) {
      if (!surfaces[0].hasAttribute('tabindex')) surfaces[0].tabIndex = -1;
      surfaces[0].focus({preventScroll:true});
      return;
    }
    const index = tabbable.indexOf(document.activeElement);
    const next = index < 0 ? (event.shiftKey ? tabbable.length - 1 : 0) :
      (index + (event.shiftKey ? -1 : 1) + tabbable.length) % tabbable.length;
    tabbable[next].focus();
  }, true);
  // The shared lifecycle receives this surface as the outside-click target,
  // so no underlying control can activate.
  backdrop.addEventListener('pointerdown', event => event.preventDefault());
  window.addEventListener('pointerdown', event => {
    if (event.button === 0 && !backdrop.contains(event.target)) pressHeld = true;
  }, true);
  const releasePress = event => {
    if (!pressHeld || (event.type === 'blur' && event.target !== window)) return;
    pressHeld = false;
    schedule();
  };
  // Keep the opening press protected through click, not just pointerup. WebViews
  // can paint between release and click; exposing the backdrop in that gap
  // retargets the opening click to the dismissal surface and closes the menu.
  ['click', 'pointercancel', 'blur'].forEach(name => window.addEventListener(name, releasePress, true));
  // A cancelled/dragged press may not produce a click. Recover on the next hover.
  window.addEventListener('pointermove', event => {
    if (event.buttons === 0) releasePress(event);
  }, true);
  const observer = new MutationObserver(records => {
    if (records.some(record => record.target !== backdrop && !backdrop.contains(record.target))) schedule();
  });
  observer.observe(document.documentElement, {subtree: true, childList: true, attributes: true,
    attributeFilter: ['class', 'style', 'hidden', 'inert', 'open', 'data-menu-open', 'data-kanban-menu-open', 'data-menu-dismissed']});
  ['focusin', 'focusout', 'toggle', 'transitionrun', 'transitionend'].forEach(name => document.addEventListener(name, schedule, true));
  schedule();
})();

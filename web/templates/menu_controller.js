// Shared popup lifecycle. Adapters own rendering, positioning and selection only.
(function () {
  if (window.ovMenuController) return;
  const active = new Set();
  const value = source => typeof source === 'function' ? source() : source;
  function create(config) {
    let opened = false;
    const handle = {
      trigger: () => value(config.trigger),
      panels: () => (value(config.panels) || []).filter(Boolean),
      isOpen: () => config.isOpen ? !!config.isOpen() : opened,
      contains(target) {
        return !!(handle.trigger()?.contains(target) || handle.panels().some(panel => panel.contains(target)) || config.contains?.(target));
      },
      sync() {
        if (handle.isOpen()) active.add(handle); else active.delete(handle);
      },
      open(event) {
        if (handle.isOpen()) { handle.sync(); return; }
        const trigger = handle.trigger();
        if (trigger && (!trigger.isConnected || trigger.disabled || trigger.getAttribute('aria-disabled') === 'true')) return;
        window.syncOpenDropdownMenus?.();
        for (const other of Array.from(active)) if (other !== handle) other.close(false);
        opened = true;
        active.add(handle);
        try { config.open(event); } catch (error) { opened = false; active.delete(handle); throw error; }
        if (trigger) trigger.setAttribute('aria-expanded', 'true');
        handle.sync();
      },
      close(restoreFocus = false) {
        const trigger = handle.trigger();
        const wasOpen = handle.isOpen();
        opened = false;
        active.delete(handle);
        if (wasOpen) config.close();
        if (trigger) trigger.setAttribute('aria-expanded', 'false');
        if (restoreFocus && trigger?.isConnected) {
          if (config.restoreFocus) config.restoreFocus(trigger);
          else trigger.focus({preventScroll: true});
        }
      },
      toggle(event) { if (handle.isOpen()) handle.close(true); else handle.open(event); },
      escape(event) {
        if (config.escape && config.escape(event) === false) return;
        handle.close(true);
      },
      destroy() {
        if (config.detach) { opened = false; active.delete(handle); config.detach(); }
        else handle.close(false);
      },
    };
    return handle;
  }
  function menus() {
    window.syncOpenDropdownMenus?.();
    return Array.from(active).filter(menu => {
      if (!menu.isOpen()) { active.delete(menu); return false; }
      const trigger = menu.trigger();
      if ((trigger && !trigger.isConnected) || !menu.panels().some(panel => panel.isConnected)) {
        menu.destroy(); return false;
      }
      const modal = document.querySelector('dialog:modal');
      return !modal || modal.contains(trigger) || menu.panels().some(panel => modal === panel || modal.contains(panel));
    });
  }
  function outside(event) {
    if (event.type === 'pointerdown' && event.button !== 0) return;
    const outsideMenus = menus().filter(menu => !menu.contains(event.target));
    if (!outsideMenus.length) return;
    // Leave the layer in place through release; consume both halves of dismissal.
    event.preventDefault();
    event.stopImmediatePropagation();
    if (event.type === 'click') outsideMenus.forEach(menu => menu.close(false));
  }
  window.addEventListener('pointerdown', outside, true);
  window.addEventListener('click', outside, true);
  // Menu Escape takes precedence over page shortcuts; adapters can dismiss a nested panel first.
  window.addEventListener('keydown', event => {
    if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || event.key !== 'Escape') return;
    const menu = menus().at(-1);
    if (!menu) return;
    event.preventDefault(); event.stopImmediatePropagation(); menu.escape(event);
  }, true);
  document.addEventListener('cancel', event => {
    const menu = menus().find(menu => menu.panels().some(panel => panel === event.target || event.target.contains(panel)));
    if (!menu) return;
    event.preventDefault(); menu.close(true);
  }, true);
  document.addEventListener('htmx:beforeCleanupElement', event => {
    const node = event.detail?.elt || event.target;
    for (const menu of Array.from(active)) {
      if (node === menu.trigger() || node.contains(menu.trigger()) || menu.panels().some(panel => node === panel || node.contains(panel))) menu.destroy();
    }
  });
  document.addEventListener('htmx:beforeHistorySave', () => {
    menus().forEach(menu => menu.close(false));
  });
  new MutationObserver(() => {
    for (const menu of Array.from(active)) {
      if ((menu.trigger() && !menu.trigger().isConnected) || !menu.panels().some(panel => panel.isConnected)) menu.destroy();
      else if (!menu.isOpen()) active.delete(menu);
    }
  }).observe(document.documentElement, {childList: true, subtree: true});
  window.ovMenuController = {create};
})();

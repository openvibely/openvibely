(function () {
  'use strict';

  // Reveal a target in ONE panel. Never scroll the document or hidden shell
  // ancestors, as Element.scrollIntoView does (including in WKWebView).
  // Pass {container: panel} when known; otherwise use the nearest auto/scroll
  // ancestor. block/inline accept start, center, end, or nearest (the default).
  // Call focus({preventScroll: true}) separately when revealing keyboard focus.
  window.openVibelyScrollIntoPanel = function (target, options) {
    options = options || {};
    if (!target || !target.isConnected) return false;
    var panel = options.container;
    var scrollable = function (value) { return value === 'auto' || value === 'scroll'; };
    if (!panel) {
      for (var parent = target.parentElement; parent && parent !== document.body && parent !== document.documentElement; parent = parent.parentElement) {
        var style = getComputedStyle(parent);
        if (scrollable(style.overflowY) || scrollable(style.overflowX)) { panel = parent; break; }
      }
    }
    if (!panel || panel === document.body || panel === document.documentElement || panel === target || !panel.contains(target)) return false;
    var css = getComputedStyle(panel);
    var box = panel.getBoundingClientRect();
    var item = target.getBoundingClientRect();
    function delta(start, end, size, align) {
      if (align === 'start') return start;
      if (align === 'end') return end - size;
      // Keep the beginning of oversized content readable.
      if (align === 'center') return start - Math.max(0, (size - (end - start)) / 2);
      if (start < 0 && end > size) return 0;
      if (start < 0) return end - start > size ? end - size : start;
      if (end > size) return end - start > size ? start : end - size;
      return 0;
    }
    var top = panel.scrollTop;
    var left = panel.scrollLeft;
    if (scrollable(css.overflowY)) top += delta(item.top - box.top - panel.clientTop, item.bottom - box.top - panel.clientTop, panel.clientHeight, options.block || 'nearest');
    if (scrollable(css.overflowX)) left += delta(item.left - box.left - panel.clientLeft, item.right - box.left - panel.clientLeft, panel.clientWidth, options.inline || 'nearest');
    panel.scrollTo({top: top, left: left, behavior: options.behavior || 'instant'});
    return true;
  };
})();

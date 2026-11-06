(function () {
  'use strict';

  // Reveal through nested content panels, never through the document or shell.
  // An explicit container is an inclusive boundary; otherwise stop at the page,
  // sidebar, tab strip, or a fixed overlay. Hidden/clip ancestors never scroll.
  // block/inline accept start, center, end, or nearest (the default).
  // Call focus({preventScroll: true}) separately when revealing keyboard focus.
  window.openVibelyScrollIntoPanel = function (target, options) {
    options = options || {};
    if (!target || !target.isConnected) return false;
    var boundary = options.container;
    if (boundary && (boundary === document.body || boundary === document.documentElement || boundary === target || !boundary.contains(target))) return false;
    var scrollable = function (value) { return value === 'auto' || value === 'scroll'; };
    var panels = [];
    for (var parent = target.parentElement; parent && parent !== document.body && parent !== document.documentElement; parent = parent.parentElement) {
      if (parent.matches('.drawer, .drawer-content, #desktop-project-titlebar')) break;
      var css = getComputedStyle(parent);
      if (scrollable(css.overflowY) || scrollable(css.overflowX)) panels.push({element: parent, css: css});
      if (parent === boundary || parent.matches('#main-content, #task-list-groups, #desktop-project-tabs') || css.position === 'fixed') break;
    }
    var rect = target.getBoundingClientRect();
    var item = {top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right};
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
    panels.forEach(function (entry) {
      var panel = entry.element, css = entry.css, box = panel.getBoundingClientRect();
      var oldTop = panel.scrollTop, oldLeft = panel.scrollLeft;
      var top = oldTop, left = oldLeft;
      if (scrollable(css.overflowY)) top += delta(item.top - box.top - panel.clientTop, item.bottom - box.top - panel.clientTop, panel.clientHeight, options.block || 'nearest');
      if (scrollable(css.overflowX)) left += delta(item.left - box.left - panel.clientLeft, item.right - box.left - panel.clientLeft, panel.clientWidth, options.inline || 'nearest');
      top = Math.max(0, Math.min(top, panel.scrollHeight - panel.clientHeight));
      var maxLeft = panel.scrollWidth - panel.clientWidth;
      left = css.direction === 'rtl' ? Math.max(-maxLeft, Math.min(left, 0)) : Math.max(0, Math.min(left, maxLeft));
      panel.scrollTo({top: top, left: left, behavior: options.behavior || 'instant'});
      // Use the final position even while a smooth scroll is still animating,
      // so outer panels reveal where the target will actually end up.
      item.top -= top - oldTop; item.bottom -= top - oldTop;
      item.left -= left - oldLeft; item.right -= left - oldLeft;
    });
    return panels.length > 0;
  };

  // HTMX also reveals URL fragments with native scrolling after a swap. Take
  // ownership before it captures the anchor, preserving the URL/history while
  // revealing the new content through the same shell-safe path as app code.
  var anchors = new WeakMap();
  document.addEventListener('htmx:beforeSwap', function (event) {
    var detail = event.detail;
    var path = detail && detail.pathInfo;
    // Cached thread previews share the pending request's pathInfo, but do not
    // perform HTMX anchor scrolling. Leave that request intact until its real
    // response: HTMX has not computed the history URL yet during the preview.
    if (event.defaultPrevented || detail._ovCachedPreview || !detail.shouldSwap || !path || !path.anchor) return;
    anchors.set(path, path.anchor);
    path.anchor = null;
  });
  document.addEventListener('htmx:afterSettle', function (event) {
    var path = event.detail && event.detail.pathInfo;
    if (!path || !anchors.has(path)) return;
    var id = anchors.get(path);
    anchors.delete(path);
    try { id = decodeURIComponent(id); } catch (_) {}
    // Asynchronously loaded approval cards reveal themselves when they arrive.
    window.openVibelyScrollIntoPanel(document.getElementById(id), {block: 'start'});
  });
})();

// Faint dither backdrop for empty Chat and New Task threads. Dots drift out from
// a cluster above the composer once per page render, then stay static.
(function() {
  if (window._ovThreadEmptyStage) return;
  window._ovThreadEmptyStage = true;

  var bayer = [[0, 8, 2, 10], [12, 4, 14, 6], [3, 11, 1, 9], [15, 7, 13, 5]].map(function(r) { return r.map(function(v) { return (v + .5) / 16; }); });
  var images = {};
  var reduced = window.matchMedia ? window.matchMedia('(prefers-reduced-motion: reduce)') : null;

  function noise(x, y) {
    return (Math.sin(x * .011 + Math.sin(y * .007) * 2.1) + Math.sin(y * .013 - x * .004) + Math.sin((x + y) * .006)) / 6 + .5;
  }

  function host(stage) { return stage.closest('#tab-chat, #chat-page-root'); }
  function composer(stage) { var h = host(stage); return h && h.querySelector('.chat-input-shadow-gutter'); }

  // Cover the host from the transcript top down, bleeding into main's padding.
  function place(stage) {
    var h = host(stage), cb = stage.offsetParent;
    if (!h || !cb) return null;
    var hr = h.getBoundingClientRect(), cr = cb.getBoundingClientRect();
    var tr = stage.parentElement.parentElement.getBoundingClientRect();
    var main = stage.closest('#main-content');
    var mr = main ? main.getBoundingClientRect() : hr;
    stage.style.top = Math.round(tr.top - cr.top) + 'px';
    stage.style.left = Math.round(Math.max(mr.left, hr.left - 24) - cr.left) + 'px';
    stage.style.right = Math.round(cr.right - Math.min(mr.right, hr.right + 24)) + 'px';
    stage.style.bottom = Math.round(cr.bottom - Math.min(mr.bottom, hr.bottom + 24)) + 'px';
    var r = stage.getBoundingClientRect();
    if (!r.width || !r.height) return null;
    var canvas = stage.querySelector('canvas');
    var dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(r.width * dpr);
    canvas.height = Math.round(r.height * dpr);
    var ctx = canvas.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    return {ctx: ctx, r: r, W: Math.ceil(r.width), H: Math.ceil(r.height)};
  }

  function dotColor() {
    return document.documentElement.getAttribute('data-theme') === 'light' ? 'rgba(89,102,242,.32)' : 'rgba(116,128,255,.36)';
  }

  function dots(img, W, H, plot) {
    var size = H * 1.08, cx = W / 2, cy = H * .44, step = 5;
    var off = document.createElement('canvas');
    off.width = W; off.height = H;
    var o = off.getContext('2d');
    o.drawImage(img, cx - size / 2, cy - size / 2, size, size);
    var px = o.getImageData(0, 0, W, H).data;
    var seed = 7;
    function rnd() { return (seed = (seed * 16807) % 2147483647) / 2147483647; }
    for (var y = 0; y < H; y += step) {
      for (var x = 0; x < W; x += step) {
        var i = (y * W + x) * 4;
        var a = px[i + 3] / 255;
        var lum = (px[i] * .3 + px[i + 1] * .59 + px[i + 2] * .11) / 255;
        var v = a * (.25 + lum * .95);
        var d = Math.hypot((x - cx) / (W * .62), (y - cy) / (H * .72));
        v = Math.max(v, Math.max(0, noise(x, y) - .6) * Math.max(0, 1 - d));
        if (v > bayer[(y / step) & 3][(x / step) & 3] + (rnd() - .5) * .12) plot(x, y);
      }
    }
    return {cx: cx, cy: cy};
  }

  function drawStatic(stage) {
    var img = images[stage.dataset.src];
    var g = img && img.complete && place(stage);
    if (!g) return;
    g.ctx.fillStyle = dotColor();
    dots(img, g.W, g.H, function(x, y) { g.ctx.fillRect(x, y, 1.6, 1.6); });
    stage.querySelector('canvas').style.opacity = '';
  }

  function play(stage) {
    var img = images[stage.dataset.src];
    var g = place(stage);
    if (!g) return;
    var form = composer(stage);
    var fr = form ? form.getBoundingClientRect() : null;
    var asize = 192;
    var ax = g.W / 2;
    var ay = fr ? fr.top - g.r.top - 40 - asize / 2 : g.H * .3;
    var list = [];
    var geo = dots(img, g.W, g.H, function(x, y) { list.push(x, y); });
    var k = asize / (g.H * 1.08), maxR = asize * .42, diag = Math.hypot(g.W, g.H);
    var parts = [];
    for (var i = 0; i < list.length; i += 2) {
      var x = list[i], y = list[i + 1];
      var ox = (x - geo.cx) * k, oy = (y - geo.cy) * k, len = Math.hypot(ox, oy);
      if (len > maxR) { var s = maxR * Math.random() / len; ox *= s; oy *= s; }
      parts.push(x, y, ax + ox, ay + oy, Math.hypot(x - geo.cx, y - geo.cy) / diag * .45 + Math.random() * .25);
    }
    var canvas = stage.querySelector('canvas');
    var color = dotColor();
    var t0 = performance.now(), dur = 1500;
    function frame(now) {
      if (!stage.isConnected || stage.hidden) return;
      var T = (now - t0) / dur;
      g.ctx.clearRect(0, 0, g.W, g.H);
      g.ctx.fillStyle = color;
      for (var j = 0; j < parts.length; j += 5) {
        var t = Math.max(0, Math.min(1, (T - parts[j + 4]) / .6));
        if (t <= 0) continue;
        var e = 1 - Math.pow(1 - t, 3);
        g.ctx.fillRect(parts[j + 2] + (parts[j] - parts[j + 2]) * e, parts[j + 3] + (parts[j + 1] - parts[j + 3]) * e, 1.6, 1.6);
      }
      canvas.style.opacity = String(1 - .45 * (1 - Math.pow(1 - Math.min(1, T), 3)));
      if (T < 1.35) requestAnimationFrame(frame);
      else { stage._ovPlaying = false; drawStatic(stage); }
    }
    stage._ovPlaying = true;
    requestAnimationFrame(frame);
  }

  function init(stage) {
    if (stage._ovInit) return;
    stage._ovInit = true;
    var src = stage.dataset.src;
    var img = images[src];
    if (!img) { img = images[src] = new Image(); img.src = src; }
    function start() {
      if (!stage.isConnected || stage.hidden) return;
      if (reduced && reduced.matches) drawStatic(stage);
      else play(stage);
    }
    if (host(stage)) watch(stage);
    function ready() {
      if (img.complete) requestAnimationFrame(start);
      else img.addEventListener('load', start, {once: true});
    }
    var splash = document.getElementById('ov-splash');
    if (splash && !splash.classList.contains('ov-splash-out')) document.addEventListener('ov-splash-fading', ready, {once: true});
    else ready();
  }

  function scan() { document.querySelectorAll('[data-thread-empty-stage]').forEach(init); }

  function watch(stage) {
    var timer = 0;
    var observer = new ResizeObserver(function() {
      if (!stage.isConnected) { observer.disconnect(); return; }
      clearTimeout(timer);
      timer = setTimeout(function() { if (!stage._ovPlaying && !stage.hidden) drawStatic(stage); }, 120);
    });
    observer.observe(host(stage));
  }

  function stageFor(form) {
    var h = form && form.closest && form.closest('#tab-chat, #chat-page-root');
    return h && h.querySelector('[data-thread-empty-stage]');
  }
  document.addEventListener('htmx:beforeRequest', function(event) {
    var elt = event.detail && event.detail.elt;
    if (!elt || (elt.id !== 'task-thread-form' && elt.id !== 'chat-form')) return;
    var stage = stageFor(elt);
    if (stage) stage.hidden = true;
  });
  document.addEventListener('htmx:afterRequest', function(event) {
    var detail = event.detail || {};
    var elt = detail.elt;
    if (!elt || (elt.id !== 'task-thread-form' && elt.id !== 'chat-form')) return;
    var xhr = detail.xhr;
    // A 204 is the server's model-setup guard: nothing was sent.
    if (detail.successful && xhr && xhr.status !== 204) return;
    var stage = stageFor(elt);
    if (!stage || !stage.isConnected) return;
    stage.hidden = false;
    drawStatic(stage);
  });

  document.addEventListener('htmx:afterSettle', scan);
  document.addEventListener('htmx:historyRestore', scan);
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', scan, {once: true});
  else scan();
})();

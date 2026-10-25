(() => {
  // SVG is never inserted into the app DOM. Only a scrubbed image document is
  // decoded by <img>, where scripts and external resources cannot execute/load.
  const processed = new WeakSet();
  const images = new Map();
  const tags = new Set('svg g defs title desc path rect circle ellipse line polyline polygon text tspan textPath use symbol clipPath mask linearGradient radialGradient stop pattern marker filter feBlend feColorMatrix feComponentTransfer feComposite feConvolveMatrix feDiffuseLighting feDisplacementMap feDistantLight feDropShadow feFlood feFuncA feFuncB feFuncG feFuncR feGaussianBlur feMerge feMergeNode feMorphology feOffset fePointLight feSpecularLighting feSpotLight feTile feTurbulence'.split(' '));
  function imageSource(source) {
    if (source.length > 1024 * 1024 || /<!DOCTYPE|<!ENTITY/i.test(source)) return null;
    const doc = new DOMParser().parseFromString(source, 'image/svg+xml');
    const root = doc.documentElement;
    if (doc.querySelector('parsererror') || root.localName !== 'svg' || root.namespaceURI !== 'http://www.w3.org/2000/svg') return null;
    for (const node of [root, ...root.querySelectorAll('*')]) {
      if (node.namespaceURI !== root.namespaceURI || !tags.has(node.localName)) { node.remove(); continue; }
      for (const attr of [...node.attributes]) {
        const name = attr.localName.toLowerCase(), value = attr.value.trim();
        if (name.startsWith('on') || name === 'style' || name === 'base' ||
            (name === 'href' && !/^#[\w:.-]+$/.test(value)) ||
            (/url\s*\(/i.test(value) && !/^url\(\s*#[\w:.-]+\s*\)$/.test(value))) node.removeAttributeNode(attr);
      }
    }
    return new XMLSerializer().serializeToString(root);
  }
  // Preview resources depend on a source snapshot, never on a live transcript node.
  window.createChatSVGPreview = function(source) {
    if (!source) return null;
    const safe = imageSource(source);
    if (!safe) return null;
    const url = URL.createObjectURL(new Blob([safe], {type:'image/svg+xml'}));
    const download = URL.createObjectURL(new Blob([source], {type:'image/svg+xml'}));
    return {url, download, dispose() { URL.revokeObjectURL(url); URL.revokeObjectURL(download); }};
  };
  // Keep completed drawings (and their blob URLs/gallery state) through stream commits.
  window.prepareChatSVG = function(fragment, previous) {
    const saved = new Map();
    previous.querySelectorAll('.chat-svg-preview').forEach(figure => {
      if (!images.has(figure)) return;
      const source = figure.querySelector('pre > code.language-svg')?.textContent;
      if (!saved.has(source)) saved.set(source, []);
      saved.get(source).push(figure);
    });
    fragment.querySelectorAll('pre > code.language-svg').forEach(code => {
      const figure = saved.get(code.textContent)?.shift();
      if (figure) code.parentElement.replaceWith(figure);
    });
  };
  function scan() {
    for (const [figure, urls] of images) {
      if (!figure.isConnected) {
        urls.forEach(url => URL.revokeObjectURL(url)); images.delete(figure);
        const code = figure.querySelector('pre > code.language-svg');
        if (code) processed.delete(code); // Recreate thumbnail URLs if a virtual row returns.
      }
    }
    document.querySelectorAll('pre > code.language-svg').forEach(code => {
      if (code.closest('[data-thread-refresh-preview]') || processed.has(code)) return;
      processed.add(code);
      const source = code.textContent, safe = imageSource(source);
      if (!safe) return; // Incomplete/invalid streamed output stays readable source.
      const pre = code.parentElement;
      const figure = document.createElement('figure');
      figure.className = 'chat-svg-preview';
      figure.dataset.imageGalleryGroup = '';
      const url = URL.createObjectURL(new Blob([safe], {type: 'image/svg+xml'}));
      const original = URL.createObjectURL(new Blob([source], {type: 'image/svg+xml'}));
      const link = document.createElement('a');
      link.href = url;
      link.dataset.imageGalleryItem = '';
      link.dataset.imageName = 'drawing.svg';
      const image = document.createElement('img');
      image.alt = 'SVG drawing';
      image.src = url;
      image.style.cssText = 'display:block;max-width:100%;max-height:480px';
      link.append(image);
      const download = document.createElement('a');
      download.href = original;
      download.download = 'drawing.svg';
      download.textContent = 'Download SVG';
      const details = document.createElement('details'), summary = document.createElement('summary');
      summary.textContent = 'SVG source';
      details.append(summary);
      const restored = pre.closest('.chat-svg-preview');
      if (restored) restored.replaceWith(figure);
      else pre.before(figure);
      details.append(pre);
      figure.append(link, download, details);
      images.set(figure, [url, original]);
    });
  }
  let queued = false;
  new MutationObserver(() => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => { queued = false; scan(); });
  }).observe(document.body, {subtree: true, childList: true});
  scan();
})();

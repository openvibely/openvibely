(function () {
  "use strict";
  if (window.ovModelPicker) return;
  const favoritesKey = "openvibely-model-favorites";
  const read = (key) => {
    try {
      return JSON.parse(localStorage.getItem(key) || "{}");
    } catch (_) {
      return {};
    }
  };
  const write = (key, value) => {
    try {
      localStorage.setItem(key, JSON.stringify(value));
    } catch (_) {}
  };
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  };
  const panel = el("div", "ov-menu ov-model-picker"),
    sub = el("div", "ov-menu ov-model-submenu");
  panel.id = "conversation-model-picker";
  panel.hidden = true;
  panel.setAttribute("role", "dialog");
  panel.setAttribute("aria-label", "Choose model");
  sub.hidden = true;
  sub.id = "conversation-provider-models";
  sub.setAttribute("role", "dialog");
  sub.setAttribute("aria-label", "Provider models");
  const searchWrap = el("div", "ov-menu-search ov-mp-search"),
    search = el("input"),
    clear = el("button", "ov-mp-clear", "×");
  search.placeholder = "Search models…";
  search.setAttribute("aria-label", "Search models");
  clear.type = "button";
  clear.setAttribute("aria-label", "Clear search");
  searchWrap.append(search, clear);
  const list = el("div", "ov-menu-list ov-menu-scroll ov-mp-list"),
    effortBox = el("div", "ov-mp-effort"),
    status = el("div", "ov-mp-status");
  status.setAttribute("role", "status");
  panel.append(searchWrap, list, effortBox, status);
  const subhead = el("div", "ov-mp-subhead"),
    back = el("button", "ov-mp-back", "‹"),
    sublist = el("div", "ov-menu-list ov-menu-scroll ov-mp-sublist");
  back.type = "button";
  back.setAttribute("aria-label", "Back to providers");
  subhead.append(back);
  subhead.hidden = true;
  sub.append(subhead, sublist);
  document.body.append(panel, sub);
  let active = null,
    provider = null,
    subCloseTimer = null,
    providerSwitchTimer = null,
    providerSwitchButton = null,
    providerPointerOrigin = null,
    favorites = {},
    favoritesLoading = false,
    favoritesError = "",
    favoritesLoadToken = 0,
    favoriteWrites = Promise.resolve();
  const favoritesEndpoint = "/ui/model-favorites";
  async function requestFavorites(payload) {
    const response = await fetch(favoritesEndpoint, payload ? {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    } : undefined);
    if (!response.ok) throw Error("Could not save favorites. Reopen the picker to retry.");
    return response.json();
  }
  async function loadFavorites() {
    const token = ++favoritesLoadToken;
    favoritesLoading = true;
    favoritesError = "";
    render();
    try {
      await favoriteWrites.catch(() => {});
      let saved = await requestFavorites();
      const legacy = read(favoritesKey);
      const importIDs = Object.keys(legacy || {}).filter((id) => legacy[id]);
      if (importIDs.length) saved = await requestFavorites({ import_ids: importIDs });
      try { localStorage.removeItem(favoritesKey); } catch (_) {}
      if (token !== favoritesLoadToken) return;
      favorites = saved;
      favoritesError = "";
    } catch (_) {
      if (token === favoritesLoadToken)
        favoritesError = "Could not load favorites. Reopen the picker to retry.";
    } finally {
      if (token === favoritesLoadToken) {
        favoritesLoading = false;
        render();
        renderSub();
      }
    }
  }
  function options(btn) {
    const all = [...btn.parentElement.querySelectorAll("li[data-value]")].map(
      (li) => ({
        id: li.dataset.value,
        name: li.dataset.pickerName || li.textContent.trim(),
        model: li.dataset.pickerModel || "",
        provider: li.dataset.pickerProvider || "",
        levels: (li.dataset.pickerEfforts || "").split(",").filter(Boolean),
        defaultEffort: li.dataset.pickerDefault || "",
        effectiveID: li.dataset.pickerEffectiveId || "",
        detail: li.dataset.pickerDetail || "",
      }),
    );
    const fallback = all.find((m) => m.id === "default");
    const resolved = all.find((m) => m.id === fallback?.effectiveID);
    if (resolved) {
      fallback.model = resolved.model;
      fallback.levels = resolved.levels;
      fallback.defaultEffort = resolved.defaultEffort;
    }
    return all;
  }
  function state(btn) {
    return btn._ovModelState;
  }
  function key(m) {
    return (m.effectiveID || m.id) + ":" + m.model;
  }
  function current(btn) {
    return options(btn).find((m) => m.id === btn.dataset.currentValue);
  }
  function field(btn) {
    return btn.closest("form").querySelector('[name="reasoning_effort"]');
  }
  function sync(btn) {
    const s = state(btn),
      m = current(btn);
    if (!s || !m) return;
    const value = s.efforts[key(m)] || "";
    field(btn).value = m.levels.includes(value) ? value : "";
    btn.title =
      m.name +
      (m.detail ? " · " + m.detail : "") +
      (field(btn).value ? " · " + label(field(btn).value) : "");
  }
  function label(v) {
    return (
      {
        none: "None",
        minimal: "Minimal",
        low: "Low",
        medium: "Medium",
        high: "High",
        xhigh: "Extra high",
        max: "Max",
        ultra: "Ultra",
      }[v] || v
    );
  }
  function taskEndpoint(btn) {
    return btn.closest("form").getAttribute("data-model-select-endpoint");
  }
  function save(btn) {
    const s = state(btn);
    sync(btn);
    if (!taskEndpoint(btn)) {
      write(s.storage, s.efforts);
      return Promise.resolve();
    }
    const body = new URLSearchParams({
      agent_id: btn.dataset.currentValue,
      reasoning_effort: field(btn).value,
    });
    const request = () =>
      fetch(taskEndpoint(btn), { method: "POST", body }).then((r) => {
        if (!r.ok) throw Error("Could not save model settings. Try again.");
      });
    s.pending = (s.pending || Promise.resolve()).catch(() => {}).then(request);
    s.pending.then(
      () => {
        s.error = "";
        s.failedSave = false;
        if (s.selectionPending === body.get("agent_id"))
          s.selectionPending = null;
        if (active === btn) {
          status.textContent = "";
          renderEffort();
        }
      },
      (err) => {
        s.error = err.message;
        s.failedSave = true;
        if (active === btn) {
          status.textContent = s.error;
          renderEffort();
        }
      },
    );
    return s.pending;
  }
  async function load(btn) {
    const s = state(btn),
      m = current(btn),
      endpoint = taskEndpoint(btn);
    if (!endpoint || !m || (!m.provider && !m.effectiveID)) return;
    const token = ++s.loadToken;
    s.loading = true;
    sync(btn);
    if (active === btn) renderEffort();
    try {
      // A failed write must not poison subsequent refreshes or selections.
      s.pending = s.pending.catch(() => {});
      await s.pending;
      const response = await fetch(
        endpoint + "?agent_id=" + encodeURIComponent(m.id),
      );
      if (!response.ok)
        throw Error("Could not load effort. Reopen the picker to retry.");
      const data = await response.json();
      const effort = data.reasoning_effort || "";
      // A send started before another model was selected still needs this
      // result, even though the stale lookup must not update the current UI.
      if (token !== s.loadToken) return effort;
      s.efforts[key(m)] = effort;
      s.error = "";
      return effort;
    } catch (e) {
      if (token === s.loadToken) s.error = e.message;
    } finally {
      if (token === s.loadToken) {
        s.loading = false;
        sync(btn);
        if (active === btn) {
          renderEffort();
          status.textContent = s.error;
        }
      }
    }
  }
  function cancelSubClose() {
    if (subCloseTimer !== null) clearTimeout(subCloseTimer);
    subCloseTimer = null;
  }
  function cancelProviderSwitch(button) {
    if (button && providerSwitchButton !== button) return;
    if (providerSwitchTimer !== null) clearTimeout(providerSwitchTimer);
    providerSwitchTimer = null;
    providerSwitchButton = null;
  }
  function closeSub() {
    cancelSubClose();
    cancelProviderSwitch();
    provider = null;
    providerPointerOrigin = null;
    sub.hidden = true;
    list
      .querySelectorAll(".ov-mp-provider")
      .forEach((b) => b.setAttribute("aria-expanded", "false"));
  }
  function close(focus) {
    const btn = active;
    closeSub();
    panel.hidden = true;
    active = null;
    if (btn) {
      btn.setAttribute("aria-expanded", "false");
      if (focus && btn.isConnected) btn.focus();
    }
  }
  function choose(m) {
    const btn = active;
    if (!btn) return;
    state(btn).loadToken++;
    state(btn).loading = false;
    window._setChatCustomSelectValue(btn, m.id);
    sync(btn);
    btn.dispatchEvent(
      new CustomEvent("chat-select-change", {
        detail: { value: m.id },
        bubbles: true,
      }),
    );
    render();
    renderSub();
    if (provider) {
      sublist
        .querySelector('[data-model="' + CSS.escape(m.id) + '"]')
        ?.focus({ preventScroll: true });
    } else search.focus({ preventScroll: true });
  }
  function activateRow(row, target) {
    if (row.hasAttribute("data-picker-active")) return;
    target
      .querySelectorAll("[data-picker-active]")
      .forEach((item) => item.removeAttribute("data-picker-active"));
    row.setAttribute("data-picker-active", "");
  }
  function row(m, target) {
    const r = el("div", "ov-menu-row ov-mp-row");
    r.onpointermove = () => activateRow(r, target);
    r.onfocusin = () => activateRow(r, target);
    r.dataset.selected = String(m.id === active.dataset.currentValue);
    if (m.provider) {
      const star = el("button", "ov-mp-star", favorites[m.id] ? "★" : "☆");
      star.type = "button";
      star.setAttribute(
        "aria-label",
        (favorites[m.id] ? "Unfavorite " : "Favorite ") + m.name,
      );
      star.setAttribute("aria-pressed", String(!!favorites[m.id]));
      star.disabled = favoritesLoading || !!favoritesError;
      star.onclick = () => {
        const desired = !favorites[m.id];
        if (desired) favorites[m.id] = true;
        else delete favorites[m.id];
        renderList();
        renderSub();
        position();
        const button = [
          ...panel.querySelectorAll(".ov-mp-star"),
          ...sub.querySelectorAll(".ov-mp-star"),
        ].find((b) => b.dataset.id === m.id);
        button?.focus({ preventScroll: true });
        favoriteWrites = favoriteWrites.catch(() => {}).then(() =>
          requestFavorites({ model_id: m.id, favorite: desired }),
        );
        favoriteWrites.catch(() => {
          favoritesError = "Could not save favorites. Reopen the picker to retry.";
          if (active) {
            status.textContent = favoritesError;
            renderList();
            renderSub();
          }
        });
      };
      star.dataset.id = m.id;
      r.append(star);
    }
    const pick = el("button", "ov-menu-option ov-mp-pick");
    pick.type = "button";
    pick.dataset.model = m.id;
    pick.setAttribute(
      "aria-pressed",
      String(m.id === active.dataset.currentValue),
    );
    const copy = el("span", "ov-mp-copy");
    copy.append(el("span", "ov-mp-name", m.name));
    if (m.provider || m.detail)
      copy.append(
        el(
          "span",
          "ov-mp-detail",
          m.detail || m.provider + (m.model ? " · " + m.model : ""),
        ),
      );
    pick.append(copy);
    pick.onclick = () => choose(m);
    r.prepend(pick);
    const check = el(
      "span",
      "ov-menu-check ov-mp-check",
      m.id === active.dataset.currentValue ? "✓" : "",
    );
    check.setAttribute("aria-hidden", "true");
    pick.prepend(check);
    target.append(r);
  }
  function renderList() {
    if (!active) return;
    const scroll = list.scrollTop,
      all = options(active),
      q = search.value.trim().toLowerCase();
    list.replaceChildren();
    clear.hidden = !q;
    if (q) {
      const matches = all.filter((m) =>
        (m.name + " " + m.provider + " " + m.model + " " + m.detail).toLowerCase().includes(q),
      );
      matches.sort((a, b) => Number(!a.provider) - Number(!b.provider));
      matches.forEach((m) => row(m, list));
      if (!matches.length)
        list.append(el("div", "ov-mp-empty", "No matching models"));
      return;
    }
    [
      ...new Set(
        all
          .filter((m) => m.provider && !favorites[m.id])
          .map((m) => m.provider),
      ),
    ]
      .sort()
      .forEach((p) => {
        const b = el("button", "ov-menu-option ov-mp-provider");
        b.type = "button";
        b.dataset.provider = p;
        b.onpointermove = () => {
          if (providerSwitchButton !== b) activateRow(b, list);
        };
        b.onfocus = () => activateRow(b, list);
        b.onpointerenter = (e) => {
          if (e.pointerType !== "touch") hoverProvider(b, p, e);
        };
        b.onpointerleave = () => cancelProviderSwitch(b);
        b.setAttribute("aria-haspopup", "dialog");
        b.setAttribute("aria-controls", sub.id);
        b.setAttribute("aria-expanded", String(provider === p));
        b.append(el("span", "", p), el("span", "", "›"));
        b.onclick = (e) => {
          openProvider(p);
          if (e.detail) providerPointerOrigin = { x: e.clientX, y: e.clientY };
        };
        list.append(b);
      });
    const defaults = all.filter((m) => !m.provider);
    if (defaults.length) {
      if (list.children.length) list.append(el("div", "ov-menu-rule ov-mp-rule"));
      defaults.forEach((m) => row(m, list));
    }
    const fav = all.filter((m) => m.provider && favorites[m.id]);
    if (fav.length) {
      if (list.children.length) list.append(el("div", "ov-menu-rule ov-mp-rule"));
      list.append(el("div", "ov-menu-heading ov-mp-heading", "FAVORITES"));
      fav.forEach((m) => row(m, list));
    }
    list.scrollTop = scroll;
  }
  function openProvider(p) {
    cancelSubClose();
    cancelProviderSwitch();
    if (provider === p && !sub.hidden) return;
    providerPointerOrigin = null;
    provider = p;
    list.querySelectorAll(".ov-mp-provider").forEach((b) => {
      b.setAttribute("aria-expanded", String(b.dataset.provider === p));
    });
    renderSub();
  }
  function hoverProvider(button, p, event) {
    if (provider === p) {
      cancelSubClose();
      cancelProviderSwitch();
      providerPointerOrigin = { x: event.clientX, y: event.clientY };
      return;
    }
    if (provider && !sub.hidden && providerPointerOrigin) {
      const bounds = sub.getBoundingClientRect();
      const right = bounds.left >= panel.getBoundingClientRect().right - 8;
      const toward = right
        ? event.clientX > providerPointerOrigin.x + 8
        : event.clientX < providerPointerOrigin.x - 8;
      if (toward && event.clientY >= bounds.top - 12 && event.clientY <= bounds.bottom + 12) {
        cancelProviderSwitch();
        providerSwitchButton = button;
        providerSwitchTimer = setTimeout(() => {
          if (active && button.isConnected && providerSwitchButton === button) {
            openProvider(p);
            providerPointerOrigin = { x: event.clientX, y: event.clientY };
          }
        }, 300);
        return;
      }
    }
    openProvider(p);
    providerPointerOrigin = { x: event.clientX, y: event.clientY };
  }
  function renderSub() {
    if (!active || !provider) {
      sub.hidden = true;
      return;
    }
    sublist.replaceChildren();
    sub.setAttribute("aria-label", provider + " models");
    options(active)
      .filter((m) => m.provider === provider && !favorites[m.id])
      .forEach((m) => row(m, sublist));
    if (!sublist.children.length) {
      closeSub();
      return;
    }
    sub.hidden = false;
    positionSub();
  }
  function renderEffort() {
    effortBox.replaceChildren();
    const m = active && current(active);
    effortBox.hidden = !m?.levels.length;
    if (effortBox.hidden) return;
    const s = state(active),
      override = field(active).value,
      value = override || m.defaultEffort;
    const levels = m.defaultEffort ? m.levels : ["", ...m.levels];
    const head = el("div", "ov-mp-effort-head");
    head.append(el("span", "", "Reasoning effort"));
    effortBox.append(head);
    const slider = el("input");
    slider.type = "range";
    slider.min = "0";
    slider.max = String(levels.length - 1);
    slider.step = "1";
    slider.value = String(Math.max(0, levels.indexOf(value)));
    const updateTrack = () =>
      slider.style.setProperty(
        "--ov-mp-progress",
        (Number(slider.value) / Math.max(1, levels.length - 1)) * 100 + "%",
      );
    updateTrack();
    slider.disabled = s.loading || !!s.error;
    slider.setAttribute("aria-label", "Reasoning effort");
    slider.setAttribute(
      "aria-valuetext",
      value ? label(value) : "Model default",
    );
    slider.oninput = () => {
      updateTrack();
      const value = levels[Number(slider.value)];
      s.efforts[key(m)] = value;
      sync(active);
      reset.hidden = !value;
      slider.setAttribute(
        "aria-valuetext",
        value ? label(value) : "Model default",
      );
    };
    slider.onchange = () => {
      save(active);
      renderEffort();
    };
    const ticks = el("div", "ov-mp-ticks");
    levels.forEach((v) =>
      ticks.append(el("span", "", v ? label(v) : "Default")),
    );
    const reset = el("button", "ov-mp-reset", "Use default");
    reset.type = "button";
    reset.hidden = !override;
    reset.disabled = s.loading;
    reset.onclick = () => {
      delete s.efforts[key(m)];
      save(active);
      renderEffort();
    };
    head.append(reset);
    effortBox.append(slider, ticks);
  }
  function render() {
    if (!active) return;
    const atEnd = !search.value.trim() &&
      list.scrollTop + list.clientHeight >= list.scrollHeight - 1;
    renderList();
    renderEffort();
    status.textContent = state(active).error || favoritesError || "";
    position();
    if (atEnd) list.scrollTop = list.scrollHeight;
  }
  function position() {
    if (!active) return;
    if (!active.isConnected) {
      close(false);
      return;
    }
    const r = active.getBoundingClientRect(),
      height = window.innerHeight,
      width = window.innerWidth;
    const above = r.top - 8,
      below = height - r.bottom - 8,
      up = above >= below;
    panel.style.maxHeight =
      Math.max(120, Math.min(height - 16, Math.max(above, below))) + "px";
    panel.style.left =
      Math.max(8, Math.min(r.left, width - panel.offsetWidth - 8)) + "px";
    panel.style.top =
      (up ? Math.max(8, r.top - panel.offsetHeight - 6) : r.bottom + 6) + "px";
    positionSub();
  }
  function positionSub() {
    if (active && provider && !sub.hidden) {
      const b = [...list.querySelectorAll(".ov-mp-provider")].find(
        (b) => b.dataset.provider === provider,
      );
      if (!b) {
        closeSub();
        return;
      }
      const height = window.innerHeight,
        width = window.innerWidth,
        p = panel.getBoundingClientRect(),
        a = b.getBoundingClientRect();
      let left = p.right - 4;
      if (left + sub.offsetWidth > width - 8)
        left = p.left - sub.offsetWidth + 4;
      subhead.hidden = left >= 8;
      if (left < 8) {
        left = p.left;
        sub.style.width = p.width + "px";
      } else sub.style.width = "300px";
      sub.style.maxHeight = height - 16 + "px";
      sublist.style.maxHeight = Math.max(80, height - 80) + "px";
      sub.style.left = Math.max(8, left) + "px";
      sub.style.top =
        Math.max(8, Math.min(a.top, height - sub.offsetHeight - 8)) + "px";
    }
  }
  function usesAppleShortcuts() {
    return /Mac|iPhone|iPad|iPod|iOS/i.test(
      navigator.userAgentData?.platform || navigator.platform || navigator.userAgent || "",
    );
  }
  function init(btn) {
    const apple = usesAppleShortcuts();
    btn.setAttribute("aria-keyshortcuts", apple ? "Meta+Shift+M" : "Control+Shift+M");
    btn.title = apple ? "Models (⌘⇧M)" : "Models (Ctrl+Shift+M)";
    if (state(btn)) return;
    const form = btn.closest("form"),
      project = form.querySelector('[name="_project_id"]')?.value || "",
      storage = project ? "chat-effort-" + project : "";
    btn._ovModelState = {
      efforts: storage ? read(storage) : {},
      storage,
      pending: Promise.resolve(),
      loading: false,
      loadToken: 0,
      error: "",
    };
    btn.setAttribute("aria-haspopup", "dialog");
    btn.setAttribute("aria-controls", panel.id);
    sync(btn);
    if (taskEndpoint(btn)) {
      btn._ovModelState.ready = load(btn);
    } else btn._ovModelState.ready = Promise.resolve();
  }
  function loadSelection(btn) {
    const s = state(btn),
      chosen = btn.dataset.currentValue;
    const promise = load(btn),
      token = s.loadToken;
    return promise
      .then((effort) => {
        if (
          token !== s.loadToken ||
          chosen !== btn.dataset.currentValue ||
          s.error
        )
          return effort;
        return save(btn).then(() => effort);
      })
      .catch(() => {});
  }
  window.ovModelPicker = {
    init,
    save,
    close,
    open(btn) {
      init(btn);
      active = btn;
      provider = null;
      search.value = "";
      panel.hidden = false;
      btn.setAttribute("aria-expanded", "true");
      render();
      list.scrollTop = list.scrollHeight;
      renderSub();
      loadFavorites();
      search.focus({ preventScroll: true });
      const s = state(btn);
      if (s.error) {
        s.ready = s.failedSave
          ? save(btn).catch(() => {})
          : s.selectionPending
            ? loadSelection(btn)
            : load(btn);
      }
    },
    changed(btn) {
      init(btn);
      const s = state(btn);
      s.loadToken++;
      s.loading = false;
      s.error = "";
      s.failedSave = false;
      sync(btn);
      // Fetch an existing task/model preference before persisting a newly selected model.
      if (taskEndpoint(btn)) {
        s.selectionPending = btn.dataset.currentValue;
        s.ready = loadSelection(btn);
      } else save(btn);
    },
  };
  panel.onpointerover = (e) => {
    if (e.pointerType === "touch") return;
    if (!e.target.closest(".ov-mp-row,.ov-mp-provider"))
      list
        .querySelectorAll("[data-picker-active]")
        .forEach((row) => row.removeAttribute("data-picker-active"));
    if (!provider) return;
    if (e.target.closest(".ov-mp-provider")) cancelSubClose();
    else if (e.target.closest(".ov-mp-row,.ov-mp-search,.ov-mp-effort"))
      closeSub();
    else if (subCloseTimer === null)
      subCloseTimer = setTimeout(closeSub, 350);
  };
  sub.onpointerenter = () => {
    cancelSubClose();
    cancelProviderSwitch();
  };
  search.oninput = () => {
    closeSub();
    list.scrollTop = 0;
    render();
  };
  clear.onclick = () => {
    search.value = "";
    render();
    search.focus();
  };
  back.onclick = () => {
    const old = provider;
    closeSub();
    [...list.querySelectorAll(".ov-mp-provider")]
      .find((b) => b.dataset.provider === old)
      ?.focus();
  };
  document.addEventListener(
    "click",
    (e) => {
      if (
        !active ||
        panel.contains(e.target) ||
        sub.contains(e.target) ||
        active.contains(e.target)
      )
        return;
      close(false);
    },
    true,
  );
  document.addEventListener(
    "keydown",
    (e) => {
      if (
        !e.defaultPrevented && !e.isComposing && !e.repeat &&
        (usesAppleShortcuts() ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey) &&
        e.shiftKey && !e.altKey &&
        e.key.toLowerCase() === "m"
      ) {
        const buttons = [...document.querySelectorAll("form.chat-input-container .chat-model-select")];
        const visible = (btn) => !btn.disabled && btn.getClientRects().length > 0 &&
          getComputedStyle(btn).visibility !== "hidden" && !btn.closest("[inert]");
        const focusedForm = document.activeElement?.closest("form.chat-input-container");
        const btn = buttons.find((btn) => visible(btn) && btn.closest("form") === focusedForm) ||
          buttons.find(visible);
        if (!btn || (document.querySelector("dialog[open]") && !btn.closest("dialog[open]"))) return;
        e.preventDefault();
        e.stopPropagation();
        if (active) close(false);
        window.ovModelPicker.open(btn);
        return;
      }
      if (!active) return;
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        if (provider) back.click();
        else close(true);
        return;
      }
      if (e.key === "ArrowLeft" && provider && e.target !== search) {
        e.preventDefault();
        back.click();
        return;
      }
      if (e.key === "ArrowRight" && e.target.matches(".ov-mp-provider")) {
        e.preventDefault();
        e.target.click();
        sublist.querySelector(".ov-mp-pick")?.focus();
        return;
      }
      if (
        e.target.type === "range" ||
        !["ArrowDown", "ArrowUp"].includes(e.key)
      )
        return;
      const root = provider ? sublist : list,
        rows = [...root.querySelectorAll(".ov-mp-pick,.ov-mp-provider")];
      if (!rows.length) return;
      e.preventDefault();
      const i = rows.indexOf(document.activeElement),
        next =
          e.key === "ArrowDown"
            ? (i + 1) % rows.length
            : i <= 0
              ? rows.length - 1
              : i - 1;
      rows[next].focus();
      rows[next].scrollIntoView({ block: "nearest" });
    },
    true,
  );
  window.addEventListener("resize", position);
  document.addEventListener(
    "scroll",
    (e) => {
      if (active && !panel.contains(e.target) && !sub.contains(e.target))
        position();
      else if (active && provider) positionSub();
    },
    true,
  );
  document.addEventListener("htmx:beforeCleanupElement", (e) => {
    if (active && e.detail?.elt?.contains(active)) close(false);
  });
  document.addEventListener(
    "submit",
    (e) => {
      const btn = e.target.querySelector(".chat-model-select");
      if (!btn || !state(btn) || btn._ovResubmit) return;
      const s = state(btn);
      if (!taskEndpoint(btn)) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      const submitter = e.submitter;
      const form = e.target;
      const message = form.querySelector('[name="message"]');
      const attachment = form.querySelector('[name="attachment_session_id"]');
      const model = form.querySelector('[name="agent_id"]');
      const effort = form.querySelector('[name="reasoning_effort"]');
      // The composer temporarily installs a steering fallback payload, then
      // restores the newer draft synchronously. Preserve the selected model
      // and effort too, since either can change while the save completes.
      const payload = {
        ...(form._chatNextSubmissionPayload || {
          message: message?.value || "",
          attachmentSessionID: attachment?.value || "",
        }),
        modelID: model?.value || "",
        reasoningEffort: effort?.value || "",
      };
      const waitingForEffort = s.loading;
      const ready = s.ready;
      const pending = s.pending;
      Promise.resolve(ready)
        .then((loadedEffort) => {
          if (waitingForEffort) {
            if (loadedEffort === undefined)
              throw Error("Could not load effort for the selected model.");
            payload.reasoningEffort = loadedEffort;
          }
          return pending;
        })
        .then(() => {
          if (btn.dataset.currentValue === payload.modelID && s.error)
            throw Error(s.error);
          if (!form.isConnected) return;
          const draft = message?.value;
          const draftAttachments = attachment?.value;
          const draftModel = model?.value;
          const draftEffort = effort?.value;
          form._chatNextSubmissionPayload = payload;
          if (message) message.value = payload.message;
          if (attachment) attachment.value = payload.attachmentSessionID;
          if (model) model.value = payload.modelID;
          if (effort) effort.value = payload.reasoningEffort;
          const managedSelection = el("input");
          managedSelection.type = "hidden";
          managedSelection.name = "model_selection_managed";
          managedSelection.value = "1";
          form.append(managedSelection);
          btn._ovResubmit = true;
          try {
            form.requestSubmit(submitter);
          } finally {
            btn._ovResubmit = false;
            managedSelection.remove();
            if (message) message.value = draft;
            if (attachment) attachment.value = draftAttachments;
            if (model) model.value = draftModel;
            if (effort) effort.value = draftEffort;
          }
        })
        .catch((err) => {
          window.ovModelPicker.open(btn);
          status.textContent = err.message;
        });
    },
    true,
  );
})();

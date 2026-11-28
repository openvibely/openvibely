(function () {
  "use strict";
  if (window.ovModelPicker) return;
  const favoritesKey = "openvibely-model-favorites";
  const providerIcons = () => window._ovModelProviderIcons || {};
  let iconsLoaded = false;
  const read = (key) => {
    try {
      return JSON.parse(localStorage.getItem(key) || "{}");
    } catch (_) {
      return {};
    }
  };
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  };
  const panel = el("div", "ov-menu ov-model-picker");
  panel.id = "conversation-model-picker";
  panel.hidden = true;
  panel.setAttribute("role", "dialog");
  panel.setAttribute("aria-label", "Choose model");

  const rail = el("div", "ov-mp-rail"),
    railInner = el("div", "ov-mp-rail-inner"),
    railTabs = el("div", "ov-mp-rail-tabs"),
    railFooter = el("div", "ov-mp-rail-footer"),
    manageBtn = el("button", "ov-mp-rail-btn ov-mp-rail-action");
  railTabs.setAttribute("role", "tablist");
  railTabs.setAttribute("aria-orientation", "vertical");
  railTabs.setAttribute("aria-label", "Favorites and providers");
  manageBtn.type = "button";
  manageBtn.setAttribute("aria-label", "Manage models");
  manageBtn.title = "Manage models";
  railFooter.append(manageBtn);
  railInner.append(railTabs, railFooter);
  rail.append(railInner);

  const pane = el("div", "ov-mp-pane"),
    searchWrap = el("div", "ov-menu-search ov-mp-search"),
    search = el("input"),
    clear = el("button", "ov-mp-clear", "×");
  search.placeholder = "Search models…";
  search.setAttribute("aria-label", "Search models");
  clear.type = "button";
  clear.setAttribute("aria-label", "Clear search");
  searchWrap.append(search, clear);

  const list = el("div", "ov-menu-list ov-menu-scroll ov-mp-list"),
    effortBox = el("div", "ov-mp-effort"),
    speedBox = el("div", "ov-mp-speed"),
    status = el("div", "ov-mp-status");
  list.setAttribute("role", "tabpanel");
  speedBox.hidden = true;
  status.setAttribute("role", "status");

  // WebKit can miss native wheel scrolling in floating menus. Keep vertical
  // gestures inside both the model list and the icon rail, including at ends.
  for (const scrollList of [list, railTabs]) {
    scrollList.addEventListener(
      "wheel",
      (event) => {
        if (
          event.ctrlKey ||
          event.metaKey ||
          !event.deltaY ||
          Math.abs(event.deltaX) > Math.abs(event.deltaY) ||
          scrollList.scrollHeight <= scrollList.clientHeight
        )
          return;
        const unit =
          event.deltaMode === 1
            ? 16
            : event.deltaMode === 2
              ? scrollList.clientHeight
              : 1;
        scrollList.scrollTop += event.deltaY * unit;
        event.preventDefault();
      },
      { passive: false },
    );
  }

  pane.append(searchWrap, list, effortBox, speedBox, status);
  panel.append(rail, pane);
  document.body.append(panel);

  let active = null,
    activeTab = "pins",
    userSwitchedTab = false,
    favorites = {},
    favoritesLoading = false,
    favoritesError = "",
    favoritesLoadToken = 0,
    favoriteWrites = Promise.resolve();

  const favoritesEndpoint = "/ui/model-favorites";
  function favoritesURL() {
    const url = new URL(favoritesEndpoint, location.href);
    const form = active?.closest("form");
    const destination = form?.querySelector("[data-project-input]");
    const project =
      form?.elements.namedItem("destination_project")?.value ||
      form?.querySelector('[name="_project_id"]')?.value ||
      (destination &&
        document.getElementById(destination.dataset.projectInput)?.value) ||
      active?.closest("[data-project-id]")?.dataset.projectId ||
      "";
    if (/^r-[a-f0-9]{16}-/.test(project))
      url.searchParams.set("project_id", project);
    return url.pathname + url.search;
  }
  async function requestFavorites(payload, endpoint = favoritesURL()) {
    const response = await fetch(
      endpoint,
      payload
        ? {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
          }
        : undefined,
    );
    if (!response.ok)
      throw Error("Could not save favorites. Reopen the picker to retry.");
    return response.json();
  }
  async function loadFavorites() {
    const token = ++favoritesLoadToken;
    favoritesLoading = true;
    favoritesError = "";
    panel.setAttribute("aria-busy", "true");
    render();
    try {
      await favoriteWrites.catch(() => {});
      let saved = await requestFavorites();
      const legacy = favoritesURL().includes("project_id=r-")
        ? {}
        : read(favoritesKey);
      const importIDs = Object.keys(legacy || {}).filter((id) => legacy[id]);
      if (importIDs.length)
        saved = await requestFavorites({ import_ids: importIDs });
      try {
        if (!favoritesURL().includes("project_id=r-"))
          localStorage.removeItem(favoritesKey);
      } catch (_) {}
      if (token !== favoritesLoadToken) return;
      favorites = saved;
      favoritesError = "";
    } catch (_) {
      if (token === favoritesLoadToken)
        favoritesError =
          "Could not load favorites. Reopen the picker to retry.";
    } finally {
      if (token === favoritesLoadToken) {
        favoritesLoading = false;
        panel.removeAttribute("aria-busy");
        if (active && !userSwitchedTab && !search.value.trim()) {
          const nextTab = initialTabForCurrentModel();
          const changed = nextTab !== activeTab;
          activeTab = nextTab;
          render();
          if (changed) scrollSelectedIntoView();
        } else {
          render();
        }
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
        fast: li.dataset.pickerFast === "true",
        defaultTier: li.dataset.pickerTier || "",
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
      fallback.fast = resolved.fast;
      fallback.defaultTier = resolved.defaultTier;
    }
    return all;
  }
  function sortModelsByName(models) {
    return models.sort((a, b) => {
      const byName = a.name.localeCompare(b.name, undefined, {
        sensitivity: "base",
      });
      return byName || a.id.localeCompare(b.id);
    });
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
  function tierField(btn) {
    return btn.closest("form").querySelector('[name="service_tier"]');
  }
  function sync(btn) {
    const s = state(btn),
      m = current(btn);
    if (!s || !m) return;
    const value = s.efforts[key(m)] || "";
    field(btn).value = m.levels.includes(value) ? value : "";
    const tier = s.tiers[key(m)] || "";
    if (tierField(btn))
      tierField(btn).value =
        m.fast && ["priority", "default"].includes(tier) ? tier : "";
    btn.title = usesAppleShortcuts() ? "Models (⌘M)" : "Models (Ctrl+M)";
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
  function settingsEndpoint(btn) {
    const form = btn.closest("form");
    const destination = form.querySelector(
      '[data-project-send-url]:not([data-project-send-url=""])[data-project-input]',
    );
    const project =
      destination &&
      document.getElementById(destination.dataset.projectInput)?.value;
    const endpoint = destination
      ? project
        ? "/conversation/model-settings?project_id=" +
          encodeURIComponent(project)
        : ""
      : form.getAttribute("data-model-select-endpoint");
    if (!endpoint) return endpoint;
    const url = new URL(endpoint, window.location.href);
    if (form.dataset.conversationTaskId)
      url.searchParams.set("task_id", form.dataset.conversationTaskId);
    return url.pathname + url.search;
  }

  async function settingsError(response, fallback) {
    let message = fallback;
    const permissionDenied =
      response.status === 403 &&
      response.headers.get("X-OpenVibely-Permission-Denied") === "true";
    if (permissionDenied) {
      message =
        "You do not have permission to change these model settings. Ask the project owner or an organization admin for access.";
      let toast;
      try {
        toast = JSON.parse(
          response.headers.get("HX-Trigger") || "{}",
        ).openvibelyToast;
      } catch (_) {}
      if (typeof toast?.message === "string" && toast.message) {
        message = toast.message;
      } else {
        try {
          const body = await response.json();
          if (typeof body.message === "string" && body.message)
            message = body.message;
        } catch (_) {}
      }
      if (window.showToast) window.showToast(message, "failed");
    }
    const error = Error(message);
    error.toastOnly = permissionDenied;
    return error;
  }
  function inlineSettingsError(s) {
    return s.permissionError ? "" : s.error;
  }

  async function importLegacyPreferences(btn) {
    const form = btn.closest("form");
    if (form.dataset.conversationTaskId) return;
    const project = form.querySelector('[name="_project_id"]')?.value;
    if (!project) return;
    const keys = ["chat-model-", "chat-effort-", "chat-service-tier-"].map(
      (k) => k + project,
    );
    let selected, efforts, tiers;
    try {
      if (!keys.some((k) => localStorage.getItem(k) !== null)) return;
      selected = localStorage.getItem(keys[0]);
      efforts = read(keys[1]);
      tiers = read(keys[2]);
    } catch (_) {
      return;
    }
    if (!options(btn).some((m) => m.id === selected))
      selected = btn.dataset.currentValue;
    const overrides = {};
    for (const model of options(btn)) {
      const id = key(model);
      if (efforts[id] !== undefined || tiers[id] !== undefined)
        overrides[id] = {
          reasoning_effort: efforts[id] || "",
          service_tier: tiers[id] || "",
        };
    }
    const response = await fetch(settingsEndpoint(btn), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ agent_id: selected, import_only: true, overrides }),
    });
    if (!response.ok)
      throw await settingsError(
        response,
        "Could not migrate model settings. Reopen the picker to retry.",
      );
    try {
      keys.forEach((k) => localStorage.removeItem(k));
    } catch (_) {}
  }
  function save(btn) {
    const s = state(btn);
    sync(btn);
    const endpoint = settingsEndpoint(btn);
    if (!endpoint) {
      return Promise.resolve();
    }
    const body = new URLSearchParams({
      agent_id: btn.dataset.currentValue,
      reasoning_effort: field(btn).value,
      service_tier: tierField(btn)?.value || "",
    });
    const request = () =>
      fetch(endpoint, { method: "POST", body }).then(async (r) => {
        if (!r.ok)
          throw await settingsError(
            r,
            "Could not save model settings. Try again.",
          );
      });
    s.pending = (s.pending || Promise.resolve()).catch(() => {}).then(request);
    s.pending.then(
      () => {
        s.error = "";
        s.permissionError = false;
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
        s.permissionError = !!err.toastOnly;
        s.failedSave = true;
        if (active === btn) {
          status.textContent = inlineSettingsError(s);
          renderEffort();
        }
      },
    );
    return s.pending;
  }
  async function load(btn, initial = false) {
    const s = state(btn),
      endpoint = settingsEndpoint(btn);
    initial = initial || s.initialLoad;
    let m = current(btn);
    if (!endpoint || !m) return;
    const token = ++s.loadToken;
    s.loading = true;
    sync(btn);
    if (active === btn) renderEffort();
    try {
      // A failed write must not poison subsequent refreshes or selections.
      s.pending = s.pending.catch(() => {});
      await s.pending;
      if (initial) await importLegacyPreferences(btn);
      const url = new URL(endpoint, window.location.href);
      if (!initial) url.searchParams.set("agent_id", m.id);
      const response = await fetch(url.pathname + url.search);
      if (!response.ok)
        throw Error("Could not load effort. Reopen the picker to retry.");
      const data = await response.json();
      if (initial && data.agent_id !== undefined) {
        const selected = options(btn).find((item) => item.id === data.agent_id);
        if (selected) {
          m = selected;
          if (token === s.loadToken) {
            window._setChatCustomSelectValue(btn, selected.id);
            btn.closest("form").querySelector('[name="agent_id"]').value =
              selected.id;
          }
        }
      }
      const effort = data.reasoning_effort || "";
      const tier = data.service_tier || "";
      // A send started before another model was selected still needs this
      // result, even though the stale lookup must not update the current UI.
      if (token !== s.loadToken) return { effort, tier, modelID: m.id };
      s.initialLoad = false;
      s.efforts[key(m)] = effort;
      s.tiers[key(m)] = tier;
      s.error = "";
      s.permissionError = false;
      return { effort, tier, modelID: m.id };
    } catch (e) {
      if (token === s.loadToken) {
        s.error = e.message;
        s.permissionError = !!e.toastOnly;
      }
    } finally {
      if (token === s.loadToken) {
        s.loading = false;
        sync(btn);
        if (active === btn) {
          renderEffort();
          status.textContent = inlineSettingsError(s);
        }
      }
    }
  }
  function ownerVisible(btn) {
    return (
      btn.isConnected &&
      !btn.closest("[hidden],[inert]") &&
      btn.getClientRects().length > 0 &&
      getComputedStyle(btn).visibility === "visible"
    );
  }
  // Body-level panels must not outlive their visible owner. Navigation can
  // retain a hidden composer without firing HTMX cleanup or outside clicks.
  const ownerObserver = new MutationObserver(() => {
    if (!active) return;
    if (!ownerVisible(active)) close(false);
    else observeOwner();
  });
  function observeOwner() {
    ownerObserver.disconnect();
    for (let node = active; node; node = node.parentElement) {
      ownerObserver.observe(node, {
        childList: true,
        attributes: true,
        attributeFilter: ["hidden", "inert", "class", "style"],
      });
    }
  }
  const modelMenus = new WeakMap();
  function modelMenu(btn) {
    let menu = modelMenus.get(btn);
    if (!menu) {
      menu = window.ovMenuController.create({
        trigger: btn,
        panels: () => [panel],
        isOpen: () => active === btn && !panel.hidden,
        open: () => showModelPicker(btn),
        close: hideModelPicker,
      });
      modelMenus.set(btn, menu);
    }
    return menu;
  }
  function close(focus) {
    if (active) modelMenu(active).close(focus);
  }
  function hideModelPicker() {
    ownerObserver.disconnect();
    const btn = active;
    panel.hidden = true;
    active = null;
    if (btn) {
      btn.setAttribute("aria-expanded", "false");
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
    const picked = list.querySelector('[data-model="' + CSS.escape(m.id) + '"]');
    if (picked) picked.focus({ preventScroll: true });
    else search.focus({ preventScroll: true });
  }
  function activateRow(row, target) {
    if (row.hasAttribute("data-picker-active")) return;
    target
      .querySelectorAll("[data-picker-active]")
      .forEach((item) => item.removeAttribute("data-picker-active"));
    row.setAttribute("data-picker-active", "");
  }
  function providers(all) {
    return [
      ...new Set(all.filter((m) => m.provider).map((m) => m.provider)),
    ].sort();
  }
  function initialTabForCurrentModel() {
    if (!active) return "pins";
    const all = options(active);
    const m = current(active);
    if (!m || !m.provider || favorites[m.id]) return "pins";
    return providers(all).includes(m.provider) ? m.provider : "pins";
  }
  function scrollSelectedIntoView() {
    railTabs
      .querySelector('.ov-mp-rail-btn[aria-selected="true"]')
      ?.scrollIntoView({ block: "nearest", inline: "nearest" });
    const selectedRow = list.querySelector('.ov-mp-row[data-selected="true"]');
    if (selectedRow) selectedRow.scrollIntoView({ block: "nearest" });
    else list.scrollTop = 0;
  }
  function switchTab(tab, clearSearch = false) {
    userSwitchedTab = true;
    if (clearSearch && search.value) search.value = "";
    if (activeTab === tab && !clearSearch) return;
    activeTab = tab;
    list.scrollTop = 0;
    renderRail();
    renderList();
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
        const endpoint = favoritesURL();
        const prevStars = [...panel.querySelectorAll(".ov-mp-star")];
        const prevIndex = prevStars.indexOf(star);
        const desired = !favorites[m.id];
        if (desired) favorites[m.id] = true;
        else delete favorites[m.id];
        renderRail();
        renderList();
        position();
        const nextStars = [...panel.querySelectorAll(".ov-mp-star")];
        const focusTarget =
          nextStars.find((b) => b.dataset.id === m.id) ||
          nextStars[Math.min(prevIndex, nextStars.length - 1)] ||
          list.querySelector(".ov-mp-pick") ||
          search;
        focusTarget?.focus({ preventScroll: true });
        favoriteWrites = favoriteWrites
          .catch(() => {})
          .then(() =>
            requestFavorites({ model_id: m.id, favorite: desired }, endpoint),
          );
        favoriteWrites.catch(() => {
          favoritesError =
            "Could not save favorites. Reopen the picker to retry.";
          if (active) {
            status.textContent = favoritesError;
            renderRail();
            renderList();
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
    target.append(r);
  }
  function syncRailSelection() {
    const searching = search.value.trim().length > 0;
    let selectedTabID = "";
    railTabs.querySelectorAll(".ov-mp-rail-btn").forEach((b) => {
      const selected = !searching && activeTab === b.dataset.tab;
      b.setAttribute("aria-selected", String(selected));
      if (selected) selectedTabID = b.id;
    });
    if (selectedTabID) list.setAttribute("aria-labelledby", selectedTabID);
    else list.removeAttribute("aria-labelledby");
  }
  function renderRail() {
    if (!active) return;
    const icons = providerIcons();
    const hasIcons = !!window._ovModelProviderIcons;
    if (!manageBtn.innerHTML && icons.__manage) {
      manageBtn.innerHTML = icons.__manage;
    }
    const scroll = railTabs.scrollTop;
    const all = options(active);
    const listProviders = providers(all);
    if (activeTab !== "pins" && !listProviders.includes(activeTab)) {
      activeTab = "pins";
    }
    const existing = [...railTabs.querySelectorAll(".ov-mp-provider")].map(
      (b) => b.dataset.provider,
    );
    const sameProviders =
      iconsLoaded === hasIcons &&
      railTabs.children.length === listProviders.length + 1 &&
      existing.length === listProviders.length &&
      existing.every((p, i) => p === listProviders[i]);
    if (sameProviders) {
      syncRailSelection();
      return;
    }
    iconsLoaded = hasIcons;
    railTabs.replaceChildren();

    const pinsBtn = el("button", "ov-mp-rail-btn");
    pinsBtn.id = "ov-mp-tab-pins";
    pinsBtn.type = "button";
    pinsBtn.setAttribute("role", "tab");
    pinsBtn.dataset.tab = "pins";
    pinsBtn.setAttribute("aria-label", "Favorites");
    pinsBtn.innerHTML = icons.__pins || "★";
    pinsBtn.onpointerenter = (e) => {
      if (e.pointerType !== "touch" && !search.value.trim()) switchTab("pins");
    };
    pinsBtn.onclick = () => switchTab("pins", true);
    railTabs.append(pinsBtn);

    listProviders.forEach((p, idx) => {
      const b = el("button", "ov-mp-rail-btn ov-mp-provider");
      b.id = "ov-mp-tab-" + idx;
      b.type = "button";
      b.setAttribute("role", "tab");
      b.dataset.tab = p;
      b.dataset.provider = p;
      b.setAttribute("aria-label", p);
      b.innerHTML = icons[p] || icons.__fallback || "";
      b.onpointerenter = (e) => {
        if (e.pointerType !== "touch" && !search.value.trim()) switchTab(p);
      };
      b.onclick = () => switchTab(p, true);
      railTabs.append(b);
    });
    syncRailSelection();
    railTabs.scrollTop = scroll;
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
        (m.name + " " + m.provider + " " + m.model + " " + m.detail)
          .toLowerCase()
          .includes(q),
      );
      matches.sort((a, b) => Number(!a.provider) - Number(!b.provider));
      matches.forEach((m) => row(m, list));
      if (!matches.length)
        list.append(el("div", "ov-mp-empty", "No matching models"));
      return;
    }
    if (activeTab === "pins") {
      const fav = sortModelsByName(
        all.filter((m) => m.provider && favorites[m.id]),
      );
      fav.forEach((m) => row(m, list));
      const defaults = all.filter((m) => !m.provider);
      if (defaults.length) {
        if (fav.length) list.append(el("div", "ov-menu-rule ov-mp-rule"));
        defaults.forEach((m) => row(m, list));
      }
      if (!list.children.length)
        list.append(el("div", "ov-mp-empty", "No pinned models yet"));
    } else {
      const group = sortModelsByName(
        all.filter((m) => m.provider === activeTab),
      );
      group.forEach((m) => row(m, list));
      if (!group.length)
        list.append(el("div", "ov-mp-empty", "No models for this provider"));
    }
    list.scrollTop = scroll;
  }
  function renderEffort() {
    renderSpeed();
    effortBox.replaceChildren();
    const m = active && current(active);
    effortBox.hidden = !m?.levels.length;
    if (effortBox.hidden) {
      if (!panel.hidden) position();
      return;
    }
    const s = state(active),
      override = field(active).value,
      value = override || m.defaultEffort;
    const levels = m.defaultEffort ? m.levels : ["", ...m.levels];
    const head = el("div", "ov-mp-effort-head");
    head.hidden = !override;
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
    slider.disabled = s.loading || !!inlineSettingsError(s);
    slider.setAttribute("aria-label", "Reasoning effort");
    slider.setAttribute(
      "aria-valuetext",
      value ? label(value) : "Model default",
    );
    slider.oninput = () => {
      updateTrack();
      const chosen = levels[Number(slider.value)];
      s.efforts[key(m)] = chosen;
      sync(active);
      const hasOverride = !!field(active).value;
      reset.hidden = !hasOverride;
      head.hidden = !hasOverride;
      position();
      slider.setAttribute(
        "aria-valuetext",
        chosen ? label(chosen) : "Model default",
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
    effortBox.append(head, slider, ticks);
    if (!panel.hidden) position();
  }
  function renderSpeed() {
    speedBox.replaceChildren();
    const m = active && current(active);
    speedBox.hidden = !m?.fast;
    if (speedBox.hidden) return;
    const s = state(active),
      override = s.tiers[key(m)] || "";
    const rowEl = el("div", "ov-mp-speed-row"),
      title = el("span", "", "Fast mode");
    title.id = "ov-mp-fast-label";
    const actions = el("div", "ov-mp-speed-actions");
    const reset = el("button", "ov-mp-reset", "Use default");
    reset.type = "button";
    reset.hidden = !override;
    reset.disabled = s.loading || !!inlineSettingsError(s);
    reset.onclick = () => {
      delete s.tiers[key(m)];
      save(active);
      renderEffort();
    };
    const toggle = el("button", "ov-mp-fast-toggle");
    toggle.type = "button";
    toggle.setAttribute("role", "switch");
    toggle.setAttribute("aria-labelledby", title.id);
    toggle.setAttribute(
      "aria-checked",
      String((override || m.defaultTier) === "priority"),
    );
    toggle.disabled = s.loading || !!inlineSettingsError(s);
    toggle.onclick = () => {
      s.tiers[key(m)] =
        (override || m.defaultTier) === "priority" ? "default" : "priority";
      save(active);
      renderEffort();
    };
    actions.append(reset, toggle);
    rowEl.append(title, actions);
    speedBox.append(rowEl);
  }
  function render() {
    if (!active) return;
    renderRail();
    renderList();
    renderEffort();
    status.textContent =
      inlineSettingsError(state(active)) || favoritesError || "";
    position();
  }
  function position() {
    if (!active) return;
    if (!active.isConnected) {
      close(false);
      return;
    }
    // The button scales when pressed; its wrapper keeps a stable layout box
    // while opening the menu and when a selection updates the button label.
    const r = active.parentElement.getBoundingClientRect(),
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
  }
  function usesAppleShortcuts() {
    return /Mac|iPhone|iPad|iPod|iOS/i.test(
      navigator.userAgentData?.platform ||
        navigator.platform ||
        navigator.userAgent ||
        "",
    );
  }
  function init(btn) {
    const apple = usesAppleShortcuts();
    btn.setAttribute("aria-keyshortcuts", apple ? "Meta+M" : "Control+M");
    btn.title = apple ? "Models (⌘M)" : "Models (Ctrl+M)";
    if (state(btn)) return;
    btn._ovModelState = {
      efforts: {},
      tiers: {},
      pending: Promise.resolve(),
      loading: false,
      loadToken: 0,
      error: "",
      initialLoad: true,
    };
    btn.setAttribute("aria-haspopup", "dialog");
    btn.setAttribute("aria-controls", panel.id);
    sync(btn);
    if (settingsEndpoint(btn)) {
      btn._ovModelState.ready = load(btn, true);
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
  function showModelPicker(btn) {
    if (!ownerVisible(btn)) return;
    init(btn);
    active = btn;
    userSwitchedTab = false;
    activeTab = initialTabForCurrentModel();
    search.value = "";
    panel.hidden = false;
    btn.setAttribute("aria-expanded", "true");
    render();
    scrollSelectedIntoView();
    loadFavorites();
    search.focus({ preventScroll: true });
    observeOwner();
    const s = state(btn);
    if (s.error) {
      s.ready = s.failedSave
        ? save(btn).catch(() => {})
        : s.selectionPending
          ? loadSelection(btn)
          : load(btn);
    }
  }
  window.ovModelPicker = {
    init,
    save,
    close,
    open(btn) {
      if (ownerVisible(btn)) modelMenu(btn).open();
    },
    toggle(btn, event) {
      if (ownerVisible(btn)) modelMenu(btn).toggle(event);
    },
    destinationLoading(btn) {
      init(btn);
      const s = state(btn);
      s.loadToken++;
      s.selectionPending = null;
      s.error = "";
      s.permissionError = false;
      s.failedSave = false;
      s.initialLoad = true;
    },
    restore(btn) {
      init(btn);
      const s = state(btn);
      s.ready = load(btn, true);
      return s.ready;
    },
    changed(btn) {
      init(btn);
      const s = state(btn);
      s.loadToken++;
      s.initialLoad = false;
      s.loading = false;
      s.error = "";
      s.permissionError = false;
      s.failedSave = false;
      sync(btn);
      // Fetch an existing task/model preference before persisting a newly selected model.
      if (settingsEndpoint(btn)) {
        s.selectionPending = btn.dataset.currentValue;
        s.ready = loadSelection(btn);
      } else save(btn);
    },
  };
  manageBtn.onclick = () => {
    const btn = active;
    close(false);
    const navLink = document.querySelector(
      '#appearance-settings-nav [data-nav-base="/models"], [data-nav-base="/models"]',
    );
    if (navLink) {
      navLink.click();
      return;
    }
    const project =
      btn?.closest("form")?.querySelector('[name="_project_id"]')?.value ||
      document.getElementById("project-selector")?.value ||
      "";
    const url = project
      ? "/models?project_id=" + encodeURIComponent(project)
      : "/models";
    window.location.assign(url);
  };
  panel.onpointerover = (e) => {
    if (e.pointerType === "touch") return;
    if (!e.target.closest(".ov-mp-row"))
      list
        .querySelectorAll("[data-picker-active]")
        .forEach((rowEl) => rowEl.removeAttribute("data-picker-active"));
  };
  search.oninput = () => {
    list.scrollTop = 0;
    render();
  };
  clear.onclick = () => {
    search.value = "";
    render();
    search.focus();
  };
  document.addEventListener(
    "keydown",
    (e) => {
      if (
        !e.defaultPrevented &&
        !e.isComposing &&
        !e.repeat &&
        (usesAppleShortcuts()
          ? e.metaKey && !e.ctrlKey
          : e.ctrlKey && !e.metaKey) &&
        !e.shiftKey &&
        !e.altKey &&
        e.key.toLowerCase() === "m"
      ) {
        const buttons = [
          ...document.querySelectorAll(
            "form.chat-input-container .chat-model-select",
          ),
        ];
        const visible = (btn) =>
          !btn.disabled &&
          btn.getClientRects().length > 0 &&
          getComputedStyle(btn).visibility !== "hidden" &&
          !btn.closest("[inert]");
        const focusedForm = document.activeElement?.closest(
          "form.chat-input-container",
        );
        const btn =
          buttons.find(
            (btn) => visible(btn) && btn.closest("form") === focusedForm,
          ) || buttons.find(visible);
        if (
          !btn ||
          (document.querySelector("dialog[open]") &&
            !btn.closest("dialog[open]"))
        )
          return;
        e.preventDefault();
        e.stopPropagation();
        if (active) close(true);
        else window.ovModelPicker.open(btn);
        return;
      }
      if (!active || e.defaultPrevented || e.isComposing || e.keyCode === 229)
        return;
      if (
        e.key === "ArrowRight" &&
        document.activeElement?.closest(".ov-mp-rail")
      ) {
        const firstPick = list.querySelector(".ov-mp-pick");
        if (firstPick) {
          e.preventDefault();
          firstPick.focus({ preventScroll: true });
          window.openVibelyScrollIntoPanel?.(firstPick);
        }
        return;
      }
      if (
        e.key === "ArrowLeft" &&
        document.activeElement?.closest(".ov-mp-list")
      ) {
        const tab =
          railTabs.querySelector('.ov-mp-rail-btn[aria-selected="true"]') ||
          railTabs.querySelector(".ov-mp-rail-btn");
        if (tab) {
          e.preventDefault();
          tab.focus({ preventScroll: true });
        }
        return;
      }
      if (
        e.target.type === "range" ||
        !["ArrowDown", "ArrowUp"].includes(e.key)
      )
        return;
      if (document.activeElement === manageBtn) {
        const tabs = [...railTabs.querySelectorAll(".ov-mp-rail-btn")];
        if (!tabs.length) return;
        e.preventDefault();
        const targetTab = e.key === "ArrowUp" ? tabs[tabs.length - 1] : tabs[0];
        targetTab.focus({ preventScroll: true });
        switchTab(targetTab.dataset.tab, true);
        targetTab.scrollIntoView({ block: "nearest" });
        return;
      }
      if (document.activeElement?.closest(".ov-mp-rail-tabs")) {
        const tabs = [...railTabs.querySelectorAll(".ov-mp-rail-btn")];
        if (!tabs.length) return;
        e.preventDefault();
        const i = tabs.indexOf(document.activeElement),
          next =
            e.key === "ArrowDown"
              ? (i + 1) % tabs.length
              : i <= 0
                ? tabs.length - 1
                : i - 1;
        tabs[next].focus({ preventScroll: true });
        switchTab(tabs[next].dataset.tab, true);
        tabs[next].scrollIntoView({ block: "nearest" });
        return;
      }
      const rows = [...list.querySelectorAll(".ov-mp-pick")];
      if (!rows.length) return;
      e.preventDefault();
      const i = rows.indexOf(document.activeElement),
        next =
          e.key === "ArrowDown"
            ? (i + 1) % rows.length
            : i <= 0
              ? rows.length - 1
              : i - 1;
      rows[next].focus({ preventScroll: true });
      if (window.openVibelyScrollIntoPanel)
        window.openVibelyScrollIntoPanel(rows[next]);
      else rows[next].scrollIntoView({ block: "nearest" });
    },
    true,
  );
  window.addEventListener("resize", position);
  document.addEventListener(
    "scroll",
    (e) => {
      if (active && !panel.contains(e.target)) position();
    },
    true,
  );
  document.addEventListener("htmx:beforeCleanupElement", (e) => {
    if (active && e.detail?.elt?.contains(active)) close(false);
  });
  document.addEventListener(
    "submit",
    (e) => {
      if ((e.target.dataset.destinationModelsPending === "true" || e.target.dataset.destinationAttachmentsPending === "true")) {
        e.preventDefault();
        e.stopImmediatePropagation();
        return;
      }
      const btn = e.target.querySelector(".chat-model-select");
      if (!btn || !state(btn)) return;
      if (btn._ovResubmit) {
        // The event owns this snapshot until HTMX starts (or drops) this send.
        e._chatSubmissionPayload = e.target._chatNextSubmissionPayload;
        return;
      }
      const s = state(btn);
      if (!settingsEndpoint(btn)) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      const submitter = e.submitter;
      const form = e.target;
      const message = form.querySelector('[name="message"]');
      const attachment = form.querySelector('[name="attachment_session_id"]');
      const model = form.querySelector('[name="agent_id"]');
      const effort = form.querySelector('[name="reasoning_effort"]');
      const tier = tierField(btn);
      // The composer temporarily installs a steering fallback payload, then
      // restores the newer draft synchronously. Preserve the selected model
      // and effort too, since either can change while the save completes.
      const capturedEffort =
        form._chatNextSubmissionPayload?.reasoningEffort !== undefined;
      const capturedTier =
        form._chatNextSubmissionPayload?.serviceTier !== undefined;
      const payload = form._captureChatSubmission(
        form._chatNextSubmissionPayload,
      );
      payload.managedModelSelection = !!form.dataset.conversationTaskId;
      // This attempt owns the captured snapshot while settings load. Do not
      // leave it on the form for a later Send if loading or saving fails.
      form._chatNextSubmissionPayload = null;
      const waitingForEffort = s.loading && !capturedEffort;
      const waitingForTier = s.loading && !capturedTier;
      const ready = s.ready;
      const pending = s.pending;
      Promise.resolve(ready)
        .then((loadedEffort) => {
          if (waitingForEffort) {
            if (loadedEffort === undefined)
              throw Error("Could not load effort for the selected model.");
            payload.reasoningEffort = loadedEffort.effort;
            if (loadedEffort.modelID) payload.modelID = loadedEffort.modelID;
          }
          if (waitingForTier && loadedEffort)
            payload.serviceTier = loadedEffort.tier;
          return pending;
        })
        .then(() => {
          if (btn.dataset.currentValue === payload.modelID && s.error) {
            const error = Error(s.error);
            error.toastOnly = s.permissionError;
            throw error;
          }
          if (!form.isConnected) return;
          const draft = message?.value;
          const draftAttachments = attachment?.value;
          const draftModel = model?.value;
          const draftEffort = effort?.value;
          const draftTier = tier?.value;
          form._chatNextSubmissionPayload = payload;
          if (message) message.value = payload.message;
          if (attachment) attachment.value = payload.attachmentSessionID;
          if (model) model.value = payload.modelID;
          if (effort) effort.value = payload.reasoningEffort;
          if (tier) tier.value = payload.serviceTier || "";
          const managedSelection = el("input");
          managedSelection.type = "hidden";
          managedSelection.name = "model_selection_managed";
          managedSelection.value = "1";
          form.append(managedSelection);
          btn._ovResubmit = true;
          try {
            // Model settings may finish loading after polling replaces the
            // send button. Only reuse a submitter still owned by this form.
            // Older desktop WebViews use the composer's synthetic-submit path.
            if (typeof form.requestSubmit === "function") {
              const liveSubmitter =
                submitter?.form === form && submitter.type === "submit"
                  ? submitter
                  : form.querySelector(
                      'button[type="submit"],input[type="submit"]',
                    );
              form.requestSubmit(liveSubmitter || undefined);
            } else {
              form.dispatchEvent(
                new Event("submit", { bubbles: true, cancelable: true }),
              );
            }
          } finally {
            // Queued sends retain the snapshot on their triggering event.
            // Clear the temporary handoff even when submission fails or is
            // cancelled, without clearing a newer submission's snapshot.
            if (form._chatNextSubmissionPayload === payload)
              form._chatNextSubmissionPayload = null;
            btn._ovResubmit = false;
            managedSelection.remove();
            if (message) message.value = draft;
            if (attachment) attachment.value = draftAttachments;
            if (model) model.value = draftModel;
            if (effort) effort.value = draftEffort;
            if (tier) tier.value = draftTier;
          }
        })
        .catch((err) => {
          if (!err.toastOnly) {
            window.ovModelPicker.open(btn);
            status.textContent = err.message;
          }
          form._chatSubmissionFailed?.(payload);
        });
    },
    true,
  );
})();

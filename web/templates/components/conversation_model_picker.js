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
  const panel = el("div", "ov-model-picker"),
    sub = el("div", "ov-model-submenu");
  panel.id = "conversation-model-picker";
  panel.hidden = true;
  panel.setAttribute("role", "dialog");
  panel.setAttribute("aria-label", "Choose model");
  sub.hidden = true;
  sub.id = "conversation-provider-models";
  sub.setAttribute("role", "dialog");
  sub.setAttribute("aria-label", "Provider models");
  const searchWrap = el("div", "ov-mp-search"),
    search = el("input"),
    clear = el("button", "ov-mp-clear", "×");
  search.placeholder = "Search models…";
  search.setAttribute("aria-label", "Search models");
  clear.type = "button";
  clear.setAttribute("aria-label", "Clear search");
  searchWrap.append(search, clear);
  const list = el("div", "ov-mp-list"),
    effortBox = el("div", "ov-mp-effort"),
    status = el("div", "ov-mp-status");
  status.setAttribute("role", "status");
  panel.append(searchWrap, list, effortBox, status);
  const subhead = el("div", "ov-mp-subhead"),
    back = el("button", "ov-mp-back", "‹"),
    sublist = el("div", "ov-mp-sublist");
  back.type = "button";
  back.setAttribute("aria-label", "Back to providers");
  subhead.append(back);
  subhead.hidden = true;
  sub.append(subhead, sublist);
  document.body.append(panel, sub);
  let active = null,
    provider = null,
    subCloseTimer = null,
    favorites = read(favoritesKey);
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
      m.name + (field(btn).value ? " · " + label(field(btn).value) : "");
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
  function closeSub() {
    cancelSubClose();
    provider = null;
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
    target
      .querySelectorAll("[data-picker-active]")
      .forEach((item) => item.removeAttribute("data-picker-active"));
    row.setAttribute("data-picker-active", "");
  }
  function row(m, target) {
    const r = el("div", "ov-mp-row");
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
      star.onclick = () => {
        favorites[m.id] = !favorites[m.id];
        write(favoritesKey, favorites);
        renderList();
        renderSub();
        const button = [
          ...panel.querySelectorAll(".ov-mp-star"),
          ...sub.querySelectorAll(".ov-mp-star"),
        ].find((b) => b.dataset.id === m.id);
        button?.focus({ preventScroll: true });
      };
      star.dataset.id = m.id;
      r.append(star);
    }
    const pick = el("button", "ov-mp-pick");
    pick.type = "button";
    pick.dataset.model = m.id;
    pick.setAttribute(
      "aria-pressed",
      String(m.id === active.dataset.currentValue),
    );
    const copy = el("span", "ov-mp-copy");
    copy.append(el("span", "ov-mp-name", m.name));
    if (m.provider)
      copy.append(
        el(
          "span",
          "ov-mp-detail",
          m.provider + (m.model ? " · " + m.model : ""),
        ),
      );
    pick.append(copy);
    pick.onclick = () => choose(m);
    r.prepend(pick);
    const check = el(
      "span",
      "ov-mp-check",
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
        (m.name + " " + m.provider + " " + m.model).toLowerCase().includes(q),
      );
      matches.sort((a, b) => Number(!a.provider) - Number(!b.provider));
      matches.forEach((m) => row(m, list));
      if (!matches.length)
        list.append(el("div", "ov-mp-empty", "No matching models"));
      return;
    }
    all.filter((m) => !m.provider).forEach((m) => row(m, list));
    list.append(el("div", "ov-mp-rule"));
    const fav = all.filter((m) => m.provider && favorites[m.id]);
    if (fav.length) {
      list.append(el("div", "ov-mp-heading", "FAVORITES"));
      fav.forEach((m) => row(m, list));
      list.append(el("div", "ov-mp-rule"));
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
        const b = el("button", "ov-mp-provider");
        b.type = "button";
        b.dataset.provider = p;
        b.onpointermove = () => activateRow(b, list);
        b.onfocus = () => activateRow(b, list);
        b.onpointerenter = (e) => {
          if (e.pointerType !== "touch") openProvider(p);
        };
        b.setAttribute("aria-haspopup", "dialog");
        b.setAttribute("aria-controls", sub.id);
        b.setAttribute("aria-expanded", String(provider === p));
        b.append(el("span", "", p), el("span", "", "›"));
        b.onclick = () => openProvider(p);
        list.append(b);
      });
    list.scrollTop = scroll;
  }
  function openProvider(p) {
    cancelSubClose();
    if (provider === p && !sub.hidden) return;
    provider = p;
    list.querySelectorAll(".ov-mp-provider").forEach((b) => {
      b.setAttribute("aria-expanded", String(b.dataset.provider === p));
    });
    renderSub();
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
    renderList();
    renderEffort();
    status.textContent = state(active).error || "";
    position();
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
      let left = p.right + 6;
      if (left + sub.offsetWidth > width - 8)
        left = p.left - sub.offsetWidth - 6;
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
  function init(btn) {
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
      favorites = read(favoritesKey);
      const selected = current(btn);
      provider =
        selected?.provider && !favorites[selected.id]
          ? selected.provider
          : null;
      search.value = "";
      panel.hidden = false;
      btn.setAttribute("aria-expanded", "true");
      render();
      renderSub();
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
  sub.onpointerenter = cancelSubClose;
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

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
    subname = el("span"),
    sublist = el("div", "ov-mp-sublist");
  back.type = "button";
  back.setAttribute("aria-label", "Back to providers");
  subhead.append(back, subname);
  sub.append(subhead, sublist);
  document.body.append(panel, sub);
  let active = null,
    provider = null,
    favorites = read(favoritesKey);
  function options(btn) {
    return [...btn.parentElement.querySelectorAll("li[data-value]")].map(
      (li) => ({
        id: li.dataset.value,
        name: li.dataset.pickerName || li.textContent.trim(),
        model: li.dataset.pickerModel || "",
        provider: li.dataset.pickerProvider || "",
        levels: (li.dataset.pickerEfforts || "").split(",").filter(Boolean),
        defaultEffort: li.dataset.pickerDefault || "",
      }),
    );
  }
  function state(btn) {
    return btn._ovModelState;
  }
  function key(m) {
    return m.id + ":" + m.model;
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
        if (active === btn) status.textContent = "";
      },
      (err) => {
        s.error = err.message;
        if (active === btn) status.textContent = s.error;
      },
    );
    return s.pending;
  }
  async function load(btn) {
    const s = state(btn),
      m = current(btn),
      endpoint = taskEndpoint(btn);
    if (!endpoint || !m || !m.provider) return;
    const token = ++s.loadToken;
    s.loading = true;
    sync(btn);
    if (active === btn) renderEffort();
    try {
      await s.pending;
      const response = await fetch(
        endpoint + "?agent_id=" + encodeURIComponent(m.id),
      );
      if (!response.ok)
        throw Error("Could not load effort. Reopen the picker to retry.");
      const data = await response.json();
      if (token !== s.loadToken) return;
      s.efforts[key(m)] = data.reasoning_effort || "";
      s.error = "";
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
  function closeSub() {
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
  function row(m, target) {
    const r = el("div", "ov-mp-row");
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
    pick.append(
      copy,
      el(
        "span",
        "ov-mp-check",
        m.id === active.dataset.currentValue ? "✓" : "",
      ),
    );
    pick.onclick = () => choose(m);
    r.append(pick);
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
        b.setAttribute("aria-haspopup", "dialog");
        b.setAttribute("aria-controls", sub.id);
        b.setAttribute("aria-expanded", String(provider === p));
        b.append(el("span", "", p), el("span", "", "›"));
        b.onclick = () => {
          provider = provider === p ? null : p;
          renderList();
          renderSub();
        };
        list.append(b);
      });
    list.scrollTop = scroll;
  }
  function renderSub() {
    if (!active || !provider) {
      sub.hidden = true;
      return;
    }
    sublist.replaceChildren();
    subname.textContent = provider;
    options(active)
      .filter((m) => m.provider === provider && !favorites[m.id])
      .forEach((m) => row(m, sublist));
    if (!sublist.children.length) {
      closeSub();
      return;
    }
    sub.hidden = false;
    position();
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
    head.append(
      el("span", "", "Reasoning effort"),
      el("span", "", value ? label(value) : "Model default"),
    );
    effortBox.append(
      head,
      el("div", "ov-mp-effort-model", m.name + (override ? "" : " · Default")),
    );
    const slider = el("input");
    slider.type = "range";
    slider.min = "0";
    slider.max = String(levels.length - 1);
    slider.step = "1";
    slider.value = String(Math.max(0, levels.indexOf(value)));
    slider.disabled = s.loading || !!s.error;
    slider.setAttribute("aria-label", "Reasoning effort");
    slider.setAttribute(
      "aria-valuetext",
      value ? label(value) : "Model default",
    );
    slider.oninput = () => {
      const value = levels[Number(slider.value)];
      s.efforts[key(m)] = value;
      sync(active);
      head.lastChild.textContent = value ? label(value) : "Model default";
      slider.setAttribute(
        "aria-valuetext",
        value ? label(value) : "Model default",
      );
      effortBox.querySelector(".ov-mp-effort-model").textContent = m.name;
    };
    slider.onchange = () => {
      save(active);
      renderEffort();
    };
    const ticks = el("div", "ov-mp-ticks");
    levels.forEach((v) =>
      ticks.append(el("span", "", v ? label(v) : "Default")),
    );
    const reset = el("button", "ov-mp-reset", "Use model default");
    reset.type = "button";
    reset.disabled = !override || s.loading;
    reset.onclick = () => {
      delete s.efforts[key(m)];
      save(active);
      renderEffort();
    };
    effortBox.append(slider, ticks, reset);
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
    if (provider && !sub.hidden) {
      const b = [...list.querySelectorAll(".ov-mp-provider")].find(
        (b) => b.dataset.provider === provider,
      );
      if (!b) {
        closeSub();
        return;
      }
      const p = panel.getBoundingClientRect(),
        a = b.getBoundingClientRect();
      let left = p.right + 6;
      if (left + sub.offsetWidth > width - 8)
        left = p.left - sub.offsetWidth - 6;
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
      if (state(btn).error) state(btn).ready = load(btn);
    },
    changed(btn) {
      init(btn);
      const s = state(btn);
      s.loadToken++;
      s.loading = false;
      s.error = "";
      sync(btn);
      // Fetch an existing task/model preference before persisting a newly selected model.
      if (taskEndpoint(btn)) {
        const chosen = btn.dataset.currentValue;
        const promise = load(btn);
        const token = s.loadToken;
        s.ready = promise
          .then(() => {
            if (
              token !== s.loadToken ||
              chosen !== btn.dataset.currentValue ||
              s.error
            )
              return;
            return save(btn);
          })
          .catch(() => {});
      } else save(btn);
    },
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
      else if (active && provider) position();
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
      Promise.resolve(s.ready)
        .then(() => s.pending)
        .then(() => {
          if (s.error) throw Error(s.error);
          btn._ovResubmit = true;
          try {
            e.target.requestSubmit(submitter);
          } finally {
            btn._ovResubmit = false;
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

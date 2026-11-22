(() => {
  "use strict";
  const bridge = window.cageWorkstations;
  if (!bridge) return;

  let requestedServer = "";
  const labels = {
    approved: "已配对", unpaired: "可申请连接", pending: "等待 Cage 批准",
    denied: "申请已拒绝", offline: "离线", identity_changed: "客机身份已变化",
  };
  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text) node.textContent = text;
    return node;
  }
  function button(text, handler, className) {
    const btn = element("button", className, text);
    btn.type = "button";
    btn.onclick = handler;
    return btn;
  }

  // Each mounted page owns its polling. Leaving it also invalidates outstanding UI updates.
  function mount(panel) {
    const standalone = panel.hasAttribute("standalone");
    let alive = true, polling = false, acting = false, scanAt = 0;
    let info, auto = false, filter = "all", rows, rendered = "";
    const status = element("p", "cage-station-status");
    status.setAttribute("role", "status");
    const report = message => {
      if (alive) status.textContent = String(message?.message || message).replace(/^Error invoking remote method '[^']+': (Error: )?/, "");
    };

    const filterBar = element("div", "cage-station-filters");
    filterBar.setAttribute("role", "group");
    filterBar.setAttribute("aria-label", "筛选服务器");
    const all = button("全部服务器", () => changeFilter("all"));
    const paired = button("已配对", () => changeFilter("paired"));
    filterBar.append(all, paired);
    const scan = button("重新搜索", () => refresh(true), "cage-station-rescan");
    const toolbar = element("div", "cage-station-toolbar");
    toolbar.append(filterBar, scan);
    const list = element("ul", "cage-station-list");
    list.setAttribute("aria-label", "服务器列表");
    const empty = element("p", "cage-station-empty", "正在搜索局域网服务器…");
    const local = button("打开原有本机工作台", () => perform(() => open("local")), "cage-station-local");
    local.hidden = true;
    if (standalone) panel.append(element("h1", "", "局域网服务器"));
    panel.append(toolbar, list, empty, status, local);

    function changeFilter(value) { filter = value; render(); }
    async function open(id) {
      requestedServer = "";
      auto = false;
      await bridge.call("open", { id });
      if (alive && !standalone) window.dispatchEvent(new Event("cage-workbench-opened"));
    }
    async function perform(action) {
      if (!alive || acting) return;
      acting = true;
      render();
      try { await action(); } catch (error) { report(error); }
      finally {
        acting = false;
        if (alive) { render(); void refresh(false); }
      }
    }
    async function request(id) {
      const reply = await bridge.call("request", { id });
      if (reply.state === "approved") {
        if (alive) await open(id);
      } else if (reply.state === "pending") {
        requestedServer = id;
        report("请在 Cage 服务端的「工作机连接」中核对配对编号并批准。");
      } else {
        report("服务端暂时无法受理，请稍后重试。");
      }
    }
    function render() {
      all.setAttribute("aria-pressed", String(filter === "all"));
      paired.setAttribute("aria-pressed", String(filter === "paired"));
      local.hidden = !(info?.local && filter === "all");
      local.disabled = acting;
      if (!rows) return;
      const visible = rows.filter(row => filter === "all" || row.saved || row.state === "approved");
      const signature = JSON.stringify([filter, visible, acting]);
      if (signature === rendered) return;
      rendered = signature;
      list.replaceChildren();
      empty.hidden = visible.length > 0;
      empty.textContent = filter === "paired" ? "暂无已配对的服务器。" :
        "尚未发现服务器。请确认 Cage 已运行，且两台电脑在同一局域网。";
      for (const row of visible) {
        const item = element("li", "cage-station-row");
        const detail = element("div", "cage-station-detail");
        detail.append(element("strong", "", row.name),
          element("span", "cage-station-meta", (labels[row.state] || row.state) + " · " + row.address));
        if (row.code) detail.append(element("span", "cage-station-code", "配对编号 " + row.code));
        const actions = element("div", "cage-station-actions");
        function addAction(label, handler, primary = false) {
          const action = button(label, () => perform(handler), primary ? "cage-station-primary" : "");
          action.disabled = acting;
          actions.append(action);
        }
        if (row.state === "approved") addAction("打开工作台", () => open(row.id), true);
        if (row.state === "unpaired" || row.state === "denied") addAction("申请连接", () => request(row.id), true);
        if (filter === "paired") addAction("忘记连接", async () => {
          if (window.confirm("忘记这台服务器的本机配对记录？")) {
            await bridge.call("forget", { id: row.id });
            if (requestedServer === row.id) requestedServer = "";
            report("已忘记连接。");
          }
        });
        item.append(detail, actions);
        list.append(item);
      }
    }
    async function refresh(force) {
      if (force) scanAt = 0;
      if (!alive || polling || acting) return;
      polling = true;
      try {
        if (!info) {
          info = await bridge.call("info");
          if (!alive) return;
          auto = standalone && info.auto;
        }
        const discover = Date.now() >= scanAt;
        if (discover) scanAt = Date.now() + 10000;
        scan.disabled = discover;
        if (discover) scan.textContent = "搜索中…";
        const result = await bridge.call(discover ? "scan" : "servers", discover ? {} : undefined);
        if (!alive) return;
        rows = result;
        render();
        const desired = requestedServer || (auto ? info.last : "");
        const target = rows.find(row => row.id === desired);
        if (!acting && target?.state === "approved") {
          await perform(() => open(target.id));
        } else if (requestedServer && (target?.state === "unpaired" || target?.state === "denied")) {
          requestedServer = "";
          report("申请已结束，可以重新申请。");
        }
      } catch (error) {
        if (alive && !rows) empty.hidden = true;
        report(error);
      }
      finally {
        polling = false;
        if (alive) { scan.disabled = false; scan.textContent = "重新搜索"; }
      }
    }
    const rescan = () => refresh(true);
    panel.addEventListener("cage-scan", rescan);
    render();
    void refresh(true);
    const timer = setInterval(() => { if (standalone || !document.hidden) void refresh(false); }, 1800);
    return () => {
      alive = false;
      clearInterval(timer);
      panel.removeEventListener("cage-scan", rescan);
      panel.replaceChildren();
    };
  }

  customElements.define("cage-server-list", class extends HTMLElement {
    connectedCallback() { this.dispose = mount(this); }
    disconnectedCallback() { this.dispose?.(); this.dispose = undefined; }
  });
  if (document.body.classList.contains("cage-selector")) {
    const scan = () => document.querySelector("cage-server-list")?.dispatchEvent(new Event("cage-scan"));
    bridge.onShow(scan);
    document.getElementById("cage-selector-servers")?.addEventListener("click", scan);
  }
})();

(() => {
  "use strict";
  const bridge = window.cageWorkstations;
  if (!bridge) return;
  const onHistory = () => location.pathname.replace(/\/$/, "") === "/sessions";
  const element = (tag, cls, text) => {
    const node = document.createElement(tag);
    node.className = cls;
    if (text) node.textContent = text;
    return node;
  };
  const dialog = element("dialog", "cage-history-dialog");
  const heading = element("h2", "", "彻底删除本机会话？");
  heading.id = "cage-history-heading";
  dialog.setAttribute("aria-labelledby", heading.id);
  const description = element("p", ""), error = element("p", "cage-history-error");
  error.setAttribute("role", "alert");
  const actions = element("div", "cage-history-actions");
  const cancel = element("button", "", "取消"), confirm = element("button", "cage-history-confirm", "彻底删除");
  actions.append(cancel, confirm);
  dialog.append(heading, description, error, actions);
  const menu = element("div", "cage-history-menu");
  menu.setAttribute("role", "menu");
  const menuDelete = element("button", "", "彻底删除本机会话…");
  menuDelete.setAttribute("role","menuitem");
  menu.append(menuDelete); menu.hidden = true;
  const recovery = element("div", "cage-history-recovery");
  recovery.hidden = true;
  document.body.append(dialog, menu, recovery);
  let server = "", items = new Map(), chosen, busy = false, updating = false;
  let lastRead = 0, scheduled = false;
  const friendly = e => String(e?.message || e).replace(/^Error invoking remote method '[^']+': (Error: )?/, "");
  const rowId = row => {
    const prefix = "agent-row-" + server + "-";
    const key = row?.getAttribute("data-testid");
    return server && key?.startsWith(prefix) ? key.slice(prefix.length) : null;
  };
  function ask(item) {
    if (!item || busy) return;
    chosen = item; menu.hidden = true;
    description.textContent = "将永久清理「" + (item.title || item.id) + "」在这台工作机上的会话记录和专属文件。Cage 服务端副本可在「工作机连接」中管理。";
    error.textContent = "";
    if (!dialog.open) dialog.showModal();
    cancel.focus();
  }
  cancel.onclick = () => dialog.close();
  dialog.addEventListener("cancel", e => { if (busy) e.preventDefault(); });
  async function clearCachedConversation(serverId, agentId) {
    const name = "paseo-replica-row-store";
    if (!(await indexedDB.databases()).some(db => db.name === name)) return;
    const db = await new Promise((resolve,reject) => {
      const request = indexedDB.open(name);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    try {
      if (!db.objectStoreNames.contains("rows")) return;
      await new Promise((resolve,reject) => {
        const tx = db.transaction("rows","readwrite");
        tx.oncomplete = resolve;
        tx.onabort = tx.onerror = () => reject(tx.error || Error("工作台历史缓存清理失败"));
        for (const kind of ["agent","timeline"]) tx.objectStore("rows").delete([serverId,kind,agentId]);
      });
    } finally { db.close(); }
  }
  confirm.onclick = async () => {
    if (busy || !chosen) return;
    busy = true; confirm.disabled = cancel.disabled = true;
    try {
      await clearCachedConversation(server, chosen.id);
      await bridge.call("delete-local-session", {serverId: server, agentId: chosen.id});
      items.delete(chosen.id);
      dialog.close();
      window.dispatchEvent(new Event("cage-history-changed"));
      await refresh();
    } catch (e) {
      error.textContent = friendly(e);
      window.dispatchEvent(new Event("cage-history-changed"));
      await refresh();
    } finally { busy = false; confirm.disabled = cancel.disabled = false; }
  };
  function wire() {
    if (!onHistory()) { menu.hidden = true; recovery.hidden = true; return; }
    for (const row of document.querySelectorAll('[role="button"][data-testid^="agent-row-"]')) {
      const id = rowId(row), item = items.get(id);
      const old = row.querySelector(":scope > .cage-history-delete");
      if (!item) { if (old) old.remove(); continue; }
      if (old?.dataset.session === id) continue;
      old?.remove();
      const btn = element("button","cage-history-delete","彻底删除");
      btn.type = "button";
      btn.dataset.session = id;
      btn.setAttribute("aria-label","彻底删除本机会话 " + (item.title || id));
      // Don't trigger Paseo's row long press or navigation while pressing a nested action.
      for (const type of ["pointerdown","pointerup","mousedown","mouseup","touchstart","touchend","keydown","keyup"])
        btn.addEventListener(type, e => e.stopPropagation());
      btn.onclick = e => {e.preventDefault();e.stopPropagation();ask(items.get(id));};
      row.append(btn);
    }
  }
  async function refresh() {
    if (updating || !onHistory()) return;
    updating = true; lastRead = Date.now();
    try {
      const page = await bridge.call("local-history");
      server = page.serverId;
      items = new Map(page.archived.map(r => [r.id,r]));
      recovery.replaceChildren();
      for (const r of page.archived.filter(r => r.pending)) {
        const b = element("button","","继续清理「"+(r.title || r.id)+"」");
        b.onclick = () => ask(r); recovery.append(b);
      }
      recovery.hidden = !onHistory() || !recovery.children.length;
      wire();
    } catch (e) {
      recovery.replaceChildren(element("span","",friendly(e)));
      recovery.hidden = !onHistory();
    } finally { updating = false; }
  }
  document.addEventListener("contextmenu", e => {
    if (!onHistory()) return;
    const row = e.target.closest?.('[role="button"][data-testid^="agent-row-"]');
    const item = items.get(rowId(row));
    if (!item) return;
    e.preventDefault(); e.stopPropagation();
    chosen = item; menu.hidden = false;
    menu.style.left = Math.max(0,Math.min(e.clientX, innerWidth-menu.offsetWidth-6))+"px";
    menu.style.top = Math.max(0,Math.min(e.clientY, innerHeight-menu.offsetHeight-6))+"px";
    menuDelete.focus();
  }, true);
  menuDelete.onclick = () => ask(chosen);
  document.addEventListener("pointerdown", e => { if (!menu.contains(e.target)) menu.hidden = true; }, true);
  document.addEventListener("keydown", e => { if (e.key === "Escape") menu.hidden = true; });
  function tick() {
    scheduled = false;
    if (onHistory() && Date.now()-lastRead>3500) void refresh();
    wire();
  }
  new MutationObserver(() => {
    if (!scheduled) { scheduled=true; queueMicrotask(tick); }
  }).observe(document.getElementById("root") || document.body, {subtree:true,childList:true});
  document.addEventListener("visibilitychange", () => { if (!document.hidden) tick(); });
  setInterval(() => { if (!document.hidden) tick(); },4000);
  tick();
})();

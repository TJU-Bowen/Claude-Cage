import React, { useCallback, useEffect, useRef, useState } from "react";
import { ArrowLeft, MonitorCheck, RefreshCw, Trash2 } from "lucide-react";
import { native } from "./api";

function space(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = bytes / 1024, i = 0;
  while (value >= 1024 && i < units.length - 1) { value /= 1024; i++; }
  return `${value.toLocaleString("zh-CN", {maximumFractionDigits: 1})} ${units[i]}`;
}
const selectable = row => !row.running && !row.ambiguous && !row.blocked;

export function WorkstationSessions({ device, onBack, onConfirm }) {
  const [rows, setRows] = useState(null);
  const [selected, setSelected] = useState(new Set());
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [errors, setErrors] = useState([]);
  const mounted = useRef(false);
  const all = useRef(null);
  const refresh = useCallback(async () => {
    const response = await native("workstationConnections", {operation: "device-sessions", device: device.id});
    if (!mounted.current) return;
    setRows(response.sessions);
    setSelected(old => new Set(response.sessions.filter(r => selectable(r) && old.has(r.id)).map(r => r.id)));
  }, [device.id]);
  useEffect(() => {
    mounted.current = true;
    refresh().catch(e => { if (mounted.current) setErrors([e.message]); });
    return () => { mounted.current = false; };
  }, [refresh]);
  const available = (rows || []).filter(selectable);
  useEffect(() => { if (all.current) all.current.indeterminate = selected.size > 0 && selected.size < available.length; }, [selected, available.length]);
  const toggle = id => setSelected(old => {
    const next = new Set(old);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  async function remove(items) {
    if (!items.length || busy) return;
    const title = items.length === 1 ? "删除「" + items[0].title + "」？" : "删除选中的 " + items.length + " 个会话？";
    if (!await onConfirm({title, body:"将永久删除 Cage 虚拟机中的对话记录和专属文件，预计释放 " + space(items.reduce((n,r) => n+r.bytes,0)) + "。工作机上的副本可在其「历史」页面清理。",confirm:"删除服务端记录"})) return;
    setBusy(true); setErrors([]); setMessage("");
    const failures = [];
    let count = 0, bytes = 0;
    try {
      for (let start = 0; start < items.length; start += 200) {
        const batch = items.slice(start, start + 200);
        const reply = await native("workstationConnections", {operation:"delete-device-sessions", device:device.id, ids:batch.map(r => r.id)});
        for (const r of reply.results) {
          bytes += r.bytes || 0;
          if (r.error) failures.push((batch.find(item => item.id === r.id)?.title || r.id) + " · " + r.error);
          else count++;
        }
      }
    } catch (e) { failures.push(e.message); }
    try { await refresh(); } catch (e) { failures.push(e.message); }
    if (mounted.current) {
      setBusy(false); setErrors(failures);
      if (count || bytes) setMessage("已删除 " + count + " 个服务端会话，释放 " + space(bytes) + "。");
    }
  }
  async function reload() {
    setBusy(true); setErrors([]); setMessage("");
    try { await refresh(); } catch (e) { setErrors([e.message]); } finally { setBusy(false); }
  }
  return <section className="tailscale-panel workstation-connections ws-sessions">
    <button type="button" className="quiet-button ws-back" onClick={onBack} disabled={busy}><ArrowLeft size={15}/>已配对工作机</button>
    <div className="tailscale-state">
      <div className="tailscale-network-icon"><MonitorCheck size={24}/></div>
      <div><strong>{device.name}</strong><span>{rows ? `${rows.length} 个服务端会话 · ${space(rows.reduce((n, r) => n + r.bytes, 0))}` : "正在读取客机记录…"}</span></div>
      <button type="button" className="icon-button" aria-label="刷新会话" onClick={reload} disabled={busy}><RefreshCw size={16}/></button>
    </div>
    {message && <p role="status" className="tailscale-notice">{message}</p>}
    {errors.length > 0 && <ul className="ws-errors" role="alert">{errors.map((err,i) => <li key={i}>{err}</li>)}</ul>}
    {rows?.length > 0 && <>
      <div className="ws-selection"><label><input ref={all} type="checkbox" checked={available.length > 0 && selected.size === available.length} disabled={busy || !available.length}
        onChange={event => setSelected(new Set(event.target.checked ? available.map(r => r.id) : []))}/>全选可删除会话</label>
        <button type="button" className="quiet-button ws-delete" disabled={busy || !selected.size} onClick={() => remove(available.filter(r => selected.has(r.id)))}><Trash2 size={14}/>删除所选{selected.size > 0 && `（${selected.size}）`}</button>
      </div>
      <ul className="ws-session-list">{rows.map(row => <li key={row.id}>
        <input type="checkbox" aria-label={"选择 " + row.title} checked={selected.has(row.id)} disabled={busy || !selectable(row)} onChange={() => toggle(row.id)}/>
        <div className="ws-session-info"><strong>{row.title}</strong><span title={row.project}>{row.project}</span>
          <small>{row.blocked || (row.ambiguous ? "标识重复" : row.running ? "运行中" : row.archived ? "已归档" : "已保存")} · {row.blocked ? "大小未知" : space(row.bytes)} · {new Date(row.updated * 1000).toLocaleString("zh-CN")}</small></div>
        <button className="quiet-button ws-delete" type="button" disabled={busy || !selectable(row)} aria-label={"删除 " + row.title} onClick={() => remove([row])}><Trash2 size={14}/>删除</button>
      </li>)}</ul>
    </>}
    {rows?.length === 0 && <div className="tailscale-empty"><MonitorCheck size={24}/><strong>暂无服务端会话</strong><p>这台工作机在当前客机里还没有可管理的对话记录。</p></div>}
  </section>;
}

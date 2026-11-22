import React, { useCallback, useEffect, useRef, useState } from "react";
import { Cable, Check, ChevronDown, ChevronRight, Laptop, MonitorCheck, RefreshCw, X } from "lucide-react";
import { WorkstationSessions } from "./workstation-sessions";
import { native } from "./api";
import { getClientContext } from "./client-context.mjs";
import "./workstation-connections.css";

export function useWorkstationDevices(enabled) {
  const [state, setState] = useState(null), [error, setError] = useState("");
  const inFlight = useRef(false), alive = useRef(false);
  const refresh = useCallback(async () => {
    if (!enabled || inFlight.current || getClientContext().mode !== "local") return;
    inFlight.current = true;
    try {
      const next = await native("workstationConnections", { operation: "devices" });
      if (alive.current) {
        setState(old => JSON.stringify(old) === JSON.stringify(next) ? old : next);
        setError("");
      }
    } catch (e) { if (alive.current) setError(e.message); }
    finally { inFlight.current = false; }
  }, [enabled]);
  useEffect(() => {
    alive.current = true;
    if (!enabled) return () => { alive.current = false; };
    const tick = () => { if (!document.hidden) refresh(); };
    tick(); const timer = setInterval(tick, 2200);
    document.addEventListener("visibilitychange", tick);
    return () => { alive.current = false; clearInterval(timer); document.removeEventListener("visibilitychange", tick); };
  }, [enabled, refresh]);
  return { state, error, refresh };
}

export function WorkstationPanel({ store, onConfirm }) {
  const { state, error, refresh } = store;
  const [busy, setBusy] = useState(false), [notice, setNotice] = useState("");
  const [selectedDevice, setSelectedDevice] = useState(null);
  const requests = state?.requests || [], devices = state?.devices || [];
  const network = Object.entries(state?.network || {}).filter(([ip]) => ip !== "127.0.0.1");
  const decide = async (operation, device = {}) => {
    if (operation === "revoke" && !await onConfirm({ title: "撤销工作机授权？", body: device.name + " 的当前连接会断开。下次使用时需要重新申请。", confirm: "撤销授权" })) return;
    setBusy(true); setNotice("");
    try {
      await native("workstationConnections", { operation, device: device.id || "", ticket: device.ticket || "" });
      setNotice(({ approve: "已批准，工作机正在连接。", deny: "已拒绝申请。", revoke: "已撤销授权。", firewall: "已请求 Windows 防火墙授权。" })[operation]);
      await refresh();
    } catch (e) { setNotice(e.message); } finally { setBusy(false); }
  };
  if (selectedDevice) return <WorkstationSessions key={selectedDevice.id} device={selectedDevice} onBack={() => setSelectedDevice(null)} onConfirm={onConfirm} />;
  return <div className="tailscale-panel workstation-connections">
    <section className="tailscale-overview">
      <div className="tailscale-state">
        <div className="tailscale-network-icon"><Cable size={24} /></div>
        <div><strong>{!state ? "正在读取服务状态…" : network.some(([, value]) => value === "online") ? "可在局域网中发现" : "等待局域网连接"}</strong>
          {network.length > 0 && <span>{network.map(([ip]) => ip).join(" · ")}</span>}</div>
        <button className="icon-button" aria-label="刷新工作机连接" onClick={refresh} disabled={busy}><RefreshCw size={16} /></button>
      </div>
      <p className="field-help">工作机首次连接时，在这里核对配对编号并批准。已配对的设备可以直接使用当前 Cage 客机。</p>
    </section>
    {(notice || error) && <p role="status" className="tailscale-notice">{notice || error}</p>}
    <section className="tailscale-section" aria-labelledby="workstation-pending">
      <h3 id="workstation-pending">连接申请{requests.length > 0 && <span>{requests.length}</span>}</h3>
      {requests.length ? requests.map(d => <article key={d.id} className="ws-request">
        <div className="tailscale-peer-row"><Laptop size={20}/><div className="tailscale-peer-info"><strong>{d.name}</strong><span>{d.address}</span></div></div>
        <div className="ws-decision"><div><span>配对编号</span><strong>{d.code}</strong></div><div className="ws-buttons">
          <button className="quiet-button" disabled={busy} onClick={() => decide("deny", d)}><X size={14}/>拒绝</button>
          <button className="primary-button" disabled={busy} onClick={() => decide("approve", d)}><Check size={15}/>批准连接</button>
        </div></div>
      </article>) : <div className="tailscale-empty"><Laptop size={24}/><strong>暂无连接申请</strong><p>在工作机的「局域网服务器」中选择这台 Cage 并申请连接，申请就会出现在这里。</p></div>}
    </section>
    <section className="tailscale-section" aria-labelledby="workstation-paired">
      <h3 id="workstation-paired">已配对工作机{devices.length > 0 && <span>{devices.length}</span>}</h3>
      {devices.length ? devices.map(d => <div key={d.id} className="ws-device-card">
        <button type="button" className="ws-device-open" aria-label={d.name + " 的会话"} onClick={() => setSelectedDevice(d)}>
          <MonitorCheck size={20}/><span className="tailscale-peer-info"><strong>{d.name}</strong><span>{d.disabled ? "等待清理客机授权" : d.address}</span></span>
          <ChevronRight size={16} aria-hidden="true"/>
        </button>
        <button className="quiet-button" disabled={busy} onClick={() => decide("revoke", d)}>{d.disabled ? "重试清理" : "撤销授权"}</button>
      </div>) : <p className="field-help">尚未配对工作机。</p>}
    </section>
    <details className="ws-network"><summary><ChevronDown size={14}/>局域网连接设置</summary>
      <p className="field-help">如果同一局域网的其他电脑搜索不到此服务器，可以允许 Windows 防火墙接收工作机连接。</p>
      <button className="quiet-button" disabled={busy} onClick={() => decide("firewall")}>允许局域网连接</button>
    </details>
  </div>;
}

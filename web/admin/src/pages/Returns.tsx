import { useState } from "react";
import { api, ApiRequestError } from "../api";
import { useAsync } from "../hooks";
import { label, money, when } from "../types";

const STATUSES = ["REQUESTED", "APPROVED", "REJECTED", ""];

export function Returns() {
  const [status, setStatus] = useState("REQUESTED");
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const { data, loading, error, reload } = useAsync(() => api.listReturns({ status }), status || "all");

  async function decide(id: string, approve: boolean) {
    setBusy(id);
    setMsg(null);
    try {
      const r = await api.decideReturn(id, approve, approve ? "Approved from admin" : "Rejected from admin");
      setMsg(`Return #${id.slice(0, 8)} → ${label(r.status)}`);
      reload();
    } catch (e) {
      setMsg(e instanceof ApiRequestError ? e.info.reason || e.info.code : String(e));
    } finally {
      setBusy(null);
    }
  }

  const rows = data?.returns ?? [];

  return (
    <div>
      <header className="page-head">
        <h1>Returns</h1>
        <div className="filters">
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {s === "" ? "All" : label(`RETURN_STATUS_${s}`)}
              </option>
            ))}
          </select>
          <button className="linkbtn" onClick={reload}>
            Refresh
          </button>
        </div>
      </header>

      {msg && <p className="notice">{msg}</p>}
      {error && <p className="error">{error}</p>}
      {loading ? (
        <p className="muted">Loading…</p>
      ) : (
        <table className="grid">
          <thead>
            <tr>
              <th>Return</th>
              <th>Order</th>
              <th>Items</th>
              <th>Refund</th>
              <th>Reason</th>
              <th>Requested</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id}>
                <td>
                  #{r.id.slice(0, 8)}{" "}
                  <span className={`pill s-${r.status}`}>{label(r.status)}</span>
                </td>
                <td className="mono">{r.order_id.slice(0, 8)}</td>
                <td>{r.lines.map((l) => `${l.quantity}× ${l.product_id.slice(0, 6)}`).join(", ")}</td>
                <td>{money(r.refund_total)}</td>
                <td>{r.reason || "—"}</td>
                <td className="muted">{when(r.created_at)}</td>
                <td>
                  {r.status === "RETURN_STATUS_REQUESTED" && (
                    <span className="row-actions">
                      <button disabled={busy === r.id} onClick={() => decide(r.id, true)}>
                        Approve
                      </button>
                      <button className="linkbtn" disabled={busy === r.id} onClick={() => decide(r.id, false)}>
                        Reject
                      </button>
                    </span>
                  )}
                </td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td colSpan={7} className="muted">
                  Nothing here.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      )}
    </div>
  );
}

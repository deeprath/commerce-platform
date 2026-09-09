import { useState } from "react";
import { api, ApiRequestError } from "../api";
import { useAsync } from "../hooks";
import { label, when } from "../types";

const STATUSES = ["", "PENDING", "SHIPPED", "DELIVERED", "CANCELLED"];

export function Shipments() {
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const { data, loading, error, reload } = useAsync(() => api.listShipments({ status }), status || "all");

  async function act(id: string, fn: () => Promise<{ status: string }>) {
    setBusy(id);
    setMsg(null);
    try {
      const s = await fn();
      setMsg(`Shipment #${id.slice(0, 8)} → ${label(s.status)}`);
      reload();
    } catch (e) {
      setMsg(e instanceof ApiRequestError ? e.info.reason || e.info.code : String(e));
    } finally {
      setBusy(null);
    }
  }

  const rows = data?.shipments ?? [];

  return (
    <div>
      <header className="page-head">
        <h1>Shipments</h1>
        <div className="filters">
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {s === "" ? "All statuses" : label(`SHIPMENT_STATUS_${s}`)}
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
              <th>Shipment</th>
              <th>Order</th>
              <th>Carrier / tracking</th>
              <th>Created</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((sh) => (
              <tr key={sh.id}>
                <td>
                  #{sh.id.slice(0, 8)}{" "}
                  <span className={`pill s-${sh.status}`}>{label(sh.status)}</span>
                </td>
                <td className="mono">{sh.order_id.slice(0, 8)}</td>
                <td>{sh.tracking_number ? `${sh.carrier} · ${sh.tracking_number}` : "—"}</td>
                <td className="muted">{when(sh.created_at)}</td>
                <td>
                  <span className="row-actions">
                    {sh.status === "SHIPMENT_STATUS_PENDING" && (
                      <button
                        disabled={busy === sh.id}
                        onClick={() => act(sh.id, () => api.shipShipment(sh.id, "MANUAL", `TRK-${sh.id.slice(0, 8)}`))}
                      >
                        Ship
                      </button>
                    )}
                    {sh.status === "SHIPMENT_STATUS_SHIPPED" && (
                      <button disabled={busy === sh.id} onClick={() => act(sh.id, () => api.deliverShipment(sh.id))}>
                        Mark delivered
                      </button>
                    )}
                    {(sh.status === "SHIPMENT_STATUS_PENDING" || sh.status === "SHIPMENT_STATUS_SHIPPED") && (
                      <button
                        className="linkbtn"
                        disabled={busy === sh.id}
                        onClick={() => act(sh.id, () => api.cancelShipment(sh.id, "Cancelled from admin"))}
                      >
                        Cancel
                      </button>
                    )}
                  </span>
                </td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td colSpan={5} className="muted">
                  No shipments.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      )}
    </div>
  );
}

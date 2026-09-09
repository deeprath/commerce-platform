import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { useAsync } from "../hooks";
import { label, money, when } from "../types";

const STATUSES = ["", "PENDING_PAYMENT", "CONFIRMED", "FULFILLED", "CANCELLED"];

export function Orders() {
  const [status, setStatus] = useState("");
  const { data, loading, error, reload } = useAsync(() => api.listOrders({ status }), status || "all");

  return (
    <div>
      <header className="page-head">
        <h1>Orders</h1>
        <div className="filters">
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {s === "" ? "All statuses" : label(`ORDER_STATUS_${s}`)}
              </option>
            ))}
          </select>
          <button className="linkbtn" onClick={reload}>
            Refresh
          </button>
        </div>
      </header>

      {error && <p className="error">{error}</p>}
      {loading ? (
        <p className="muted">Loading…</p>
      ) : (
        <table className="grid">
          <thead>
            <tr>
              <th>Order</th>
              <th>Status</th>
              <th>Total</th>
              <th>Customer</th>
              <th>Placed</th>
            </tr>
          </thead>
          <tbody>
            {(data?.orders ?? []).map((o) => (
              <tr key={o.id}>
                <td>
                  <Link to={`/orders/${o.id}`}>#{o.id.slice(0, 8)}</Link>
                </td>
                <td>
                  <span className={`pill s-${o.status}`}>{label(o.status)}</span>
                </td>
                <td>{money(o.total)}</td>
                <td className="mono">{o.owner_id.slice(0, 8)}</td>
                <td className="muted">{when(o.created_at)}</td>
              </tr>
            ))}
            {(data?.orders ?? []).length === 0 && (
              <tr>
                <td colSpan={5} className="muted">
                  No orders.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      )}
    </div>
  );
}

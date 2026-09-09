import { Link, useParams } from "react-router-dom";
import { api } from "../api";
import { useAsync } from "../hooks";
import { label, money, when } from "../types";

export function OrderDetail() {
  const { id = "" } = useParams();
  const { data: o, loading, error } = useAsync(() => api.getOrder(id), id);

  if (loading) return <p className="muted">Loading…</p>;
  if (error) return <p className="error">{error}</p>;
  if (!o) return null;

  return (
    <div>
      <header className="page-head">
        <h1>
          <Link to="/orders">Orders</Link> / #{o.id.slice(0, 8)}
        </h1>
        <span className={`pill s-${o.status}`}>{label(o.status)}</span>
      </header>

      <dl className="kv">
        <div>
          <dt>Customer</dt>
          <dd className="mono">{o.owner_id}</dd>
        </div>
        <div>
          <dt>Payment</dt>
          <dd className="mono">{o.payment_id || "—"}</dd>
        </div>
        <div>
          <dt>Placed</dt>
          <dd>{when(o.created_at)}</dd>
        </div>
        {o.cancel_reason && (
          <div>
            <dt>Cancel reason</dt>
            <dd>{o.cancel_reason}</dd>
          </div>
        )}
      </dl>

      <table className="grid">
        <thead>
          <tr>
            <th>Item</th>
            <th>Qty</th>
            <th>Unit</th>
            <th>Line</th>
          </tr>
        </thead>
        <tbody>
          {o.lines.map((l) => (
            <tr key={l.product_id}>
              <td>{l.title || l.product_id}</td>
              <td>{l.quantity}</td>
              <td>{money(l.unit_price)}</td>
              <td>{money(l.line_total)}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <dl className="totals">
        <div>
          <dt>Subtotal</dt>
          <dd>{money(o.subtotal)}</dd>
        </div>
        {o.discount && o.discount.units !== "0" && (
          <div>
            <dt>Discount</dt>
            <dd>−{money(o.discount)}</dd>
          </div>
        )}
        <div>
          <dt>Tax</dt>
          <dd>{money(o.tax)}</dd>
        </div>
        <div className="grand">
          <dt>Total</dt>
          <dd>{money(o.total)}</dd>
        </div>
      </dl>
    </div>
  );
}

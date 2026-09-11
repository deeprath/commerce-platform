import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../api";
import type { Order } from "../types";
import { formatMoney } from "../types";

const LABEL: Record<string, string> = {
  ORDER_STATUS_PENDING_PAYMENT: "Pending payment",
  ORDER_STATUS_CONFIRMED: "Confirmed",
  ORDER_STATUS_CANCELLED: "Cancelled",
  ORDER_STATUS_FULFILLED: "Fulfilled",
};

export function Orders({ authed }: Readonly<{ authed: boolean }>) {
  const [orders, setOrders] = useState<Order[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!authed) return;
    api
      .listOrders()
      .then((r) => setOrders(r.orders ?? []))
      .catch((e) => setErr(String(e.message ?? e)));
  }, [authed]);

  if (!authed)
    return (
      <p className="muted">
        Please <Link to="/login">sign in</Link> to see your orders.
      </p>
    );
  if (err) return <p className="error">{err}</p>;
  if (!orders) return <p className="muted">Loading…</p>;
  if (orders.length === 0) return <p className="muted">You have no orders yet.</p>;

  return (
    <div className="orders">
      <h1>Your orders</h1>
      <ul className="order-list">
        {orders.map((o) => (
          <li key={o.id}>
            <Link to={`/orders/${o.id}`}>
              <span className="ono">#{o.id.slice(0, 8)}</span>
              <span className={`ostat s-${o.status}`}>{LABEL[o.status] ?? o.status}</span>
              <span>{formatMoney(o.total)}</span>
              <span className="muted">{new Date(o.created_at).toLocaleString()}</span>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function OrderDetail({ authed }: Readonly<{ authed: boolean }>) {
  const { id = "" } = useParams();
  const [order, setOrder] = useState<Order | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!authed) return;
    api
      .getOrder(id)
      .then(setOrder)
      .catch((e) => setErr(e.info?.reason === "ORDER_NOT_FOUND" ? "notfound" : String(e.message ?? e)));
  }, [authed, id]);

  if (!authed)
    return (
      <p className="muted">
        Please <Link to="/login">sign in</Link>.
      </p>
    );
  if (err === "notfound") return <p className="muted">Order not found.</p>;
  if (err) return <p className="error">{err}</p>;
  if (!order) return <p className="muted">Loading…</p>;

  return (
    <div className="order-detail">
      <Link to="/orders" className="back">
        ← Orders
      </Link>
      <h1>Order #{order.id.slice(0, 8)}</h1>
      <p className={`ostat s-${order.status}`}>{LABEL[order.status] ?? order.status}</p>
      {order.cancel_reason && <p className="muted">Reason: {order.cancel_reason}</p>}

      <table className="order-lines">
        <tbody>
          {order.lines.map((l) => (
            <tr key={l.product_id}>
              <td>{l.title || l.product_id}</td>
              <td>{l.quantity}×</td>
              <td>{formatMoney(l.unit_price)}</td>
              <td>{formatMoney(l.line_total)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <dl className="totals">
        <div>
          <dt>Subtotal</dt>
          <dd>{formatMoney(order.subtotal)}</dd>
        </div>
        {order.discount && order.discount.units !== "0" && (
          <div>
            <dt>Discount</dt>
            <dd>−{formatMoney(order.discount)}</dd>
          </div>
        )}
        <div>
          <dt>Tax</dt>
          <dd>{formatMoney(order.tax)}</dd>
        </div>
        <div className="grand">
          <dt>Total</dt>
          <dd>{formatMoney(order.total)}</dd>
        </div>
      </dl>
    </div>
  );
}

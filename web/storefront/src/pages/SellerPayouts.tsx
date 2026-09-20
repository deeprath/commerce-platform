import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import type { Money, Payout } from "../types";
import { formatMoney } from "../types";
import { SellerNav } from "../components/SellerNav";

const STATUS_LABEL: Record<string, string> = {
  PAYOUT_STATUS_PENDING: "Pending",
  PAYOUT_STATUS_PAID: "Paid",
  PAYOUT_STATUS_REVERSED: "Reversed",
};

// Cents held in a Money, which the API sends as units + nanos.
function cents(m: Money | undefined): number {
  if (!m) return 0;
  return Number(m.units ?? 0) * 100 + Math.round((m.nanos ?? 0) / 1e7);
}

// What a payout actually settles for. A partly reversed payout still reads
// PENDING, so the status alone would overstate what the shop is owed.
function net(p: Payout): Money {
  const gross = cents(p.amount);
  const reversed = cents(p.reversed_amount);
  const remaining = gross - reversed;
  return {
    currency_code: p.amount?.currency_code ?? "USD",
    units: String(Math.trunc(remaining / 100)), // int64 is a string in protojson
    nanos: (remaining % 100) * 1e7,
  };
}

export function SellerPayouts({ authed }: Readonly<{ authed: boolean }>) {
  const [payouts, setPayouts] = useState<Payout[] | null>(null);
  const [status, setStatus] = useState("");
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!authed) return;
    setPayouts(null);
    api
      .listShopPayouts(status || undefined)
      .then((r) => setPayouts(r.payouts ?? []))
      .catch((e) => setErr(String(e.message ?? e)));
  }, [authed, status]);

  if (!authed)
    return (
      <p className="muted">
        Please <Link to="/login">sign in</Link>.
      </p>
    );

  return (
    <div className="seller">
      <h1>Seller dashboard</h1>
      <SellerNav />

      <div className="seller-section-head">
        <h2>Payouts</h2>
        <select value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">All</option>
          <option value="PENDING">Pending</option>
          <option value="PAID">Paid</option>
          <option value="REVERSED">Reversed</option>
        </select>
      </div>

      {err && <p className="error">{err}</p>}
      {!payouts && !err && <p className="muted">Loading…</p>}
      {payouts?.length === 0 && <p className="muted">No payouts yet.</p>}

      {payouts && payouts.length > 0 && (
        <table className="seller-table">
          <thead>
            <tr>
              <th>Order</th>
              <th>Amount</th>
              <th>Returned</th>
              <th>Net</th>
              <th>Status</th>
              <th>Created</th>
              <th>Paid</th>
            </tr>
          </thead>
          <tbody>
            {payouts.map((p) => (
              <tr key={p.id}>
                <td>
                  <code>{p.order_id.slice(0, 8)}</code>
                </td>
                <td>{formatMoney(p.amount)}</td>
                <td className={cents(p.reversed_amount) > 0 ? "reversed" : "muted"}>
                  {cents(p.reversed_amount) > 0 ? `−${formatMoney(p.reversed_amount)}` : "—"}
                </td>
                <td>{formatMoney(net(p))}</td>
                <td>
                  <span className={`ostat s-${p.status}`}>{STATUS_LABEL[p.status] ?? p.status}</span>
                </td>
                <td className="muted small">{new Date(p.created_at).toLocaleString()}</td>
                <td className="muted small">{p.paid_at ? new Date(p.paid_at).toLocaleString() : "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

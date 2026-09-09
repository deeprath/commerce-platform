import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../api";
import type { Address } from "../api";
import { useCart } from "../cart-context";
import { formatMoney } from "../types";

const BLANK: Address = {
  full_name: "Test User",
  line1: "1 Main St",
  line2: "",
  city: "Springfield",
  region: "IL",
  postal_code: "62701",
  country_code: "US",
  phone: "",
};

type Phase = "form" | "paying" | "done" | "error";

export function Checkout({ authed }: { authed: boolean }) {
  const nav = useNavigate();
  const { cart, loading, refresh } = useCart();
  const [addr, setAddr] = useState<Address>(BLANK);
  const [coupon, setCoupon] = useState("");
  const [method, setMethod] = useState("pm_card_ok");
  const [phase, setPhase] = useState<Phase>("form");
  const [paymentId, setPaymentId] = useState("");
  const [orderId, setOrderId] = useState("");
  const [msg, setMsg] = useState<string | null>(null);

  if (!authed) {
    return (
      <div className="checkout">
        <h1>Checkout</h1>
        <p className="muted">
          Please <Link to="/login">sign in</Link> to check out.
        </p>
      </div>
    );
  }
  // Only block on an empty cart while the shopper is still filling in the form —
  // once an order exists, checkout has already consumed the cart and we want to
  // keep showing the payment / outcome steps.
  if (phase === "form") {
    if (loading) return <p className="muted">Loading…</p>;
    if (!cart || cart.items.length === 0) {
      return (
        <div className="checkout">
          <h1>Checkout</h1>
          <p className="muted">Your cart is empty.</p>
          <Link to="/">← Browse</Link>
        </div>
      );
    }
  }

  function field(k: keyof Address, label: string, required = true) {
    return (
      <label>
        {label}
        <input
          value={addr[k] ?? ""}
          required={required}
          onChange={(e) => setAddr({ ...addr, [k]: e.target.value })}
        />
      </label>
    );
  }

  async function placeOrder(e: React.FormEvent) {
    e.preventDefault();
    setMsg(null);
    setPhase("paying");
    try {
      const res = await api.checkout({
        ship_to: addr,
        coupon_code: coupon.trim() || undefined,
        currency_code: "USD",
        payment_method_token: method,
      });
      setPaymentId(res.payment_id);
      setOrderId(res.order_id);
    } catch (err) {
      setPhase("error");
      setMsg((err as { info?: { reason?: string } }).info?.reason ?? "Checkout failed.");
    }
  }

  async function confirm(outcome: "authorize" | "fail") {
    setMsg(null);
    try {
      await api.confirmCheckout(paymentId, outcome);
      // poll the order until it leaves PENDING_PAYMENT
      for (let i = 0; i < 15; i++) {
        const o = await api.getOrder(orderId);
        if (o.status !== "ORDER_STATUS_PENDING_PAYMENT") {
          setPhase("done");
          setMsg(o.status);
          void refresh();
          return;
        }
        await new Promise((r) => setTimeout(r, 1000));
      }
      setPhase("done");
      setMsg("ORDER_STATUS_PENDING_PAYMENT");
    } catch (err) {
      setPhase("error");
      setMsg((err as { info?: { reason?: string } }).info?.reason ?? "Confirmation failed.");
    }
  }

  return (
    <div className="checkout">
      <h1>Checkout</h1>

      <div className="checkout-body">
        <div className="checkout-main">
          {phase === "form" && (
            <form onSubmit={placeOrder} className="addr-form">
              <h3>Shipping address</h3>
              {field("full_name", "Full name")}
              {field("line1", "Address line 1")}
              {field("line2", "Address line 2", false)}
              <div className="row3">
                {field("city", "City")}
                {field("region", "State")}
                {field("postal_code", "ZIP")}
              </div>
              {field("country_code", "Country (ISO-2)")}

              <h3>Coupon</h3>
              <input
                placeholder="Coupon code (optional)"
                value={coupon}
                onChange={(e) => setCoupon(e.target.value)}
              />

              <h3>Payment (sandbox)</h3>
              <select value={method} onChange={(e) => setMethod(e.target.value)}>
                <option value="pm_card_ok">Test card — succeeds</option>
                <option value="pm_card_declined">Test card — declined</option>
              </select>

              <button type="submit" className="checkout-btn">
                Place order · {formatMoney(cart?.total)}
              </button>
            </form>
          )}

          {phase === "paying" && !paymentId && <p className="muted">Creating your order…</p>}

          {phase === "paying" && paymentId && (
            <div className="pay-step">
              <h3>Complete payment</h3>
              <p className="muted">
                Order <code>{orderId.slice(0, 8)}</code> is pending payment. In production the PSP
                widget runs here; in the sandbox, choose an outcome:
              </p>
              <div className="pay-actions">
                <button className="checkout-btn" onClick={() => confirm("authorize")}>
                  Authorize payment
                </button>
                <button className="linkbtn" onClick={() => confirm("fail")}>
                  Abandon
                </button>
              </div>
            </div>
          )}

          {phase === "done" && (
            <div className="pay-step">
              {msg === "ORDER_STATUS_CONFIRMED" ? (
                <>
                  <h3>Order confirmed 🎉</h3>
                  <p>
                    Thanks! Your order <code>{orderId.slice(0, 8)}</code> is confirmed.
                  </p>
                </>
              ) : msg === "ORDER_STATUS_CANCELLED" ? (
                <>
                  <h3>Payment didn’t go through</h3>
                  <p className="muted">The order was cancelled and stock released. Try again.</p>
                </>
              ) : (
                <p className="muted">Order status: {msg}</p>
              )}
              <button className="checkout-btn" onClick={() => nav(`/orders/${orderId}`)}>
                View order
              </button>
            </div>
          )}

          {phase === "error" && (
            <div className="pay-step">
              <p className="error">Checkout error: {msg}</p>
              <button className="linkbtn" onClick={() => setPhase("form")}>
                Back
              </button>
            </div>
          )}
        </div>

        {cart && cart.items.length > 0 && (
          <aside className="checkout-summary">
            <h3>Order summary</h3>
            {cart.items.map((it) => (
              <div className="sum-line" key={it.product_id}>
                <span>
                  {it.quantity}× {it.title}
                </span>
                <span>{formatMoney(it.line_total)}</span>
              </div>
            ))}
            <dl className="totals">
              <div>
                <dt>Subtotal</dt>
                <dd>{formatMoney(cart.subtotal)}</dd>
              </div>
              {cart.discount && cart.discount.units !== "0" && (
                <div>
                  <dt>Discount</dt>
                  <dd>−{formatMoney(cart.discount)}</dd>
                </div>
              )}
              <div>
                <dt>Tax</dt>
                <dd>{formatMoney(cart.tax)}</dd>
              </div>
              <div className="grand">
                <dt>Total</dt>
                <dd>{formatMoney(cart.total)}</dd>
              </div>
            </dl>
          </aside>
        )}
      </div>
    </div>
  );
}

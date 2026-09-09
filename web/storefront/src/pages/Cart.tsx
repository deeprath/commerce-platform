import { useState } from "react";
import { Link } from "react-router-dom";
import { useCart } from "../cart-context";
import { mediaUrl } from "../api";
import { formatMoney } from "../types";

export function Cart() {
  const { cart, loading, refresh, setQty, remove } = useCart();
  const [coupon, setCoupon] = useState("");

  if (loading) return <p className="muted">Loading…</p>;
  if (!cart || cart.items.length === 0) {
    return (
      <div className="cart">
        <h1>Your cart</h1>
        <p className="muted">Your cart is empty.</p>
        <Link to="/">← Browse products</Link>
      </div>
    );
  }

  return (
    <div className="cart">
      <h1>Your cart</h1>
      <div className="cart-lines">
        {cart.items.map((it) => (
          <div className="cart-line" key={it.product_id}>
            <div className="cart-line-img">
              {it.primary_media_key ? (
                <img src={mediaUrl(it.primary_media_key)} alt={it.title} />
              ) : (
                <div className="noimg" aria-hidden />
              )}
            </div>
            <div className="cart-line-info">
              <Link to={`/p/${it.slug}`}>{it.title || it.product_id}</Link>
              <span className="muted">{formatMoney(it.unit_price)} each</span>
            </div>
            <div className="cart-line-qty">
              <button onClick={() => setQty(it.product_id, Math.max(0, it.quantity - 1))} aria-label="Decrease">
                −
              </button>
              <span>{it.quantity}</span>
              <button onClick={() => setQty(it.product_id, it.quantity + 1)} aria-label="Increase">
                +
              </button>
            </div>
            <div className="cart-line-total">{formatMoney(it.line_total)}</div>
            <button className="linkbtn" onClick={() => remove(it.product_id)}>
              Remove
            </button>
          </div>
        ))}
      </div>

      <div className="cart-summary">
        <form
          className="coupon"
          onSubmit={(e) => {
            e.preventDefault();
            void refresh(coupon.trim() || undefined);
          }}
        >
          <input
            placeholder="Coupon code"
            value={coupon}
            onChange={(e) => setCoupon(e.target.value)}
            aria-label="Coupon code"
          />
          <button type="submit">Apply</button>
        </form>
        {cart.coupon_error && <p className="error small">Coupon: {cart.coupon_error}</p>}

        <dl className="totals">
          <div>
            <dt>Subtotal</dt>
            <dd>{formatMoney(cart.subtotal)}</dd>
          </div>
          {cart.discount && cart.discount.units !== "0" && (
            <div>
              <dt>Discount{cart.coupon_code ? ` (${cart.coupon_code})` : ""}</dt>
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

        <Link to="/checkout" className="checkout-btn">
          Checkout
        </Link>
      </div>
    </div>
  );
}

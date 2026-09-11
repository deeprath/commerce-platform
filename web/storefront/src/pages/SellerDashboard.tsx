import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import type { ShopInput } from "../api";
import type { Shop } from "../types";
import { SellerNav } from "../components/SellerNav";

const STATUS_LABEL: Record<string, string> = {
  SHOP_STATUS_PENDING_REVIEW: "Pending review",
  SHOP_STATUS_ACTIVE: "Active",
  SHOP_STATUS_SUSPENDED: "Suspended",
};

const BLANK: ShopInput = { name: "", description: "", contact_email: "" };

export function SellerDashboard({ authed }: Readonly<{ authed: boolean }>) {
  // shop stays null both while genuinely not-yet-fetched and once a fetch
  // confirms the caller has no shop (SHOP_NOT_FOUND) — either way the
  // Onboarding form is the right thing to show, and it hands a freshly
  // created shop straight to setShop, so there's no separate "not found"
  // flag to fall out of sync with it.
  const [shop, setShop] = useState<Shop | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!authed) return;
    let cancelled = false;
    setLoading(true);
    api
      .getMyShop()
      .then((s) => !cancelled && setShop(s))
      .catch((e) => {
        if (cancelled) return;
        if (e.info?.reason !== "SHOP_NOT_FOUND") setErr(String(e.message ?? e));
      })
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [authed]);

  if (!authed)
    return (
      <p className="muted">
        Please <Link to="/login">sign in</Link> to access the seller dashboard.
      </p>
    );
  if (loading) return <p className="muted">Loading…</p>;
  if (err) return <p className="error">{err}</p>;
  if (!shop) return <Onboarding onCreated={setShop} />;

  return (
    <div className="seller">
      <h1>Seller dashboard</h1>
      <SellerNav />
      <ShopCard shop={shop} onUpdated={setShop} />
    </div>
  );
}

function Onboarding({ onCreated }: Readonly<{ onCreated: (s: Shop) => void }>) {
  const [form, setForm] = useState<ShopInput>(BLANK);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const shop = await api.createShop(form);
      onCreated(shop);
    } catch (e) {
      setErr(String((e as { message?: string }).message ?? e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="seller">
      <h1>Become a seller</h1>
      <p className="muted">Open a shop to list your own products on the marketplace.</p>
      <form onSubmit={submit} className="seller-form">
        <label>
          Shop name{" "}
          <input
            required
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
        </label>
        <label>
          Description{" "}
          <textarea
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
        </label>
        <label>
          Contact email{" "}
          <input
            type="email"
            value={form.contact_email}
            onChange={(e) => setForm({ ...form, contact_email: e.target.value })}
          />
        </label>
        {err && <p className="error">{err}</p>}
        <button type="submit" disabled={busy || !form.name}>
          {busy ? "Creating…" : "Create shop"}
        </button>
      </form>
    </div>
  );
}

function ShopCard({ shop, onUpdated }: Readonly<{ shop: Shop; onUpdated: (s: Shop) => void }>) {
  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState<ShopInput>({
    name: shop.name,
    description: shop.description,
    contact_email: shop.contact_email,
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const updated = await api.updateShop(form);
      onUpdated(updated);
      setEditing(false);
    } catch (e) {
      setErr(String((e as { message?: string }).message ?? e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="shop-card">
      <div className="shop-card-head">
        <h2>{shop.name}</h2>
        <span className={`ostat s-${shop.status}`}>{STATUS_LABEL[shop.status] ?? shop.status}</span>
      </div>
      {shop.status === "SHOP_STATUS_ACTIVE" && (
        <p className="muted small">
          Live at <Link to={`/shops/${shop.slug}`}>/shops/{shop.slug}</Link>
        </p>
      )}
      {shop.status === "SHOP_STATUS_SUSPENDED" && shop.suspension_reason && (
        <p className="error">Suspended: {shop.suspension_reason}</p>
      )}
      {shop.status === "SHOP_STATUS_PENDING_REVIEW" && (
        <p className="muted small">An operator will review your shop before it goes live.</p>
      )}

      {!editing ? (
        <>
          {shop.description && <p className="desc">{shop.description}</p>}
          <p className="muted small">{shop.contact_email || "No contact email set"}</p>
          <button className="linkbtn" onClick={() => setEditing(true)}>
            Edit shop details
          </button>
        </>
      ) : (
        <form onSubmit={submit} className="seller-form">
          <label>
            Shop name{" "}
            <input
              required
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </label>
          <label>
            Description{" "}
            <textarea
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
            />
          </label>
          <label>
            Contact email{" "}
            <input
              type="email"
              value={form.contact_email}
              onChange={(e) => setForm({ ...form, contact_email: e.target.value })}
            />
          </label>
          {err && <p className="error">{err}</p>}
          <div className="seller-form-actions">
            <button type="submit" disabled={busy || !form.name}>
              {busy ? "Saving…" : "Save"}
            </button>
            <button type="button" className="linkbtn" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </div>
  );
}

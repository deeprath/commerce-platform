import { Fragment, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import type { ProductInput } from "../api";
import type { Product } from "../types";
import { formatMoney } from "../types";
import { SellerNav } from "../components/SellerNav";

const STATUS_LABEL: Record<string, string> = {
  PRODUCT_STATUS_DRAFT: "Draft",
  PRODUCT_STATUS_ACTIVE: "Active",
  PRODUCT_STATUS_ARCHIVED: "Archived",
};

const BLANK: ProductInput = {
  slug: "",
  title: "",
  description: "",
  category_id: "",
  list_price: { currency_code: "USD", units: "0", nanos: 0 },
};

// list_price.units is a whole-dollar string over the wire; the form edits a
// single decimal "dollars" field and we split it back out on submit.
function dollarsToUnits(v: string): { units: string; nanos: number } {
  const [whole, frac = ""] = v.split(".");
  const units = whole || "0";
  const nanos = frac ? Number((frac + "0000000").slice(0, 9)) : 0;
  return { units, nanos };
}
function unitsToDollars(units: string, nanos: number): string {
  const cents = Math.round(nanos / 1e7);
  return cents ? `${units}.${String(cents).padStart(2, "0")}` : units;
}

export function SellerProducts({ authed }: { authed: boolean }) {
  const [products, setProducts] = useState<Product[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);

  function load() {
    api
      .listMyShopProducts()
      .then((r) => setProducts(r.products ?? []))
      .catch((e) => setErr(String(e.message ?? e)));
  }

  useEffect(() => {
    if (!authed) return;
    load();
  }, [authed]);

  if (!authed)
    return (
      <p className="muted">
        Please <Link to="/login">sign in</Link>.
      </p>
    );

  async function archive(id: string) {
    if (!confirm("Archive this product? It will no longer be listed.")) return;
    await api.archiveShopProduct(id);
    load();
  }

  return (
    <div className="seller">
      <h1>Seller dashboard</h1>
      <SellerNav />

      <div className="seller-section-head">
        <h2>Products</h2>
        <button className="linkbtn" onClick={() => setCreating((c) => !c)}>
          {creating ? "Cancel" : "+ New product"}
        </button>
      </div>

      {creating && (
        <ProductForm
          initial={BLANK}
          submitLabel="Create"
          onSubmit={async (input) => {
            await api.createShopProduct(input);
            setCreating(false);
            load();
          }}
        />
      )}

      {err && <p className="error">{err}</p>}
      {!products && !err && <p className="muted">Loading…</p>}
      {products && products.length === 0 && <p className="muted">No products yet.</p>}

      {products && products.length > 0 && (
        <table className="seller-table">
          <thead>
            <tr>
              <th>Title</th>
              <th>Status</th>
              <th>Price</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {products.map((p) => (
              <Fragment key={p.id}>
                <tr>
                  <td>{p.title}</td>
                  <td>
                    <span className={`ostat s-${p.status}`}>{STATUS_LABEL[p.status] ?? p.status}</span>
                  </td>
                  <td>{formatMoney(p.list_price)}</td>
                  <td className="seller-table-actions">
                    <button className="linkbtn" onClick={() => setEditingId(editingId === p.id ? null : p.id)}>
                      {editingId === p.id ? "Close" : "Edit"}
                    </button>
                    {p.status !== "PRODUCT_STATUS_ARCHIVED" && (
                      <button className="linkbtn" onClick={() => archive(p.id)}>
                        Archive
                      </button>
                    )}
                  </td>
                </tr>
                {editingId === p.id && (
                  <tr>
                    <td colSpan={4}>
                      <ProductForm
                        initial={{
                          title: p.title,
                          description: p.description,
                          category_id: p.category_id,
                          list_price: {
                            currency_code: p.list_price.currency_code,
                            units: p.list_price.units,
                            nanos: p.list_price.nanos,
                          },
                          status: p.status,
                        }}
                        submitLabel="Save"
                        onSubmit={async (input) => {
                          await api.updateShopProduct(p.id, input);
                          setEditingId(null);
                          load();
                        }}
                      />
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function ProductForm({
  initial,
  submitLabel,
  onSubmit,
}: {
  initial: ProductInput;
  submitLabel: string;
  onSubmit: (input: ProductInput) => Promise<void>;
}) {
  const [form, setForm] = useState<ProductInput>(initial);
  const [price, setPrice] = useState(unitsToDollars(initial.list_price.units, initial.list_price.nanos ?? 0));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const isCreate = initial.slug !== undefined;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await onSubmit({ ...form, list_price: { currency_code: "USD", ...dollarsToUnits(price) } });
    } catch (e) {
      setErr(String((e as { message?: string }).message ?? e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="seller-form product-form">
      {isCreate && (
        <label>
          Slug (URL-safe, permanent)
          <input required value={form.slug} onChange={(e) => setForm({ ...form, slug: e.target.value })} />
        </label>
      )}
      <label>
        Title
        <input required value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} />
      </label>
      <label>
        Description
        <textarea value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
      </label>
      <div className="row3">
        <label>
          Category
          <input
            required
            value={form.category_id}
            onChange={(e) => setForm({ ...form, category_id: e.target.value })}
          />
        </label>
        <label>
          Price (USD)
          <input required type="number" step="0.01" min="0" value={price} onChange={(e) => setPrice(e.target.value)} />
        </label>
        {!isCreate && (
          <label>
            Status
            <select value={form.status} onChange={(e) => setForm({ ...form, status: e.target.value })}>
              <option value="PRODUCT_STATUS_DRAFT">Draft</option>
              <option value="PRODUCT_STATUS_ACTIVE">Active</option>
              <option value="PRODUCT_STATUS_ARCHIVED">Archived</option>
            </select>
          </label>
        )}
      </div>
      {err && <p className="error">{err}</p>}
      <button type="submit" disabled={busy}>
        {busy ? "Saving…" : submitLabel}
      </button>
    </form>
  );
}

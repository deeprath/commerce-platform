import { useState } from "react";
import { api, ApiRequestError } from "../api";
import { useAsync } from "../hooks";
import { money } from "../types";

const BLANK = { slug: "", title: "", description: "", category_id: "general", price: "" };

export function Products() {
  const [form, setForm] = useState(BLANK);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const { data, loading, error, reload } = useAsync(() => api.listProducts(), "products");

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      const dollars = Number(form.price);
      // Number.isFinite first: NaN <= 0 is false, so a bare "dollars <= 0"
      // would silently accept a non-numeric price (e.g. Number("abc")).
      if (!Number.isFinite(dollars) || dollars <= 0) throw new Error("Enter a price in dollars.");
      const p = await api.createProduct({
        slug: form.slug,
        title: form.title,
        description: form.description,
        category_id: form.category_id,
        list_price: {
          currency_code: "USD",
          units: String(Math.trunc(dollars)),
          nanos: Math.round((dollars % 1) * 1e9),
        },
      });
      setMsg(`Created “${p.title}”.`);
      setForm(BLANK);
      reload();
    } catch (e) {
      setMsg(e instanceof ApiRequestError ? e.info.reason || e.info.code : (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function archive(id: string) {
    setMsg(null);
    try {
      await api.archiveProduct(id);
      setMsg("Archived.");
      reload();
    } catch (e) {
      setMsg(e instanceof ApiRequestError ? e.info.reason || e.info.code : String(e));
    }
  }

  const hits = data?.hits ?? [];

  return (
    <div>
      <header className="page-head">
        <h1>Products</h1>
        <button className="linkbtn" onClick={reload}>
          Refresh
        </button>
      </header>

      {msg && <p className="notice">{msg}</p>}

      <form className="new-product" onSubmit={create}>
        <h3>New product</h3>
        <div className="row3">
          <label>
            Slug{" "}
            <input value={form.slug} required onChange={(e) => setForm({ ...form, slug: e.target.value })} />
          </label>
          <label>
            Title{" "}
            <input value={form.title} required onChange={(e) => setForm({ ...form, title: e.target.value })} />
          </label>
          <label>
            Price (USD){" "}
            <input
              value={form.price}
              required
              inputMode="decimal"
              onChange={(e) => setForm({ ...form, price: e.target.value })}
            />
          </label>
        </div>
        <label>
          Description{" "}
          <input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
        </label>
        <button type="submit" disabled={busy}>
          {busy ? "Creating…" : "Create"}
        </button>
      </form>

      {error && <p className="error">{error}</p>}
      {loading ? (
        <p className="muted">Loading…</p>
      ) : (
        <table className="grid">
          <thead>
            <tr>
              <th>Title</th>
              <th>Slug</th>
              <th>Price</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {hits.map((p) => (
              <tr key={p.product_id}>
                <td>{p.title}</td>
                <td className="mono">{p.slug}</td>
                <td>{money(p.list_price)}</td>
                <td>
                  <button className="linkbtn" onClick={() => archive(p.product_id)}>
                    Archive
                  </button>
                </td>
              </tr>
            ))}
            {hits.length === 0 && (
              <tr>
                <td colSpan={4} className="muted">
                  No active products.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      )}
    </div>
  );
}

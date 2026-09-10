import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api";
import type { SearchResponse } from "../types";
import { ProductCard } from "../components/ProductCard";
import { track } from "../track";

const SORTS = [
  { v: "", label: "Relevance" },
  { v: "newest", label: "Newest" },
  { v: "price_asc", label: "Price ↑" },
  { v: "price_desc", label: "Price ↓" },
] as const;

export function Browse() {
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const category = params.get("category_id") ?? "";
  const sort = params.get("sort") ?? "";

  const [data, setData] = useState<SearchResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [term, setTerm] = useState(q);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setErr(null);
    api
      .browse({
        q,
        category_id: category,
        sort: (sort || undefined) as never,
        page_size: 24,
      })
      .then((r) => {
        if (cancelled) return;
        setData(r);
        if (q) track("search", { query: q });
      })
      .catch((e) => !cancelled && setErr(String(e.message ?? e)))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [q, category, sort]);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    const next = new URLSearchParams(params);
    if (term) next.set("q", term);
    else next.delete("q");
    setParams(next);
  }

  function setParam(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next);
  }

  const categoryFacet = data?.facets.find((f) => f.field === "category_id");

  return (
    <div className="browse">
      <form className="searchbar" onSubmit={submit}>
        <input
          aria-label="Search products"
          placeholder="Search products…"
          value={term}
          onChange={(e) => setTerm(e.target.value)}
        />
        <button type="submit">Search</button>
      </form>

      <div className="browse-body">
        <aside className="facets">
          <div className="facet">
            <h3>Sort</h3>
            <select value={sort} onChange={(e) => setParam("sort", e.target.value)}>
              {SORTS.map((s) => (
                <option key={s.v} value={s.v}>
                  {s.label}
                </option>
              ))}
            </select>
          </div>

          {categoryFacet && categoryFacet.values.length > 0 && (
            <div className="facet">
              <h3>Category</h3>
              <ul>
                <li>
                  <button
                    className={!category ? "active" : ""}
                    onClick={() => setParam("category_id", "")}
                  >
                    All
                  </button>
                </li>
                {categoryFacet.values.map((v) => (
                  <li key={v.value}>
                    <button
                      className={category === v.value ? "active" : ""}
                      onClick={() => setParam("category_id", v.value)}
                    >
                      {v.value} <span className="count">{v.count}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </aside>

        <section className="results">
          {loading && <p className="muted">Loading…</p>}
          {err && <p className="error">Couldn’t load products: {err}</p>}
          {!loading && !err && data && data.hits.length === 0 && (
            <p className="muted">No products match your search.</p>
          )}
          {data && data.hits.length > 0 && (
            <>
              <p className="muted">{data.page.total_size} results</p>
              <div className="grid">
                {data.hits.map((h) => (
                  <Link key={h.product_id} to={`/p/${h.slug}`} className="card-link">
                    <ProductCard hit={h} />
                  </Link>
                ))}
              </div>
            </>
          )}
        </section>
      </div>
    </div>
  );
}

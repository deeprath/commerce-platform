import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../api";
import type { Product, Shop as ShopT } from "../types";
import { ProductCard } from "../components/ProductCard";
import { track } from "../track";

export function Shop() {
  const { slug = "" } = useParams();
  const [shop, setShop] = useState<ShopT | null>(null);
  const [products, setProducts] = useState<Product[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setErr(null);
    setShop(null);
    setProducts(null);
    Promise.all([api.shop(slug), api.shopProducts(slug)])
      .then(([shopRes, productsRes]) => {
        if (cancelled) return;
        setShop(shopRes);
        setProducts(productsRes.products ?? []);
        track("page_view", { path: `/shops/${slug}` });
      })
      .catch(
        (e) =>
          !cancelled &&
          setErr(e.info?.reason === "SHOP_NOT_FOUND" ? "notfound" : String(e.message ?? e)),
      )
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [slug]);

  if (loading) return <p className="muted">Loading…</p>;
  if (err === "notfound")
    return (
      <div className="shop-page">
        <p className="muted">That shop doesn’t exist.</p>
        <Link to="/">← Back to browse</Link>
      </div>
    );
  if (err) return <p className="error">Couldn’t load this shop: {err}</p>;
  if (!shop) return null;

  return (
    <div className="shop-page">
      <Link to="/" className="back">
        ← Browse
      </Link>
      <header className="shop-header">
        <h1>{shop.name}</h1>
        {shop.description && <p className="desc">{shop.description}</p>}
      </header>

      {products?.length === 0 && (
        <p className="muted">This shop has no products listed yet.</p>
      )}
      {products && products.length > 0 && (
        <div className="grid">
          {products.map((p) => (
            <Link key={p.id} to={`/p/${p.slug}`} className="card-link">
              <ProductCard
                hit={{
                  title: p.title,
                  list_price: p.list_price,
                  primary_media_key: p.media_keys[0] ?? "",
                }}
              />
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

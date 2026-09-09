import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, mediaUrl } from "../api";
import { formatMoney } from "../types";
import type { Product as P } from "../types";

export function Product() {
  const { slug = "" } = useParams();
  const [product, setProduct] = useState<P | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setErr(null);
    api
      .product(slug)
      .then((r) => !cancelled && setProduct(r.product))
      .catch((e) => !cancelled && setErr(e.info?.reason === "PRODUCT_NOT_FOUND" ? "notfound" : String(e.message ?? e)))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [slug]);

  if (loading) return <p className="muted">Loading…</p>;
  if (err === "notfound")
    return (
      <div className="pdp">
        <p className="muted">That product doesn’t exist.</p>
        <Link to="/">← Back to browse</Link>
      </div>
    );
  if (err) return <p className="error">Couldn’t load this product: {err}</p>;
  if (!product) return null;

  return (
    <div className="pdp">
      <Link to="/" className="back">
        ← Browse
      </Link>
      <div className="pdp-body">
        <div className="pdp-media">
          {product.media_keys[0] ? (
            <img src={mediaUrl(product.media_keys[0])} alt={product.title} />
          ) : (
            <div className="noimg big" aria-hidden />
          )}
        </div>
        <div className="pdp-info">
          <h1>{product.title}</h1>
          <p className="price big">{formatMoney(product.list_price)}</p>
          <p className="desc">{product.description}</p>
          {Object.keys(product.attributes ?? {}).length > 0 && (
            <dl className="attrs">
              {Object.entries(product.attributes).map(([k, v]) => (
                <div key={k}>
                  <dt>{k}</dt>
                  <dd>{v}</dd>
                </div>
              ))}
            </dl>
          )}
          <button className="buy" disabled title="Cart arrives in Phase 2">
            Add to cart
          </button>
        </div>
      </div>
    </div>
  );
}
